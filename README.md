# vLLM Router Lab

在没有 GPU 的机器上，把请求送进真实的 vLLM Router，再观察它选了哪个 Worker、哪个 DP rank，以及路由器声称命中的前缀是否真的在那个 rank。适合开发路由策略、复现控制面问题，以及在提交上游 PR 前跑回归。

Go 进程可启动多个独立 HTTP 监听地址。每个 rank 有自己的槽位、LRU 前缀缓存、ZMQ 发布和 replay 缓冲区。回放器支持 token-ID completions、SSE 和按到达时间排列的 JSONL。Router 使用上游二进制，实验专用控制接口放在 Worker 的 `/lab/*`。仓库的实现与上游分开维护。

## 跑第一个双 rank 实验

需要 Go 1.25+、Python 3.10+，以及可执行的上游 Rust Router。回归矩阵固定 Router 提交 `159dcfcfcd717765d48b2ad24267f2a073d2501b`。以下命令在仓库根目录运行。

```sh
go test -race ./...
go build -o routerlab .

git clone https://github.com/vllm-project/router.git ../vllm-router
git -C ../vllm-router checkout --detach 159dcfcfcd717765d48b2ad24267f2a073d2501b
(cd ../vllm-router && cargo build --locked --bin vllm-router)
export ROUTER=../vllm-router/target/debug/vllm-router

python3 scripts/regress.py kv --router "$ROUTER" --version 159dcfc --worker-version 0.34.0
```

预期 `results/regression/kv.json` 的状态为 `passed`。测试先给 rank 0、1 各写入一段独有前缀，冻结写入，然后通过 Router 访问。只有 Router 命中计数和目标 rank 的实际到达数、缓存命中数同时增长才算通过。随后丢掉 rank 1 的 clear 通知，用下一条事件制造序号缺口，检查 replay 后旧前缀失效、新前缀可命中、rank 0 保留信用。

## 比较策略与回放 trace

```sh
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --workers 4 --requests 5000 --concurrency 32 --stream --output 4 \
  --profile examples/contention.json --results results/contention

python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --trace examples/trace.jsonl --results results/trace

ulimit -n 8192
python3 scripts/compare.py --router "$ROUTER" --router-version 159dcfc \
  --workers 100 --requests 1000000 --concurrency 128 \
  --policies round_robin --results results/million
```

每种策略都重启 Router 和 Worker，使用相同的生成器种子或同一份 trace。JSONL 每行给出相对开始时间 `at_ms`、非空 token ID 数组 `prompt`、输出长度 `max_tokens`、是否 `stream`。所有请求都通过 HTTP；成功数和各 Worker 接收数分别采集。报告的 TTFT 为客户端发起请求到首个非空文本 chunk 的时间，含排队；`inter_chunk_ms` 按请求先取相邻文本 chunk 的平均间隔，再对请求取分位数。发送时间落后于计划的量见 `dispatch_lag_ms`。

模拟配置可设置每个未命中 token 的 prefill 时间、decode 间隔、固定响应延迟、每 rank 并发槽位，以及每轮迭代共享的 token 预算。逻辑缓存只存 block 身份和 LRU 状态。GPU 内核耗时、真实模型输出、混合注意力层、LoRA、多模态和 KV 张量搬运均超出本矩阵范围。研究具体 GPU 的延迟需要用真实引擎校准。

- [实测报告与原始数据](evidence/README.md)
- [滚动维护最近十个 vLLM 版本的 runbook](RUNBOOK.md)
- [已固定的版本、提交和协议字段](protocol/versions.json)
