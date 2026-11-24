"""Process lifecycle and HTTP helpers shared by the lab commands."""

import contextlib
import json
import subprocess
import time
from urllib.request import Request, urlopen


def get(url):
    with urlopen(url, timeout=3) as response:
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
            time.sleep(0.1)
    raise TimeoutError(url)


def call(url, body=None, rank=None, method=None):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"}
    if rank is not None:
        headers["X-Data-Parallel-Rank"] = str(rank)
    req = Request(url, data, headers, method=method)
    with urlopen(req, timeout=6) as res:
        raw = res.read()
        return json.loads(raw) if raw else None


def until(check, timeout=10):
    deadline = time.monotonic() + timeout
    while True:
        last = check()
        if last:
            return last
        if time.monotonic() > deadline:
            raise AssertionError("condition timed out: " + check.__name__)
        time.sleep(0.05)


def state(worker, rank=0):
    return call(worker + "/lab/state", rank=rank)


def control(worker, action, rank=0):
    return call(worker + "/lab/" + action, rank=rank, method="POST")


def body(tokens, stream=False, n=1):
    return {
        "model": "lab",
        "prompt": tokens,
        "max_tokens": n,
        "stream": stream,
        "temperature": 0,
    }


def complete(target, tokens, rank=None):
    return call(target + "/v1/completions", body(tokens), rank)
