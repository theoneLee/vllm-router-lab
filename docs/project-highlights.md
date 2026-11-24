# vLLM Router Lab engineering reference

vLLM Router Lab tests a real Router binary against CPU-hosted workers whose cache contents the experiment controls. A response can succeed even when the Router credits the wrong data-parallel (DP) rank. The lab checks three observations together: the Router's prefix lookup, the rank that actually served the request, and the number of cached prompt tokens at that rank.

## A regression you can reason about

Warm prefix A on rank 0 and prefix B on rank 1. Freeze cache writes while probing both prefixes through the Router. Freezing matters because a misrouted probe could otherwise warm the wrong rank and hide the original mistake.

Now clear rank 1. Keep its clear event in replay history but suppress the live notification. Store a new prefix C on that rank so the Router sees a gap in the sequence numbers. The scenario passes when the Router requests replay, loses credit for B, gains credit for C, and keeps credit for A. A separate eviction check fills the same rank's LRU and checks that the old prefix disappears. [Scenario implementation](../scripts/kv_scenarios.py) · [Recorded assertions](../evidence/correctness/kv.json)

## Where each observation comes from

| Component | Responsibility | Starting point |
| --- | --- | --- |
| Upstream Rust Router | Discovers workers, consumes KV events, and makes placement decisions. Supply a release or a candidate PR binary on the command line. | [First dual-rank run](../README.md) |
| Go worker fleet | Opens an independent HTTP address per worker. Each rank has its own concurrency slots, block cache and event history. Multiple addresses can share one host process. | [Fleet](../fleet.go) · [HTTP serving](../worker_http.go) |
| Cache and event stream | The LRU emits typed store, remove and clear events. At the wire boundary, the encoder represents each block hash as either 32 bytes or its low 64 bits. The feed assigns sequence numbers and retains serialized batches for replay. | [Cache](../cache.go) · [Encoder](../event_wire.go) · [Feed](../feed.go) |
| Replay client | Sends seeded workloads or timestamped JSONL traces, records dispatch lag and client latency, and treats truncated SSE streams as errors. | [Client](../replay.go) · [Example trace](../examples/trace.jsonl) |
| Python test harness | Starts fresh processes, injects failures, reads observations from both sides and writes a JSON verdict. Scenarios can prepare worker metadata before the Router starts. | [Environment](../scripts/environment.py) · [Scenario registry](../scripts/regress.py) |

## Protocol compatibility

The recorded matrix covers ten stable vLLM worker profiles from v0.26.0 through v0.34.0, including patch release v0.27.1. It pins an upstream commit per profile and the Router under test separately.

1. The source audit records discovery, hash-root and event-schema differences. A changed source expression calls for review.
2. The wire test loads the pinned tag's Python event types and hashing functions. It decodes Go-generated MessagePack and checks chained block hashes, store/remove/clear events, rank, replay order and the end marker. Both byte and integer encodings are covered.
3. The placement test drives a real Router with byte hashes. Older profiles receive statically configured event addresses; newer profiles use HTTP discovery. The positive test configures a shared seed for versions with a random default root.

This matrix exercises token-ID completions and a single full-attention cache group. Testing hybrid attention, other hash algorithms end to end, tensor transfer or model execution requires additional fixtures or a real engine. [Version manifest](../protocol/versions.json) · [Matrix results](../evidence/correctness/matrix.json) · [Maintenance runbook](../RUNBOOK.md)

## What the experiments established

| Experiment | Observation | What it measures |
| --- | --- | --- |
| macOS arm64, 100 worker addresses, round robin | All 1,000,000 HTTP requests completed. Each worker counted exactly 10,000. The recorded run reached 8,608 req/s with 33.84 ms end-to-end p99. | Client and backend accounting through the native Router on one host. |
| Linux, 100 workers, three policies | Round robin, cache-aware and KV-aware each completed 1,000,000 requests with matching backend totals. | Policy-specific distribution under the same seeded workload. |
| Linux streaming under cache pressure | Median across three runs: cache-aware reused 72.6% of input tokens with a 12.24 ms p95 time to first token. Round robin reused 17.9% with a 10.37 ms p95. | Cache reuse and user-visible latency can rank policies differently for the same workload. |

The runs use simulated prefill and decode costs as specified in each profile. Hardware inference capacity requires separate GPU measurements. [Benchmark settings and raw JSON](../evidence/README.md)

Two failing tests also serve as acceptance cases for Router issue #619. At pinned Router commit `159dcfc`, rank 1 advertised an incompatible hash algorithm at startup and still received prefix credit. In a separate runtime test the metadata read count stayed at one throughout the 45-second observation window. The fixture deliberately preserves otherwise matchable events so the assertions isolate metadata trust. [Startup observations](../evidence/correctness/619-startup.json) · [Runtime observations](../evidence/correctness/619.json)

## Could the lab be all Python?

Yes. A Python worker can use asyncio for the HTTP endpoints, pyzmq for the event stream, and the same CBOR, MessagePack and hash libraries used in the compatibility tests. For a one-worker regression, Python would simplify debugging and remove a toolchain. If the worker reuses upstream code, keep independent wire fixtures so a shared encoding error cannot validate itself.

Load generation has a different requirement. The client needs enough headroom to drive the Router at the requested rate. CPU work on a single event loop can delay sending the next request or consuming an SSE chunk. Such delay contributes to client-observed TTFT. Multiple worker processes can distribute the work, with additional choices about rank ownership, port allocation and metric aggregation.

We tested the replay client separately. On Linux x86_64 we kept the same Go fleet, release Router v0.1.17, 100 HTTP addresses and round-robin policy. Each trial replayed the same 20,000 non-streaming requests at concurrency 128. Every worker received exactly 200 requests per trial. Three trials per client, ordered Go/Python, Python/Go, Go/Python, gave these median rates:

| Replay client | Successful requests/s |
| --- | ---: |
| Go lab CLI | 18,958 |
| CPython 3.12.14, one asyncio loop, aiohttp 3.14.5 | 6,416 |

These are end-to-end rates for two specific client implementations under saturation. Both runs used a Go worker, so the result establishes a reason to retain the Go load generator. It leaves the throughput and memory cost of a Python fleet open. For an experiment with long decode delays or a low target request rate, both clients can have adequate headroom. [Raw trials](../evidence/replay-clients/summary.json) · [Experiment source](experiments/replay_clients.py)

To repeat the comparison, build the lab CLI, install `aiohttp==3.14.5` in the experiment's Python environment, and run from the repository root:

```sh
go build -o routerlab .
python docs/experiments/replay_clients.py --router vllm-router --router-version v0.1.17
```
