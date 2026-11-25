# 参考

[English](reference.md) | 简体中文

> 本文是英文版的中文对照，内容以 [英文版](reference.md) 为准。

## `routerlab fleet`

启动假 Worker。每个 Worker 在 `127.0.0.1` 上有自己的 HTTP 监听地址，从 `-port` 开始依次递增。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `-workers` | `2` | Worker 数量（独立的 HTTP 监听地址）。 |
| `-ranks` | `1` | 每个 Worker 的 DP rank 数。 |
| `-port` | `18000` | 第一个 Worker 的端口，第 *i* 个 Worker 监听 `port + i`。 |
| `-slots` | `16` | 每个 rank 同时运行的序列数，多出来的请求排队等待。 |
| `-blocks` | `256` | 每个 rank 的前缀缓存容量，单位是 block。 |
| `-block-size` | `16` | 每个 block 的 token 数。 |
| `-prefill` | `0` | 每个未命中 prompt token 的模拟耗时，例如 `80us`。 |
| `-decode` | `0` | 输出 token 之间的间隔，例如 `1ms`。 |
| `-delay` | `0` | 每个响应额外加的固定延迟。 |
| `-tick` | `0` | continuous batching 的迭代周期，`0` 表示关闭。 |
| `-batch-tokens` | `128` | 一次迭代内所有序列共享的 token 预算。 |
| `-events` | `true` | 是否发布 ZMQ KV 事件。 |
| `-replay-window` | `128` | 每个 rank 保留多少个事件批次用于 replay。 |
| `-vllm-version` | 不设置 | 模拟 [`protocol/versions.json`](../protocol/versions.json) 里的某个 vLLM 版本。不设置时按 v0.34.0 的行为，并额外开启开发用的 `/server_info` 系列接口。 |
| `-hash-seed` | 不设置 | SHA-256 CBOR 根哈希的数字种子，作用同 `PYTHONHASHSEED`。vLLM v0.26–v0.28 在不设置时使用随机根。 |
| `-int-hashes` | `false` | 用低 64 位（vLLM 的整数形式）发送 block 哈希，而不是 32 字节。 |
| `-metrics-only` | `false` | 隐藏 `/get_server_info`，迫使 Router 从 `/metrics` 读缓存元数据。 |

## `routerlab replay`

发送负载，并向 stdout 输出一段 JSON 汇总。只要有请求失败，就以非零状态退出。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `-url` | `http://127.0.0.1:30000` | 目标地址，通常是 Router。 |
| `-n` | `1000` | 生成的请求数。 |
| `-concurrency` | `64` | 最多同时在途的请求数。 |
| `-rate` | `0` | 计划发送速率（请求/秒），`0` 表示在并发上限内尽快发送。 |
| `-seed` | `42` | 生成负载用的随机种子。 |
| `-stream` | `false` | 生成流式（SSE）请求。 |
| `-output` | `1` | 每个生成请求的输出 token 数。 |
| `-trace` | 不设置 | 回放 JSONL trace，不再生成请求。 |

生成的负载在请求之间共享前缀，这样缓存感知策略才有东西可以命中。

### trace 格式

每行一个 JSON 对象，按 `at_ms` 排序：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `at_ms` | number | 相对于开始时间的发送时刻。 |
| `prompt` | int 数组 | token ID，不能为空。 |
| `max_tokens` | int | 输出长度，1–16384。 |
| `stream` | bool | 是否使用 SSE。 |

### 输出字段

| 字段 | 含义 |
| --- | --- |
| `requests`、`success`、`errors`、`first_error` | 请求计数。没有收到 `[DONE]` 就结束的流记为错误。 |
| `seconds`、`success_rps` | 墙钟耗时和每秒成功请求数。 |
| `ttft_ms` | 从发出请求到收到第一个非空文本 chunk 的时间，包含排队时间。 |
| `e2e_ms` | 从发出请求到响应结束的时间。 |
| `inter_chunk_ms` | 先算每个请求文本 chunk 之间的平均间隔，再在所有请求上取分位数。 |
| `dispatch_lag_ms` | 实际发送时间比计划晚了多少。这个值很大，说明瓶颈在客户端，不在 Router。 |

时延字段包含 `p50`、`p95`、`p99`。`scripts/compare.py` 还会补充 `policy`、`worker_counts`、`backend_received`、`prompt_tokens`、`cached_prompt_tokens`、`cached_fraction` 以及本次运行的参数。

## Worker HTTP 接口

用 `X-Data-Parallel-Rank` 请求头选择 rank，不带时落到 rank 0。Router 用 `--intra-node-data-parallel-size` 启动时会带上这个请求头。

completion 响应会带 `X-Lab-Worker` 和 `X-Lab-Rank` 响应头。非流式响应还会返回 `lab_rank` 和 `usage.prompt_tokens_details.cached_tokens`，能直接看出请求落在哪里、prompt 命中了多少缓存。

### 兼容 vLLM 的接口

| 接口 | 说明 |
| --- | --- |
| `POST /v1/completions` | token-ID 形式的 `prompt`、`max_tokens`，可选 `stream`。响应里的文本是占位内容。 |
| `GET /health`、`/health_generate`、`/ready` | 总是返回 200。 |
| `GET /version`、`/v1/models` | profile 版本号，以及一个名为 `lab` 的模型。 |
| `GET /get_model_info`、`/get_server_info`、`/model_info`、`/server_info` | 开发用接口。设置了 `-vllm-version` 时返回 404，和正式部署保持一致。 |
| `GET /kv_event_sources` | 每个 rank 的 ZMQ 地址和 topic。v0.31.0 之前的 profile 没有 HTTP discovery，返回 404。 |
| `GET /metrics` | Prometheus 文本格式，见下文。 |

### 实验控制接口

作用于 `X-Data-Parallel-Rank` 选中的 rank。控制接口用 `POST`，成功返回 204。

| 接口 | 效果 |
| --- | --- |
| `GET /lab/state` | 该 rank 的计数：`received`、`hits`、`active`、`waiting`、`canceled`、`broken`、`metrics_reads`、`next_seq`、`replays`。 |
| `GET /lab/event-sources` | 内容和 `/kv_event_sources` 相同，所有 profile 都可用。老版本用它配置静态事件源。 |
| `POST /lab/clear` | 清空该 rank 的缓存，并发出一条 `AllBlocksCleared` 事件。 |
| `POST /lab/freeze?value=1` | 停止写缓存（`value=0` 恢复）。查找仍会统计命中。 |
| `POST /lab/drop-next` | 下一个事件批次照常进 replay 历史，但不发布。Router 会看到序号缺口。 |
| `POST /lab/expire-replay` | 丢弃 replay 历史。 |
| `POST /lab/cut?value=N` | 把下一个流式响应在第 N 个 chunk 后截断，不发 `[DONE]`。请求头 `X-Lab-Cut-After: N` 对单个请求有同样效果。 |
| `POST /lab/algorithm?value=NAME` | 修改该 rank 在 `/metrics` 里上报的 `prefix_caching_hash_algo` 标签。 |

### 指标

每个序列都带 `engine="<rank>"` 标签。

| 序列 | 含义 |
| --- | --- |
| `vllm:cache_config_info` | block size、前缀缓存开关和 `prefix_caching_hash_algo`。 |
| `vllm:prefix_cache_hits_total` | 命中缓存的 prompt token 数。 |
| `vllm:num_requests_running`、`vllm:num_requests_waiting` | 当前负载。 |
| `vllm:kv_cache_usage_perc` | 已用 block / 容量。 |
| `lab_requests_total`、`lab_prompt_tokens_total` | 收到的请求数和 prompt token 数。 |
| `lab_canceled_total`、`lab_broken_total` | 被客户端取消、被 Worker 截断的流。 |
| `lab_metrics_reads_total` | `/metrics` 被抓取的次数，用来判断 Router 有没有重新读取元数据。 |

## KV 事件

每个 rank 在本地随机端口上绑定一个 ZMQ PUB socket 和一个 ROUTER（replay）socket，地址可以通过 `/kv_event_sources` 或 `/lab/event-sources` 查到。

- 实时帧：`topic`、8 字节大端序号、MessagePack 批次。
- replay 请求：标准的 vLLM DEALER 请求（空分隔帧 + 8 字节起始序号）。回复是从该序号起保留的所有批次，最后是一个结束标记，topic 为空、序号为 `2^64-1`。
- 事件类型：带父哈希和 token ID 的 `BlockStored`，LRU 淘汰时的 `BlockRemoved`，以及 `AllBlocksCleared`。
- block 哈希：对 `(parent_hash, token_ids, extra_keys)` 的 CBOR 编码做 SHA-256，从根哈希开始逐级链接，和 vLLM 计算 `sha256_cbor` 的方式一致。
