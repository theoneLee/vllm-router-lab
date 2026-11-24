"""KV placement, continuity and metadata regressions against a real Router."""

import json
import re
import time
from urllib.request import Request, urlopen

from harness import body, complete, control, get, state, until


def metric(metrics):
    text = get(metrics + "/metrics")
    m = re.search(
        r'^vllm_router_kv_placement_lookups_total\{[^}]*result="hit"[^}]*\} ([\d.e+E-]+)',
        text,
        re.M,
    )
    return float(m[1]) if m else 0


def has_hit(target, metrics, tokens):
    before = metric(metrics)
    res = complete(target, tokens)
    return metric(metrics) > before, res


def warm_ranks(worker, target, metrics, log):
    prompts = [list(range(offset, offset + 129)) for offset in (1000, 4000)]
    for r, tokens in enumerate(prompts):
        complete(worker, tokens, r)
        control(worker, "freeze?value=1", r)
    for r, tokens in enumerate(prompts):

        def warmed():
            prev = state(worker, r)
            hit, _ = has_hit(target, metrics, tokens)
            after = state(worker, r)
            return (
                hit
                and after["received"] == prev["received"] + 1
                and after["hits"] > prev["hits"]
            )

        until(warmed)
    log.append("separate warm prefixes reached their actual ranks with Router credit")
    return prompts


def kv(env, report):
    worker, target, metrics = env.worker, env.target, env.metrics
    log = report["passed"]
    prompts = warm_ranks(worker, target, metrics, log)
    before = state(worker, 1)["replays"]
    control(worker, "drop-next", 1)
    control(worker, "clear", 1)
    control(worker, "freeze?value=0", 1)
    complete(worker, list(range(10000, 10129)), 1)
    control(worker, "freeze?value=1", 1)
    until(
        lambda: (
            state(worker, 1)["replays"] > before
            and not has_hit(target, metrics, prompts[1])[0]
        )
    )
    assert has_hit(target, metrics, prompts[0])[0], "loss on rank 1 cleared rank 0"
    assert has_hit(target, metrics, list(range(10000, 10129)))[0], (
        "new rank 1 prefix missing after gap"
    )
    log.append("missing clear recovered via replay; healthy rank retained credit")

    resident = list(range(10000, 10129))
    replacement = list(range(20000, 20129))
    control(worker, "freeze?value=0", 1)
    complete(worker, replacement, 1)
    control(worker, "freeze?value=1", 1)

    def evicted():
        new_hit, response = has_hit(target, metrics, replacement)
        return (
            new_hit
            and response.get("lab_rank") == 1
            and response["usage"]["prompt_tokens_details"]["cached_tokens"] == 128
            and not has_hit(target, metrics, resident)[0]
        )

    until(evicted)
    assert has_hit(target, metrics, prompts[0])[0]
    log.append("rank 1 LRU removal revoked the old prefix while rank 0 stayed warm")


def runtime_metadata(env, report):
    worker, target, metrics = env.worker, env.target, env.metrics
    log, observed = report["passed"], report["observed"]
    prompts = warm_ranks(worker, target, metrics, log)
    before = state(worker, 1)["metrics_reads"]
    control(worker, "algorithm?value=sha256", 1)

    # Check after a fresh metadata observation. Keep polling through reconciliation.
    def revoked():
        good, _ = has_hit(target, metrics, prompts[0])
        bad, _ = has_hit(target, metrics, prompts[1])
        latest = state(worker, 1)
        after = latest["metrics_reads"]
        observed.update(
            metrics_reads_before=before,
            metrics_reads_after=after,
            rank1_last_reported_algorithm=latest["last_reported_algorithm"],
            rank0_prefix_lookup_hit=good,
            rank1_prefix_lookup_hit=bad,
        )
        return (
            after > before
            and latest["last_reported_algorithm"] == "sha256"
            and good
            and not bad
        )

    until(revoked, 45)
    # With one competing long request on rank 0, the ineligible cache rank can still serve.
    req = Request(
        target + "/v1/completions",
        json.dumps(body(prompts[0], True, 300)).encode(),
        {"Content-Type": "application/json"},
    )
    with urlopen(req, timeout=8) as stream:
        stream.readline()
        until(lambda: state(worker, 0)["active"] > 0)
        before = state(worker, 1)["received"]

        def reached_rank_one():
            complete(target, [65001])
            return state(worker, 1)["received"] > before

        until(reached_rank_one, 2)
    log.append("rank 1 lost cache credit and remained routable; rank 0 retained credit")


def startup_metadata(env, report):
    worker, target, metrics = env.worker, env.target, env.metrics
    log, observed = report["passed"], report["observed"]
    tokens0, tokens1 = [list(range(start, start + 129)) for start in (1000, 4000)]
    for rank, tokens in enumerate([tokens0, tokens1]):
        complete(worker, tokens, rank)
        control(worker, "freeze?value=1", rank)
    until(lambda: has_hit(target, metrics, tokens0)[0])
    s = state(worker, 1)
    observed.update(
        rank1_last_reported_algorithm=s["last_reported_algorithm"],
        rank1_metrics_reads=s["metrics_reads"],
    )
    assert s["metrics_reads"] > 0 and s["last_reported_algorithm"] == "sha256"
    log.append(
        "Router fetched incompatible rank 1 metadata, rank 0 still receives KV credit"
    )
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        hit, res = has_hit(target, metrics, tokens1)
        observed.update(
            rank1_prefix_lookup_hit=hit,
            routed_rank=res.get("lab_rank"),
            backend_cached_tokens=res["usage"]["prompt_tokens_details"][
                "cached_tokens"
            ],
        )
        assert not hit, (
            "Router credited rank 1 prefix after reading incompatible startup metadata"
        )
        time.sleep(0.05)


def set_incompatible_algorithm(worker):
    control(worker, "algorithm?value=sha256", 1)
