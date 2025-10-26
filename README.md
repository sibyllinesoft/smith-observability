# Smith Observability Toolkit

Smith Observability bundles a runnable Bifrost + OpenTelemetry + ClickHouse stack and a small Node.js wrapper (`smith observe`) that launches your favourite agent CLI with production-grade telemetry defaults. The goal is to make it trivial to demonstrate end‑to‑end tracing for Codex (and other CLIs) while serving as instructional material for wiring Bifrost into an OTEL pipeline.

## Table of contents

- [Quick start](#quick-start)
- [How it works](#how-it-works)
  - [Stack topology](#stack-topology)
  - [Telemetry flow](#telemetry-flow)
  - [Captured span metadata](#captured-span-metadata)
- [Configuration reference](#configuration-reference)
  - [Environment variables](#environment-variables)
  - [Bifrost configuration](#bifrost-configuration)
- [Working with Codex](#working-with-codex)
  - [Deterministic local stub](#deterministic-local-stub)
  - [Inspecting ClickHouse](#inspecting-clickhouse)
  - [Using real providers](#using-real-providers)
- [Development workflow](#development-workflow)
  - [Repository layout](#repository-layout)
  - [Tests](#tests)
- [Troubleshooting](#troubleshooting)
- [Additional resources](#additional-resources)

## Quick start

### Prerequisites

- Docker (with Compose plugin) or standalone `docker-compose`
- Node.js 18 or newer
- An agent CLI on your `PATH` (for example `codex`, `claude`, or `smith-agent`)

### Install

```bash
npm install -g smith-observability
```

During local development run `npm link` inside this repository to create a global symlink without publishing.

### First trace

```bash
smith observe codex -- --model openai/gpt-5-codex exec "Call the \`list_directory\` tool on \".\""
```

The CLI ensures the observability stack is running, injects OTEL defaults, adds git metadata to the resource attributes, rewrites provider base URLs so requests flow through Bifrost, launches the agent, and streams its stdout/stderr. All spans land in ClickHouse (`otel.otel_traces`) within a few seconds.

Provide upstream credentials via standard environment variables such as `OPENAI_API_KEY`; Bifrost passes the header through to the provider or falls back to the supplied env key when requests omit it.

Stop the stack at any time:

```bash
docker compose -p smith-observability -f "$(npm root -g)/smith-observability/docker-compose.yaml" down
```

## How it works

### Stack topology

`docker-compose.yaml` starts three services, all defined in this repository:

| Service | Purpose | Ports |
| ------- | ------- | ----- |
| `bifrost` | Proxies requests from agents to upstream LLM providers and emits spans/usage metrics | `16080` (HTTP) |
| `otel-collector` | Accepts OTLP traces over gRPC (13317) and HTTP (13318), decorates spans, and exports to ClickHouse | `13317`, `13318` |
| `clickhouse` | Stores spans and exposes SQL endpoints for exploration | `13123` (HTTP), `13900` (native) |

The Docker image for Bifrost is rebuilt locally on demand (`docker/bifrost/`). A Python step patches the OTEL converter so `gen_ai.responses.output_json` always contains the full message list, including tool call arguments and outputs.

### Telemetry flow

1. `smith observe` runs `docker compose ... up -d`.
2. The wrapper waits for OTLP/HTTP (`http://localhost:13318/v1/traces`) and Bifrost (`http://localhost:16080/healthz`) to report ready.
3. Unless `SMITH_OBSERVABILITY_KEEP_OTEL=1` is set, default OTEL environment variables are defined for the child process:
   - `OTEL_EXPORTER_OTLP_ENDPOINT=localhost:13317`
   - `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:13318/v1/traces`
   - `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`
   - `OTEL_EXPORTER_OTLP_INSECURE=true`
   - `OTEL_RESOURCE_ATTRIBUTES` gains `service.name=smith-agent-<agent>` and git metadata (`smith.git.*`)
4. Supported CLIs (currently Codex and Claude) have their base URLs rewritten to `http://localhost:16080` so every request is recorded by Bifrost.
5. The wrapper emits its own `smith.observe` span and launches the agent with the enriched environment.
6. The collector batches spans and writes them into ClickHouse; helper views (`otel.agent_runs`, `otel.session_timeline`) are created during container init.

### Captured span metadata

Every Codex invocation routed through the stack captures:

- Git metadata (`smith.git.root`, `smith.git.branch`, `smith.git.remote`, `smith.git.status_dirty`)
- Agent arguments (`smith.agent.args`, `smith.agent.model`)
- Request/response usage metrics (`gen_ai.usage.*`, `gen_ai.responses.*`)
- Full response output payload (`gen_ai.responses.output_json`) including tool call arguments/results via the Bifrost converter patch
- Raw Bifrost gateway request/response logs on disk (`/srv/bifrost/logs/openai-requests.jsonl`) when `request_logging` is enabled

Refer to [`observability/clickhouse-init.sql`](observability/clickhouse-init.sql) for the exact schema and helper views.

## Configuration reference

### Environment variables

| Variable | Description |
| -------- | ----------- |
| `OPENAI_API_KEY` (and other provider keys) | Passed directly into the Bifrost container. |
| `SMITH_OBSERVABILITY_BIFROST_CONFIG` | Path (inside the host) to a Bifrost config file that should be bind-mounted into the container. Used by the e2e test to swap in the stub config. |
| `SMITH_OBSERVABILITY_OPENAI_BASE_URL` / `SMITH_OPENAI_BASE_URL` | Overrides the provider base URL if the default `https://api.openai.com` is not reachable (for example, when pointing at a local gateway). |
| `SMITH_OBSERVABILITY_KEEP_OTEL` | Skip injecting OTEL defaults; only git metadata is merged into existing values. |
| `SMITH_OBSERVABILITY_SKIP_MAIN` | Internal flag used by unit tests to prevent the CLI from bootstrapping the stack. |
| `CODEX_DEFAULT_MODEL` | Default Codex model (in `provider/model` form) when the user does not supply `--model`. |

### Bifrost configuration

- [`bifrost.config.json`](bifrost.config.json) ships with sane defaults for the OpenAI provider, JSON logging, and OTEL + telemetry plugins.
- The Dockerfile (`docker/bifrost/Dockerfile`) builds Bifrost from source and applies a Go patch so streaming responses always carry IDs and serialize full `responses` payloads into OTEL span attributes.
- Test runs mount [`test/support/bifrost-stub.config.json`](test/support/bifrost-stub.config.json) so the gateway points at the bundled SSE stub while exercising the same instrumentation.

Customise these files (or provide your own via `SMITH_OBSERVABILITY_BIFROST_CONFIG`) to add providers, change logging destinations, or extend telemetry labels.

## Working with Codex

### Deterministic local stub

The repository includes a streaming OpenAI-compatible stub (`test/support/openai-stub.mjs`). The new end-to-end test boots the `openai-stub` Compose profile, runs `smith observe codex`, and asserts that ClickHouse contains the tool call arguments and outputs. Run it manually:

```bash
npm run test:e2e
```

The test logs each ClickHouse query and result to stdout for quick sanity checks while the agent runs.

### Inspecting ClickHouse

After running the CLI (or the e2e test), inspect the stored spans:

```bash
docker compose -p smith-observability exec -T clickhouse clickhouse-client \
  --query "SELECT SpanAttributes['gen_ai.responses.output_json'] FROM otel.otel_traces WHERE SpanName='gen_ai.responses' ORDER BY Timestamp DESC LIMIT 1 FORMAT TSVRaw"
```

You should see the full response payload, including the `function_call` and `function_call_output` entries emitted by the stub (or real provider).

### Using real providers

Set `OPENAI_API_KEY` (and optional `SMITH_OBSERVABILITY_OPENAI_BASE_URL`) before launching Codex. When running against the hosted API, disable the stub:

```bash
OPENAI_API_KEY=sk-... smith observe codex -- --model openai/gpt-4o-mini exec "Generate a release checklist"
```

Traces still land in ClickHouse; only the upstream traffic shifts from the stub to the real endpoint.

For a guided walkthrough that starts from an empty project and inspects the resulting spans, see [docs/codex-observability-guide.md](docs/codex-observability-guide.md).

## Development workflow

### Repository layout

```
bin/smith.mjs           CLI entrypoint and orchestration logic
docker-compose.yaml     Compose stack for Bifrost + OTEL + ClickHouse
docker/bifrost/         Docker image build + patches for the Bifrost gateway
observability/          Collector and ClickHouse configuration
test/                   Unit tests, e2e harness, stub configs
```

### Tests

- `npm test` &mdash; unit tests for the CLI argument transformations (fast, no Docker required).
- `npm run test:e2e` &mdash; boots the Docker stack, runs `smith observe codex`, prints the ClickHouse queries it issues, and validates that `gen_ai.responses.output_json` contains the full tool call transcript.

The e2e test is a release gate: it exercises the full stack, verifies tool call telemetry, and confirms the Bifrost patches continue to function.

## Troubleshooting

- **Docker conflicts:** If you see compose name conflicts, run `docker compose -p smith-observability down -v` to stop the stack and drop volumes.
- **Collector not ready:** Ensure nothing else is binding `13317`/`13318`. The CLI waits ~60 seconds before giving up.
- **Missing spans:** Run `docker compose -p smith-observability logs bifrost` to confirm requests are flowing. The `openai-requests.jsonl` file inside `/srv/bifrost/logs/` contains raw proxied traffic.
- **Custom OTEL settings:** Export `SMITH_OBSERVABILITY_KEEP_OTEL=1` and set your own `OTEL_EXPORTER_OTLP_*` values. The CLI merges git metadata automatically.
- **ClickHouse schema drift:** If you reuse persistent volumes created before this release, reapply `observability/clickhouse-init.sql` to add the helper views.

## Additional resources

- [docs/codex-observability-guide.md](docs/codex-observability-guide.md) &mdash; step-by-step walkthrough for capturing Codex traces with the included stub.
- [docs/release-checklist.md](docs/release-checklist.md) &mdash; tasks to run before publishing a new package version.
- `observability/` directory for collector + ClickHouse configuration examples you can adapt to other projects.
- The Bifrost patches in `docker/bifrost/Dockerfile` illustrate how to extend upstream gateways when you need richer OTEL attributes.
