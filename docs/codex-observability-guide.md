# Codex Observability Walkthrough

This guide walks through capturing Codex telemetry with the Smith Observability stack. It uses the bundled OpenAI streaming stub so you can explore the entire flow without external credentials, then explains how to switch to a real provider.

## Prerequisites

- Docker with the Compose plugin (or `docker-compose`)
- Node.js 18+
- `git` (for repository metadata in spans)

Install the toolkit:

```bash
git clone https://github.com/maximhq/smith-observability.git
cd smith-observability
npm install
npm link   # optional, lets you invoke `smith` globally from this checkout
```

## Step 1 – Boot the observability stack

The CLI spins up Bifrost, the OpenTelemetry collector, and ClickHouse automatically. For a deterministic demonstration run the E2E suite (it prints the ClickHouse queries it executes):

```bash
npm run test:e2e
```

Behind the scenes the test:

1. Starts the Compose profile `openai-stub` (a local SSE endpoint that mimics OpenAI’s Responses API with a tool call + tool result).
2. Mounts `test/support/bifrost-stub.config.json` into the Bifrost container so provider traffic targets the stub.
3. Launches `smith observe codex` with the prompt “Call the `list_directory` tool on "." to list the repository root, then summarize what you found.”
4. Polls ClickHouse for the most recent `gen_ai.responses` span, logging both the SQL and the returned JSON.

You’ll see the stubbed Codex session drive a tool invocation whose arguments and output are preserved verbatim in `gen_ai.responses.output_json`.

## Step 2 – Run Codex manually

To inspect the workflow yourself:

```bash
docker compose --profile test -p smith-observability up -d openai-stub
OPENAI_API_KEY=test \
SMITH_OBSERVABILITY_BIFROST_CONFIG=$(pwd)/test/support/bifrost-stub.config.json \
  node bin/smith.mjs observe codex -- \
    --model openai/gpt-4o-mini \
    exec "Call the \`list_directory\` tool on \".\" to list the repository root, then summarize what you found."
```

The CLI prints the stack status, the Codex prompt, and the session metadata (`approval: never`, sandbox mode, etc.). Allow a couple seconds for spans to land in ClickHouse, then query the captured payload:

```bash
docker compose -p smith-observability exec -T clickhouse clickhouse-client \
  --query "SELECT SpanAttributes['gen_ai.responses.output_json'] FROM otel.otel_traces WHERE SpanName='gen_ai.responses' ORDER BY Timestamp DESC LIMIT 1 FORMAT TSVRaw"
```

The JSON array contains:

1. The assistant transcript.
2. A `function_call` message whose `arguments` field is the raw JSON string sent to the tool (`{"path":"."}`).
3. A `function_call_output` message whose `output` field carries the tool result (`["README.md","package.json"]`).
4. The follow-up assistant message that summarises the tool call.

## Step 3 – Explore ClickHouse helper views

The ClickHouse init script defines `otel.agent_runs` and `otel.session_timeline` to make ad-hoc analysis easier. For quick insight into the most recent run:

```bash
docker compose -p smith-observability exec -T clickhouse clickhouse-client \
  --query "SELECT agent_name, duration_seconds, prompt_tokens, completion_tokens FROM otel.agent_runs ORDER BY end_time DESC LIMIT 5 FORMAT Markdown"
```

These views aggregate spans by trace ID, expose git metadata (`smith.git.*`), and summarise token counts pulled from the gateway telemetry plugin.

## Step 4 – Switch to the real API (optional)

Once you are comfortable with the flow, point Bifrost at OpenAI’s hosted endpoint:

```bash
docker compose -p smith-observability down -v   # clear the stub
OPENAI_API_KEY=sk-your-key \
  smith observe codex -- --model openai/gpt-4o-mini exec "Summarize the last git commit."
```

The same spans appear in ClickHouse. You can continue to poll the database or connect a visualisation tool (for example Grafana) using the ClickHouse HTTP endpoint (`http://localhost:13123`).

## Troubleshooting tips

- **Stack not ready:** Run `docker compose -p smith-observability ps` to check container health. Bifrost exposes `/healthz`; the CLI refuses to launch the agent until it responds.
- **No spans in ClickHouse:** Inspect collector logs (`docker compose -p smith-observability logs otel-collector`). The collector writes warnings to stdout if the export fails.
- **Custom base URL:** Export `SMITH_OBSERVABILITY_OPENAI_BASE_URL` to point at a gateway in another network namespace.
- **Preserve OTEL settings:** Set `SMITH_OBSERVABILITY_KEEP_OTEL=1` if you already route telemetry elsewhere. Git metadata is appended to your existing resource attributes.

## Clean up

```bash
docker compose -p smith-observability down -v
```

This stops all services and removes volumes so future demos start from a clean slate.
