// Fake AI CLI for tests. Usage: node mock-agent.js <id> <read|write|--version>
// Reads the prompt from stdin and behaves like a tiny, predictable agent.
import fs from 'node:fs';

const [id, mode] = process.argv.slice(2);
if (mode === '--version') { console.log(`mock-${id} 1.0.0`); process.exit(0); }

let prompt = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (d) => { prompt += d; });
process.stdin.on('end', () => {
  const fail = process.env[`MOCK_FAIL_${id.toUpperCase()}`];
  if (fail === 'limit') { console.error('Error: You have reached your usage limit. Try again later.'); process.exit(1); }
  if (fail === 'crash') { console.error('boom'); process.exit(2); }

  if (/lead planner/.test(prompt)) {
    console.log(`## Summary\nCreate hello.txt.\n## Steps\n1. Write hello.txt\n## Acceptance criteria\n- [ ] hello.txt says hello and is marked fixed\n## Risks\nNone.`);
  } else if (/You are the builder/.test(prompt)) {
    if (mode !== 'write') { console.error('not allowed to write'); process.exit(3); }
    fs.writeFileSync('hello.txt', `hello from ${id}\n`);
    console.log(`Created hello.txt (${id}).`);
  } else if (/code reviewer/.test(prompt)) {
    const ok = fs.existsSync('hello.txt') && fs.readFileSync('hello.txt', 'utf8').includes('fixed');
    console.log(ok ? `## Findings\nNone.\n## Verdict\nVERDICT: APPROVED` : `## Findings\n1. hello.txt is not marked fixed.\n## Verdict\nVERDICT: CHANGES_REQUESTED`);
  } else if (/You are fixing code/.test(prompt)) {
    fs.appendFileSync('hello.txt', `fixed by ${id}\n`);
    console.log('Marked hello.txt as fixed.');
  } else if (/chairing a panel/.test(prompt)) {
    const n = (prompt.match(/^### /gm) || []).length;
    console.log(`Final merged answer from ${n} panel answers.\n\n## Where the models disagreed\nNone of note.`);
  } else if (/## Revised answer|Revised answer/.test(prompt)) {
    console.log(`## Critique\nFine.\n## Revised answer\n${id} revised answer.`);
  } else {
    console.log(`${id} first answer.`);
  }
});
