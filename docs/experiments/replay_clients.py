"""Compare a single-loop aiohttp client with Go replay against the same servers."""

import argparse
import asyncio
import json
import platform
import random
import shlex
import statistics
import subprocess
import sys
import time
from contextlib import ExitStack
from pathlib import Path

import aiohttp

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts"))
from compare import counter
from harness import get, process, ready

REQUESTS = 20_000
CONCURRENCY = 128
WORKERS = 100


def write_trace(path):
    rng = random.Random(42)
    with path.open("w") as output:
        for i in range(REQUESTS):
            start = 1 + 32 * rng.randrange(32)
            prompt = [*range(start, start + 32), i % 8192]
            row = {"at_ms": 0, "prompt": prompt, "max_tokens": 1, "stream": False}
            print(json.dumps(row), file=output)


async def python_replay(path, target):
    connector = aiohttp.TCPConnector(limit=CONCURRENCY, limit_per_host=CONCURRENCY)
    with path.open() as rows:
        async with aiohttp.ClientSession(connector=connector) as client:

            async def consume():
                latencies = []
                for line in rows:
                    row = json.loads(line)
                    del row["at_ms"]
                    row["model"] = "lab"
                    start = time.perf_counter()
                    async with client.post(
                        target + "/v1/completions", json=row
                    ) as response:
                        response.raise_for_status()
                        if not (await response.json())["choices"]:
                            raise ValueError("empty choices")
                    latencies.append(1000 * (time.perf_counter() - start))
                return latencies

            start = time.perf_counter()
            samples = await asyncio.gather(*(consume() for _ in range(CONCURRENCY)))
            elapsed = time.perf_counter() - start
    latencies = sorted(x for batch in samples for x in batch)
    return {
        "success": len(latencies),
        "success_rps": len(latencies) / elapsed,
        "e2e_ms": {"p99": latencies[(99 * len(latencies) + 99) // 100 - 1]},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--router", default="vllm-router")
    parser.add_argument("--router-version", required=True)
    parser.add_argument("--lab", default="./routerlab")
    parser.add_argument("--results", type=Path, default=Path("results/replay-clients"))
    args = parser.parse_args()
    args.results.mkdir(parents=True, exist_ok=True)
    trace = args.results / "trace.jsonl"
    write_trace(trace)
    workers = [f"http://127.0.0.1:{21600 + i}" for i in range(WORKERS)]
    target = "http://127.0.0.1:32110"
    trials = []
    for i, language in enumerate(
        ["go", "python", "python", "go", "go", "python"], start=1
    ):
        with ExitStack() as stack:
            fleet = stack.enter_context(
                process(
                    [args.lab, "fleet", "-workers", str(WORKERS), "-port", "21600"],
                    args.results / f"{i}.fleet.log",
                )
            )
            ready(workers[-1] + "/health", fleet)
            router = stack.enter_context(
                process(
                    shlex.split(args.router)
                    + [
                        "--worker-urls",
                        *workers,
                        "--port",
                        "32110",
                        "--prometheus-port",
                        "32111",
                        "--host",
                        "127.0.0.1",
                        "--policy",
                        "round_robin",
                        "--log-level",
                        "error",
                    ],
                    args.results / f"{i}.router.log",
                )
            )
            ready(target + "/health", router)
            if language == "go":
                report = json.loads(
                    subprocess.check_output(
                        [
                            args.lab,
                            "replay",
                            "-trace",
                            str(trace),
                            "-url",
                            target,
                            "-concurrency",
                            str(CONCURRENCY),
                        ],
                        text=True,
                    )
                )
            else:
                report = asyncio.run(python_replay(trace, target))
            counts = [
                int(counter(get(w + "/metrics"), "lab_requests_total")) for w in workers
            ]
            if (
                counts != [REQUESTS // WORKERS] * WORKERS
                or report["success"] != REQUESTS
            ):
                raise ValueError(f"receipt mismatch in trial {i}")
            trials.append(
                {
                    "client": language,
                    "success": report["success"],
                    "received": sum(counts),
                    "success_rps": report["success_rps"],
                    "e2e_p99_ms": report["e2e_ms"]["p99"],
                }
            )
    result = {
        "router_version": args.router_version,
        "os": platform.system(),
        "arch": platform.machine(),
        "python": platform.python_version(),
        "aiohttp": aiohttp.__version__,
        "workers": WORKERS,
        "concurrency": CONCURRENCY,
        "requests_per_trial": REQUESTS,
        "trials": trials,
        "median_success_rps": {
            lang: statistics.median(
                t["success_rps"] for t in trials if t["client"] == lang
            )
            for lang in ["go", "python"]
        },
    }
    (args.results / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    print(result["median_success_rps"])


if __name__ == "__main__":
    main()
