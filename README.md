# vLLM Router Lab

English | [简体中文](README.zh-CN.md)

Test the [vLLM Router](https://github.com/vllm-project/router) on your laptop. No GPU needed.

vLLM Router Lab runs a fleet of fake vLLM workers on CPU and puts the real, unmodified Router in front of them. You decide what sits in each worker's prefix cache, so you can check every routing decision against ground truth: which worker and data-parallel (DP) rank the Router picked, and whether the prefix it took credit for is really cached there.

Why bother? A request can return 200 even when the Router credits the wrong rank. Status codes won't show that. In production it shows up as lost cache hits and slower time to first token (TTFT). The lab makes this kind of bug visible and turns it into a repeatable test.

It's built for people who:

- develop or review Router routing policies,
- need to reproduce a control-plane bug (KV events, discovery, replay) without a GPU cluster,
- want a regression run before sending a PR upstream.

## Highlights

- **Real Router, fake engines.** Point the lab at any Router binary: a release, `main`, or your PR branch. Nothing on the Router side is mocked.
- **Checks both sides.** A scenario passes only when three observations agree: the Router's prefix-hit counter, the rank that actually served the request, and the cache hits recorded at that rank.
- **Speaks vLLM's KV-event protocol.** Each rank publishes on a ZMQ PUB socket and serves a replay socket. Batches are MessagePack with sequence numbers. Block hashes are chained SHA-256 CBOR, sent as 32 bytes or as the low 64 bits.
- **Ten vLLM versions.** Wire profiles cover v0.26.0 through v0.34.0. Each profile is verified by decoding the lab's frames with that tag's own Python event types and hash functions.
- **Fault injection.** Drop a cache-clear event, expire replay history, freeze cache writes, cut an SSE stream mid-response, or change the hash algorithm a rank advertises.
- **Scales on one machine.** One Go process serves 100 worker addresses. On a GitHub macOS arm64 runner, 1,000,000 requests went through the Router at 8,608 req/s, and every worker received exactly 10,000.
- **Policy comparisons.** Simulate prefill and decode cost, per-rank concurrency slots, cache capacity, and a continuous-batching token budget. Replay a seeded workload or your own JSONL trace, with every policy getting the same requests.

## How it works

```
                    HTTP                     HTTP
routerlab replay ─────────▶ vLLM Router ─────────▶ routerlab fleet
(load generator)          (real binary) ◀───────── workers 0..N, ranks 0..M
                                        ZMQ KV events  (cache + event feed per rank)

scripts/*.py   start fresh processes, inject faults through /lab/*,
               read /metrics and /lab/state, write a JSON verdict
```

| Piece | What it does |
| --- | --- |
| `routerlab fleet` | Opens one HTTP listener per worker. Each rank has its own concurrency slots, LRU block cache, ZMQ event feed and replay buffer. Serves vLLM's endpoints plus lab-only controls under `/lab/*`. |
| `routerlab replay` | Sends a seeded workload or a timestamped JSONL trace to `/v1/completions`. Reports TTFT, end-to-end latency, inter-chunk gap and dispatch lag. Treats a truncated SSE stream as an error. |
| `scripts/regress.py` | Black-box regression scenarios against a real Router. |
| `scripts/compare.py` | Runs the same workload under several routing policies and collects client- and worker-side numbers. |
| `scripts/matrix.py`, `scripts/audit.py` | Check the wire format against each pinned vLLM tag. See [Maintaining](docs/maintaining.md). |

## Scope

No model runs. Responses are placeholder text, and latency comes from the profile you set. Don't use the lab to measure GPU performance.

The lab covers token-ID completions, a single full-attention cache group, `sha256_cbor` hashing and block size 16. Hybrid attention, LoRA, multimodal input and KV tensor transfer are out of scope.

## Quick start

You need Go 1.25+ and Python 3.10+ (tested with 3.12). The regression and compare scripts use only the Python standard library.

### 1. Build the lab

```sh
git clone https://github.com/theoneLee/vllm-router-lab.git
cd vllm-router-lab
go test -race ./...
go build -o routerlab ./cmd/routerlab
```

### 2. Try the fleet on its own

```sh
./routerlab fleet -workers 2 -port 18000 -decode 1ms &
./routerlab replay -url http://127.0.0.1:18000 -n 200 -stream -output 4
curl -s http://127.0.0.1:18000/lab/state
kill %1
```

`replay` prints a JSON summary with success counts and latency percentiles. `/lab/state` shows how many requests rank 0 received and how many prompt tokens hit its cache.

### 3. Get a Router

**Linux, quickest:** install the release wheel. It gives you a `vllm-router` command.

```sh
pip install vllm-router==0.1.17
export ROUTER=vllm-router
```

**Any OS, from source:** this is the build the recorded regression results use. You need a Rust toolchain, a C++ compiler, CMake and `protoc`. The build uses PyO3, so if your default Python is newer than 3.12, set `PYO3_PYTHON` to the absolute path of a 3.12 interpreter.

```sh
git clone https://github.com/vllm-project/router.git ../vllm-router
git -C ../vllm-router checkout --detach 159dcfcfcd717765d48b2ad24267f2a073d2501b
(cd ../vllm-router && cargo build --locked --bin vllm-router)
export ROUTER=../vllm-router/target/debug/vllm-router
```

### 4. Run your first regression

```sh
python3 scripts/regress.py kv --router "$ROUTER" --version 159dcfc --worker-version 0.34.0
```

Open `results/regression/kv.json`. You should see `"status": "passed"`.

Here's what the scenario does. It warms a different prefix on rank 0 and rank 1, freezes cache writes, then probes both through the Router. Freezing matters: a misrouted probe could otherwise warm the wrong rank and hide the mistake. Next it clears rank 1 but drops the clear event from the live stream, then stores a new prefix, so the Router sees a gap in sequence numbers. The test passes when the Router asks for replay, drops credit for the old prefix, picks up the new one, and leaves rank 0's credit alone.

## Usage

### Regression scenarios

```sh
python3 scripts/regress.py <case> --router "$ROUTER" --version <router-commit-or-tag> [--worker-version 0.34.0]
```

| Case | What it checks |
| --- | --- |
| `kv` | Prefix routing across two ranks, recovery from a missed clear via replay, and LRU eviction revoking old credit. |
| `stream` | Client-cancelled SSE and an upstream cut both free their slots. The replay client exits non-zero on a truncated stream. |
| `scale` | Adding and removing workers at runtime, and re-registering a worker restarted on the same address. |
| `619-startup` | A rank advertises an incompatible hash algorithm before the Router starts. The Router should stop giving it prefix credit. |
| `619` | Same check, but the algorithm changes while the Router is running. |

The two `619` cases are acceptance tests for [Router issue #619](https://github.com/vllm-project/router/issues/619). They **fail on purpose** at the pinned commit `159dcfc` and should pass once a fix lands. Run them one at a time and read each JSON report.

Every run writes `results/regression/<case>.json` with the status, the checks that passed and the observed values, next to the Router and fleet logs. `--version` is a label of your choice. It goes into the report so you know which Router you tested.

### Compare routing policies

```sh
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --workers 4 --requests 5000 --concurrency 32 --stream --output 4 \
  --profile examples/contention.json --results results/contention
```

The script restarts the fleet and the Router for each policy: `round_robin`, `cache_aware` and `kv_aware` by default, or pick with `--policies`. Every policy gets the same seed or the same trace. Each policy writes one JSON file with client latency, request counts per worker, and `cached_fraction`, the share of prompt tokens that hit a worker's cache.

To push 1,000,000 requests through 100 workers, raise the file descriptor limit first:

```sh
ulimit -n 8192
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --workers 100 --requests 1000000 --concurrency 128 \
  --policies round_robin --results results/million
```

### Replay your own trace

```sh
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --trace examples/trace.jsonl --results results/trace
```

A trace is JSONL, one request per line, sorted by `at_ms`:

```json
{"at_ms": 0, "prompt": [1, 2, 3, 4], "max_tokens": 3, "stream": true}
```

`at_ms` is the send time relative to the start of the run. `prompt` is a non-empty array of token IDs.

### Simulate engine cost

A profile is a JSON object whose keys are `routerlab fleet` flags. [`examples/contention.json`](examples/contention.json) gives each rank room for only 12 blocks while the hot prefixes need 64, so the policies have to compete for cache:

```json
{"blocks": 12, "slots": 8, "prefill": "80us", "decode": "1ms", "tick": "1ms", "batch-tokens": 128}
```

### Wire it up by hand

You can also run each piece yourself:

```sh
./routerlab fleet -workers 2 -port 18000 &
$ROUTER --worker-urls http://127.0.0.1:18000 http://127.0.0.1:18001 \
  --policy cache_aware --host 127.0.0.1 --port 30000 &
./routerlab replay -url http://127.0.0.1:30000 -n 1000 -concurrency 32
```

All flags, endpoints and output fields are listed in the [reference](docs/reference.md).

## Results

| Run | Result |
| --- | --- |
| macOS arm64, 100 workers, round robin | 1,000,000 / 1,000,000 requests, 8,608 req/s, p99 33.8 ms |
| Linux x86_64, 100 workers, 3 policies | 1,000,000 requests per policy, client and worker counts match exactly |
| Streaming under cache pressure, median of 3 | `cache_aware` reused 72.6% of prompt tokens at p95 TTFT 12.2 ms. `round_robin` reused 17.9% at p95 TTFT 10.4 ms. |
| vLLM v0.26.0 – v0.34.0 | Wire decoding (byte and integer hashes) and two-rank Router tests pass for all ten versions |

The streaming row is why you should look at more than one metric: on this workload, the policy with the best cache reuse didn't have the best tail TTFT. Settings, caveats and raw JSON are in [Benchmarks](docs/benchmarks.md).

## Documentation

- [Reference](docs/reference.md): CLI flags, HTTP endpoints, metrics, output fields
- [Benchmarks](docs/benchmarks.md): recorded runs, how to read them, raw data
- [Maintaining](docs/maintaining.md): add a vLLM version, run the version matrix, debug a failure
- [Contributing](CONTRIBUTING.md)

## Repository layout

```
cmd/routerlab/     CLI entry point (one binary: routerlab)
internal/fleet/    Fake workers: HTTP endpoints, /lab controls, metrics, batching, version profiles
internal/kvcache/  Per-rank prefix cache, KV event encoding, ZMQ feed and replay
internal/replay/   Load generator and trace replay
scripts/           Python harness, scenarios, policy comparison, version matrix
protocol/          Pinned vLLM versions and their wire-format features
examples/          Sample trace and cost profile
evidence/          Raw JSON behind every number in the docs
docs/              Reference, benchmarks, maintenance guide
```

## License

[Apache License 2.0](LICENSE). This is an independent project. It isn't affiliated with vLLM and doesn't include code from vLLM or the vLLM Router.
