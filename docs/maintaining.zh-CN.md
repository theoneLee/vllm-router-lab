# 维护指南

[English](maintaining.md) | 简体中文

> 本文是英文版的中文对照，内容以 [英文版](maintaining.md) 为准。

这份指南讲的是怎么和上游保持同步：新增 vLLM 版本、重跑版本矩阵、定位失败原因。

lab 跟踪最近十个 vLLM 稳定版（`vMAJOR.MINOR.PATCH` 标签，按数值排序，补丁版单独计数）。Router 单独固定版本。请使用独立的上游 checkout，任何时候都不需要把 vLLM 或 Router 的代码拷进这个仓库。

## 1. 刷新上游，看看有什么变化

```sh
git clone --filter=blob:none --no-checkout https://github.com/vllm-project/vllm.git ../vllm
git -C ../vllm fetch origin --tags
python3 scripts/audit.py ../vllm
git diff -- protocol/versions.json
```

已经有 checkout 的话从 `fetch` 开始。只有上游标签都拉全了，“最近十个”才成立，所以要确认每个新标签指向的 commit 和上游一致。

`audit.py` 读取每个 tag 的源码，记录事件源发现方式、哈希根、是否默认使用整数哈希，以及 `BlockStored` 的字段。如果它报出意料之外的结果，去看两个 tag 之间的源码差异。新字段即使有默认值也要重视，想清楚它会不会影响路由。

新版本需要回答下面这些问题，答案在上游这些地方：

| 问题 | 上游源码 | 决定 lab 的哪部分行为 |
| --- | --- | --- |
| Router 怎么找到每个 rank 的事件源？ | `vllm/entrypoints/serve/instrumentator/basic.py`、`vllm/config/kv_events.py` | HTTP discovery 还是静态地址，以及 404 分支 |
| 事件怎么分帧、怎么发现缺口、replay 怎么结束？ | `vllm/distributed/kv_events.py` | topic、8 字节大端序号、MessagePack 批次、DEALER 分帧、结束标记 |
| 两边能不能算出同一个 block key？ | `vllm/utils/hashing.py`、`vllm/v1/core/kv_cache_utils.py`、`vllm/envs.py` | 根种子、父哈希和 token、32 字节还是低 64 位 |
| 事件有没有带来新的索引维度？ | `BlockStored`、`BlockRemoved` 及其生产方 | group、locality、ownership、session、lookahead。如果路由依赖它，加一个专门的场景。 |
| 控制面能读到哪些缓存元数据？ | `vllm/config/cache.py`、`vllm/v1/metrics/`、Router 的 `src/kv_index/source/discovery/` | 每个 engine 的标签、元数据重读、按 rank 撤销信用 |
| HTTP 客户端看到的是什么？ | vLLM completions 服务、Router 的 HTTP 转发 | token-ID 请求、SSE 结束和断开、取消后释放槽位 |

如果行为确实变了，修改 `internal/fleet`（profile 和 Worker）或 `internal/kvcache`（缓存和事件），重新构建，再跑一遍。

## 2. 跑版本矩阵

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r scripts/requirements-audit.txt
go build -o routerlab ./cmd/routerlab
python scripts/matrix.py ../vllm --router "$ROUTER" --router-version "$ROUTER_REV"
```

`ROUTER` 是 Router 二进制，`ROUTER_REV` 是你实际构建的 commit，例如 `159dcfcfcd717765d48b2ad24267f2a073d2501b`。

对每个 tag，矩阵会：

1. 核对 tag 和 `protocol/versions.json` 是否一致；
2. 从该 commit 取出 msgspec 事件类型和哈希函数，只加载这些纯 Python 代码，不需要引擎、CUDA 或 torch；
3. 解码 Go Worker 发出的真实 replay 帧，检查 store、父哈希、淘汰、clear、rank 和结束标记，字节和整数两种哈希形式都测；
4. 用字节哈希跑真实 Router 的双 rank 回归。

任何一步失败都会以非零状态退出。准备发布的每次运行请用新的 `--results` 目录。

特定版本需要注意：

- **v0.26.0 – v0.28.0** 不设种子时，每个进程随机选一个 SHA-256 CBOR 根。正向 KV 测试会给 Worker 传 `-hash-seed 0`，并给 Router 传对应的 layout。从 v0.29.0 起，矩阵使用默认的固定根。
- **v0.26.0 – v0.30.0** 没有 HTTP discovery，Router 通过静态配置拿到每个 rank 的发布地址、replay 地址和 topic。测试框架从 `/lab/event-sources` 读这些信息，这些 profile 的原生 `/kv_event_sources` 返回 404。从 v0.31.0 起，矩阵直接测 HTTP discovery。
- 所有 profile 统一使用 `sha256_cbor`、单个 full-attention 组、block size 16，不开开发用的 `server_info`。对应到真实引擎就是 `--prefix-caching-hash-algo sha256_cbor` 加 `VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES=0`。十个 tag 默认都用整数事件哈希，fleet 的 `-int-hashes` 参数覆盖这条路径。版本号对得上，不代表所有引擎默认值或所有协议细节都覆盖到了。

只有源码审计、上游类型解码、Router 黑盒测试三层都通过后，才归档 JSON。完整引擎互通或 GPU 时延请另行记录。

## 3. 回归和问题定位

```sh
for case in stream scale kv; do
  python scripts/regress.py "$case" --router "$ROUTER" --version "$ROUTER_REV" || exit 1
done
python scripts/regress.py 619-startup --router "$ROUTER" --version "$ROUTER_REV" --worker-version 0.34.0
python scripts/regress.py 619 --router "$ROUTER" --version "$ROUTER_REV" --worker-version 0.34.0
```

最后两个是 Router issue #619 的验收用例。在当前基线上两个都会失败，请分开运行，分别看 JSON 报告。

- `619-startup` 在 Router 探测之前修改 rank 1 的指标。
- `619` 先在两个 rank 上建立信用，再修改 rank 1 的指标。

故障只改元数据，原有哈希仍可匹配，所以测试只回答一个问题：Router 还信不信这个 rank？判断结果时要四样一起看：Worker 上报的标签、指标读取次数、Router 的查找计数、实际处理请求的 rank。如果读取次数没涨，只能证明观察窗口内没有重读。正确的修复要保留 rank 0 的信用，并且 rank 1 仍能处理普通请求。

事件类测试失败时：

1. 先确认报告里写的版本是不是你想测的；
2. 看同一个 results 目录下的 `*.router.log` 和 `*.fleet.log`；
3. 查询每个 rank 的 `/lab/state`（带上 `X-Data-Parallel-Rank`）。

做性能测试时，固定机器、构建模式、种子、请求数、并发和缓存大小。对比策略时，先确认客户端和 Worker 的请求数一致，再看缓存 token 占比和 TTFT。

## CI

- [`ci.yml`](../.github/workflows/ci.yml) 在每次 push 和 pull request 时运行 vet、race 测试和构建。
- [`mac-lab.yml`](../.github/workflows/mac-lab.yml) 需要手动触发（`workflow_dispatch`）。它在 macOS arm64 上原生构建固定版本的 Router，在两个 Worker 上对比策略，跑百万次请求基线，并把 `results/` 上传为 artifact。
