# Testing, CI and live provider proof

## Local checks

The repository holds five Go modules: the core module at the root, [`mcp`](../mcp), [`examples/mcp-server`](../examples/mcp-server), [`examples/inventory-demo`](../examples/inventory-demo), and [`integration/providers`](../integration/providers). Run the gates in each:

```sh
go vet ./... && go test -race ./...
(cd mcp && go vet ./... && go test -race ./...)
(cd examples/mcp-server && go vet ./... && go test -race ./...)
(cd examples/inventory-demo && CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test -race ./...)
(cd integration/providers && go vet ./... && go test -run '^$' ./...)
```

Benchmarks cover engine dispatch, not network throughput or model/tool latency:

```sh
go test -run '^$' -bench . -benchmem ./...
```

## Continuous integration

[CI](../.github/workflows/ci.yml) runs formatting, build, vet, and race tests for the core, `mcp`, `examples/mcp-server`, and `examples/inventory-demo` modules on the exact minimum Go 1.25.5 and current stable Go. Automatic toolchain upgrades are disabled so the minimum-version check cannot silently use a newer compiler. A separate stable-Go job builds, vets, and compiles the official-provider SDK tests without executing paid requests. Inventory tests use temporary SQLite databases with CGO; they never start Claude Code or call a model.

The SDK tests live in the separate [integration/providers](../integration/providers) module, with pinned OpenAI and Anthropic SDK dependencies and committed checksums. The core module remains dependency-free; MCP-related modules use the official MCP Go SDK, and the inventory example additionally uses SQLite through a CGO driver. Compile the provider tests locally without credentials:

```sh
(cd integration/providers && go test -run '^$' ./...)
```

## Live provider proof and release gate

The opt-in tests make two real requests per provider: a forced current tool call, followed by a response that must consume an unpredictable confirmation token generated during local tool execution. SDK retries are disabled, outputs are capped, and each provider has a 90-second context. Tests target the official provider endpoints, not configurable mock/proxy URLs.

Live tests are skipped unless `BLAZE_LIVE_PROVIDERS=1`. Once enabled, missing credentials/models fail rather than producing a skipped green result. See the [OpenAI](adapters/openai.md#live-sdk-proof) and [Anthropic](adapters/anthropic.md#live-sdk-proof) instructions. Compilation, skipped tests, and local conversion tests are **not** live-provider proof.

[Live provider proof](../.github/workflows/providers.yml) is manual-only and makes billed API requests. Configure `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` repository secrets without putting keys in source or chat:

```sh
gh secret set OPENAI_API_KEY
gh secret set ANTHROPIC_API_KEY
```

Then run the workflow from GitHub Actions with model IDs available to those keys. The workflow must be present on the default branch before it can be manually dispatched. Choose models supporting named function-tool choice and short text replies; provider subscriptions or coding-agent logins are not API credentials.

Publish a release only after ordinary CI and both live round trips pass for the intended commit. See the [changelog](../CHANGELOG.md) for what each release contains. Production concurrency/load limits still require workload-specific measurement.


