// A Run is one council or build session. It records every step to disk,
// streams events to listeners (CLI or dashboard), and handles fallbacks
// when an agent is missing, signed out, or out of plan usage.

import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { runAgent, describeFailure } from './agents.js';

export function newRunId() {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}-${crypto.randomBytes(2).toString('hex')}`;
}

function slug(s) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40);
}

export class Run extends EventEmitter {
  constructor({ kind, title, config, readyAgents, runDir, cwd }) {
    super();
    this.id = path.basename(runDir);
    this.kind = kind;
    this.title = title;
    this.config = config;
    this.ready = new Set(readyAgents);
    this.exhausted = new Set();
    this.runDir = runDir;
    this.cwd = cwd;
    this.events = [];
    this.status = 'running';
    this.result = null;
    this.stepNo = 0;
    this.controller = new AbortController();
    this.startedAt = new Date().toISOString();
    fs.mkdirSync(runDir, { recursive: true });
    this.logFile = path.join(runDir, 'log.md');
    fs.writeFileSync(this.logFile, `# ${kind === 'council' ? 'Council' : 'Build'}: ${title}\n\nStarted ${this.startedAt}\n\n`);
  }

  get signal() { return this.controller.signal; }

  emitEvent(ev) {
    const e = { t: Date.now(), ...ev };
    this.events.push(e);
    this.emit('event', e);
    return e;
  }

  note(text, level = 'info') {
    fs.appendFileSync(this.logFile, `> ${text}\n\n`);
    this.emitEvent({ type: 'note', level, text });
  }

  label(id) { return this.config.agents[id]?.label || id; }

  canUse(id, mode) {
    const a = this.config.agents[id];
    if (!a || a.enabled === false) return false;
    if (!this.ready.has(id) || this.exhausted.has(id)) return false;
    if (mode === 'write' && a.allowWrite === false) return false;
    return true;
  }

  /** Picks the first usable agent from a preference list. */
  pick(prefs, mode) {
    return (Array.isArray(prefs) ? prefs : [prefs]).find((id) => this.canUse(id, mode));
  }

  /**
   * Runs one step. `prefs` is an agent id or an ordered list; on failure it
   * falls through to the next usable agent unless `fallback` is false.
   */
  async step({ title, prefs, prompt, mode = 'read', cwd = this.cwd, fallback = true, quiet = false }) {
    const order = (Array.isArray(prefs) ? prefs : [prefs]).filter((id) => this.canUse(id, mode));
    if (!order.length) throw new Error(`No ready agent can do "${title}"${mode === 'write' ? ' (needs file-editing permission)' : ''}.`);
    const tries = fallback ? order : order.slice(0, 1);
    for (const id of tries) {
      if (this.signal.aborted) throw new Error('Run cancelled.');
      const n = ++this.stepNo;
      const stepId = `s${n}`;
      const agent = this.config.agents[id];
      this.emitEvent({ type: 'step-start', stepId, title, agent: id, label: agent.label, mode, quiet });
      const res = await runAgent(agent, {
        prompt,
        mode,
        cwd,
        timeoutMs: (this.config.timeoutMinutes || 20) * 60000,
        signal: this.signal,
        onChunk: (text, stream) => this.emitEvent({ type: 'chunk', stepId, stream, text }),
      });
      const file = path.join(this.runDir, `${String(n).padStart(2, '0')}-${slug(title)}-${id}.md`);
      fs.writeFileSync(file, `# ${title} (${agent.label}, ${mode})\n\n## Prompt\n\n${prompt}\n\n## Output\n\n${res.output || '(none)'}\n${res.stderr ? `\n## stderr\n\n\`\`\`\n${res.stderr.slice(-4000)}\n\`\`\`\n` : ''}`);
      if (res.ok) {
        fs.appendFileSync(this.logFile, `## ${title} (${agent.label})\n\n${res.output}\n\n`);
        this.emitEvent({ type: 'step-end', stepId, ok: true, ms: res.ms, output: res.output });
        return { agent: id, label: agent.label, output: res.output };
      }
      const why = describeFailure(agent.label, res);
      this.emitEvent({ type: 'step-end', stepId, ok: false, ms: res.ms, error: why });
      if (res.cancelled) throw new Error('Run cancelled.');
      if (res.limited || res.authProblem || res.notInstalled) this.exhausted.add(id);
      this.note(`${why}${fallback ? ' Trying the next agent.' : ''}`, 'warn');
    }
    throw new Error(`Every agent failed at "${title}".`);
  }

  finish(result) {
    this.status = 'done';
    this.result = result;
    fs.writeFileSync(path.join(this.runDir, 'result.md'), result.markdown || '');
    fs.appendFileSync(this.logFile, `---\n\nFinished ${new Date().toISOString()}\n`);
    this.emitEvent({ type: 'done', result });
  }

  fail(err) {
    this.status = this.signal.aborted ? 'cancelled' : 'failed';
    fs.appendFileSync(this.logFile, `---\n\n${this.status.toUpperCase()}: ${err.message}\n`);
    this.emitEvent({ type: 'error', message: err.message, status: this.status });
  }

  cancel() { this.controller.abort(); }

  summary() {
    return { id: this.id, kind: this.kind, title: this.title, status: this.status, startedAt: this.startedAt, runDir: this.runDir };
  }
}
