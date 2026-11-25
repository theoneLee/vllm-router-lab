# Contributing

English | [简体中文](CONTRIBUTING.zh-CN.md)

Thanks for helping out. Bug reports, new scenarios, new vLLM profiles and doc fixes are all welcome.

## Before you open a PR

```sh
go vet ./...
go test -race ./...
go build -o routerlab .
```

If you touched anything the Router sees (HTTP endpoints, events, hashing, discovery), also run the regressions against a real Router:

```sh
for case in kv stream scale; do
  python3 scripts/regress.py "$case" --router "$ROUTER" --version <router-rev> || break
done
```

Python changes should pass `ruff check scripts docs/experiments`.

## Guidelines

- **One concern per PR.** A new scenario, a new vLLM version and a refactor are three PRs.
- **Make tests prove routing, not just success.** A 200 response isn't enough. Assert on what the Router counted *and* what the target rank saw. See `scripts/kv_scenarios.py` for the pattern.
- **New vLLM version?** Follow [Maintaining](docs/maintaining.md). Commit the updated `protocol/versions.json` with the matrix results.
- **New numbers in the docs?** Add the raw JSON under `evidence/` and link it. Say which machine, Router build and settings you used.
- **Keep the lab independent.** Don't copy vLLM or Router source into this repo. Read upstream from a separate checkout.
- **Keep the scope.** The lab models routing-visible behaviour on CPU. Features that need a real model or GPU belong in a separate harness.

## Reporting a bug

Please include:

- the Router version or commit, and the `--worker-version` profile,
- the exact command,
- the JSON report and the `*.router.log` / `*.fleet.log` files from the results directory,
- your OS and architecture.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
