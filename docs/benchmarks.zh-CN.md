# 基准测试

[English](benchmarks.md) | 简体中文

> 本文是英文版的中文对照，内容以 [英文版](benchmarks.md) 为准。

记录于 2026-11-24。这里的每个数字都链接到 [`evidence/`](../evidence) 下的原始 JSON。

## 看数据之前

- 所有推理都是 CPU 上的模拟。吞吐体现的是 HTTP 转发、缓存查找和 Go/Rust 调度在短响应下的表现，跟 GPU 能力没有关系。
- “Worker 数”指独立的 HTTP 地址，可能同属一个进程。
- 吞吐 = 客户端成功请求数 / 墙钟时间。
- 单次运行适合做容量检查和发现回归。要给策略排名，请在目标机器上重复测试，控制好后台负载，并使用真实 trace。

## 正确性

Router 固定在 [`159dcfc`](https://github.com/vllm-project/router/tree/159dcfcfcd717765d48b2ad24267f2a073d2501b)。Linux 这轮矩阵的 Go Worker 开了 race 检测。

对十个 vLLM tag，分别用该 tag 自己的 Python 事件类型解码 lab 发出的 MessagePack 帧，再用该 tag 自己的哈希函数校验父子 block 哈希。十个 tag 的字节和整数两种哈希形式全部通过。随后用真实 Router 对每个 profile 跑双 rank 测试：热前缀选路、遗漏 clear 后的 replay、LRU 淘汰撤销旧信用，十个全部通过。[逐版本结果](../evidence/correctness/matrix.json) · [双 rank 断言](../evidence/correctness/kv.json)

| vLLM tag | 不设种子时的 SHA-256 CBOR 根 | 事件源 | `BlockStored` 变化 |
| --- | --- | --- | --- |
| 0.26.0、0.27.0、0.27.1、0.28.0 | 随机（正向测试设种子为 0） | 静态配置 | 已有 group、spec、locality |
| 0.29.0 | 固定 | 静态配置 | 新增可选 `ownership` |
| 0.30.0 | 固定 | 静态配置 | 新增可选 `session_id` |
| 0.31.0、0.32.0 | 固定 | HTTP discovery | 无新字段 |
| 0.33.0、0.34.0 | 固定 | HTTP discovery | 新增可选 `lookahead_token_ids` |

以下也都通过：增删 Worker、Worker 在同一地址重启后重新注册、客户端取消 SSE、上游截断，以及回放客户端遇到截断流时以非零状态退出。[伸缩](../evidence/correctness/scale.json) · [流式](../evidence/correctness/stream.json)

### Router issue #619：两个刻意保留的失败

- **启动时：** Worker 确认已经把 rank 1 的 `sha256`（不兼容）上报给 Router，Router 仍然给这个前缀记了命中并选了 rank 1。[启动报告](../evidence/correctness/619-startup.json)
- **运行中：** rank 1 的算法改了以后，在整整 45 秒的观察窗口内，Router 的指标读取次数一直是 1，rank 1 的旧前缀照样被记命中。这只能说明窗口内没有发生重读，结论仅限于此。[运行时报告](../evidence/correctness/619.json)

故障注入有意让 rank 1 的事件保持可匹配，这样测试只回答一个问题：某个 rank 的元数据变了，Router 还信不信它？在这个提交上，`EngineCacheConfig` 也没有保存算法标签（[解析器源码](https://github.com/vllm-project/router/blob/159dcfcfcd717765d48b2ad24267f2a073d2501b/src/kv_index/source/discovery/metrics_meta.rs)）。正确的修复应该保留 rank 0 的信用，并且 rank 1 仍能处理普通请求。

## 吞吐

### macOS arm64：100 个 Worker，100 万次请求

GitHub 托管的 macOS 15 arm64 runner。原生编译的 Router `159dcfc`（debug 构建，不带调试符号）加 Go fleet，`round_robin`，并发 128。

| 客户端成功 / Worker 收到 | 错误 | 每个 Worker | 耗时 | 吞吐 | 端到端 p99 |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1,000,000 / 1,000,000 | 0 | 正好 10,000 | 116.2 s | 8,608 req/s | 33.8 ms |

同一个 CI 任务还在两个 Worker 上让每种策略各跑了 1,000 次流式请求。[原始 JSON](../evidence/mac/million/round_robin.json) · [策略数据](../evidence/mac/policies/) · [CI 记录](https://github.com/theoneLee/vllm-router-lab/actions/runs/45414886888) · [Workflow](../.github/workflows/mac-lab.yml)

### Linux x86_64：100 个 Worker，每种策略 100 万次

官方 Router v0.1.17 wheel，并发 128，种子 42。每个请求 33 个 prompt token，取自 32 种共享前缀，最后一个 token 各不相同。输出 1 个 token，非流式，prefill 和 decode 模拟耗时都是零。每种策略都用全新进程跑同一个请求序列。这一轮主要验证请求计数和请求在 Worker 间的分布。部分时段和小规模功能测试重叠，吞吐数值仅供参考。

| 策略 | 客户端成功 / Worker 收到 | 错误 | 收到过请求的 Worker | 吞吐 | 端到端 p99 |
| --- | ---: | ---: | ---: | ---: | ---: |
| [round_robin](../evidence/linux-million/round_robin.json) | 1,000,000 / 1,000,000 | 0 | 100 | 19,557 req/s | 22.6 ms |
| [cache_aware](../evidence/linux-million/cache_aware.json) | 1,000,000 / 1,000,000 | 0 | 27 | 11,735 req/s | 21.3 ms |
| [kv_aware](../evidence/linux-million/kv_aware.json) | 1,000,000 / 1,000,000 | 0 | 42 | 6,746 req/s | 55.5 ms |

注意 macOS 用的是 `main` 的 debug 构建，Linux 用的是正式版 wheel。两组数据各自和自己的基线比，不要互相比。

### Linux：缓存压力下的流式负载

在百万次测试之后单独运行，用的是 Router `main` 分支的 debug 构建。4 个 Worker，并发 32，每种策略每轮 5,000 个请求，共 3 轮，输出 4 个 token。每个 rank 只能放 12 个 block，而 32 种双 block 热前缀需要 64 个。每个未命中 token 的 prefill 耗时 80 µs，decode 每个 token 1 ms。每一轮客户端和 Worker 的计数都一致。[profile](../examples/contention.json) · [逐轮 JSON](../evidence/streaming/)

下表取三轮中位数。TTFT 一列是三轮各自 p95 的中位数。

| 策略 | 吞吐 | TTFT p95 | prompt token 缓存命中率 |
| --- | ---: | ---: | ---: |
| round_robin | 2,450 req/s | 10.37 ms | 17.9% |
| cache_aware | 2,492 req/s | 12.24 ms | 72.6% |
| kv_aware | 2,367 req/s | 11.42 ms | 40.9% |

在这里，按缓存复用和按尾部时延排出来的策略顺序不一样。选策略前两个都要看。

## 为什么压测客户端用 Go 写

单 Worker 的回归用 Python 写 Worker 完全没问题，调试起来还更方便。压测客户端则不同：它得有足够的余量，才能按目标速率压 Router。单个事件循环上的 CPU 开销会同时拖慢下一个请求的发送和 SSE chunk 的读取，这部分延迟会混进测出来的 TTFT。

为了验证这一点，我们在 Linux x86_64 上对正式版 Router v0.1.17、100 个 Go Worker、`round_robin` 回放了同样的 20,000 个非流式请求，并发 128。每个客户端各跑三轮，顺序是 Go/Python、Python/Go、Go/Python。每一轮每个 Worker 都正好收到 200 次请求。

| 回放客户端 | 成功 req/s 中位数 |
| --- | ---: |
| Go（`routerlab replay`） | 18,958 |
| CPython 3.12.14，单个 asyncio 事件循环，aiohttp 3.14.5 | 6,416 |

这是这两个客户端在饱和状态下的端到端速率。两组测试的 Worker 都是 Go 写的，所以这个结果并不能说明 Python 版 fleet 的开销。如果 decode 延迟较长或者请求速率较低，两个客户端的余量都够用。[原始数据](../evidence/replay-clients/summary.json) · [实验脚本](experiments/replay_clients.py)

想复现的话，先装好 `aiohttp==3.14.5`、构建 `routerlab`，然后在仓库根目录运行：

```sh
python docs/experiments/replay_clients.py --router vllm-router --router-version v0.1.17
```
