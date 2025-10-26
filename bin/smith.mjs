#!/usr/bin/env node
import { spawn, spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import process from 'node:process';

import {
  diag,
  DiagConsoleLogger,
  DiagLogLevel,
  SpanStatusCode,
  trace
} from '@opentelemetry/api';
import { Resource } from '@opentelemetry/resources';
import { BatchSpanProcessor } from '@opentelemetry/sdk-trace-base';
import { NodeTracerProvider } from '@opentelemetry/sdk-trace-node';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http';

const OBSERVE_COMMAND = 'observe';
const COMPOSE_PROJECT_NAME = 'smith-observability';
const DEFAULT_OTEL_HTTP_ENDPOINT = 'http://localhost:13318';
const DEFAULT_OTEL_HTTP_TRACES_ENDPOINT = `${DEFAULT_OTEL_HTTP_ENDPOINT}/v1/traces`;
const DEFAULT_OTEL_GRPC_ENDPOINT = 'localhost:13317';
const DEFAULT_BIFROST_URL = 'http://127.0.0.1:16080';
const HEALTH_CHECK_TIMEOUT_MS = 15000;
const HEALTH_CHECK_INTERVAL_MS = 500;
const CODEX_FALLBACK_MODEL = 'gpt-5-codex';
const CODEX_PROVIDER_KEY = 'openai-responses';
const BIFROST_HEALTH_PATH = '/healthz';
const CLICKHOUSE_SCHEMA_PATH = '/docker-entrypoint-initdb.d/00-init.sql';

async function main() {
  const [command, ...rawArgs] = process.argv.slice(2);

  if (!command || command === '--help' || command === '-h') {
    printUsage();
    process.exit(command ? 0 : 1);
  }

  if (command !== OBSERVE_COMMAND) {
    console.error(`Unknown command "${command}".`);
    printUsage();
    process.exit(1);
  }

  const [agent, ...rest] = rawArgs;
  if (!agent) {
    console.error('Missing agent name.\n');
    printUsage();
    process.exit(1);
  }

  const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
  await ensureObservabilityStack(packageRoot);

  const agentArgs = rest[0] === '--' ? rest.slice(1) : rest;
  const { env: baseEnv, resourceAttributes } = await buildAgentConfiguration(agent);
  await waitForBifrost(baseEnv.SMITH_BIFROST_URL);
  const {
    env: finalEnv,
    args: finalArgs,
    resolvedModel,
    resolvedModelSource
  } = configureAgent(agent, agentArgs, baseEnv);

  if (resolvedModel) {
    resourceAttributes['smith.agent.model'] = resolvedModel;
    finalEnv.OTEL_RESOURCE_ATTRIBUTES = serialiseResourceAttributes(resourceAttributes);
  }

  if (resolvedModel && resolvedModelSource === 'fallback') {
    console.log(`[smith] Using Codex model fallback: ${resolvedModel}`);
  } else if (resolvedModel && resolvedModelSource === 'env') {
    console.log(`[smith] Using Codex model from CODEX_DEFAULT_MODEL: ${resolvedModel}`);
  }

  const tracing = initializeTracing(resourceAttributes, finalEnv);

  await runAgent(agent, finalArgs, finalEnv, tracing, resourceAttributes);
}

function printUsage() {
  console.log(`
Usage: smith observe <agent> [-- <agent args>]

Starts (or reuses) the Smith observability stack, wires OTEL environment
variables, and launches the specified agent binary.
`.trim());
}

async function ensureObservabilityStack(packageRoot) {
  const composeFile = path.join(packageRoot, 'docker-compose.yaml');
  const dockerCmd = resolveDockerComposeCommand();
  const composeEnv = buildComposeEnvironment();

  console.log('[smith] Ensuring observability stack is running…');
  const args = [
    ...dockerCmd.args,
    '-f',
    composeFile,
    '-p',
    COMPOSE_PROJECT_NAME,
    'up',
    '-d',
    '--remove-orphans'
  ];

  try {
    await runCommand(dockerCmd.command, args, { env: composeEnv });
  } catch (error) {
    if (error?.message?.includes('Conflict')) {
      console.error(
        '[smith] Docker reported a name conflict. Remove old containers via ' +
          `"${dockerCmd.command} ${dockerCmd.args.join(' ')} -p ${COMPOSE_PROJECT_NAME} ` +
          `-f ${composeFile} down" or delete the conflicting container manually.`
      );
    }
    throw error;
  }

  await waitForCollector();
  await ensureClickhouseSchema(dockerCmd, composeFile, composeEnv);
}

function buildComposeEnvironment() {
  const env = { ...process.env };
  const overrideBase =
    env.SMITH_OBSERVABILITY_OPENAI_BASE_URL ?? env.SMITH_OPENAI_BASE_URL;
  if (overrideBase && !env.OPENAI_BASE_URL) {
    env.OPENAI_BASE_URL = overrideBase;
  }
  return env;
}

async function waitForCollector() {
  const deadline = Date.now() + HEALTH_CHECK_TIMEOUT_MS;

  while (Date.now() < deadline) {
    try {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 2000);
      const response = await fetch(DEFAULT_OTEL_HTTP_ENDPOINT, {
        method: 'GET',
        signal: controller.signal
      });
      clearTimeout(timeout);

      if (response.ok || response.status === 404 || response.status === 405) {
        return;
      }
    } catch (error) {
      // ignore — retry until timeout
    }

    try {
      await delay(HEALTH_CHECK_INTERVAL_MS);
    } catch {
      return;
    }
  }

  console.warn('[smith] Timed out waiting for the OTEL collector to respond on port 13318.');
}

async function ensureClickhouseSchema(dockerCmd, composeFile, composeEnv) {
  const args = [
    ...dockerCmd.args,
    '-f',
    composeFile,
    '-p',
    COMPOSE_PROJECT_NAME,
    'exec',
    '-T',
    'clickhouse',
    'bash',
    '-lc',
    `clickhouse-client --multiquery < ${CLICKHOUSE_SCHEMA_PATH}`
  ];

  try {
    await runCommand(dockerCmd.command, args, { env: composeEnv });
  } catch (error) {
    console.warn('[smith] Failed to refresh ClickHouse schema:', error?.message ?? error);
  }
}

async function waitForBifrost(rawGatewayUrl) {
  const baseUrl = normaliseBaseUrl(rawGatewayUrl) ?? DEFAULT_BIFROST_URL;
  let healthUrl;

  try {
    const parsed = new URL(baseUrl);
    const trimmedPath = parsed.pathname.replace(/\/+$/, '');
    parsed.pathname = `${trimmedPath}${BIFROST_HEALTH_PATH}`.replace(/\/{2,}/g, '/');
    parsed.search = '';
    parsed.hash = '';
    healthUrl = parsed.toString();
  } catch {
    healthUrl = `${DEFAULT_BIFROST_URL}${BIFROST_HEALTH_PATH}`;
  }

  const deadline = Date.now() + HEALTH_CHECK_TIMEOUT_MS;

  while (Date.now() < deadline) {
    try {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 2000);
      const response = await fetch(healthUrl, {
        method: 'GET',
        signal: controller.signal
      });
      clearTimeout(timeout);

      if (response.ok || response.status === 404) {
        return;
      }
    } catch {
      // ignore — retry until timeout
    }

    try {
      await delay(HEALTH_CHECK_INTERVAL_MS);
    } catch {
      return;
    }
  }

  console.warn(`[smith] Timed out waiting for Bifrost to respond on ${healthUrl}.`);
}

function resolveDockerComposeCommand() {
  const docker = spawnSync('docker', ['compose', 'version']);
  if (docker.status === 0) {
    return { command: 'docker', args: ['compose'] };
  }

  const legacy = spawnSync('docker-compose', ['version']);
  if (legacy.status === 0) {
    return { command: 'docker-compose', args: [] };
  }

  console.error('Neither "docker compose" nor "docker-compose" is available on PATH.');
  process.exit(1);
}

async function runCommand(command, args, options = {}) {
  const child = spawn(command, args, {
    stdio: 'inherit',
    ...options
  });

  await new Promise((resolve, reject) => {
    child.on('exit', code => {
      if (code === 0) resolve();
      else reject(new Error(`Command "${command} ${args.join(' ')}" exited with code ${code}`));
    });
    child.on('error', reject);
  });
}

async function buildAgentConfiguration(agentName) {
  const env = { ...process.env };
  const resourceAttributes = parseResourceAttributes(env.OTEL_RESOURCE_ATTRIBUTES);
  const defaultServiceName = `smith-agent-${agentName}`;

  if (!resourceAttributes['service.name']) {
    resourceAttributes['service.name'] = defaultServiceName;
  }
  resourceAttributes['smith.agent.name'] = agentName;

  const gitMetadata = detectGitMetadata();
  Object.assign(resourceAttributes, gitMetadata);

  if (env.SMITH_OBSERVABILITY_KEEP_OTEL === '1') {
    // honour caller-provided OTEL variables
  } else {
    env.OTEL_EXPORTER_OTLP_ENDPOINT = DEFAULT_OTEL_GRPC_ENDPOINT;
    env.OTEL_EXPORTER_OTLP_PROTOCOL = 'grpc';
    env.OTEL_EXPORTER_OTLP_INSECURE = 'true';

    env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = DEFAULT_OTEL_HTTP_TRACES_ENDPOINT;
    env.OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = 'http/protobuf';
    env.OTEL_EXPORTER_OTLP_TRACES_INSECURE = 'true';
  }

  const providedGateway =
    normaliseBaseUrl(env.SMITH_BIFROST_URL) ?? normaliseBaseUrl(env.BIFROST_GATEWAY_URL);
  env.BIFROST_GATEWAY_URL = providedGateway ?? DEFAULT_BIFROST_URL;
  env.SMITH_BIFROST_URL = env.BIFROST_GATEWAY_URL;
  resourceAttributes['smith.bifrost.gateway_url'] = env.BIFROST_GATEWAY_URL;

  env.OTEL_RESOURCE_ATTRIBUTES = serialiseResourceAttributes(resourceAttributes);
  env.OTEL_SERVICE_NAME = resourceAttributes['service.name'];
  env.SMITH_OBSERVABILITY_ENABLED = '1';

  return { env, resourceAttributes };
}

function initializeTracing(resourceAttributes, env) {
  if (globalThis.__SMITH_TRACING) {
    return globalThis.__SMITH_TRACING;
  }

  try {
    diag.setLogger(new DiagConsoleLogger(), DiagLogLevel.ERROR);

    const resource = Resource.default().merge(new Resource(resourceAttributes));
    const exporter = new OTLPTraceExporter({
      url: env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT ?? DEFAULT_OTEL_HTTP_TRACES_ENDPOINT
    });

    const provider = new NodeTracerProvider({ resource });
    provider.addSpanProcessor(new BatchSpanProcessor(exporter));
    provider.register();

    const tracer = trace.getTracer('smith-observability-cli');
    const tracing = { tracer, provider };
    globalThis.__SMITH_TRACING = tracing;
    return tracing;
  } catch (error) {
    console.warn('[smith] Failed to initialise local tracing:', error?.message ?? error);
    return null;
  }
}

async function runAgent(agent, args, env, tracing, resourceAttributes) {
  console.log(`[smith] Launching agent "${agent}"…`);

  const span = tracing?.tracer?.startSpan('smith.observe', {
    attributes: buildSpanAttributes(agent, args, resourceAttributes)
  });
  span?.addEvent('agent.start', {
    'smith.agent.command': agent,
    'smith.agent.args': args.join(' ')
  });

  const child = spawn(agent, args, {
    stdio: 'inherit',
    env
  });

  let exitCode;
  let capturedError = null;

  try {
    await new Promise((resolve, reject) => {
      child.on('exit', code => {
        exitCode = code;
        if (code === 0) resolve();
        else reject(new Error(`Agent "${agent}" exited with code ${code}`));
      });
      child.on('error', reject);
    });
  } catch (error) {
    capturedError = error;
    throw error;
  } finally {
    if (span) {
      if (typeof exitCode === 'number') {
        span.setAttribute('process.exit_code', exitCode);
      }
      if (capturedError) {
        span.recordException(capturedError);
        span.setStatus({ code: SpanStatusCode.ERROR, message: capturedError.message });
      } else {
        span.setStatus({ code: SpanStatusCode.OK });
      }
      span.addEvent('agent.end', {
        'process.exit_code': exitCode ?? 'unknown'
      });
      span.end();
    }

    await safeForceFlush(tracing?.provider);
  }
}

async function safeForceFlush(provider) {
  if (!provider) return;
  try {
    await provider.forceFlush();
  } catch (error) {
    console.warn('[smith] Failed to flush spans:', error?.message ?? error);
  }
}

function parseResourceAttributes(raw) {
  const attributes = {};
  if (!raw) return attributes;

  for (const segment of raw.split(',')) {
    const [key, value] = segment.split('=');
    if (!key || value === undefined) continue;
    const trimmedKey = key.trim();
    const trimmedValue = value.trim();
    if (!trimmedKey || !trimmedValue) continue;
    attributes[trimmedKey] = trimmedValue;
  }
  return attributes;
}

function serialiseResourceAttributes(map) {
  return Object.entries(map)
    .filter(([, value]) => value !== undefined && value !== null && `${value}`.length > 0)
    .map(([key, value]) => `${key}=${value}`)
    .join(',');
}

function detectGitMetadata() {
  const metadata = {};
  const cwd = process.cwd();

  const topLevel = spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd,
    encoding: 'utf8'
  });
  if (topLevel.status !== 0) {
    return metadata;
  }

  const root = topLevel.stdout.trim();
  if (!root) return metadata;

  metadata['smith.git.root'] = escapeAttributeValue(root);

  const remote = spawnSync('git', ['config', '--get', 'remote.origin.url'], {
    cwd,
    encoding: 'utf8'
  });
  if (remote.status === 0) {
    const repoUrl = remote.stdout.trim();
    if (repoUrl) {
      metadata['smith.git.remote'] = escapeAttributeValue(repoUrl);
    }
  }

  const branch = spawnSync('git', ['rev-parse', '--abbrev-ref', 'HEAD'], {
    cwd,
    encoding: 'utf8'
  });
  if (branch.status === 0) {
    const name = branch.stdout.trim();
    if (name) {
      metadata['smith.git.branch'] = escapeAttributeValue(name);
    }
  }

  metadata['smith.git.status_dirty'] = isGitDirty(cwd) ? 'true' : 'false';

  return metadata;
}

function escapeAttributeValue(value) {
  return value.replace(/,/g, '\\,');
}

function isGitDirty(cwd) {
  const status = spawnSync('git', ['status', '--porcelain'], {
    cwd,
    encoding: 'utf8'
  });
  if (status.status !== 0) {
    return false;
  }
  return status.stdout.trim().length > 0;
}

function configureAgent(agentName, args, env) {
  const normalized = agentName.toLowerCase();
  if (normalized === 'codex') {
    return configureCodexAgent(args, env);
  }
  if (normalized.startsWith('claude')) {
    return configureClaudeAgent(args, env);
  }
  return { args, env };
}

function configureCodexAgent(originalArgs, env) {
  const nextEnv = { ...env };
  const gatewayUrl = resolveGatewayUrl(nextEnv);
  const openAiBaseUrl = resolveOpenAiBaseUrl(nextEnv, gatewayUrl);

  if (!nextEnv.OPENAI_API_KEY) {
    const discovered = resolveOpenAiKeyFromProfile();
    if (discovered) {
      nextEnv.OPENAI_API_KEY = discovered;
    }
  }

  if (!nextEnv.OPENAI_BASE_URL) {
    nextEnv.OPENAI_BASE_URL = openAiBaseUrl;
  }
  if (!nextEnv.OPENAI_API_BASE) {
    nextEnv.OPENAI_API_BASE = openAiBaseUrl;
  }
  if (!nextEnv.CODEX_BASE_URL) {
    nextEnv.CODEX_BASE_URL = openAiBaseUrl;
  }

  const {
    args: transformedArgs,
    resolvedModel,
    resolvedModelSource
  } = transformCodexArgs(originalArgs, openAiBaseUrl);
  return {
    args: transformedArgs,
    env: nextEnv,
    resolvedModel,
    resolvedModelSource
  };
}

function configureClaudeAgent(originalArgs, env) {
  const nextEnv = { ...env };
  const gatewayUrl = resolveGatewayUrl(nextEnv);
  const anthropicBaseUrl = resolveAnthropicBaseUrl(nextEnv, gatewayUrl);

  if (!nextEnv.CLAUDE_CODE_GATEWAY_URL) {
    nextEnv.CLAUDE_CODE_GATEWAY_URL = gatewayUrl;
  }
  if (!nextEnv.ANTHROPIC_API_URL) {
    nextEnv.ANTHROPIC_API_URL = anthropicBaseUrl;
  }
  if (!nextEnv.ANTHROPIC_BASE_URL) {
    nextEnv.ANTHROPIC_BASE_URL = anthropicBaseUrl;
  }

  return {
    args: originalArgs,
    env: nextEnv
  };
}

function transformCodexArgs(originalArgs, openAiBaseUrl) {
  const args = [...originalArgs];
  let hasModelArgument = false;
  let hasModelProviderOverride = false;
  let hasProviderConfigOverride = false;
  let resolvedModel;
  let resolvedModelSource;
  const result = [];

  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (typeof arg !== 'string') {
      result.push(arg);
      continue;
    }

    if (arg === '--model' || arg === '-m') {
      const next = args[index + 1];
      if (typeof next === 'string') {
        resolvedModel = prefixModel(next);
        resolvedModelSource = 'args';
        result.push(arg, resolvedModel);
        hasModelArgument = true;
        index += 1;
        continue;
      }
      result.push(arg);
      continue;
    }

    if (arg.startsWith('--model=')) {
      const [, value] = arg.split('=', 2);
      resolvedModel = prefixModel(value ?? '');
      resolvedModelSource = 'args';
      result.push(`--model=${resolvedModel}`);
      hasModelArgument = true;
      continue;
    }

    if (arg.startsWith('-m=')) {
      const [, value] = arg.split('=', 2);
      resolvedModel = prefixModel(value ?? '');
      resolvedModelSource = 'args';
      result.push(`-m=${resolvedModel}`);
      hasModelArgument = true;
      continue;
    }

    if ((arg === '--config' || arg === '-c') && typeof args[index + 1] === 'string') {
      const next = args[index + 1];
      if (next.includes('model_provider=')) {
        hasModelProviderOverride = true;
      }
      if (next.includes(`model_providers.${CODEX_PROVIDER_KEY}`)) {
        hasProviderConfigOverride = true;
      }
      result.push(arg);
      continue;
    }

    if (arg.startsWith('--config=') || arg.startsWith('-c=')) {
      if (arg.includes('model_provider=')) {
        hasModelProviderOverride = true;
      }
      if (arg.includes(`model_providers.${CODEX_PROVIDER_KEY}`)) {
        hasProviderConfigOverride = true;
      }
      result.push(arg);
      continue;
    }

    if (arg.includes('model_provider=')) {
      hasModelProviderOverride = true;
    }
    if (arg.includes(`model_providers.${CODEX_PROVIDER_KEY}`)) {
      hasProviderConfigOverride = true;
    }

    result.push(arg);
  }

  if (!hasModelArgument) {
    const { model, source } = resolveDefaultCodexModel();
    if (model) {
      resolvedModel = prefixModel(model);
      resolvedModelSource = source;
      result.push('--model', resolvedModel);
    }
  }

  if (!hasModelProviderOverride) {
    result.push('--config', `model_provider="${CODEX_PROVIDER_KEY}"`);
  }
  if (!hasProviderConfigOverride) {
    const providerConfig = `model_providers.${CODEX_PROVIDER_KEY}={name="OpenAI Responses",base_url="${openAiBaseUrl}",env_key="OPENAI_API_KEY",wire_api="responses"}`;
    result.push('--config', providerConfig);
  }

  return {
    args: result,
    resolvedModel,
    resolvedModelSource
  };
}

function resolveGatewayUrl(env) {
  return (
    normaliseBaseUrl(
      env.SMITH_BIFROST_URL ??
        env.BIFROST_GATEWAY_URL ??
        DEFAULT_BIFROST_URL
    ) ?? DEFAULT_BIFROST_URL
  );
}

function resolveOpenAiBaseUrl(env, gatewayUrl) {
  const explicit =
    normaliseBaseUrl(env.OPENAI_BASE_URL) ??
    normaliseBaseUrl(env.OPENAI_API_BASE) ??
    normaliseBaseUrl(env.CODEX_BASE_URL);
  if (explicit) {
    return explicit.toLowerCase().endsWith('/v1')
      ? explicit
      : `${explicit}/v1`;
  }
  const base = normaliseBaseUrl(gatewayUrl) ?? DEFAULT_BIFROST_URL;
  return `${base}/v1`;
}

function resolveAnthropicBaseUrl(env, gatewayUrl) {
  const explicit =
    normaliseBaseUrl(env.ANTHROPIC_API_URL) ??
    normaliseBaseUrl(env.ANTHROPIC_BASE_URL);
  if (explicit) {
    return explicit.toLowerCase().endsWith('/anthropic')
      ? explicit
      : `${explicit}/anthropic`;
  }
  const base = normaliseBaseUrl(gatewayUrl) ?? DEFAULT_BIFROST_URL;
  return `${base}/anthropic`;
}

function normaliseBaseUrl(value) {
  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  if (trimmed.length === 0) return undefined;
  return trimmed.replace(/\/+$/, '');
}

function prefixModel(model) {
  if (typeof model !== 'string' || model.trim().length === 0) {
    return model;
  }
  if (model.includes('/')) {
    return model;
  }
  return `openai/${model}`;
}

function resolveOpenAiKeyFromProfile() {
  const envKey = process.env.OPENAI_API_KEY;
  if (typeof envKey === 'string' && envKey.trim().length > 0) {
    return envKey.trim();
  }

  const home = process.env.HOME;
  if (!home) return null;

  const bashrcPath = path.join(home, '.bashrc');
  try {
    const contents = readFileSync(bashrcPath, 'utf8');
    const match = contents.match(/export\s+OPENAI_API_KEY\s*=\s*['\"]?([^'\"\n]+)['\"]?/);
    if (match && match[1]) {
      return match[1].trim();
    }
  } catch {
    // ignore – not present or unreadable
  }

  return null;
}

function buildSpanAttributes(agent, args, resourceAttributes) {
  const attributes = {
    'smith.agent.name': agent,
    'smith.agent.args': args.join(' '),
    'process.executable.name': agent,
    'process.command_line': [agent, ...args].join(' '),
    'smith.working_directory': process.cwd()
  };

  for (const [key, value] of Object.entries(resourceAttributes)) {
    if (value !== undefined && value !== null && value !== '') {
      attributes[key] = value;
    }
  }

  return attributes;
}

function resolveDefaultCodexModel() {
  const envValue = process.env.CODEX_DEFAULT_MODEL;
  if (typeof envValue === 'string') {
    const trimmed = envValue.trim();
    if (trimmed.length > 0) {
      return { model: trimmed, source: 'env' };
    }
  }
  return { model: CODEX_FALLBACK_MODEL, source: 'fallback' };
}

if (process.env.SMITH_OBSERVABILITY_SKIP_MAIN === '1') {
  // Running under tests – skip executing the CLI entrypoint.
} else {
  main().catch(error => {
    console.error('[smith] Fatal error:', error.message ?? error);
    process.exit(1);
  });
}

export { transformCodexArgs, prefixModel, resolveDefaultCodexModel };
