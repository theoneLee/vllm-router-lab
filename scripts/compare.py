"""Start a fresh fleet and real Router per policy and verify backend receipt counts."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import time
import urllib.request


def get(url):
    with urllib.request.urlopen(url, timeout=3) as response:
        return response.read().decode()


@contextlib.contextmanager
def process(args, log):
    with open(log, "w") as output:
        child = subprocess.Popen(args, stdout=output, stderr=subprocess.STDOUT)
        try:
            yield child
        finally:
            child.terminate()
            try:
                child.wait(5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()


def ready(url, child):
    for _ in range(300):
        if child.poll() is not None:
            raise RuntimeError(f"process exited {child.returncode} before {url}")
        try:
            get(url)
            return
        except OSError:
            time.sleep(.1)
    raise TimeoutError(url)


def counter(url, name):
    text = get(url + "/metrics")
    return sum(float(v) for v in re.findall(r"^" + re.escape(name) + r"(?:\{[^\n]*?\})? ([\d.e+E-]+)", text, re.M))


def cli():
    p = argparse.ArgumentParser()
    p.add_argument("--lab", default="./routerlab")
    p.add_argument("--router", default="vllm-router")
    p.add_argument("--policies", nargs="+", default=["round_robin", "cache_aware", "kv_aware"])
    p.add_argument("--workers", type=int, default=2)
    p.add_argument("--requests", type=int, default=1000)
    p.add_argument("--rate", type=float, default=0)
    p.add_argument("--concurrency", type=int, default=64)
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--trace")
    p.add_argument("--base", type=int, default=18000)
    p.add_argument("--port", type=int, default=30000)
    p.add_argument("--results", default="results")
    p.add_argument("--profile", default="", help="JSON file of fleet flags such as prefill and decode")
    p.add_argument("--stream", action="store_true")
    p.add_argument("--output", type=int, default=1)
    p.add_argument("--router-version", required=True, help="tested commit or release")
    return p.parse_args()


def main(a):
    out = Path(a.results)
    out.mkdir(parents=True, exist_ok=True)
    urls = [f"http://127.0.0.1:{a.base+i}" for i in range(a.workers)]
    target = f"http://127.0.0.1:{a.port}"
    profile = json.loads(Path(a.profile).read_text()) if a.profile else {}
    for policy in a.policies:
        flags = [x for k, v in profile.items() for x in ["-"+k, str(v)]]
        fleet = [a.lab, "fleet", "-workers", str(a.workers), "-port", str(a.base), *flags]
        router = shlex.split(a.router) + ["--worker-urls", *urls, "--policy", policy,
            "--host", "127.0.0.1", "--port", str(a.port), "--prometheus-port", str(a.port+1),
            "--log-level", "error"]
        with process(fleet, out / (policy+".fleet.log")) as workers:
            ready(urls[-1] + "/health", workers)
            with process(router, out / (policy+".router.log")) as route:
                ready(target + "/health", route)
                cmd = [a.lab, "replay", "-url", target, "-n", str(a.requests), "-rate", str(a.rate),
                       "-concurrency", str(a.concurrency), "-seed", str(a.seed), "-output", str(a.output)]
                if a.stream:
                    cmd.append("-stream")
                if a.trace:
                    cmd.extend(["-trace", a.trace])
                run = subprocess.run(cmd, stdout=subprocess.PIPE, text=True)
                report = json.loads(run.stdout)
                counts = [int(counter(u, "lab_requests_total")) for u in urls]
                report.update(policy=policy, router_version=a.router_version, worker_counts=counts,
                              workers=a.workers, profile=profile, backend_received=sum(counts))
                (out / (policy+".json")).write_text(json.dumps(report, indent=2)+"\n")
                print(policy, report["success"], report["backend_received"], report["success_rps"], flush=True)
                if run.returncode or sum(counts) != report["success"]:
                    raise RuntimeError("client and backend counts disagree or replay failed")
                if policy=="round_robin" and report["success"] >= a.workers and min(counts)==0:
                    raise RuntimeError("round-robin did not visit every endpoint")


if __name__ == "__main__":
    main(cli())
