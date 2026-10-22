// Launches an official AI CLI, feeds it a prompt, and collects its answer.

import { spawn, resolveCommand, isBatchFile } from './spawn.js';
import { spawn as nodeSpawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { getKey } from './keys.js';
import { HOME_DIR } from './config.js';

const LIMIT_PATTERNS = /usage limit|rate.?limit|quota|limit (?:reached|exceeded)|too many requests|\b429\b|resource.?exhausted|out of (?:credits|messages)|insufficient (?:balance|credits?|funds)|\b402\b|payment required/i;
const AUTH_PATTERNS = /not (?:logged|signed) in|please (?:log|sign) ?in|unauthori[sz]ed|authentication (?:required|failed)|invalid api key|\b401\b|run .*login/i;

let promptCounter = 0;

function killTree(child) {
  if (!child || child.exitCode !== null) return;
  if (process.platform === 'win32') {
    nodeSpawn('taskkill', ['/pid', String(child.pid), '/T', '/F'], { stdio: 'ignore' });
  } else {
    try { process.kill(-child.pid, 'SIGTERM'); } catch { child.kill('SIGTERM'); }
  }
}

/** Builds argv and stdin for one call, moving oversized prompts into a file. */
export function preparePrompt(agent, mode, prompt, cwd, { forceFile = false } = {}) {
  const template = agent.modes?.[mode];
  if (!template) throw new Error(`${agent.label} has no "${mode}" mode configured`);
  const usesArg = template.includes('{prompt}');
  let text = prompt;
  if (usesArg && (forceFile || (agent.maxArgPrompt && prompt.length > agent.maxArgPrompt))) {
    const dir = path.join(cwd, '.ensemble', 'prompts');
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, `prompt-${Date.now()}-${++promptCounter}.md`);
    fs.writeFileSync(file, prompt);
    const rel = path.relative(cwd, file).split(path.sep).join('/');
    text = `Your full instructions are in the file ${rel} (relative to the current folder). Read that whole file first, then do exactly what it says. Reply only with what it asks for.`;
  }
  const args = template.map((a) => (a === '{prompt}' ? text : a));
  return { args, stdin: usesArg ? null : text };
}

/**
 * Runs one agent call.
 * @returns {Promise<{ok, output, stderr, code, limited, authProblem, cancelled, timedOut, ms}>}
 */
/** Environment for a CLI agent, including any API key it needs. */
export function agentEnv(agent) {
  const env = { ...process.env, NO_COLOR: '1', FORCE_COLOR: '0' };
  for (const k of agent.unsetEnv || []) delete env[k];
  if (agent.env) {
    const key = agent.keyName ? getKey(agent.keyName) : null;
    for (const [k, v] of Object.entries(agent.env)) {
      env[k] = String(v).replace('{key}', key || '').replace('{home}', HOME_DIR);
    }
  }
  return env;
}

export function runAgent(agent, opts) {
  if (agent.keyName && !getKey(agent.keyName)) {
    return Promise.resolve({ ok: false, output: '', stderr: '', code: -1, keyMissing: true, ms: 0 });
  }
  if (agent.type === 'perplexity') return runPerplexity(agent, opts);
  return runCli(agent, opts);
}

/** Perplexity Agent API (web search with sources). Answers only; never edits files. */
async function runPerplexity(agent, { prompt, mode = 'read', timeoutMs, signal, onChunk = () => {} }) {
  const started = Date.now();
  const base = { output: '', stderr: '', code: 0, ms: 0 };
  if (mode === 'write') return { ...base, ok: false, code: -1, stderr: 'Perplexity cannot edit files.' };
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort('timeout'), timeoutMs);
  const onAbort = () => ctrl.abort('cancel');
  signal?.addEventListener('abort', onAbort, { once: true });
  try {
    const body = { input: prompt, ...(agent.request || { preset: 'low' }) };
    const res = await fetch(agent.endpoint || 'https://api.perplexity.ai/v1/agent', {
      method: 'POST',
      headers: { Authorization: `Bearer ${getKey(agent.keyName)}`, 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: ctrl.signal,
    });
    const raw = await res.text();
    if (!res.ok) {
      const msg = `HTTP ${res.status}: ${raw.slice(0, 300)}`;
      return { ...base, ok: false, code: res.status, stderr: msg, limited: [402, 429].includes(res.status) || LIMIT_PATTERNS.test(raw), authProblem: [401, 403].includes(res.status), ms: Date.now() - started };
    }
    const json = JSON.parse(raw);
    const output = formatPerplexity(json);
    onChunk(output, 'stdout');
    const cost = json.usage?.cost?.total_cost;
    return { ...base, ok: output.length > 0, output, stderr: cost != null ? `cost: $${Number(cost).toFixed(4)}` : '', ms: Date.now() - started };
  } catch (e) {
    const reason = ctrl.signal.reason;
    return { ...base, ok: false, code: -1, stderr: e.message, cancelled: reason === 'cancel', timedOut: reason === 'timeout', ms: Date.now() - started };
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', onAbort);
  }
}

export function formatPerplexity(json) {
  const items = Array.isArray(json.output) ? json.output : [];
  const text = (json.output_text || items
    .filter((o) => o.type === 'message')
    .flatMap((o) => o.content || [])
    .filter((c) => c.type === 'output_text' || typeof c.text === 'string')
    .map((c) => c.text)
    .join('\n')).trim();
  const sources = [];
  const seen = new Set();
  for (const o of items) {
    if (o.type !== 'search_results') continue;
    for (const r of o.results || []) {
      if (!r.url || seen.has(r.url)) continue;
      seen.add(r.url);
      sources.push(`- ${r.id != null ? `[${r.id}] ` : ''}[${(r.title || r.url).replace(/[\[\]]/g, '')}](${r.url})`);
    }
  }
  return text && sources.length ? `${text}\n\n**Sources**\n${sources.slice(0, 15).join('\n')}` : text;
}

function runCli(agent, { prompt, mode = 'read', cwd, timeoutMs, signal, onChunk = () => {} }) {
  return new Promise((resolve) => {
    const started = Date.now();
    let args, stdin;
    try {
      // cmd.exe truncates arguments at line breaks, so batch shims always get a file.
      const forceFile = isBatchFile(resolveCommand(agent.command)) && /[\r\n]/.test(prompt);
      ({ args, stdin } = preparePrompt(agent, mode, prompt, cwd, { forceFile }));
    } catch (e) {
      resolve({ ok: false, output: '', stderr: e.message, code: -1, ms: 0 });
      return;
    }

    let child;
    try {
      child = spawn(agent.command, args, {
        cwd,
        env: agentEnv(agent),
        stdio: ['pipe', 'pipe', 'pipe'],
        detached: process.platform !== 'win32',
        windowsHide: true,
      });
    } catch (e) {
      resolve({ ok: false, output: '', stderr: e.message, code: -1, ms: 0 });
      return;
    }

    let out = '';
    let err = '';
    let timedOut = false;
    let cancelled = false;

    const timer = setTimeout(() => { timedOut = true; killTree(child); }, timeoutMs);
    const onAbort = () => { cancelled = true; killTree(child); };
    signal?.addEventListener('abort', onAbort, { once: true });

    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (d) => { out += d; onChunk(d, 'stdout'); });
    child.stderr.on('data', (d) => { err += d; onChunk(d, 'stderr'); });
    child.stdin.on('error', () => {});
    if (stdin !== null) child.stdin.end(stdin); else child.stdin.end();

    const finish = (code, spawnError) => {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
      const both = `${out}\n${err}`;
      const ok = !spawnError && code === 0 && !timedOut && !cancelled && out.trim().length > 0;
      resolve({
        ok,
        output: out.trim(),
        stderr: spawnError ? spawnError.message : err.trim(),
        code,
        limited: !ok && LIMIT_PATTERNS.test(both),
        authProblem: !ok && AUTH_PATTERNS.test(both),
        notInstalled: spawnError?.code === 'ENOENT',
        cancelled,
        timedOut,
        ms: Date.now() - started,
      });
    };
    child.on('error', (e) => finish(-1, e));
    child.on('close', (code) => finish(code));
  });
}

/** Quick health check used by `ensemble doctor` and the dashboard. */
export async function checkAgent(id, agent) {
  if (agent.enabled === false) return { id, label: agent.label, status: 'disabled' };
  const base0 = { id, label: agent.label, membership: agent.membership, install: agent.install, login: agent.login, keyName: agent.keyName || null };
  const hasKey = !agent.keyName || !!getKey(agent.keyName);
  if (agent.type === 'perplexity') {
    return hasKey
      ? { ...base0, status: 'ready', version: 'Agent API', canWrite: false, noFiles: true }
      : { ...base0, status: 'needs-key', detail: 'no API key yet' };
  }
  const res = await new Promise((resolve) => {
    let out = '';
    let child;
    try {
      child = spawn(agent.command, agent.versionArgs || ['--version'], { windowsHide: true });
    } catch (e) {
      resolve({ code: -1, out: '', error: e });
      return;
    }
    const t = setTimeout(() => { killTree(child); resolve({ code: -2, out }); }, 20000);
    child.stdout?.on('data', (d) => { out += d; });
    child.stderr?.on('data', (d) => { out += d; });
    child.on('error', (e) => { clearTimeout(t); resolve({ code: -1, out, error: e }); });
    child.on('close', (code) => { clearTimeout(t); resolve({ code, out }); });
  });
  const base = base0;
  if (res.code === 0 && !hasKey) return { ...base, status: 'needs-key', detail: 'no API key yet', version: res.out.trim().split('\n')[0].slice(0, 80) };
  if (res.code === 0) {
    return { ...base, status: 'ready', version: res.out.trim().split('\n')[0].slice(0, 80), canWrite: agent.allowWrite !== false };
  }
  return { ...base, status: 'missing', detail: res.error?.code === 'ENOENT' ? 'not installed' : (res.out || '').trim().slice(0, 200) };
}

export function describeFailure(label, r) {
  if (r.cancelled) return `${label} was cancelled.`;
  if (r.notInstalled) return `${label} isn't installed.`;
  if (r.timedOut) return `${label} timed out.`;
  if (r.keyMissing) return `${label} has no API key yet.`;
  if (r.limited) return `${label} hit its usage limit or ran out of credit.`;
  if (r.authProblem) return `${label} isn't signed in.`;
  const tail = (r.stderr || r.output || '').split('\n').filter(Boolean).slice(-3).join(' | ');
  return `${label} failed (exit ${r.code})${tail ? `: ${tail.slice(0, 300)}` : ''}.`;
}
