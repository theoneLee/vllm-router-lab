"""Streaming cancellation and dynamic worker membership regressions."""

import http.client
import json
import subprocess
from urllib.parse import quote
from urllib.request import Request, urlopen

from harness import body, call, complete, control, process, ready, state, until


def streams(env, report):
    worker, target, lab, log = env.worker, env.target, env.lab, report["passed"]
    tokens = list(range(65))
    pre = state(worker)
    req = Request(
        target + "/v1/completions",
        json.dumps(body(tokens, True, 300)).encode(),
        {"Content-Type": "application/json"},
    )
    with urlopen(req, timeout=8) as stream:
        chunks = 0
        for line in stream:
            if line.startswith(b"data:") and b"[DONE]" not in line:
                chunks += 1
                if chunks == 3:
                    break
        assert chunks == 3
    until(
        lambda: (
            state(worker)["canceled"] > pre["canceled"] and state(worker)["active"] == 0
        ),
        4,
    )
    control(worker, "cut?value=3")
    received = b""
    try:
        with urlopen(req, timeout=8) as stream:
            for line in stream:
                received += line
    except (http.client.IncompleteRead, ConnectionResetError):
        pass
    assert b"[DONE]" not in received and state(worker)["broken"] > pre["broken"]
    until(lambda: state(worker)["active"] == 0)
    assert complete(target, tokens)["choices"]
    log.append(
        "client cancel and upstream cut released slots; next completion succeeded"
    )
    control(worker, "cut?value=3")
    replay = subprocess.run(
        [lab, "replay", "-url", target, "-n", "1", "-stream", "-output", "8"],
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    summary = json.loads(replay.stdout)
    assert replay.returncode != 0 and summary["errors"] == 1 and summary["success"] == 0
    log.append("the replay CLI rejected the truncated SSE response and exited nonzero")


def scale(env, report):
    other = f"http://127.0.0.1:{env.base + 1}"
    cmd = [env.lab, "fleet", "-port", str(env.base + 1), "-workers", "1"]
    previous_instance = None
    for i in range(2):
        with process(cmd, env.results / ("epoch-" + str(i) + ".log")) as added:
            ready(other + "/health", added)
            info = state(other)
            if previous_instance is not None:
                assert info["instance"] != previous_instance and info["hits"] == 0
            previous_instance = info["instance"]
            call(env.target + "/workers", {"url": other})

            def reached_added_worker():
                complete(env.target, list(range(65)))
                return state(other)["received"] > 0

            until(reached_added_worker)
            call(
                env.target + "/workers/" + quote(other, safe=""),
                method="DELETE",
            )
            before = state(other)["received"]
            for _ in range(5):
                complete(env.target, list(range(65)))
            assert state(other)["received"] == before
    report["passed"].append(
        "scale out/in and same-address restart with new worker identity"
    )
