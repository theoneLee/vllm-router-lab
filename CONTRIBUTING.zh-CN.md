# 贡献指南

[English](CONTRIBUTING.md) | 简体中文

> 本文是英文版的中文对照，内容以 [英文版](CONTRIBUTING.md) 为准。

感谢参与！欢迎提交 bug 报告、新场景、新的 vLLM profile 和文档修正。

## 提 PR 之前

```sh
go vet ./...
go test -race ./...
go build -o routerlab .
```

如果你改了 Router 能感知到的部分（HTTP 接口、事件、哈希、服务发现），还要对真实 Router 跑一遍回归：

```sh
for case in kv stream scale; do
  python3 scripts/regress.py "$case" --router "$ROUTER" --version <router-rev> || break
done
```

Python 改动需要通过 `ruff check scripts docs/experiments`。

## 约定

- **一个 PR 只做一件事。** 新场景、新 vLLM 版本和重构请拆成三个 PR。
- **测试要证明路由正确，而不只是请求成功。** 返回 200 不够，要同时断言 Router 记了什么、目标 rank 实际看到了什么。可以参考 `scripts/kv_scenarios.py` 的写法。
- **新增 vLLM 版本？** 按 [维护指南](docs/maintaining.zh-CN.md) 操作，把更新后的 `protocol/versions.json` 和矩阵结果一起提交。
- **文档里加了新数字？** 把原始 JSON 放到 `evidence/` 下并加上链接，说明用的是什么机器、哪个 Router 构建、什么参数。
- **保持独立。** 不要把 vLLM 或 Router 的源码拷进来，用独立的上游 checkout 去读。
- **守住范围。** lab 只在 CPU 上模拟路由能感知到的行为。需要真实模型或 GPU 的功能请放到单独的测试框架里。

## 报告 bug

请附上：

- Router 的版本或 commit，以及 `--worker-version` 用的 profile；
- 完整的命令；
- results 目录下的 JSON 报告和 `*.router.log` / `*.fleet.log`；
- 操作系统和 CPU 架构。

## 许可证

提交贡献即表示你同意以 [Apache License 2.0](LICENSE) 授权你的贡献。
