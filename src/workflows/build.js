// Build: plan → build → review → fix, passing work between models.
// Default casting: Claude plans, Codex builds, Gemini reviews, Claude fixes.
// Work happens on a fresh git branch so nothing touches your main branch,
// and every step is saved under <project>/.ensemble/runs/.

import fs from 'node:fs';
import path from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';

const INLINE_DIFF_LIMIT = 60000;

function git(cwd, args, opts = {}) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 * 1024 * 1024, ...opts }).trim();
}
function gitOk(cwd, args) {
  try { git(cwd, args); return true; } catch { return false; }
}

export function prepareGit(dir, { taskSlug, runId, allowDirty = false, gitInit = true, newBranch = true }) {
  const info = { isRepo: gitOk(dir, ['rev-parse', '--is-inside-work-tree']), branch: null, startRef: null, created: false };
  if (!info.isRepo) {
    if (!gitInit) return info;
    git(dir, ['init', '-q']);
    info.isRepo = true;
    info.created = true;
  }
  const gitDir = path.resolve(dir, git(dir, ['rev-parse', '--git-dir']));
  const exclude = path.join(gitDir, 'info', 'exclude');
  fs.mkdirSync(path.dirname(exclude), { recursive: true });
  const ex = fs.existsSync(exclude) ? fs.readFileSync(exclude, 'utf8') : '';
  if (!ex.split(/\r?\n/).includes('.ensemble/')) fs.appendFileSync(exclude, `${ex && !ex.endsWith('\n') ? '\n' : ''}.ensemble/\n`);

  const hasCommits = gitOk(dir, ['rev-parse', '--verify', 'HEAD']);
  const dirty = git(dir, ['status', '--porcelain']);
  if (!hasCommits) {
    // Baseline commit so the models' work can be diffed and undone.
    git(dir, ['add', '-A']);
    git(dir, ['-c', 'user.name=Ensemble', '-c', 'user.email=ensemble@localhost', 'commit', '-q', '--allow-empty', '-m', 'Baseline before Ensemble']);
  } else if (dirty && !allowDirty) {
    throw new Error('This project has uncommitted changes. Commit or stash them first (or tick "Allow uncommitted changes").');
  }
  info.startRef = git(dir, ['rev-parse', 'HEAD']);
  info.originalBranch = gitOk(dir, ['symbolic-ref', '-q', 'HEAD']) ? git(dir, ['symbolic-ref', '--short', 'HEAD']) : null;
  if (newBranch) {
    info.branch = `ensemble/${taskSlug}-${runId.slice(-4)}`;
    git(dir, ['checkout', '-q', '-b', info.branch]);
  } else {
    info.branch = info.originalBranch;
  }
  return info;
}

function currentDiff(dir, startRef) {
  git(dir, ['add', '-A']);
  const stat = git(dir, ['diff', '--cached', '--stat', startRef]);
  const patch = git(dir, ['diff', '--cached', startRef]);
  return { stat, patch };
}

function runTests(cmd, cwd) {
  if (!cmd) return null;
  const r = spawnSync(cmd, { cwd, shell: true, encoding: 'utf8', timeout: 10 * 60000, maxBuffer: 16 * 1024 * 1024 });
  const out = `${r.stdout || ''}${r.stderr || ''}`.trim();
  return { ok: r.status === 0, code: r.status, output: out.length > 12000 ? `...(trimmed)\n${out.slice(-12000)}` : out };
}

function parseVerdict(text) {
  const m = text.match(/VERDICT:\s*\**\s*(APPROVED|CHANGES[_ ]REQUESTED)/i);
  if (!m) return 'unclear';
  return m[1].toUpperCase().startsWith('APPROVED') ? 'approved' : 'changes';
}

export async function runBuild(run, { dir, task, roles = {}, maxReviewRounds, testCommand, research = true, git: gitInfo }) {
  const R = run.config.roles;
  // Every role except research needs to read the project folder.
  const pref = (role) => [roles[role], ...R[role]].filter((id) => id && (role === 'researcher' || !run.config.agents[id]?.noFiles));
  const ensDir = path.join(dir, '.ensemble');
  fs.mkdirSync(ensDir, { recursive: true });
  const rounds = maxReviewRounds ?? run.config.maxReviewRounds;

  if (gitInfo.created) run.note('This folder was not a git repository, so Ensemble created one with a baseline commit.');
  if (gitInfo.branch) run.note(`Working on branch ${gitInfo.branch}. Your other branches are untouched.`);

  // 0. Research (optional): a web-searching model gathers current facts first.
  let findings = null;
  const researchers = pref('researcher').filter((id) => run.canUse(id, 'read'));
  if (research && researchers.length) {
    run.emitEvent({ type: 'phase', name: 'Research', detail: 'Current docs, versions and pitfalls from the web' });
    try {
      findings = await run.step({
        title: 'Research',
        prefs: researchers,
        mode: 'read',
        prompt: `You are the researcher on a team of AI coding agents. Another agent will plan and build the task below; they can read the project but cannot search the web. Search the web and give them what they need that may be newer than their training: current stable versions of the libraries or tools involved, relevant API changes or deprecations, the recommended approach today, and known pitfalls. Be concise and practical, use bullet points, and cite sources. Do not write the implementation. If the task needs no outside facts, say so in one line.

TASK:
${task}`,
      });
      fs.writeFileSync(path.join(ensDir, 'RESEARCH.md'), `# Research (by ${findings.label})\n\n${findings.output}\n`);
    } catch (e) {
      run.note(`Skipping research: ${e.message}`, 'warn');
    }
  }
  const researchBlock = findings ? `\n\nWEB RESEARCH (from ${findings.label}; prefer it over your own memory for versions and APIs):\n${findings.output}` : '';

  // 1. Plan
  run.emitEvent({ type: 'phase', name: 'Plan' });
  const plan = await run.step({
    title: 'Plan',
    prefs: pref('planner'),
    mode: 'read',
    prompt: `You are the lead planner on a team of AI coding agents (Claude, ChatGPT/Codex, Gemini). Another agent will implement your plan, and a third will review it.

Explore the project in the current folder (read files as needed; do not modify anything), then write an implementation plan for the task below.

Reply with markdown containing:
## Summary – one paragraph.
## Steps – numbered, each naming the files to create or change and what to do.
## Acceptance criteria – a checklist the reviewer can verify.
## Risks – anything the builder must be careful about.

If the folder is empty, plan the project from scratch (structure, stack, files).

TASK:
${task}${researchBlock}`,
  });
  const planText = `# Plan (by ${plan.label})\n\nTask: ${task}\n\n${plan.output}\n`;
  fs.writeFileSync(path.join(ensDir, 'PLAN.md'), planText);

  // 2. Build
  run.emitEvent({ type: 'phase', name: 'Build' });
  const build = await run.step({
    title: 'Build',
    prefs: pref('builder'),
    mode: 'write',
    prompt: `You are the builder on a team of AI coding agents. Implement the plan below in the current folder by creating and editing files. Follow the plan; if a step is wrong, do the right thing and say so. Do not commit to git and do not touch the .ensemble folder.

When finished, reply with a short summary of what you changed and anything left undone.

TASK:
${task}

PLAN (also saved at .ensemble/PLAN.md):
${plan.output}`,
  });

  // 3. Review / fix loop
  let verdict = 'unclear';
  let lastReview = null;
  let tests = null;
  const fixers = new Set();
  for (let r = 1; r <= rounds + 1; r++) {
    tests = runTests(testCommand, dir);
    if (tests) run.note(`Tests (${testCommand}): ${tests.ok ? 'passed' : `failed (exit ${tests.code})`}`, tests.ok ? 'info' : 'warn');
    const { stat, patch } = currentDiff(dir, gitInfo.startRef);
    if (!patch) {
      run.note('No file changes were made.', 'warn');
      verdict = 'no-changes';
      break;
    }
    const diffFile = path.join(run.runDir, `diff-round${r}.patch`);
    fs.writeFileSync(diffFile, patch);
    const relDiff = path.relative(dir, diffFile).split(path.sep).join('/');
    const diffBlock = patch.length <= INLINE_DIFF_LIMIT
      ? `DIFF:\n\`\`\`diff\n${patch}\n\`\`\``
      : `The diff is large; read it from ${relDiff}.\nDIFF STAT:\n${stat}`;

    run.emitEvent({ type: 'phase', name: `Review ${r}` });
    const review = await run.step({
      title: `Review ${r}`,
      prefs: pref('reviewer'),
      mode: 'read',
      prompt: `You are the code reviewer on a team of AI coding agents. Review the changes below against the task and plan. You may read any file in the current folder; do not modify anything.

Check: correctness, bugs, missing acceptance criteria, security problems, and anything that would not run. Ignore pure style nitpicks.

Reply in markdown:
## Findings – numbered list, most important first, each with file and what to change. Write "None." if there are none.
## Verdict – end with exactly one line: "VERDICT: APPROVED" or "VERDICT: CHANGES_REQUESTED".

TASK:
${task}

PLAN:
${plan.output}
${tests ? `\nTEST RESULT (${testCommand}, exit ${tests.code}):\n\`\`\`\n${tests.output}\n\`\`\`\n` : ''}
${diffBlock}`,
    });
    lastReview = review;
    fs.writeFileSync(path.join(ensDir, 'REVIEW.md'), `# Review ${r} (by ${review.label})\n\n${review.output}\n`);
    verdict = parseVerdict(review.output);
    run.emitEvent({ type: 'verdict', round: r, verdict, by: review.label });
    if (verdict === 'approved' && (!tests || tests.ok)) break;
    if (r > rounds) {
      run.note(`Stopped after ${rounds} fix round${rounds === 1 ? '' : 's'}; the last review still had findings.`, 'warn');
      break;
    }

    run.emitEvent({ type: 'phase', name: `Fix ${r}` });
    const fix = await run.step({
      title: `Fix ${r}`,
      prefs: pref('fixer'),
      mode: 'write',
      prompt: `You are fixing code on a team of AI coding agents. A reviewer found problems with the current changes in this folder. Address every valid finding by editing files. If you disagree with a finding, leave it and explain why. Do not commit to git and do not touch the .ensemble folder.

Reply with a short list of what you fixed and what you deliberately left.

TASK:
${task}

PLAN:
${plan.output}

REVIEW:
${review.output}
${tests && !tests.ok ? `\nFAILING TESTS (${testCommand}):\n\`\`\`\n${tests.output}\n\`\`\`` : ''}`,
    });
    fixers.add(fix.label);
  }

  // 4. Commit on the work branch
  let commit = null;
  const { stat } = currentDiff(dir, gitInfo.startRef);
  if (stat && gitOk(dir, ['diff', '--cached', '--quiet']) === false) {
    const msg = `Ensemble: ${task.split('\n')[0].slice(0, 60)}\n\nPlanned by ${plan.label}, built by ${build.label}${lastReview ? `, reviewed by ${lastReview.label} (${verdict})` : ''}.`;
    git(dir, ['-c', 'user.name=Ensemble', '-c', 'user.email=ensemble@localhost', 'commit', '-q', '-m', msg]);
    commit = git(dir, ['rev-parse', '--short', 'HEAD']);
  }

  const verdictText = {
    approved: 'Approved by the reviewer.',
    changes: 'The reviewer still requested changes; see the last review.',
    unclear: 'The reviewer did not give a clear verdict.',
    'no-changes': 'No files were changed.',
  }[verdict];

  const markdown = `# Build: ${run.title}

**Result:** ${verdictText}${tests ? ` Tests ${tests.ok ? 'pass' : 'fail'}.` : ''}
**Branch:** \`${gitInfo.branch || '(current)'}\`${commit ? ` · commit \`${commit}\`` : ''}

| Step | Model |
|---|---|
${findings ? `| Research | ${findings.label} |\n` : ''}| Plan | ${plan.label} |
| Build | ${build.label} |
| Review | ${lastReview?.label || '—'} |
${fixers.size ? `| Fix | ${[...fixers].join(', ')} |\n` : ''}
## Files changed
\`\`\`
${stat || 'none'}
\`\`\`

## Builder's notes
${build.output}

${lastReview ? `## Last review\n${lastReview.output.replace(/^(#{1,3}) /gm, '#### ')}\n` : ''}
## Next steps
${gitInfo.branch && gitInfo.originalBranch ? `Look over the branch, then merge it: \`git checkout ${gitInfo.originalBranch} && git merge ${gitInfo.branch}\`, or push it and open a pull request.` : 'Look over the changes in your project folder.'}
`;
  return { markdown, verdict, branch: gitInfo.branch, commit, stat };
}
