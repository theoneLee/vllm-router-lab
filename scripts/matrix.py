"""Check the live Go worker against each pinned tag's Python types and real Router."""

import argparse
import ast
import hashlib
import json
import subprocess
import sys
from collections.abc import Callable, Sequence
from pathlib import Path
from typing import Any
from urllib.error import HTTPError

import cbor2
import msgspec
import zmq
from audit import CACHE, CLASSES, EVENTS, HASHING, ROOT, inspect, source
from harness import call, complete, control, process, ready


def oracle(repo, commit):
    # Load only the tagged wire classes and two pure hashing functions. No
    # vLLM engine, CUDA libraries or torch imports are needed by these nodes.
    ns = dict(
        msgspec=msgspec,
        Any=Any,
        ExternalBlockHash=bytes | int,
        Callable=Callable,
        Sequence=Sequence,
        BlockHash=bytes,
        hashlib=hashlib,
        cbor2=cbor2,
    )
    for path, names in [
        (EVENTS, CLASSES),
        (HASHING, ("sha256_cbor",)),
        (CACHE, ("hash_block_tokens",)),
    ]:
        tree = ast.parse(source(repo, commit, path))
        chosen = [node for node in tree.body if getattr(node, "name", None) in names]
        if len(chosen) != len(names):
            raise ValueError(f"missing definitions in {commit}:{path}")
        snippet = "\n".join(ast.unparse(node) for node in chosen)
        exec(compile(snippet, f"{commit}:{path}", "exec"), ns)
    return ns


def wire(args, row, ns, out, integers):
    version = row["version"]
    url = f"http://127.0.0.1:{args.base}"
    cmd = [
        args.lab,
        "fleet",
        "-workers",
        "1",
        "-port",
        str(args.base),
        "-ranks",
        "2",
        "-blocks",
        "2",
        "-vllm-version",
        version,
    ]
    seed = "vllm-none-hash" if row["fixed_cbor_root"] else "0"
    if not row["fixed_cbor_root"]:
        cmd += ["-hash-seed", seed]
    if integers:
        cmd.append("-int-hashes")
    with process(cmd, out / f"wire-{integers}.log") as f:
        ready(url + "/health", f)
        assert call(url + "/version") == {"version": version}
        feeds = call(url + "/lab/event-sources")
        try:
            advertised = call(url + "/kv_event_sources")
        except HTTPError as e:
            assert e.code == 404 and not row["source_discovery"]
        else:
            assert row["source_discovery"] and advertised == feeds
        tokens = list(range(33))
        complete(url, tokens, 1)
        complete(url, list(range(500, 517)), 1)  # forces LRU eviction
        control(url, "clear", 1)
        with zmq.Context() as ctx, ctx.socket(zmq.DEALER) as client:
            client.setsockopt(zmq.LINGER, 0)
            client.setsockopt(zmq.RCVTIMEO, 3000)
            client.connect(feeds["1"]["replay_endpoint"])
            client.send_multipart([b"", bytes(8)])
            batches = []
            for seq in range(3):
                empty, topic, seq_bytes, payload = client.recv_multipart()
                assert empty == b"" and topic.decode() == feeds["1"]["topic"]
                assert seq_bytes == seq.to_bytes(8, "big")
                batch = msgspec.msgpack.decode(payload, type=ns["KVEventBatch"])
                assert batch.data_parallel_rank == 1
                batches.append(batch.events)
            assert client.recv_multipart() == [b"", b"", b"\xff" * 8, b""]
    hash_fn = ns["sha256_cbor"]
    ns["NONE_HASH"] = hash_fn(seed)
    parent = None
    for i, event in enumerate(batches[0]):
        block = tokens[16 * i : 16 * (i + 1)]
        key = ns["hash_block_tokens"](hash_fn, parent, block, None)

        def external(digest):
            return int.from_bytes(digest[-8:], "big") if integers else digest

        assert isinstance(event, ns["BlockStored"])
        assert event.block_hashes == [external(key)] and event.token_ids == block
        assert event.parent_block_hash == (external(parent) if parent else None)
        assert event.block_size == 16 and event.group_idx == 0
        parent = key
    assert len(batches[0]) == 2
    removed, replacement = batches[1]
    assert isinstance(removed, ns["BlockRemoved"])
    assert removed.block_hashes == batches[0][0].block_hashes
    assert isinstance(replacement, ns["BlockStored"])
    assert len(batches[2]) == 1 and isinstance(batches[2][0], ns["AllBlocksCleared"])


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument(
        "repo", help="trusted upstream vLLM clone containing the pinned tags"
    )
    p.add_argument("--lab", default="./routerlab")
    p.add_argument("--router", required=True)
    p.add_argument("--router-version", required=True)
    p.add_argument("--base", type=int, default=18400)
    p.add_argument("--port", type=int, default=30400)
    p.add_argument("--results", type=Path, default=Path("results/matrix"))
    args = p.parse_args()
    rows = json.loads((ROOT / "protocol/versions.json").read_text())
    args.results.mkdir(parents=True, exist_ok=True)
    summary = args.results / "matrix.json"
    summary.unlink(missing_ok=True)
    report = []
    for row in rows:
        v = row["version"]
        out = args.results / v
        out.mkdir(parents=True, exist_ok=True)
        if inspect(args.repo, "v" + v) != row:
            raise ValueError(f"tag or manifest drift for {v}")
        ns = oracle(args.repo, row["commit"])
        for integers in (False, True):
            wire(args, row, ns, out, integers)
        cmd = [
            sys.executable,
            str(ROOT / "scripts/regress.py"),
            "kv",
            "--lab",
            args.lab,
            "--router",
            args.router,
            "--version",
            args.router_version,
            "--worker-version",
            v,
            "--base",
            str(args.base),
            "--port",
            str(args.port),
            "--results",
            str(out),
        ]
        if not row["fixed_cbor_root"]:
            cmd += ["--hash-seed", "0"]
        subprocess.run(cmd, check=True)
        report.append(
            dict(
                version=v,
                vllm_commit=row["commit"],
                router=args.router_version,
                wire_bytes="passed",
                wire_uint64="passed",
                dual_rank_gap_replay="passed",
                rank_eviction="passed",
                discovery="http" if row["source_discovery"] else "static",
            )
        )
    summary.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
