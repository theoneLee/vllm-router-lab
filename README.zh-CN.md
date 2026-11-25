# vLLM Router Lab

[English](README.md) | 简体中文

> 本文是英文版的中文对照，内容以 [英文版](README.md) 为准。

在自己电脑上测 [vLLM Router](https://github.com/vllm-project/router)，不需要 GPU。

vLLM Router Lab 在 CPU 上跑一组假的 vLLM Worker，前面挂的是真实、未经修改的 Router。每个 Worker 的前缀缓存里有什么由你决定，所以 Router 的每一次路由决策都能和真实情况对账：它选了哪个 Worker、哪个数据并行（DP）rank，它认为命中的前缀是不是真的缓存在那里。

为什么要做这件事？Router 把命中算到错误的 rank 上时，请求照样返回 200，光看状态码根本发现不了。到了生产环境，表现就是缓存命中丢失、首 token 时延（TTFT）变高。这个 lab 能把这类问题暴露出来，并变成可重复的测试。

适合这些场景：

- 开发或评审 Router 的路由策略；
- 没有 GPU 集群，但要复现控制面问题（KV 事件、服务发现、replay）；
- 给上游提 PR 之前先跑一遍回归。

## 亮点

- **真 Router，假引擎。** 任何 Router 二进制都能接：正式版、`main`，或者你的 PR 分支。Router 这一侧没有任何 mock。
- **两边一起对账。** 只有三项观测同时对上，场景才算通过：Router 的前缀命中计数、实际处理请求的 rank、该 rank 记录的缓存命中数。
- **兼容 vLLM 的 KV 事件协议。** 每个 rank 有一个 ZMQ PUB socket 和一个 replay socket。批次用 MessagePack 编码，带序号。block 哈希是链式 SHA-256 CBOR，可以按 32 字节或低 64 位发送。
- **覆盖十个 vLLM 版本。** 协议 profile 覆盖 v0.26.0 到 v0.34.0。每个 profile 都用对应 tag 自己的 Python 事件类型和哈希函数解码 lab 发出的帧来验证。
- **故障注入。** 丢掉一条清空缓存的事件、清掉 replay 历史、冻结缓存写入、把 SSE 流截断在半路，或者改掉某个 rank 上报的哈希算法。
- **单机就能压大规模。** 一个 Go 进程提供 100 个 Worker 地址。在 GitHub 的 macOS arm64 runner 上，100 万次请求经过 Router，吞吐 8,608 req/s，每个 Worker 正好收到 10,000 次。
- **策略对比。** 可以模拟 prefill 和 decode 耗时、每个 rank 的并发槽位、缓存容量和 continuous batching 的 token 预算。回放带种子的负载或你自己的 JSONL trace，每种策略收到的请求完全相同。

## 工作原理

```
                    HTTP                     HTTP
routerlab replay ─────────▶ vLLM Router ─────────▶ routerlab fleet
（压测客户端）          （真实二进制） ◀───────── Worker 0..N，rank 0..M
                                        ZMQ KV 事件    （每个 rank 有自己的缓存和事件流）

scripts/*.py   启动全新进程，通过 /lab/* 注入故障，
               读取 /metrics 和 /lab/state，输出 JSON 结论
```

| 组件 | 作用 |
| --- | --- |
| `routerlab fleet` | 每个 Worker 一个 HTTP 监听地址。每个 rank 有独立的并发槽位、LRU block 缓存、ZMQ 事件流和 replay 缓冲区。提供 vLLM 的接口，外加 `/lab/*` 下的实验控制接口。 |
| `routerlab replay` | 向 `/v1/completions` 发送带种子的负载或带时间戳的 JSONL trace。统计 TTFT、端到端时延、chunk 间隔和发送滞后。SSE 流被截断会记为错误。 |
| `scripts/regress.py` | 针对真实 Router 的黑盒回归场景。 |
| `scripts/compare.py` | 用同一份负载跑多种路由策略，收集客户端和 Worker 两侧的数据。 |
| `scripts/matrix.py`、`scripts/audit.py` | 按每个固定的 vLLM tag 校验协议格式。见 [维护指南](docs/maintaining.zh-CN.md)。 |

## 范围

不跑模型。响应是占位文本，时延来自你设置的 profile。别拿它测 GPU 性能。

覆盖范围：token-ID completions、单个 full-attention 缓存组、`sha256_cbor` 哈希、block size 16。混合注意力、LoRA、多模态输入和 KV 张量传输不在范围内。

## 快速开始

需要 Go 1.25+ 和 Python 3.10+（测试用的是 3.12）。回归和对比脚本只用 Python 标准库。

### 1. 构建

```sh
git clone https://github.com/theoneLee/vllm-router-lab.git
cd vllm-router-lab
go test -race ./...
go build -o routerlab .
```

### 2. 先单独跑一下 fleet

```sh
./routerlab fleet -workers 2 -port 18000 -decode 1ms &
./routerlab replay -url http://127.0.0.1:18000 -n 200 -stream -output 4
curl -s http://127.0.0.1:18000/lab/state
kill %1
```

`replay` 会输出一段 JSON，包含成功数和时延分位数。`/lab/state` 能看到 rank 0 收到了多少请求、有多少 prompt token 命中了缓存。

### 3. 准备 Router

**Linux，最省事：** 直接装正式版 wheel，会得到一个 `vllm-router` 命令。

```sh
pip install vllm-router==0.1.17
export ROUTER=vllm-router
```

**任意系统，从源码构建：** 已归档的回归结果用的就是这个版本。需要 Rust 工具链、C++ 编译器、CMake 和 `protoc`。构建用到了 PyO3，如果默认 Python 比 3.12 新，请把 `PYO3_PYTHON` 设成某个 3.12 解释器的绝对路径。

```sh
git clone https://github.com/vllm-project/router.git ../vllm-router
git -C ../vllm-router checkout --detach 159dcfcfcd717765d48b2ad24267f2a073d2501b
(cd ../vllm-router && cargo build --locked --bin vllm-router)
export ROUTER=../vllm-router/target/debug/vllm-router
```

### 4. 跑第一个回归

```sh
python3 scripts/regress.py kv --router "$ROUTER" --version 159dcfc --worker-version 0.34.0
```

打开 `results/regression/kv.json`，应该能看到 `"status": "passed"`。

这个场景是这样跑的：先给 rank 0 和 rank 1 各预热一段不同的前缀，冻结缓存写入，然后通过 Router 去探测这两段前缀。冻结很关键，不然一次路由错误的探测会把错误的 rank 也预热了，问题就被掩盖了。接着清空 rank 1，但把这条清空事件从实时流里丢掉，再写入一段新前缀，让 Router 看到序号出现缺口。通过条件是：Router 主动请求 replay，撤销旧前缀的命中信用，认上新前缀，同时 rank 0 的信用不受影响。

## 使用

### 回归场景

```sh
python3 scripts/regress.py <case> --router "$ROUTER" --version <router-commit-or-tag> [--worker-version 0.34.0]
```

| 场景 | 检查内容 |
| --- | --- |
| `kv` | 两个 rank 之间的前缀路由、遗漏 clear 后通过 replay 恢复、LRU 淘汰撤销旧信用。 |
| `stream` | 客户端取消 SSE、上游截断流，两种情况都会释放槽位；回放客户端遇到截断流会以非零状态退出。 |
| `scale` | 运行中增删 Worker，以及 Worker 在同一地址重启后重新注册。 |
| `619-startup` | Router 启动前，某个 rank 上报不兼容的哈希算法。Router 应该不再给它记前缀命中。 |
| `619` | 同样的检查，但算法是在 Router 运行中改的。 |

两个 `619` 场景是 [Router issue #619](https://github.com/vllm-project/router/issues/619) 的验收用例。在固定的提交 `159dcfc` 上它们**本来就会失败**，修复合入后应该变绿。请分开运行，分别看各自的 JSON 报告。

每次运行都会写出 `results/regression/<case>.json`，里面有状态、通过的检查项和观测值，旁边是 Router 和 fleet 的日志。`--version` 是你自己定的标签，会写进报告，方便日后知道测的是哪个 Router。

### 对比路由策略

```sh
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --workers 4 --requests 5000 --concurrency 32 --stream --output 4 \
  --profile examples/contention.json --results results/contention
```

每种策略都会重启 fleet 和 Router。默认跑 `round_robin`、`cache_aware` 和 `kv_aware`，也可以用 `--policies` 指定。所有策略使用同一个种子或同一份 trace。每种策略输出一个 JSON，包含客户端时延、每个 Worker 收到的请求数，以及 `cached_fraction`，也就是 prompt token 命中 Worker 缓存的比例。

要让 100 万次请求经过 100 个 Worker，先调高文件描述符上限：

```sh
ulimit -n 8192
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --workers 100 --requests 1000000 --concurrency 128 \
  --policies round_robin --results results/million
```

### 回放自己的 trace

```sh
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --trace examples/trace.jsonl --results results/trace
```

trace 是 JSONL 格式，一行一个请求，按 `at_ms` 排序：

```json
{"at_ms": 0, "prompt": [1, 2, 3, 4], "max_tokens": 3, "stream": true}
```

`at_ms` 是相对于开始时间的发送时刻，`prompt` 是非空的 token ID 数组。

### 模拟引擎耗时

profile 是一个 JSON 对象，key 就是 `routerlab fleet` 的参数名。[`examples/contention.json`](examples/contention.json) 让每个 rank 只能放 12 个 block，而热前缀一共需要 64 个，于是各个策略必须争抢缓存：

```json
{"blocks": 12, "slots": 8, "prefill": "80us", "decode": "1ms", "tick": "1ms", "batch-tokens": 128}
```

### 手动组装

也可以自己把各个部分拼起来：

```sh
./routerlab fleet -workers 2 -port 18000 &
$ROUTER --worker-urls http://127.0.0.1:18000 http://127.0.0.1:18001 \
  --policy cache_aware --host 127.0.0.1 --port 30000 &
./routerlab replay -url http://127.0.0.1:30000 -n 1000 -concurrency 32
```

全部参数、接口和输出字段见 [参考文档](docs/reference.zh-CN.md)。

## 实测结果

| 测试 | 结果 |
| --- | --- |
| macOS arm64，100 个 Worker，round robin | 1,000,000 / 1,000,000 次请求，8,608 req/s，p99 33.8 ms |
| Linux x86_64，100 个 Worker，3 种策略 | 每种策略 100 万次请求，客户端和 Worker 计数完全一致 |
| 缓存压力下的流式负载，三轮中位数 | `cache_aware` 复用了 72.6% 的 prompt token，p95 TTFT 12.2 ms；`round_robin` 复用 17.9%，p95 TTFT 10.4 ms |
| vLLM v0.26.0 – v0.34.0 | 十个版本的协议解码（字节和整数哈希）和双 rank Router 测试全部通过 |

流式那一行说明了为什么不能只看一个指标：在这份负载上，缓存复用最好的策略，尾部 TTFT 反而不是最好的。测试设置、注意事项和原始 JSON 见 [基准测试](docs/benchmarks.zh-CN.md)。

## 文档

- [参考](docs/reference.zh-CN.md)：CLI 参数、HTTP 接口、指标、输出字段
- [基准测试](docs/benchmarks.zh-CN.md)：已归档的测试、怎么读数据、原始数据
- [维护指南](docs/maintaining.zh-CN.md)：新增 vLLM 版本、跑版本矩阵、定位失败
- [贡献指南](CONTRIBUTING.zh-CN.md)

## 目录结构

```
*.go         Go 写的 fleet 和回放客户端（一个二进制：routerlab）
scripts/     Python 测试框架、场景、策略对比、版本矩阵
protocol/    固定的 vLLM 版本及其协议特性
examples/    示例 trace 和耗时 profile
evidence/    文档里每个数字对应的原始 JSON
docs/        参考、基准测试、维护指南
```

## 许可证

[Apache License 2.0](LICENSE)。本项目是独立项目，与 vLLM 无隶属关系，不包含 vLLM 或 vLLM Router 的代码。
