# Ensemble

**Claude, ChatGPT and Gemini working together on your projects, using the memberships you already pay for.**

Ensemble is a small local app that turns three AI subscriptions into one team:

- **Council** – every model answers your question, reads the others' answers and revises, then one merges the best into a final answer. For writing, research, decisions and second opinions.
- **Build** – one model plans, another writes the code, a third reviews it, and fixes loop until the reviewer approves. Work lands on a new git branch so your main branch is never touched.

It runs entirely on your computer, with a browser dashboard and a command line.

## How it uses your memberships (and why it's safe)

Consumer plans like ChatGPT Plus, Claude Pro and Google AI Pro don't come with API keys, and scripting their websites breaks their terms and can get accounts flagged. Instead, each company ships an official command-line app that you sign into with your plan. Ensemble only **launches those official apps and reads what they print**. It never touches their login tokens, never automates a website, and never sends your data anywhere else.

| Membership | Official CLI Ensemble drives | Install | Sign in |
|---|---|---|---|
| Claude Pro / Max | Claude Code (`claude`) | `npm install -g @anthropic-ai/claude-code` | run `claude` once |
| ChatGPT Plus / Pro | Codex CLI (`codex`) | `npm install -g @openai/codex` | `codex login` → *Sign in with ChatGPT* |
| Google AI Pro / Ultra | Antigravity CLI (`agy`) | PowerShell: `irm https://antigravity.google/cli/install.ps1 \| iex` | run `agy` once |

Notes, as of October 2026:
- Google moved AI Pro/Ultra sign-ins from Gemini CLI to the Antigravity CLI in June 2026, which is why Ensemble uses `agy`.
- Anthropic counts `claude -p` use toward your Claude plan's normal usage limits, shared with the Claude app.
- Codex with ChatGPT sign-in draws on your ChatGPT plan's included Codex usage.

Every run uses up some of each plan's limits. If a model hits its limit, isn't signed in, or isn't installed, Ensemble skips it, says so, and hands the step to the next model.

## Install (Windows, macOS, Linux)

Needs [Node.js](https://nodejs.org) 20 or newer and [git](https://git-scm.com). No other dependencies.

```bash
git clone https://github.com/tallguy644/ensemble.git
cd ensemble
npm link            # makes the `ensemble` command available everywhere
ensemble doctor     # checks which AIs are installed and ready
ensemble ui         # opens the dashboard in your browser
```

(Without `npm link`, use `node bin/ensemble.js <command>` from the folder.)

## Command line

```bash
ensemble council "Should Kept store shifts in SQLite or Supabase?"
ensemble council "Tighten this cover letter" --file letter.md --rounds 1
ensemble build C:\Projects\my-app "Add CSV export with a date-range filter, plus tests" --test "npm test"
ensemble build ./site "Make the landing page mobile-friendly" --builder claude --reviewer codex
```

Options: `--agents claude,codex,gemini`, `--rounds N`, `--chair <id>`, `--planner/--builder/--reviewer/--fixer <id>`, `--test "<cmd>"`, `--allow-dirty`, `--no-branch`, `-v` to stream raw output.

## What each model is allowed to do

| Mode | Claude | Codex | Gemini (agy) |
|---|---|---|---|
| Answer / plan / review | read files only | read-only sandbox | approvals off, so it can only read |
| Build / fix | auto-accepts **file edits only** (no shell) | `workspace-write` sandbox (project folder only) | **off by default** |

`agy` has no "edits only" mode; letting it build means skipping *all* its approvals, shell included. To allow it anyway, run `ensemble config` and set `"agents": { "gemini": { "allowWrite": true } }`.

## Where things go

- **Build runs:** your project gets a branch `ensemble/<task>-<id>` with one commit. Plans, reviews, diffs and every prompt/answer are in `<project>/.ensemble/` (kept out of git automatically). If the folder wasn't a git repo, Ensemble creates one with a baseline commit first. It refuses to start on uncommitted changes unless you allow it.
- **Council runs:** `~/.ensemble/council/<id>/` (`final.md` is the answer).
- **Config:** `~/.ensemble/config.json` – change default roles, timeouts, or a CLI's flags if a vendor changes them.

## Dashboard security

The dashboard listens only on `127.0.0.1`, rejects requests addressed to any other host name, and requires a random token that's printed when it starts. Other websites open in your browser can't start runs.

## Tests

```bash
npm test
```

Uses a fake CLI (`test/mock-agent.js`) to exercise council, build, branch/commit handling, dirty-repo protection, and fallback when a plan runs out of usage.

## License

MIT
