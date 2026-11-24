"""Run one black-box Router regression and write its observations as JSON."""

import argparse
import json
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from environment import Environment, environment
from kv_scenarios import (
    kv,
    runtime_metadata,
    set_incompatible_algorithm,
    startup_metadata,
)
from lifecycle_scenarios import scale, streams


@dataclass(frozen=True)
class Scenario:
    check: Callable[[Environment, dict], None]
    ranks: int = 1
    metrics_only: bool = False
    before_router: Callable[[str], None] | None = None


SCENARIOS = {
    "kv": Scenario(kv, ranks=2),
    "619": Scenario(runtime_metadata, ranks=2, metrics_only=True),
    "619-startup": Scenario(
        startup_metadata,
        ranks=2,
        metrics_only=True,
        before_router=set_incompatible_algorithm,
    ),
    "stream": Scenario(streams),
    "scale": Scenario(scale),
}


def run(a):
    out = Path(a.results)
    out.mkdir(parents=True, exist_ok=True)
    case = SCENARIOS[a.case]
    report = {
        "case": a.case,
        "router": a.version,
        "worker_version": a.worker_version,
        "observed": {},
        "passed": [],
    }
    try:
        with environment(
            a, out, case.ranks, case.metrics_only, case.before_router
        ) as env:
            case.check(env, report)
        report["status"] = "passed"
    except Exception as exc:
        report.update(status="failed", error=repr(exc))
        raise
    finally:
        (out / (a.case + ".json")).write_text(json.dumps(report, indent=2) + "\n")
    print(report)


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("case", choices=SCENARIOS)
    p.add_argument("--router", default="vllm-router")
    p.add_argument("--lab", default="./routerlab")
    p.add_argument("--version", required=True)
    p.add_argument("--worker-version")
    p.add_argument("--hash-seed")
    p.add_argument("--port", type=int, default=30100)
    p.add_argument("--base", type=int, default=18100)
    p.add_argument("--results", default="results/regression")
    run(p.parse_args())
