# Reference

English | [简体中文](reference.zh-CN.md)

## `routerlab fleet`

Starts the fake workers. Each worker gets its own HTTP listener on `127.0.0.1`, starting at `-port`.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-workers` | `2` | Number of workers (independent HTTP listeners). |
| `-ranks` | `1` | DP ranks per worker. |
| `-port` | `18000` | Port of the first worker. Worker *i* listens on `port + i`. |
| `-slots` | `16` | Sequences a rank runs at once. Extra requests wait. |
| `-blocks` | `256` | Prefix cache capacity per rank, in blocks. |
| `-block-size` | `16` | Tokens per block. |
| `-prefill` | `0` | Simulated cost per uncached prompt token, e.g. `80us`. |
| `-decode` | `0` | Delay between output tokens, e.g. `1ms`. |
| `-delay` | `0` | Fixed delay added to every response. |
| `-tick` | `0` | Continuous-batching iteration period. `0` turns batching off. |
| `-batch-tokens` | `128` | Token budget shared by all sequences in one iteration. |
| `-events` | `true` | Publish ZMQ KV events. |
| `-replay-window` | `128` | Event batches kept per rank for replay. |
| `-vllm-version` | unset | Act like a specific vLLM release from [`protocol/versions.json`](../protocol/versions.json). Unset means v0.34.0 behaviour plus the developer `/server_info` endpoints. |
| `-hash-seed` | unset | Numeric seed for the SHA-256 CBOR root hash, like `PYTHONHASHSEED`. vLLM v0.26–v0.28 pick a random root when it's unset. |
| `-int-hashes` | `false` | Send block hashes as their low 64 bits (vLLM's integer form) instead of 32 bytes. |
| `-metrics-only` | `false` | Hide `/get_server_info` so the Router has to read cache metadata from `/metrics`. |

## `routerlab replay`

Sends load and prints one JSON summary to stdout. It exits non-zero if any request fails.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-url` | `http://127.0.0.1:30000` | Target, usually the Router. |
| `-n` | `1000` | Number of generated requests. |
| `-concurrency` | `64` | Maximum requests in flight. |
| `-rate` | `0` | Scheduled requests per second. `0` sends as fast as concurrency allows. |
| `-seed` | `42` | Seed for the generated workload. |
| `-stream` | `false` | Generate streaming (SSE) requests. |
| `-output` | `1` | Output tokens per generated request. |
| `-trace` | unset | Replay a JSONL trace instead of generating requests. |

The generated workload shares prefixes between requests, so cache-aware policies have something to find.

### Trace format

One JSON object per line, sorted by `at_ms`:

| Field | Type | Meaning |
| --- | --- | --- |
| `at_ms` | number | Send time relative to the start of the run. |
| `prompt` | int array | Token IDs. Must not be empty. |
| `max_tokens` | int | Output length, 1–16384. |
| `stream` | bool | Use SSE. |

### Output fields

| Field | Meaning |
| --- | --- |
| `requests`, `success`, `errors`, `first_error` | Request accounting. A stream that ends without `[DONE]` counts as an error. |
| `seconds`, `success_rps` | Wall-clock time and successful requests per second. |
| `ttft_ms` | Time from sending the request to the first non-empty text chunk. Includes queueing. |
| `e2e_ms` | Time from sending the request to the end of the response. |
| `inter_chunk_ms` | Each request's average gap between text chunks, then percentiles across requests. |
| `dispatch_lag_ms` | How far actual send times fell behind the schedule. A large value means the client, not the Router, was the bottleneck. |

Latency fields hold `p50`, `p95` and `p99`. `scripts/compare.py` adds `policy`, `worker_counts`, `backend_received`, `prompt_tokens`, `cached_prompt_tokens`, `cached_fraction` and the run settings.

## Worker HTTP endpoints

Pick a rank with the `X-Data-Parallel-Rank` header. Without it, the request goes to rank 0. The Router sets this header when you start it with `--intra-node-data-parallel-size`.

Completion responses include `X-Lab-Worker` and `X-Lab-Rank` headers. Non-streaming responses also report `lab_rank` and `usage.prompt_tokens_details.cached_tokens`, so you can see where a request landed and how much of its prompt hit the cache.

### vLLM-compatible

| Endpoint | Notes |
| --- | --- |
| `POST /v1/completions` | Token-ID `prompt`, `max_tokens`, optional `stream`. Text in the response is a placeholder. |
| `GET /health`, `/health_generate`, `/ready` | Always 200. |
| `GET /version`, `/v1/models` | Profile version and a single model named `lab`. |
| `GET /get_model_info`, `/get_server_info`, `/model_info`, `/server_info` | Developer endpoints. 404 when `-vllm-version` is set, matching a release deployment. |
| `GET /kv_event_sources` | Per-rank ZMQ endpoints and topics. 404 for profiles before v0.31.0, which had no HTTP discovery. |
| `GET /metrics` | Prometheus text, see below. |

### Lab controls

These act on the rank selected by `X-Data-Parallel-Rank`. Control endpoints take `POST` and return 204.

| Endpoint | Effect |
| --- | --- |
| `GET /lab/state` | Counters for the rank: `received`, `hits`, `active`, `waiting`, `canceled`, `broken`, `metrics_reads`, `next_seq`, `replays`. |
| `GET /lab/event-sources` | Same body as `/kv_event_sources`, available for every profile. Used to configure static event sources for old versions. |
| `POST /lab/clear` | Empty the rank's cache and emit an `AllBlocksCleared` event. |
| `POST /lab/freeze?value=1` | Stop writing to the cache (`value=0` resumes). Lookups still count hits. |
| `POST /lab/drop-next` | Keep the next event batch in replay history but don't publish it. The Router sees a sequence gap. |
| `POST /lab/expire-replay` | Throw away the replay history. |
| `POST /lab/cut?value=N` | Cut the next streamed response after N chunks, without `[DONE]`. The `X-Lab-Cut-After: N` request header does the same for a single request. |
| `POST /lab/algorithm?value=NAME` | Change the `prefix_caching_hash_algo` label the rank reports in `/metrics`. |

### Metrics

Every series has an `engine="<rank>"` label.

| Series | Meaning |
| --- | --- |
| `vllm:cache_config_info` | Block size, prefix caching flag and `prefix_caching_hash_algo`. |
| `vllm:prefix_cache_hits_total` | Prompt tokens served from cache. |
| `vllm:num_requests_running`, `vllm:num_requests_waiting` | Current load. |
| `vllm:kv_cache_usage_perc` | Used blocks / capacity. |
| `lab_requests_total`, `lab_prompt_tokens_total` | Requests and prompt tokens received. |
| `lab_canceled_total`, `lab_broken_total` | Streams cancelled by the client or cut by the worker. |
| `lab_metrics_reads_total` | How many times `/metrics` was scraped. Shows whether the Router re-reads metadata. |

## KV events

Each rank binds a ZMQ PUB socket and a ROUTER (replay) socket on random local ports. Find them through `/kv_event_sources` or `/lab/event-sources`.

- Live frames: `topic`, 8-byte big-endian sequence number, MessagePack batch.
- Replay request: the standard vLLM DEALER request (empty delimiter frame, 8-byte start sequence). The reply is every retained batch from that sequence, then an end marker with an empty topic and sequence `2^64-1`.
- Events: `BlockStored` with parent hash and token IDs, `BlockRemoved` on LRU eviction, `AllBlocksCleared`.
- Block hash: SHA-256 over CBOR of `(parent_hash, token_ids, extra_keys)`, chained from a root hash, the same way vLLM computes `sha256_cbor`.
