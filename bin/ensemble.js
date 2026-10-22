#!/usr/bin/env node
import fs from 'node:fs';
import { spawn } from 'node:child_process';
import { agentStatus, startCouncil, startBuild } from '../src/engine.js';
import { setKey, removeKey, keySource } from '../src/keys.js';
import { createServer } from '../src/server.js';
import { CONFIG_PATH, ensureHome, DEFAULT_ROLES } from '../src/config.js';

const HELP = `Ensemble: Claude, ChatGPT (Codex) and Gemini working together on your existing memberships.

Pay-as-you-go extras: Perplexity (web research with sources) and DeepSeek
need API keys, since neither offers a membership that can be used this way.

Usage:
  ensemble ui [--port 4317] [--no-open]      Open the dashboard (recommended)
  ensemble doctor                            Check which AIs are installed and ready
  ensemble council "question" [options]      All models answer, critique each other, then merge
      --agents claude,codex,gemini,perplexity,deepseek   --rounds 2   --chair claude   --file notes.md
  ensemble build <folder> "task" [options]   Plan → build → review → fix on a new git branch
      --planner claude --builder codex --reviewer gemini --fixer claude
      --rounds 2   --test "npm test"   --allow-dirty   --no-branch
      --no-research                  skip the Perplexity web-research step
  ensemble key <perplexity|deepseek> <key>   Save an API key (or --remove)
  ensemble config                            Create/show your config file

Flags: -v / --verbose streams each model's raw output.`;

function parseArgs(argv) {
  const pos = [];
  const flags = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a.startsWith('--no-')) flags[a.slice(5)] = false;
    else if (a === '-v') flags.verbose = true;
    else if (a.startsWith('--')) {
      const key = a.slice(2);
      const next = argv[i + 1];
      if (next === undefined || next.startsWith('-')) flags[key] = true;
      else { flags[key] = next; i++; }
    } else pos.push(a);
  }
  return { pos, flags };
}

const c = process.stdout.isTTY && !process.env.NO_COLOR
  ? { dim: (s) => `\x1b[2m${s}\x1b[0m`, bold: (s) => `\x1b[1m${s}\x1b[0m`, green: (s) => `\x1b[32m${s}\x1b[0m`, red: (s) => `\x1b[31m${s}\x1b[0m`, yellow: (s) => `\x1b[33m${s}\x1b[0m`, cyan: (s) => `\x1b[36m${s}\x1b[0m` }
  : new Proxy({}, { get: () => (s) => s });

function openBrowser(url) {
  const cmd = process.platform === 'win32' ? 'cmd' : process.platform === 'darwin' ? 'open' : 'xdg-open';
  const args = process.platform === 'win32' ? ['/c', 'start', '""', url.replace(/&/g, '^&')] : [url];
  try { spawn(cmd, args, { stdio: 'ignore', detached: true }).unref(); } catch { /* print only */ }
}

async function doctor() {
  console.log(c.bold('Checking your AI command-line tools...\n'));
  const list = await agentStatus({ refresh: true });
  for (const a of list) {
    if (a.status === 'ready') {
      console.log(`${c.green('✔')} ${c.bold(a.label)} ${c.dim(a.version || '')}${a.canWrite ? '' : c.dim('  (review/answer only; set allowWrite to let it edit files)')}`);
    } else if (a.status === 'needs-key') {
      console.log(`${c.yellow('●')} ${c.bold(a.label)} — needs an API key ${c.dim(`(${a.membership})`)}`);
      console.log(`    ${a.install}`);
      console.log(`    Then: ensemble key ${a.keyName} <your-key>`);
    } else if (a.status === 'disabled') {
      console.log(`${c.dim('–')} ${a.label} ${c.dim('(disabled in config)')}`);
    } else {
      console.log(`${c.red('✘')} ${c.bold(a.label)} ${c.dim(`(${a.membership})`)} — ${a.detail || 'not found'}`);
      console.log(`    Install: ${a.install}`);
      console.log(`    Sign in: ${a.login}`);
    }
  }
  const ready = list.filter((a) => a.status === 'ready').length;
  console.log(`\n${ready} of ${list.length} ready.${ready ? '' : ' Install at least one to start.'}`);
  console.log(c.dim('Sign-in can only be confirmed by a real request; if a model says it is signed out during a run, Ensemble skips it and tells you.'));
}

function follow(run, verbose) {
  const titles = {};
  run.on('event', (e) => {
    switch (e.type) {
      case 'phase': console.log(`\n${c.cyan('■')} ${c.bold(e.name)}${e.detail ? c.dim(` — ${e.detail}`) : ''}`); break;
      case 'step-start': titles[e.stepId] = `${e.title} · ${e.label}`; console.log(`  ${c.dim('…')} ${titles[e.stepId]}${e.mode === 'write' ? c.dim(' (editing files)') : ''}`); break;
      case 'chunk': if (verbose) process.stdout.write(c.dim(e.text)); break;
      case 'step-end': console.log(`  ${e.ok ? c.green('✔') : c.red('✘')} ${titles[e.stepId]} ${c.dim(`${Math.round(e.ms / 1000)}s`)}${e.ok ? '' : ` — ${e.error}`}`); break;
      case 'verdict': console.log(`  ${e.verdict === 'approved' ? c.green('→ approved') : c.yellow(`→ ${e.verdict}`)} by ${e.by}`); break;
      case 'note': console.log(`  ${e.level === 'warn' ? c.yellow('!') : c.dim('i')} ${e.text}`); break;
      default:
    }
  });
}

async function main() {
  const [cmd, ...rest] = process.argv.slice(2);
  const { pos, flags } = parseArgs(rest);
  const verbose = !!flags.verbose;

  if (!cmd || cmd === 'help' || flags.help) return console.log(HELP);

  if (cmd === 'doctor') return doctor();

  if (cmd === 'key') {
    const [name, value] = pos;
    if (!name) return console.log('Usage: ensemble key <perplexity|deepseek> <key>   (or --remove)');
    if (flags.remove) { removeKey(name); return console.log(`Removed the saved ${name} key.`); }
    if (!value) return console.log(`${name}: ${keySource(name) || 'no key set'}`);
    setKey(name, value);
    return console.log(`Saved your ${name} key (only readable by your user account).`);
  }

  if (cmd === 'config') {
    ensureHome();
    if (!fs.existsSync(CONFIG_PATH)) {
      fs.writeFileSync(CONFIG_PATH, JSON.stringify({
        roles: DEFAULT_ROLES,
        timeoutMinutes: 20,
        councilRounds: 2,
        maxReviewRounds: 2,
        agents: { gemini: { allowWrite: false } },
      }, null, 2));
      console.log(`Created ${CONFIG_PATH}`);
    } else console.log(CONFIG_PATH);
    return;
  }

  if (cmd === 'ui') {
    const srv = createServer({ port: flags.port ? Number(flags.port) : undefined });
    try {
      await srv.listen();
    } catch (e) {
      if (e.code === 'EADDRINUSE') throw new Error(`Port ${srv.port} is busy. Try: ensemble ui --port ${srv.port + 1}`);
      throw e;
    }
    console.log(`${c.bold('Ensemble dashboard')} is running at:\n\n  ${c.cyan(srv.url)}\n\nKeep this window open. Press Ctrl+C to stop.`);
    if (flags.open !== false) openBrowser(srv.url);
    return;
  }

  if (cmd === 'council') {
    const question = pos.join(' ');
    const attachments = flags.file ? [{ name: flags.file, text: fs.readFileSync(flags.file, 'utf8') }] : [];
    const run = await startCouncil({
      question,
      agents: typeof flags.agents === 'string' ? flags.agents.split(',').map((s) => s.trim()) : undefined,
      rounds: flags.rounds,
      chair: typeof flags.chair === 'string' ? flags.chair : undefined,
      attachments,
    });
    follow(run, verbose);
    await run.done;
    if (run.status !== 'done') { process.exitCode = 1; return console.error(c.red(`\n${run.events.at(-1)?.message || 'Failed.'}`)); }
    console.log(`\n${'─'.repeat(60)}\n${run.result.markdown}\n${c.dim(`Saved in ${run.runDir}`)}`);
    return;
  }

  if (cmd === 'build') {
    const [dir, ...taskParts] = pos;
    const roles = Object.fromEntries(['researcher', 'planner', 'builder', 'reviewer', 'fixer'].filter((r) => typeof flags[r] === 'string').map((r) => [r, flags[r]]));
    const run = await startBuild({
      dir,
      task: taskParts.join(' '),
      roles,
      maxReviewRounds: flags.rounds,
      testCommand: typeof flags.test === 'string' ? flags.test : undefined,
      allowDirty: !!flags['allow-dirty'],
      research: flags.research !== false,
      newBranch: flags.branch !== false,
    });
    process.on('SIGINT', () => { console.log(c.yellow('\nCancelling...')); run.cancel(); });
    follow(run, verbose);
    await run.done;
    if (run.status !== 'done') { process.exitCode = 1; return console.error(c.red(`\n${run.events.at(-1)?.message || 'Failed.'}`)); }
    console.log(`\n${'─'.repeat(60)}\n${run.result.markdown}\n${c.dim(`Full log: ${run.runDir}`)}`);
    return;
  }

  console.log(HELP);
  process.exitCode = 1;
}

main().catch((e) => {
  console.error(c.red(e.message));
  process.exitCode = 1;
});
