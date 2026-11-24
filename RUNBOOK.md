# 上游协议同步和回归

版本范围按 vLLM 的稳定 `vMAJOR.MINOR.PATCH` 标签数值排序，补丁版独立计数。Router 的提交单独固定。每次更新用独立的上游 checkout，不必将 vLLM、Router 或其他模拟器的代码合进本仓库。

## 1. 刷新上游和记录差异

```sh
git clone --filter=blob:none --no-checkout https://github.com/vllm-project/vllm.git ../vllm
git -C ../vllm fetch origin --tags
python3 scripts/audit.py ../vllm
git diff -- protocol/versions.json
```

已有 checkout 可从 fetch 开始。确认新增标签的 commit 与上游一致；只有在 upstream 标签齐全时，“最近十个”才成立。生成器检查 PUB/replay 帧、结束标识、根哈希来源、整型哈希开关，记录 discovery 路由和 BlockStored 字段。遇到未知编码时先看两个标签之间的源码差异。新增字段即使有默认值也需要判断对目标场景的影响。

| 要核对的问题 | 上游源码入口 | 决定的模拟行为 |
|---|---|---|
| Router 从哪里知道每个 rank 的事件源？ | `vllm/entrypoints/serve/instrumentator/basic.py`，`vllm/config/kv_events.py` | 原生 HTTP discovery 或显式静态地址，验证 404 分支 |
| 消费者如何解包、发现 gap、终止 replay？ | `vllm/distributed/kv_events.py` | topic、8 字节大端序号、MsgPack batch、DEALER 分隔帧与结束标识 |
| 两边能不能独立算出同一个 block key？ | `vllm/utils/hashing.py`，`vllm/v1/core/kv_cache_utils.py`，`vllm/envs.py` | 根种子、父哈希与 token 序列、32 字节或低 64 位整数 |
| 事件是否带来了新的索引维度？ | 每个 tag 的 `BlockStored`、`BlockRemoved` 和生产方 | 检查 group、locality、ownership、session、lookahead 等字段的含义；扩展场景时加入独立样例 |
| 控制面能取得哪些缓存属性？ | `vllm/config/cache.py`、`vllm/v1/metrics/`、Router `src/kv_index/source/discovery/` | 每个 engine 的标签、元数据重新读取、rank 级撤销信用 |
| HTTP 客户端看见什么？ | vLLM completions 服务、Router HTTP 转发路径 | 实际 token-ID 请求、SSE 结束与断开、取消后的槽位释放 |

## 2. 按标签运行十组验证

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r scripts/requirements-audit.txt
go build -o routerlab .
python scripts/matrix.py ../vllm --router "$ROUTER" --router-version "$ROUTER_REV"
```

`ROUTER` 指向已构建的二进制，`ROUTER_REV` 填实际编译的 SHA，例如 `159dcfcfcd717765d48b2ad24267f2a073d2501b`。矩阵先核对 tag 和清单，从指定提交提取原始 msgspec 类型与散列函数，在 CPU 上解码 Go 生成的真实 replay 帧，验证 store、parent、淘汰、clear、rank 及结束标记。每个 tag 分别测字节和整数两种表示，再用字节哈希运行真实 Router 的双 rank 回归。一次失败会以非零状态退出；每次发布报告使用新的 results 目录。

- v0.26.0 至 v0.28.0 的 SHA-256 CBOR 根在未设 seed 时按进程随机。正向 KV 测试在旧端显式设 `-hash-seed 0`，并给 Router 对应的 layout。v0.29.0 起矩阵使用未设置 seed 的默认根。
- v0.26.0 至 v0.30.0 经 Router 的静态配置传入每个 rank 的发布、replay 地址和 topic。实验的 `/lab/event-sources` 只供启动脚本读取；对应模拟端的原生 `/kv_event_sources` 返回 404。v0.31.0 起直接测 HTTP discovery。
- 实验统一选择 `sha256_cbor`、单组 full-attention、block size 16，禁用开发用 server_info。可重算的正向用例对应真实端配置 `--prefix-caching-hash-algo sha256_cbor` 和 `VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES=0`。十个上游标签的事件整数开关均默认开启，Go 的 `-int-hashes` 可覆盖此路径。版本化模拟器显式选择这套部署参数；不要用“版本名相同”推断所有引擎默认值或全部协议都已覆盖。
- 实际行为改变时修改 Go profile、Worker 或 cache，重新构建二进制再跑。通过源码筛查、原始类型解码和 Router 黑盒三层后归档 JSON。完整 vLLM 引擎互操作或 GPU 时延另行记录。

## 3. 回归和定位

```sh
for case in stream scale kv; do
  python scripts/regress.py "$case" --router "$ROUTER" --version "$ROUTER_REV" || exit 1
done
python scripts/regress.py 619-startup --router "$ROUTER" --version "$ROUTER_REV" --worker-version 0.34.0
python scripts/regress.py 619 --router "$ROUTER" --version "$ROUTER_REV" --worker-version 0.34.0
```

最后两个命令是独立的上游验收用例。当前基线两者均为红；分别运行并读取各自 JSON。启动用例在 Router 探测前改变 rank 1 的指标，运行时用例先建立两个 rank 的信用再改变 rank 1 的指标。故障注入有意只改元数据，保留可匹配的原有哈希，以便单独测“是否信任该 rank”。因此需同时看 Worker 报出的标签、指标读取次数、Router 查找计数和实际服务 rank。若指标读数没有增长，只能证明在给定窗口内没有观察到重读。修复后需保留 rank 0 信用，且 rank 1 仍能处理普通请求。

事件问题先看报告中的版本，再查同目录的 router/fleet 日志和每 rank 的 `/lab/state`。性能实验固定机器、构建模式、种子、请求数、并发与缓存大小。对比策略先看请求数与回执，再看命中 token 比例和 TTFT；本地 Mac 可以用 README 命令复测，托管 Mac 流程见 [.github/workflows/mac-lab.yml](.github/workflows/mac-lab.yml)。
