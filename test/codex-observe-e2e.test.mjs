import { strict as assert } from 'node:assert';
import test from 'node:test';
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const __filename = fileURLToPath(import.meta.url);
const projectRoot = path.resolve(path.dirname(__filename), '..');

const composeArgs = [
  'compose',
  '--profile',
  'test',
  '-f',
  path.join(projectRoot, 'docker-compose.yaml'),
  '-p',
  'smith-observability'
];

function run(command, args, { env, stdio } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: projectRoot,
      env: env ? { ...process.env, ...env } : process.env,
      stdio: stdio ?? ['ignore', 'pipe', 'pipe']
    });

    let stdout = '';
    let stderr = '';
    if (child.stdout) {
      child.stdout.setEncoding('utf8');
      child.stdout.on('data', chunk => {
        stdout += chunk;
      });
    }
    if (child.stderr) {
      child.stderr.setEncoding('utf8');
      child.stderr.on('data', chunk => {
        stderr += chunk;
      });
    }

    child.on('error', reject);
    child.on('close', code => {
      if (code === 0) {
        resolve({ stdout, stderr });
      } else {
        const error = new Error(
          `Command "${command} ${args.join(' ')}" exited with code ${code}\n${stderr}`
        );
        error.stdout = stdout;
        error.stderr = stderr;
        error.code = code;
        reject(error);
      }
    });
  });
}

async function waitForOutputJson() {
  const query =
    "SELECT SpanAttributes['gen_ai.responses.output_json'] FROM otel.otel_traces WHERE SpanName='gen_ai.responses' ORDER BY Timestamp DESC LIMIT 1 FORMAT TSVRaw";
  const queryArgs = [
    ...composeArgs,
    'exec',
    '-T',
    'clickhouse',
    'clickhouse-client',
    '--query',
    query
  ];

  for (let attempt = 0; attempt < 15; attempt += 1) {
    console.log(`[e2e] ClickHouse query attempt ${attempt + 1}/15:\n${query}`);
    const { stdout } = await run('docker', queryArgs);
    console.log('[e2e] ClickHouse query output:\n', stdout || '(empty)');
    const trimmed = stdout.trim();
    if (trimmed.length > 0) {
      return trimmed;
    }
    await delay(1000);
  }

  throw new Error('Timed out waiting for responses output JSON in ClickHouse');
}

test('smith observe codex captures responses output body in ClickHouse', async t => {
  await run('docker', [...composeArgs, 'up', '-d', 'openai-stub']);

  t.after(async () => {
    await run('docker', [...composeArgs, 'down', '-v']);
  });

  const env = {
    OPENAI_API_KEY: 'test',
    SMITH_OBSERVABILITY_BIFROST_CONFIG: path.join(
      projectRoot,
      'test',
      'support',
      'bifrost-stub.config.json'
    )
  };

  await run(
    'node',
    [
      path.join(projectRoot, 'bin', 'smith.mjs'),
      'observe',
      'codex',
      '--',
      '--model',
      'openai/gpt-4o-mini',
      'exec',
      'Call the `list_directory` tool on "." to list the repository root, then summarize what you found.'
    ],
    { env, stdio: 'inherit' }
  );

  const rawJson = await waitForOutputJson();
  let parsed;
  try {
    parsed = JSON.parse(rawJson);
  } catch (error) {
    throw new Error(`Failed to parse ClickHouse JSON payload: ${error.message}\n${rawJson}`);
  }

  assert.ok(Array.isArray(parsed), 'expected output_json to be an array of messages');
  assert.ok(parsed.length >= 1, 'expected at least one message in output_json array');

  const functionCallMessage = parsed.find(
    message => message?.type === 'function_call' && message?.role === 'assistant'
  );
  assert.ok(functionCallMessage, 'expected a function_call message in the response output');
  assert.equal(
    functionCallMessage.name,
    'list_directory',
    'function_call should target the stubbed tool'
  );
  assert.ok(
    typeof functionCallMessage.arguments === 'string' && functionCallMessage.arguments.length > 0,
    'function_call should include arguments payload'
  );
  let toolCallArgs;
  try {
    toolCallArgs = JSON.parse(functionCallMessage.arguments);
  } catch (error) {
    throw new Error(
      `function_call arguments should parse as JSON: ${error.message}\n${functionCallMessage.arguments}`
    );
  }
  assert.deepEqual(toolCallArgs, { path: '.' }, 'function_call arguments should match the stub payload');

  const functionCallOutputMessage = parsed.find(
    message => message?.type === 'function_call_output' && message?.role === 'tool'
  );
  assert.ok(
    functionCallOutputMessage,
    'expected a function_call_output message containing tool execution results'
  );
  assert.ok(
    typeof functionCallOutputMessage.output === 'string' &&
      functionCallOutputMessage.output.length > 0,
    'function_call_output should include an output payload'
  );
  let toolResultOutput;
  try {
    toolResultOutput = JSON.parse(functionCallOutputMessage.output);
  } catch (error) {
    throw new Error(
      `function_call_output should parse as JSON: ${error.message}\n${functionCallOutputMessage.output}`
    );
  }
  assert.deepEqual(
    toolResultOutput,
    ['README.md', 'package.json'],
    'function_call_output should match the stub payload'
  );

  const assistantContentParts = parsed.flatMap(message => {
    if (message?.role !== 'assistant' || !Array.isArray(message.content)) return [];
    return message.content;
  });
  const joinedContent = assistantContentParts.map(part => JSON.stringify(part)).join(' ');

  assert.match(
    joinedContent,
    /Observability stub agent acknowledging the prompt/i,
    'should include assistant output text'
  );
});
