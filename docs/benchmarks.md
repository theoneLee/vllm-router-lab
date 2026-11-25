# Benchmarks

English | [简体中文](benchmarks.zh-CN.md)

Recorded on 2026-11-24. Every number here links to its raw JSON under [`evidence/`](../evidence).

## Before you read the numbers

- All inference is simulated on CPU. Throughput reflects HTTP forwarding, cache lookups, and Go and Rust scheduling with short responses. It says nothing about GPU capacity.
- "Workers" means independent HTTP addresses. They may share one host process.
- Throughput is successful client requests divided by wall-clock time.
- A single run is good for capacity checks and catching regressions. To rank policies, repeat on your target machine with background load under control and a real trace.

## Correctness

Router pinned at [`159dcfc`](https://github.com/vllm-project/router/tree/159dcfcfcd717765d48b2ad24267f2a073d2501b). The Linux matrix ran the Go workers with the race detector on.

For each of ten vLLM tags, the lab's MessagePack frames were decoded with that tag's own Python event types, and parent-child block hashes were checked with that tag's own hash functions. Both byte and integer hash forms passed for all ten tags. A real Router then ran the two-rank test against each profile: hot-prefix routing, replay after a missed clear, and LRU eviction revoking old credit. All ten passed. [Per-version results](../evidence/correctness/matrix.json) · [Two-rank assertions](../evidence/correctness/kv.json)

| vLLM tag | SHA-256 CBOR root without a seed | Event source | `BlockStored` changes |
| --- | --- | --- | --- |
| 0.26.0, 0.27.0, 0.27.1, 0.28.0 | Random (positive test sets seed 0) | Static config | Has group, spec, locality |
| 0.29.0 | Fixed | Static config | Adds optional `ownership` |
| 0.30.0 | Fixed | Static config | Adds optional `session_id` |
| 0.31.0, 0.32.0 | Fixed | HTTP discovery | No new fields |
| 0.33.0, 0.34.0 | Fixed | HTTP discovery | Adds optional `lookahead_token_ids` |

These also passed: adding and removing workers, re-registering a worker restarted on the same address, client-cancelled SSE, an upstream cut, and the replay client exiting non-zero on a truncated stream. [Scaling](../evidence/correctness/scale.json) · [Streaming](../evidence/correctness/stream.json)

### Router issue #619: two failures, kept on purpose

- **At startup:** the worker confirmed it reported `sha256` (incompatible) for rank 1 to the Router. The Router still counted a prefix hit and routed to rank 1. [Startup report](../evidence/correctness/619-startup.json)
- **At runtime:** after rank 1's algorithm changed, the Router's metrics read count stayed at 1 for the whole 45-second window, and rank 1's old prefix still got credit. This shows no re-read happened within that window, nothing more. [Runtime report](../evidence/correctness/619.json)

The fault injection keeps rank 1's events matchable on purpose, so the test isolates one question: does the Router trust a rank whose metadata changed? At this commit, `EngineCacheConfig` doesn't store the algorithm label either ([parser source](https://github.com/vllm-project/router/blob/159dcfcfcd717765d48b2ad24267f2a073d2501b/src/kv_index/source/discovery/metrics_meta.rs)). A fix should keep rank 0's credit and leave rank 1 routable for normal requests.

## Throughput

### macOS arm64: 100 workers, 1,000,000 requests

GitHub-hosted macOS 15 arm64 runner. Native Router `159dcfc` (debug build, no debug symbols) and the Go fleet, `round_robin`, concurrency 128.

| Client success / worker received | Errors | Per worker | Time | Throughput | E2E p99 |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1,000,000 / 1,000,000 | 0 | exactly 10,000 | 116.2 s | 8,608 req/s | 33.8 ms |

The same CI job also ran 1,000 streaming requests per policy on two workers. [Raw JSON](../evidence/mac/million/round_robin.json) · [Policy runs](../evidence/mac/policies/) · [CI run](https://github.com/theoneLee/vllm-router-lab/actions/runs/45414886888) · [Workflow](../.github/workflows/mac-lab.yml)

### Linux x86_64: 100 workers, 1,000,000 requests per policy

Official Router v0.1.17 wheel, concurrency 128, seed 42. Each request has 33 prompt tokens drawn from 32 shared prefixes, with the last token unique. One output token, non-streaming, zero simulated prefill and decode cost. Each policy ran fresh processes on the same request sequence. This run checks request accounting and how requests spread across workers. Part of it overlapped with small functional tests, so treat the throughput as approximate.

| Policy | Client success / worker received | Errors | Workers that got traffic | Throughput | E2E p99 |
| --- | ---: | ---: | ---: | ---: | ---: |
| [round_robin](../evidence/linux-million/round_robin.json) | 1,000,000 / 1,000,000 | 0 | 100 | 19,557 req/s | 22.6 ms |
| [cache_aware](../evidence/linux-million/cache_aware.json) | 1,000,000 / 1,000,000 | 0 | 27 | 11,735 req/s | 21.3 ms |
| [kv_aware](../evidence/linux-million/kv_aware.json) | 1,000,000 / 1,000,000 | 0 | 42 | 6,746 req/s | 55.5 ms |

Note that the macOS run used a debug build of `main` and the Linux run used a release wheel. Compare each against its own baseline, not against each other.

### Linux: streaming under cache pressure

Run after the million-request runs, using a debug build of the Router's `main` branch. 4 workers, concurrency 32, 5,000 requests per policy per trial, 3 trials, 4 output tokens. Each rank holds only 12 blocks, while the 32 hot two-block prefixes need 64. Prefill costs 80 µs per uncached token and decode 1 ms per token. Client and worker counts matched in every trial. [Profile](../examples/contention.json) · [Per-trial JSON](../evidence/streaming/)

Medians of three trials. The TTFT column is the median of each trial's p95.

| Policy | Throughput | TTFT p95 | Cached prompt tokens |
| --- | ---: | ---: | ---: |
| round_robin | 2,450 req/s | 10.37 ms | 17.9% |
| cache_aware | 2,492 req/s | 12.24 ms | 72.6% |
| kv_aware | 2,367 req/s | 11.42 ms | 40.9% |

Cache reuse and tail latency rank the policies differently here. Look at both before picking one.

## Why the load generator is written in Go

A Python worker would work fine for a one-worker regression, and it would be easier to debug. The load generator is different: it needs enough headroom to drive the Router at the target rate. On a single event loop, CPU work delays both sending the next request and reading SSE chunks, and that delay leaks into the measured TTFT.

To check, we replayed the same 20,000 non-streaming requests at concurrency 128 against release Router v0.1.17, 100 Go workers and `round_robin`, on Linux x86_64. Three trials per client, in the order Go/Python, Python/Go, Go/Python. Every worker received exactly 200 requests per trial.

| Replay client | Median successful req/s |
| --- | ---: |
| Go (`routerlab replay`) | 18,958 |
| CPython 3.12.14, one asyncio loop, aiohttp 3.14.5 | 6,416 |

These are end-to-end rates for these two clients at saturation. The workers were Go in both cases, so this result says nothing about the cost of a Python fleet. With long decode delays or a low request rate, either client has enough headroom. [Raw trials](../evidence/replay-clients/summary.json) · [Experiment script](experiments/replay_clients.py)

To repeat it, install `aiohttp==3.14.5`, build `routerlab`, and run from the repository root:

```sh
python docs/experiments/replay_clients.py --router vllm-router --router-version v0.1.17
```
