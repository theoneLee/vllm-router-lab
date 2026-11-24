# 验证记录 · 2026-11-24

## 十版本与正确性

真实 Router 固定在 [159dcfc](https://github.com/vllm-project/router/tree/159dcfcfcd717765d48b2ad24267f2a073d2501b)。十个版本指 **十套 vLLM Worker 协议 profile**。每套用该 tag 的 Python 类型解码 Go 发出的 MsgPack，再用该 tag 的 Python 哈希函数校验父子 block；随后启动真实 Rust Router，在两个模拟 DP rank 上跑请求。Linux 这轮矩阵的 Go Worker 使用 race 构建。

| vLLM tag | 未指定 seed 的 SHA-256 CBOR 根 | 事件源 | 该组源码可见的 BlockStored 变化 |
|---|---|---|---|
| 0.26.0、0.27.0、0.27.1、0.28.0 | 随机；正向回归显式设 0 | 静态配置 | 已含 group、spec、locality |
| 0.29.0 | 固定 | 静态配置 | 增加可选 ownership |
| 0.30.0 | 固定 | 静态配置 | 增加可选 session_id |
| 0.31.0、0.32.0 | 固定 | HTTP discovery | 上述字段延续 |
| 0.33.0、0.34.0 | 固定 | HTTP discovery | 增加可选 lookahead_token_ids |

十组字节哈希解码和十组整数哈希解码全部通过。真实 Router 的十组双 rank 实验全部通过，包括热前缀选路、遗漏 clear 后 replay、LRU 淘汰撤销旧信用。其他通过项包括显式增删 Worker、同地址重启后重新注册、客户端取消 SSE、上游中断、截断流的回放客户端以非零状态退出。[逐版本结果](correctness/matrix.json) · [双 rank 断言](correctness/kv.json) · [伸缩结果](correctness/scale.json) · [SSE 结果](correctness/stream.json)

**#619 保留两个失败断言，供上游补丁复测。** 启动时，Worker 确认已把 rank 1 的 `sha256` 标签返回给 Router，Router 仍给该前缀记 hit 并选了 rank 1。运行中切换标签时，45 秒观察窗口内 metrics 读取次数始终为 1，rank 1 的旧前缀仍记 hit；这条记录只证明该窗口内没有重读。注入故意保留可匹配事件，用于隔离控制面信任。此提交的 `EngineCacheConfig` 也未保存算法标签。[启动结果](correctness/619-startup.json) · [运行时结果](correctness/619.json) · [上游解析器](https://github.com/vllm-project/router/blob/159dcfcfcd717765d48b2ad24267f2a073d2501b/src/kv_index/source/discovery/metrics_meta.rs)

## 如何读性能数据

吞吐按客户端完成的 HTTP 请求和实际墙钟时间计算。Worker 数表示独立的 HTTP 地址。所有测试使用 CPU 模拟推理；短文本响应、缓存查找、HTTP 转发和 Go/Rust 调度都会影响结果。一次运行的数值适合检查容量与回归，策略排名需要在目标机器上重复、控制背景负载并使用业务 trace。

### Linux · 100 Worker，三种策略各 100 万次

使用官方 v0.1.17 Linux x86_64 wheel、并发 128、种子 42。每个请求有 33 个输入 token，最后一个 token 区分请求，输出 1 个 token，非流式；使用 32 种共享前缀。模拟 prefill、decode 延迟均为零。各策略使用相同的请求序列和独立启动的进程。本轮主要验证请求记账和端点分布；部分时段与小规模功能测试重叠。

| 策略 | 客户端成功 / 后端接收 | 错误 | 至少接到一次请求的 Worker | 成功 req/s | E2E p99 |
|---|---:|---:|---:|---:|---:|
| [round_robin](linux-million/round_robin.json) | 1,000,000 / 1,000,000 | 0 | 100 | 19,557 | 22.6 ms |
| [cache_aware](linux-million/cache_aware.json) | 1,000,000 / 1,000,000 | 0 | 27 | 11,735 | 21.3 ms |
| [kv_aware](linux-million/kv_aware.json) | 1,000,000 / 1,000,000 | 0 | 42 | 6,746 | 55.5 ms |

### Linux · 有 prefill 与 decode 成本的流式负载

在上述百万回放结束后单独运行，使用主线 Router 的 debug 构建。4 个 Worker、并发 32、每个策略每轮 5,000 个请求，共 3 轮，输出 4 个 token。每 rank 仅能保留 12 个 block，而 32 种双 block 热前缀总共需要 64 个 block。每个未命中 token 模拟 80 µs prefill，decode 间隔 1 ms。下表为三轮中位数，TTFT 列是三轮各自 p95 的中位数。每一轮都检查客户端和后端计数相等。[工作负载](../examples/contention.json) · [逐轮 JSON](streaming/)

| 策略 | 成功 req/s | TTFT p95 | 后端命中 token / 输入 token |
|---|---:|---:|---:|
| round_robin | 2,450 | 10.37 ms | 17.9% |
| cache_aware | 2,492 | 12.24 ms | 72.6% |
| kv_aware | 2,367 | 11.42 ms | 40.9% |
