// Entry points shared by the CLI and the dashboard.

import fs from 'node:fs';
import path from 'node:path';
import { loadConfig, ensureHome, HOME_DIR } from './config.js';
import { checkAgent } from './agents.js';
import { Run, newRunId } from './run.js';
import { runCouncil } from './workflows/council.js';
import { runBuild, prepareGit } from './workflows/build.js';

export const runs = new Map();

let statusCache = null;
export function clearStatusCache() { statusCache = null; }
export async function agentStatus({ refresh = false } = {}) {
  if (statusCache && !refresh && Date.now() - statusCache.at < 60000) return statusCache.list;
  const config = loadConfig();
  const list = await Promise.all(Object.entries(config.agents).map(([id, a]) => checkAgent(id, a)));
  statusCache = { at: Date.now(), list };
  return list;
}

async function readyIds() {
  return (await agentStatus()).filter((s) => s.status === 'ready').map((s) => s.id);
}

function slug(s) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 30) || 'task';
}

function launch(run, work) {
  runs.set(run.id, run);
  run.done = (async () => {
    // Let callers attach listeners before the first event fires.
    await new Promise((r) => setImmediate(r));
    try {
      run.finish(await work());
    } catch (e) {
      run.fail(e);
    }
    return run;
  })();
  return run;
}

export async function startCouncil({ question, agents, rounds, chair, attachments }) {
  if (!question?.trim()) throw new Error('Ask the council something first.');
  const config = loadConfig();
  ensureHome();
  const ready = await readyIds();
  const members = (agents?.length ? agents : Object.keys(config.agents)).filter((id) => ready.includes(id));
  if (!members.length) throw new Error('None of the chosen agents are ready. Run `ensemble doctor` to see what needs installing or signing in.');
  const id = newRunId();
  const runDir = path.join(HOME_DIR, 'council', id);
  const run = new Run({
    kind: 'council',
    title: question.trim().split('\n')[0].slice(0, 80),
    config,
    readyAgents: ready,
    runDir,
    cwd: runDir,
  });
  return launch(run, () => runCouncil(run, {
    question: question.trim(),
    agents: members,
    rounds: Number(rounds) || config.councilRounds,
    chair,
    attachments,
  }));
}

export async function startBuild({ dir, task, roles, maxReviewRounds, testCommand, allowDirty, newBranch = true, research = true }) {
  if (!task?.trim()) throw new Error('Describe what to build first.');
  if (!dir) throw new Error('Choose a project folder.');
  const abs = path.resolve(dir.replace(/^~(?=$|[\\/])/, process.env.HOME || process.env.USERPROFILE || '~'));
  if (!fs.existsSync(abs)) fs.mkdirSync(abs, { recursive: true });
  if (!fs.statSync(abs).isDirectory()) throw new Error(`${abs} is not a folder.`);
  const config = loadConfig();
  const ready = await readyIds();
  if (!ready.length) throw new Error('No agents are ready. Run `ensemble doctor`.');
  const id = newRunId();
  const gitInfo = prepareGit(abs, { taskSlug: slug(task), runId: id, allowDirty, newBranch });
  const run = new Run({
    kind: 'build',
    title: task.trim().split('\n')[0].slice(0, 80),
    config,
    readyAgents: ready,
    runDir: path.join(abs, '.ensemble', 'runs', id),
    cwd: abs,
  });
  return launch(run, () => runBuild(run, {
    dir: abs,
    task: task.trim(),
    roles: roles || {},
    maxReviewRounds: maxReviewRounds === undefined || maxReviewRounds === '' ? undefined : Number(maxReviewRounds),
    testCommand: testCommand?.trim() || null,
    research: research !== false,
    git: gitInfo,
  }));
}
