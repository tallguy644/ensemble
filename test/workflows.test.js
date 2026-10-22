import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
process.env.ENSEMBLE_MOCK = path.join(here, 'mock-agent.js');
process.env.ENSEMBLE_HOME = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-home-'));

const { startCouncil, startBuild, agentStatus } = await import('../src/engine.js');
const { preparePrompt, runAgent, agentEnv, checkAgent, formatPerplexity } = await import('../src/agents.js');
const { loadConfig } = await import('../src/config.js');
const http = await import('node:http');

test('doctor sees all five mock agents', async () => {
  const s = await agentStatus({ refresh: true });
  assert.deepEqual(s.map((a) => `${a.id}:${a.status}`), ['claude:ready', 'codex:ready', 'gemini:ready', 'perplexity:ready', 'deepseek:ready']);
});

test('council: answers, cross-review, synthesis', async () => {
  const run = await startCouncil({ question: 'What is 2+2?', rounds: 2 });
  await run.done;
  assert.equal(run.status, 'done', JSON.stringify(run.events.at(-1)));
  const starts = run.events.filter((e) => e.type === 'step-start');
  assert.equal(starts.length, 11); // 5 answers + 5 revisions + 1 synthesis
  assert.match(run.result.markdown, /Final merged answer from 5 panel answers/);
  assert.ok(fs.existsSync(path.join(run.runDir, 'final.md')));
});

test('build: plan → build → review → fix → approve, on its own branch', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-proj-'));
  fs.writeFileSync(path.join(dir, 'README.md'), '# demo\n');
  const run = await startBuild({ dir, task: 'Create hello.txt' });
  await run.done;
  assert.equal(run.status, 'done', JSON.stringify(run.events.at(-1)));
  const steps = run.events.filter((e) => e.type === 'step-start').map((e) => `${e.title}:${e.agent}`);
  assert.deepEqual(steps, ['Research:perplexity', 'Plan:claude', 'Build:codex', 'Review 1:gemini', 'Fix 1:claude', 'Review 2:gemini']);
  assert.ok(fs.existsSync(path.join(dir, '.ensemble', 'planner-saw-research')), 'planner received the research');
  assert.ok(fs.existsSync(path.join(dir, '.ensemble', 'RESEARCH.md')));
  assert.equal(run.result.verdict, 'approved');
  assert.match(fs.readFileSync(path.join(dir, 'hello.txt'), 'utf8'), /hello from codex\nfixed by claude/);
  const branch = execFileSync('git', ['branch', '--show-current'], { cwd: dir, encoding: 'utf8' }).trim();
  assert.match(branch, /^ensemble\/create-hello-txt-/);
  const status = execFileSync('git', ['status', '--porcelain'], { cwd: dir, encoding: 'utf8' }).trim();
  assert.equal(status, '', 'work is committed and .ensemble/ is ignored');
});

test('build refuses a dirty repo unless allowed', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-dirty-'));
  execFileSync('git', ['init', '-q'], { cwd: dir });
  fs.writeFileSync(path.join(dir, 'a.txt'), 'a');
  execFileSync('git', ['-c', 'user.name=t', '-c', 'user.email=t@t', 'add', '-A'], { cwd: dir });
  execFileSync('git', ['-c', 'user.name=t', '-c', 'user.email=t@t', 'commit', '-qm', 'init'], { cwd: dir });
  fs.writeFileSync(path.join(dir, 'a.txt'), 'changed');
  await assert.rejects(startBuild({ dir, task: 'x' }), /uncommitted changes/);
});

test('falls back to the next agent when one is out of usage', async () => {
  process.env.MOCK_FAIL_CODEX = 'limit';
  try {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-limit-'));
    const run = await startBuild({ dir, task: 'Create hello.txt', maxReviewRounds: 1 });
    await run.done;
    assert.equal(run.status, 'done', JSON.stringify(run.events.at(-1)));
    const notes = run.events.filter((e) => e.type === 'note').map((e) => e.text).join('\n');
    assert.match(notes, /usage limit/);
    assert.match(fs.readFileSync(path.join(dir, 'hello.txt'), 'utf8'), /hello from claude/);
  } finally {
    delete process.env.MOCK_FAIL_CODEX;
  }
});

test('long prompts for argument-only CLIs are moved into a file', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-arg-'));
  const agent = { label: 'G', maxArgPrompt: 100, modes: { read: ['-p', '{prompt}'] } };
  const short = preparePrompt(agent, 'read', 'hi', dir);
  assert.deepEqual(short, { args: ['-p', 'hi'], stdin: null });
  const long = preparePrompt(agent, 'read', 'x'.repeat(500), dir);
  assert.match(long.args[1], /instructions are in the file \.ensemble\/prompts\/prompt-/);
  const file = long.args[1].match(/(\.ensemble\/prompts\/\S+\.md)/)[1];
  assert.equal(fs.readFileSync(path.join(dir, file), 'utf8').length, 500);
});

test('research can be turned off, and a read-only agent is never given write roles', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-ro-'));
  const run = await startBuild({ dir, task: 'Create hello.txt', research: false, roles: { planner: 'perplexity', builder: 'perplexity', fixer: 'deepseek' }, maxReviewRounds: 1 });
  await run.done;
  assert.equal(run.status, 'done', JSON.stringify(run.events.at(-1)));
  const steps = run.events.filter((e) => e.type === 'step-start').map((e) => `${e.title}:${e.agent}`);
  assert.equal(steps[0], 'Plan:claude');
  assert.equal(steps[1], 'Build:codex', 'perplexity skipped for building');
  assert.equal(steps[3], 'Fix 1:deepseek');
});

test('Perplexity adapter: parses answer + sources, and reports out-of-credit', async () => {
  let lastAuth, lastBody;
  let status = 200;
  const srv = http.createServer(async (req, res) => {
    lastAuth = req.headers.authorization;
    let b = ''; for await (const c of req) b += c; lastBody = JSON.parse(b);
    if (status !== 200) { res.writeHead(status); return res.end('{"error":"insufficient credits"}'); }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ status: 'completed', output: [
      { type: 'search_results', results: [{ id: 'web:1', title: 'React 20 release notes', url: 'https://react.dev/blog' }] },
      { type: 'message', content: [{ type: 'output_text', text: 'React 20 is current [web:1].' }] },
    ], usage: { cost: { total_cost: 0.0123 } } }));
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  process.env.PERPLEXITY_API_KEY = 'pplx-test-key-123';
  const agent = { label: 'Perplexity', type: 'perplexity', keyName: 'perplexity', endpoint: `http://127.0.0.1:${srv.address().port}/v1/agent`, request: { preset: 'low' } };
  try {
    const ok = await runAgent(agent, { prompt: 'latest react?', timeoutMs: 5000 });
    assert.equal(ok.ok, true);
    assert.equal(lastAuth, 'Bearer pplx-test-key-123');
    assert.deepEqual(lastBody, { input: 'latest react?', preset: 'low' });
    assert.match(ok.output, /React 20 is current/);
    assert.match(ok.output, /\[React 20 release notes\]\(https:\/\/react\.dev\/blog\)/);
    const w = await runAgent(agent, { prompt: 'x', mode: 'write', timeoutMs: 5000 });
    assert.equal(w.ok, false);
    status = 402;
    const broke = await runAgent(agent, { prompt: 'x', timeoutMs: 5000 });
    assert.equal(broke.ok, false);
    assert.equal(broke.limited, true);
  } finally {
    srv.close();
    delete process.env.PERPLEXITY_API_KEY;
  }
});

test('key-based agents show needs-key without a key, and DeepSeek gets an isolated env', async () => {
  const saved = process.env.ENSEMBLE_MOCK;
  delete process.env.ENSEMBLE_MOCK;
  try {
    const cfg = loadConfig();
    const p = await checkAgent('perplexity', cfg.agents.perplexity);
    assert.equal(p.status, 'needs-key');
    const missing = await runAgent(cfg.agents.deepseek, { prompt: 'hi', cwd: os.tmpdir(), timeoutMs: 1000 });
    assert.equal(missing.keyMissing, true);
    process.env.DEEPSEEK_API_KEY = 'sk-deepseek-test-1234';
    process.env.ANTHROPIC_API_KEY = 'should-be-removed';
    const env = agentEnv(cfg.agents.deepseek);
    assert.equal(env.ANTHROPIC_AUTH_TOKEN, 'sk-deepseek-test-1234');
    assert.equal(env.ANTHROPIC_BASE_URL, 'https://api.deepseek.com/anthropic');
    assert.equal(env.ANTHROPIC_API_KEY, undefined);
    assert.ok(env.CLAUDE_CONFIG_DIR.startsWith(process.env.ENSEMBLE_HOME));
  } finally {
    process.env.ENSEMBLE_MOCK = saved;
    delete process.env.DEEPSEEK_API_KEY;
    delete process.env.ANTHROPIC_API_KEY;
  }
});

test('formatPerplexity tolerates output_text-only responses', () => {
  assert.equal(formatPerplexity({ output_text: 'hi' }), 'hi');
});
