import { strict as assert } from 'node:assert';
import test from 'node:test';

const previousSkip = process.env.SMITH_OBSERVABILITY_SKIP_MAIN;
process.env.SMITH_OBSERVABILITY_SKIP_MAIN = '1';
const smithModule = await import(new URL('../bin/smith.mjs', import.meta.url));

if (previousSkip === undefined) {
  delete process.env.SMITH_OBSERVABILITY_SKIP_MAIN;
} else {
  process.env.SMITH_OBSERVABILITY_SKIP_MAIN = previousSkip;
}

const { transformCodexArgs, prefixModel } = smithModule;

function withEnv(key, value, callback) {
  const previous = process.env[key];

  if (value === undefined) {
    delete process.env[key];
  } else {
    process.env[key] = value;
  }

  try {
    callback();
  } finally {
    if (previous === undefined) {
      delete process.env[key];
    } else {
      process.env[key] = previous;
    }
  }
}

test('injects fallback Codex model when none supplied', () => {
  withEnv('CODEX_DEFAULT_MODEL', undefined, () => {
    const { args, resolvedModel, resolvedModelSource } = transformCodexArgs(
      [],
      'http://localhost:16080/v1'
    );

    const modelFlagIndex = args.indexOf('--model');
    assert.ok(modelFlagIndex >= 0, 'expected --model flag to be injected');
    assert.equal(args[modelFlagIndex + 1], 'openai/gpt-5-codex');
    assert.equal(resolvedModel, 'openai/gpt-5-codex');
    assert.equal(resolvedModelSource, 'fallback');
  });
});

test('respects CODEX_DEFAULT_MODEL environment variable', () => {
  withEnv('CODEX_DEFAULT_MODEL', 'gpt-4o', () => {
    const { args, resolvedModel, resolvedModelSource } = transformCodexArgs(
      [],
      'http://localhost:16080/v1'
    );

    const modelFlagIndex = args.indexOf('--model');
    assert.ok(modelFlagIndex >= 0, 'expected --model flag to be injected');
    assert.equal(args[modelFlagIndex + 1], 'openai/gpt-4o');
    assert.equal(resolvedModel, 'openai/gpt-4o');
    assert.equal(resolvedModelSource, 'env');
  });
});

test('keeps explicit provider/model argument', () => {
  withEnv('CODEX_DEFAULT_MODEL', 'gpt-4o', () => {
    const { args, resolvedModel, resolvedModelSource } = transformCodexArgs(
      ['--model', 'anthropic/claude-3'],
      'http://localhost:16080/v1'
    );

    assert.equal(resolvedModel, 'anthropic/claude-3');
    assert.equal(resolvedModelSource, 'args');

    const modelArgs = args.filter(value => value === '--model');
    assert.equal(modelArgs.length, 1);
  });
});

test('prefixes short model provided via -m flag', () => {
  withEnv('CODEX_DEFAULT_MODEL', undefined, () => {
    const { args, resolvedModel } = transformCodexArgs(
      ['-m', 'gpt-4o-mini'],
      'http://localhost:16080/v1'
    );

    const flagIndex = args.indexOf('-m');
    assert.ok(flagIndex >= 0);
    assert.equal(args[flagIndex + 1], 'openai/gpt-4o-mini');
    assert.equal(resolvedModel, 'openai/gpt-4o-mini');
  });
});

test('prefixes short model provided via --model=value syntax', () => {
  withEnv('CODEX_DEFAULT_MODEL', undefined, () => {
    const { args, resolvedModel } = transformCodexArgs(
      ['--model=gpt-4o-mini'],
      'http://localhost:16080/v1'
    );

    const modelArg = args.find(arg => arg.startsWith('--model='));
    assert.ok(modelArg);
    assert.equal(modelArg, '--model=openai/gpt-4o-mini');
    assert.equal(resolvedModel, 'openai/gpt-4o-mini');
  });
});

test('prefixModel adds openai/ prefix when missing', () => {
  assert.equal(prefixModel('gpt-4o-mini'), 'openai/gpt-4o-mini');
  assert.equal(prefixModel('openai/gpt-4o-mini'), 'openai/gpt-4o-mini');
  assert.equal(prefixModel(''), '');
  assert.equal(prefixModel(undefined), undefined);
});
