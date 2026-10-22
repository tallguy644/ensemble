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
const { preparePrompt } = await import('../src/agents.js');

test('doctor sees all three mock agents', async () => {
  const s = await agentStatus({ refresh: true });
  assert.deepEqual(s.map((a) => a.status), ['ready', 'ready', 'ready']);
});

test('council: answers, cross-review, synthesis', async () => {
  const run = await startCouncil({ question: 'What is 2+2?', rounds: 2 });
  await run.done;
  assert.equal(run.status, 'done', JSON.stringify(run.events.at(-1)));
  const starts = run.events.filter((e) => e.type === 'step-start');
  assert.equal(starts.length, 7); // 3 answers + 3 revisions + 1 synthesis
  assert.match(run.result.markdown, /Final merged answer from 3 panel answers/);
  assert.ok(fs.existsSync(path.join(run.runDir, 'final.md')));
});

test('build: plan → build → review → fix → approve, on its own branch', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ens-proj-'));
  fs.writeFileSync(path.join(dir, 'README.md'), '# demo\n');
  const run = await startBuild({ dir, task: 'Create hello.txt' });
  await run.done;
  assert.equal(run.status, 'done', JSON.stringify(run.events.at(-1)));
  const steps = run.events.filter((e) => e.type === 'step-start').map((e) => `${e.title}:${e.agent}`);
  assert.deepEqual(steps, ['Plan:claude', 'Build:codex', 'Review 1:gemini', 'Fix 1:claude', 'Review 2:gemini']);
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
