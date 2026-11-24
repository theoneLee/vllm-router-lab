"""Regenerate the version manifest from a vLLM git clone (bare or working tree)."""

import argparse
import ast
import json
import re
import subprocess
from pathlib import Path

EVENTS = "vllm/distributed/kv_events.py"
CACHE = "vllm/v1/core/kv_cache_utils.py"
HASHING = "vllm/utils/hashing.py"
BASIC = "vllm/entrypoints/serve/instrumentator/basic.py"
CLASSES = (
    "EventBatch",
    "KVCacheEvent",
    "BlockStored",
    "BlockRemoved",
    "AllBlocksCleared",
    "KVEventBatch",
)
ROOT = Path(__file__).resolve().parents[1]


def git(repo, *args):
    return subprocess.check_output(["git", "-C", repo, *args], text=True).strip()


def source(repo, ref, path):
    return git(repo, "show", f"{ref}:{path}")


def inspect(repo, tag):
    commit = git(repo, "rev-parse", tag + "^{commit}")
    event_source = source(repo, commit, EVENTS)
    nodes = {
        n.name: n for n in ast.parse(event_source).body if isinstance(n, ast.ClassDef)
    }
    cache = ast.unparse(ast.parse(source(repo, commit, CACHE)))
    api = ast.unparse(ast.parse(source(repo, commit, BASIC)))
    wire = [
        ast.unparse(n.args[0])
        for n in ast.walk(nodes["ZmqEventPublisher"])
        if isinstance(n, ast.Call) and getattr(n.func, "attr", "") == "send_multipart"
    ]
    required = {
        "(self._topic_bytes, seq_bytes, payload)",
        "(client_id, b'', self._topic_bytes, seq.to_bytes(8, 'big'), buf)",
        "(client_id, b'', b'', self.END_SEQ, b'')",
    }
    if set(wire) != required:
        raise ValueError(
            f"{tag}: review publisher/replay source shape against the wire tests"
        )
    if "(-1).to_bytes(8, 'big', signed=True)" not in ast.unparse(
        nodes["ZmqEventPublisher"]
    ):
        raise ValueError(f"{tag}: review replay sentinel source shape")
    if "DEFAULT_NONE_HASH_SEED = 'vllm-none-hash'" in cache:
        fixed = True
    elif "NONE_HASH = BlockHash(os.urandom(32))" in cache:
        fixed = False
    else:
        raise ValueError(f"{tag}: unknown hash root")
    env = ast.unparse(ast.parse(source(repo, commit, "vllm/envs.py")))
    if "os.getenv('VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES', '1')" not in env:
        raise ValueError(f"{tag}: review event hash format default")
    return {
        "version": tag[1:],
        "commit": commit,
        "source_discovery": "@router.get('/kv_event_sources')" in api,
        "fixed_cbor_root": fixed,
        "int_hashes_default": True,
        "block_stored_fields": [
            n.target.id
            for n in nodes["BlockStored"].body
            if isinstance(n, ast.AnnAssign)
        ],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repo", help="clone with stable vLLM tags fetched")
    parser.add_argument("--out", type=Path, default=ROOT / "protocol/versions.json")
    args = parser.parse_args()
    tags = [
        t
        for t in git(args.repo, "tag").splitlines()
        if re.fullmatch(r"v\d+\.\d+\.\d+", t)
    ]
    tags.sort(key=lambda t: tuple(map(int, t[1:].split("."))))
    if len(tags) < 10:
        parser.error("need at least ten stable tags")
    rows = [inspect(args.repo, tag) for tag in tags[-10:]]
    args.out.write_text(json.dumps(rows, indent=2) + "\n")


if __name__ == "__main__":
    main()
