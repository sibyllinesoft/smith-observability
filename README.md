# Smith Observability Toolkit

A lightweight toolkit for bootstrapping the Smith observability stack. Install
the npm package globally and call `smith observe <agent>` from any git
repository to start collecting traces from your favourite agent CLI.

The command:

- ensures the bundled Bifrost gateway plus OpenTelemetry Collector + ClickHouse stack are running,
- wires sensible OTLP defaults for the agent process,
- annotates spans with git metadata from the current repository, and
- launches the agent binary while streaming its stdout/stderr.

## Prerequisites

- Docker with the Compose plugin (or `docker-compose`)
- Node.js 18+
- An agent CLI available on your `PATH` (for example `claude`, `codex`, `smith-agent`)

## Install

```bash
npm install -g smith-observability
```

During development you can run `npm link` inside the cloned repository instead
of performing a global install.

## Usage

```bash
smith observe <agent> [-- <agent args>]
```

Arguments following the optional `--` delimiter are forwarded to the agent
exactly as provided (the delimiter itself is stripped).

Examples:

- `smith observe claude`
- `smith observe smith-agent -- --config ~/.smith/config.json`

Provide your model API keys through standard environment variables (for example
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`). They are passed directly into the Bifrost
gateway container so routed requests have access to the required credentials.
When a request carries its own `Authorization: Bearer ...` header, Bifrost prefers
that header; otherwise it falls back to the keys you supplied via environment
variables.

### Codex defaults

When you launch the Codex agent, the wrapper guarantees Bifrost receives a model
identifier in `provider/model` form. If you omit `--model` and do not set
`CODEX_DEFAULT_MODEL`, the CLI injects `openai/gpt-5-codex`. Override the default
by exporting `CODEX_DEFAULT_MODEL` (for example,
`CODEX_DEFAULT_MODEL=gpt-4o smith observe codex -- look around`). The effective
model is logged before the agent starts and is recorded on the `smith.observe`
span as `smith.agent.model`.

### What happens when you call `smith observe`

1. `docker compose up -d` runs against the package’s `docker-compose.yaml`. The
   call is idempotent and only starts services that are not already running.
2. The command waits for `http://localhost:13318` (OTLP/HTTP) to accept requests.
3. Bifrost is probed on `http://localhost:16080/healthz` so requests can be routed
   through the gateway once the agent launches.
4. Default OTEL environment variables are enforced (unless you set `SMITH_OBSERVABILITY_KEEP_OTEL=1`):
   - `OTEL_EXPORTER_OTLP_ENDPOINT=localhost:13317`
   - `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:13318/v1/traces`
   - `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` / `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf`
   - `OTEL_EXPORTER_OTLP_INSECURE=true`
   - `OTEL_RESOURCE_ATTRIBUTES` gains `service.name=smith-agent-<agent>` and `smith.agent.name=<agent>`
   - `OTEL_SERVICE_NAME` mirrors `service.name`
5. Git metadata gathered from the current working directory (root path, current
   branch, `remote.origin.url`, and whether the tree is dirty) is appended to
   `OTEL_RESOURCE_ATTRIBUTES` as `smith.git.root`, `smith.git.branch`,
   `smith.git.remote`, and `smith.git.status_dirty`.
6. Supported CLIs (today `claude` and `codex`) have their base URLs rewritten to
   `http://localhost:16080`, ensuring requests flow through Bifrost without
   manual configuration.
7. The CLI emits a parent `smith.observe` span (using the same OTLP endpoint) so
   every agent invocation generates at least one trace enriched with git context
   and exit status.
8. The agent process is launched with the enriched environment. When the agent
   exits, its exit code is propagated back to the caller.

If you need to preserve your own OTEL settings, run with
`SMITH_OBSERVABILITY_KEEP_OTEL=1 smith observe <agent>` and override only the
variables you care about (`OTEL_RESOURCE_ATTRIBUTES` values you provide will be
merged with the git metadata).

### Stopping the stack

When you are finished collecting traces:

```bash
docker compose -p smith-observability -f "$(npm root -g)/smith-observability/docker-compose.yaml" down
```

If you are working from a clone of the repository, you can also run the same
command from that directory without the absolute path. Should Docker report a
name conflict, remove any old containers (for example via `docker rm <container>`) or
run the `down` command above before restarting `smith observe`.

## Observability stack

The compose file launches three services:

- **bifrost** – A Bifrost gateway (running on `http://localhost:16080`) proxies agent traffic straight to the upstream provider. It honours inbound `Authorization` headers and falls back to the provided `OPENAI_API_KEY` when none is present. The container ships with Go auto-instrumentation so gateway spans and usage metadata are exported through OTLP.
- **otel-collector** – Receives spans via OTLP on `localhost:13317` (gRPC) / `localhost:13318` (HTTP) and forwards them to ClickHouse.
- **clickhouse** – Retains spans for historical queries (`http://localhost:13123`, native TCP on `localhost:13900`).

All configuration files live under `observability/` and ship with the npm
package, so the CLI can operate without a local checkout. Bifrost reads
`bifrost.config.json`; tweak this to change upstream providers, plugin options,
or to harden logging/telemetry behavior.

## Exploring traces

- ClickHouse SQL: `docker compose -p smith-observability exec clickhouse clickhouse-client --query "SELECT count() FROM otel.otel_traces"`.
- The OTLP endpoint (`http://localhost:13318`) remains available for additional workloads while the stack is running.
- Bifrost exposes a lightweight dashboard on `http://localhost:16080` showing routed calls and usage counters.
- Two helper views are created automatically in ClickHouse:
  - `otel.agent_runs` – one row per trace with git metadata, span counts, and timing details.
  - `otel.session_timeline` – 1-hour buckets derived from `agent_runs` for lightweight dashboards.
  (On first launch the ClickHouse container executes `observability/clickhouse-init.sql` automatically; if you reuse an older volume, rerun the script manually with `docker compose -p smith-observability exec -T clickhouse clickhouse-client --multiquery < observability/clickhouse-init.sql`.)

## Repository layout

```
bin/smith.mjs           CLI entrypoint
docker-compose.yaml     Observability services
docker/bifrost/         Bifrost container image and entrypoint
bifrost.config.json     Bifrost gateway configuration
observability/          Collector and ClickHouse configs
README.md               This guide
```

Feel free to fork and adapt the compose file or configs for your own agents.
