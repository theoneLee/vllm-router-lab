# Maintaining

English | [简体中文](maintaining.zh-CN.md)

This guide covers keeping the lab in sync with upstream: adding a new vLLM release, re-running the version matrix, and tracking down a failure.

The lab tracks the latest ten stable vLLM releases (`vMAJOR.MINOR.PATCH` tags, sorted numerically, with patch releases counted separately). The Router is pinned on its own. Use separate upstream checkouts. You never need to copy vLLM or Router code into this repo.

## 1. Refresh upstream and see what changed

```sh
git clone --filter=blob:none --no-checkout https://github.com/vllm-project/vllm.git ../vllm
git -C ../vllm fetch origin --tags
python3 scripts/audit.py ../vllm
git diff -- protocol/versions.json
```

If you already have a checkout, start from `fetch`. "Latest ten" only holds if all upstream tags were fetched, so check that each new tag points at the same commit as upstream.

`audit.py` reads each tag's source and records the event-source discovery route, the hash root, whether integer hashes are the default, and the `BlockStored` fields. When it reports something unexpected, read the source diff between the two tags. A new field matters even if it has a default, so think through whether it changes routing.

These are the questions to answer for a new release, and where upstream answers them:

| Question | Upstream source | What it controls in the lab |
| --- | --- | --- |
| How does the Router find each rank's event source? | `vllm/entrypoints/serve/instrumentator/basic.py`, `vllm/config/kv_events.py` | HTTP discovery vs. static addresses, and the 404 path |
| How are events framed, gaps detected, replay ended? | `vllm/distributed/kv_events.py` | Topic, 8-byte big-endian sequence, MessagePack batch, DEALER framing, end marker |
| Can both sides compute the same block key? | `vllm/utils/hashing.py`, `vllm/v1/core/kv_cache_utils.py`, `vllm/envs.py` | Root seed, parent hash and tokens, 32 bytes vs. low 64 bits |
| Do events add a new index dimension? | `BlockStored`, `BlockRemoved` and their producers | Group, locality, ownership, session, lookahead. Add a dedicated scenario if routing depends on it. |
| What cache metadata can the control plane read? | `vllm/config/cache.py`, `vllm/v1/metrics/`, Router `src/kv_index/source/discovery/` | Per-engine labels, metadata re-reads, per-rank credit revocation |
| What does an HTTP client see? | vLLM completions server, Router HTTP forwarding | Token-ID requests, SSE end and disconnect, slot release after cancel |

If behaviour really changed, update `profile.go`, the worker or the cache, rebuild, and re-run.

## 2. Run the version matrix

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r scripts/requirements-audit.txt
go build -o routerlab .
python scripts/matrix.py ../vllm --router "$ROUTER" --router-version "$ROUTER_REV"
```

`ROUTER` is the Router binary. `ROUTER_REV` is the exact commit you built, e.g. `159dcfcfcd717765d48b2ad24267f2a073d2501b`.

For every tag, the matrix:

1. Checks the tag against `protocol/versions.json`.
2. Pulls that commit's msgspec event types and hash functions. Only these pure-Python pieces load; no engine, CUDA or torch is needed.
3. Decodes real replay frames from the Go worker, checking store, parent hash, eviction, clear, rank and the end marker. Byte and integer hash forms are both tested.
4. Runs the two-rank Router regression with byte hashes.

Any failure exits non-zero. Use a new `--results` directory for each run you plan to publish.

Things to know about specific versions:

- **v0.26.0 – v0.28.0** pick a random SHA-256 CBOR root per process when no seed is set. The positive KV test passes `-hash-seed 0` to the workers and the matching layout to the Router. From v0.29.0 on, the matrix uses the default fixed root.
- **v0.26.0 – v0.30.0** have no HTTP discovery, so the Router gets each rank's publish address, replay address and topic through static config. The harness reads them from `/lab/event-sources`. The native `/kv_event_sources` returns 404 for these profiles. From v0.31.0 on, the matrix tests HTTP discovery directly.
- All profiles use `sha256_cbor`, one full-attention group, block size 16, and no developer `server_info`. On a real engine that means `--prefix-caching-hash-algo sha256_cbor` and `VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES=0`. All ten tags default to integer event hashes; the fleet's `-int-hashes` flag covers that path. Matching a version name doesn't mean every engine default or every protocol detail is covered.

Archive the JSON only after all three layers pass: source audit, wire decoding with upstream types, and the Router black-box test. Record full-engine interop or GPU latency separately.

## 3. Regressions and debugging

```sh
for case in stream scale kv; do
  python scripts/regress.py "$case" --router "$ROUTER" --version "$ROUTER_REV" || exit 1
done
python scripts/regress.py 619-startup --router "$ROUTER" --version "$ROUTER_REV" --worker-version 0.34.0
python scripts/regress.py 619 --router "$ROUTER" --version "$ROUTER_REV" --worker-version 0.34.0
```

The last two are acceptance tests for Router issue #619. Both fail on the current baseline, so run them separately and read each JSON report.

- `619-startup` changes rank 1's metrics before the Router probes it.
- `619` builds credit on both ranks first, then changes rank 1's metrics.

The fault only changes metadata and keeps the existing hashes matchable, so the test isolates one question: does the Router still trust this rank? To judge the result, look at four things together: the label the worker reported, the metrics read count, the Router's lookup counter, and the rank that actually served the request. If the read count didn't go up, that only proves no re-read happened within the observation window. A correct fix keeps rank 0's credit and leaves rank 1 serving normal requests.

When an event test fails:

1. Check which versions the report says you ran.
2. Read `*.router.log` and `*.fleet.log` in the same results directory.
3. Query `/lab/state` on each rank (set `X-Data-Parallel-Rank`).

For performance runs, keep the machine, build mode, seed, request count, concurrency and cache size fixed. When comparing policies, first check that request counts match between client and workers, then look at cached-token share and TTFT.

## CI

- [`ci.yml`](../.github/workflows/ci.yml) runs vet, race tests and the build on every push and pull request.
- [`mac-lab.yml`](../.github/workflows/mac-lab.yml) is manual (`workflow_dispatch`). It builds the pinned Router natively on macOS arm64, compares policies on two workers, runs the million-request baseline, and uploads `results/` as an artifact.
