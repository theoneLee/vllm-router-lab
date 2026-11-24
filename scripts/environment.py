"""Bring up the worker fleet and Router for one isolated regression."""

import json
import shlex
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from pathlib import Path
from tempfile import TemporaryDirectory
from urllib.error import HTTPError

from harness import call, get, process, ready


@dataclass(frozen=True)
class Environment:
    worker: str
    target: str
    metrics: str
    base: int
    lab: str
    results: Path


def configure_sources(router, w, ranks, hash_seed, stack):
    # Only the harness can read /lab/event-sources. Older vLLM tags need
    # explicit Router publisher addresses; newer tags discover them.

    try:
        get(w + "/kv_event_sources")
    except HTTPError as e:
        if e.code != 404:
            raise
        feeds = call(w + "/lab/event-sources")
        router += ["--kv-event-model", "lab"]
        for flag, field in [
            ("--kv-event-source", "endpoint"),
            ("--kv-event-replay-source", "replay_endpoint"),
            ("--kv-event-topic", "topic"),
        ]:
            router.extend(
                [
                    flag,
                    w + "=" + ",".join(feeds[str(i)][field] for i in range(ranks)),
                ]
            )
    if hash_seed is not None:
        tmp = Path(stack.enter_context(TemporaryDirectory())) / "layout.json"
        layout_ranks = [
            {
                "rank": i,
                "model": "lab",
                "hash_block_size": 16,
                "hash_seed": hash_seed,
                "declared_groups": 1,
                "speculative_tokens": 0,
                "groups": [{"kind": "full_attention", "block_size": 16}],
            }
            for i in range(ranks)
        ]
        tmp.write_text(json.dumps({w: {"ranks": layout_ranks}}))
        router += ["--kv-hybrid-layout", str(tmp)]


@contextmanager
def environment(a, out, ranks, metrics_only, before_router):
    w = f"http://127.0.0.1:{a.base}"
    target = f"http://127.0.0.1:{a.port}"
    lab = [
        a.lab,
        "fleet",
        "-workers",
        "1",
        "-ranks",
        str(ranks),
        "-port",
        str(a.base),
        "-decode",
        "30ms",
    ]
    if ranks > 1:
        lab += ["-blocks", "8"]
    if metrics_only:
        lab += ["-metrics-only"]
    router = shlex.split(a.router) + [
        "--worker-urls",
        w,
        "--host",
        "127.0.0.1",
        "--port",
        str(a.port),
        "--prometheus-port",
        str(a.port + 1),
        "--policy",
        "kv_aware" if ranks > 1 else "round_robin",
        "--log-level",
        "debug",
    ]
    if ranks > 1:
        router += ["--intra-node-data-parallel-size", str(ranks)]
    if a.worker_version:
        lab += ["-vllm-version", a.worker_version]
        if a.hash_seed is not None:
            lab += ["-hash-seed", a.hash_seed]
    with ExitStack() as stack:
        proc = stack.enter_context(process(lab, out / (a.case + ".fleet.log")))
        ready(w + "/health", proc)
        if before_router:
            before_router(w)
        if ranks > 1 and a.worker_version:
            configure_sources(router, w, ranks, a.hash_seed, stack)
        route = stack.enter_context(process(router, out / (a.case + ".router.log")))
        ready(target + "/health", route)
        yield Environment(
            w, target, f"http://127.0.0.1:{a.port + 1}", a.base, a.lab, out
        )
