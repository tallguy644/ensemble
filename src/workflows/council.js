// Council: every model answers on its own, then reads the others and revises,
// then a chair merges the best of all of them into one answer.
// Good for writing, research, decisions, brainstorming and second opinions.

import fs from 'node:fs';
import path from 'node:path';

export async function runCouncil(run, { question, agents, rounds = 2, chair, attachments = [] }) {
  const members = agents.filter((id) => run.canUse(id, 'read'));
  if (members.length === 0) throw new Error('No agents are ready. Run `ensemble doctor`.');
  if (members.length === 1) run.note(`Only ${run.label(members[0])} is ready, so this is a solo answer rather than a council.`, 'warn');

  const context = attachments.length
    ? `\n\nReference material:\n${attachments.map((a) => `\n--- ${a.name} ---\n${a.text}`).join('\n')}`
    : '';
  const roster = members.map((id) => run.label(id)).join(', ');

  run.emitEvent({ type: 'phase', name: 'Independent answers', detail: roster });
  const firstPrompt = (id) => `You are ${run.label(id)}, one member of a panel of AI models (${roster}) answering the same request. Answer independently and as well as you can. Be concrete. Say where you are unsure rather than guessing. Do not create or edit any files; reply in plain markdown only.

REQUEST:
${question}${context}`;

  let answers = await settleAll(run, members.map((id) => () =>
    run.step({ title: 'Answer', prefs: id, prompt: firstPrompt(id), fallback: false })));
  if (!answers.length) throw new Error('No agent produced an answer.');

  for (let r = 2; r <= rounds && answers.length > 1; r++) {
    run.emitEvent({ type: 'phase', name: `Cross-review round ${r - 1}`, detail: 'Each model reads the others and revises' });
    const prior = answers;
    const revisePrompt = (id) => {
      const mine = prior.find((a) => a.agent === id)?.output || '(you did not answer last round)';
      const others = prior.filter((a) => a.agent !== id).map((a) => `### ${a.label}\n${a.output}`).join('\n\n');
      return `You are ${run.label(id)} on a panel of AI models. Below is the original request, your previous answer, and the other models' answers.

1. Under "## Critique", list concrete errors, gaps or weak reasoning in the OTHER answers (and in your own). Be specific and fair; credit what they got right.
2. Under "## Revised answer", give your improved full answer, adopting anything better from the others.
Do not create or edit files; reply in markdown only.

REQUEST:
${question}${context}

YOUR PREVIOUS ANSWER:
${mine}

OTHER ANSWERS:
${others}`;
    };
    const revised = await settleAll(run, prior.map((a) => () =>
      run.step({ title: `Revise (round ${r - 1})`, prefs: a.agent, prompt: revisePrompt(a.agent), fallback: false })));
    if (revised.length) answers = revised;
  }

  run.emitEvent({ type: 'phase', name: 'Synthesis', detail: 'The chair merges everything' });
  const chairPrefs = [chair, ...run.config.roles.chair, ...members].filter(Boolean);
  let final;
  if (answers.length === 1) {
    final = answers[0];
  } else {
    final = await run.step({
      title: 'Final synthesis',
      prefs: chairPrefs,
      prompt: `You are chairing a panel of AI models (${roster}). Write the single best final answer to the request below, using the panel's answers. Keep what is correct and well supported, drop what was refuted, and resolve disagreements explicitly. Do not mention the panel process except in the last section.

Format:
- The final answer, complete and ready to use.
- A short final section "## Where the models disagreed" listing any open disagreements and which view you went with and why (write "None of note." if none).

Do not create or edit files; reply in markdown only.

REQUEST:
${question}${context}

PANEL ANSWERS:
${answers.map((a) => `### ${a.label}\n${a.output}`).join('\n\n')}`,
    });
  }

  const markdown = `# ${run.title}\n\n${final.output}\n\n---\n*Council: ${roster}. Synthesized by ${final.label}.*\n`;
  fs.writeFileSync(path.join(run.runDir, 'final.md'), markdown);
  return { markdown, chair: final.label, members: answers.map((a) => a.label) };
}

/** Runs step factories in parallel; failures are reported but don't stop the others. */
async function settleAll(run, factories) {
  const results = await Promise.allSettled(factories.map((f) => f()));
  if (run.signal.aborted) throw new Error('Run cancelled.');
  const ok = [];
  for (const r of results) {
    if (r.status === 'fulfilled') ok.push(r.value);
    else run.note(r.reason.message, 'warn');
  }
  return ok;
}
