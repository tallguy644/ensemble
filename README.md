# Ensemble

**Claude, ChatGPT, Gemini, Perplexity and DeepSeek working together on your projects.**

Ensemble is a small local app that turns five AIs into one team. Claude, ChatGPT and Gemini run on the memberships you already pay for; Perplexity and DeepSeek run on cheap pay-as-you-go API keys.

- **Council** – every model answers your question, reads the others' answers and revises, then one merges the best into a final answer. For writing, research, decisions and second opinions.
- **Build** – Perplexity researches current docs on the web, one model plans, another writes the code, a third reviews it, and fixes loop until the reviewer approves. Work lands on a new git branch so your main branch is never touched.

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

### Perplexity and DeepSeek (API keys)

Neither of these has a membership that outside apps can use, so they need an API key with some prepaid credit:

| AI | Why a key | What it does in Ensemble | Rough cost | Get a key |
|---|---|---|---|---|
| Perplexity | Pro/Max plans don't include API use | Web research with sources: the Research step in Build, and a council member | ~5–10¢ per request (`low` preset) | perplexity.ai/account/api |
| DeepSeek | There is no DeepSeek membership; chat is free, the API is pay-as-you-go | A full agent that can plan, build, review and fix, by running Claude Code on DeepSeek's models (DeepSeek's own documented setup) | a fraction of a cent to a few cents per step | platform.deepseek.com |

Save keys with `ensemble key perplexity <key>` / `ensemble key deepseek <key>`, or click the yellow chip in the dashboard. Keys go in `~/.ensemble/keys.json` (owner-only) or can come from `PERPLEXITY_API_KEY` / `DEEPSEEK_API_KEY`. They are only sent to that vendor, never into prompts or logs.

DeepSeek runs inside Claude Code with its **own settings folder** (`~/.ensemble/deepseek-claude`), so it never uses or changes your Claude login. It needs Claude Code installed. It uses DeepSeek's recommended Flash model; to use V4-Pro instead, set `"agents": { "deepseek": { "env": { "ANTHROPIC_MODEL": "deepseek-v4-pro" } } }` in your config.

Perplexity can't see your project files, so it's only used for research and council answers, never planning, building or reviewing.

Every run uses up some of each plan's limits (or key credit). If a model hits its limit, runs out of credit, isn't signed in, or isn't installed, Ensemble skips it, says so, and hands the step to the next model.

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
ensemble build ./site "Make the landing page mobile-friendly" --builder deepseek --reviewer codex
ensemble council "Best way to track mileage for Uber taxes in 2026?" --agents perplexity,claude,deepseek
ensemble key deepseek sk-...
```

Agent ids: `claude`, `codex`, `gemini`, `perplexity`, `deepseek`.
Options: `--agents a,b,c`, `--rounds N`, `--chair <id>`, `--researcher/--planner/--builder/--reviewer/--fixer <id>`, `--no-research`, `--test "<cmd>"`, `--allow-dirty`, `--no-branch`, `-v` to stream raw output.

## What each model is allowed to do

| Mode | Claude | Codex | Gemini (agy) | DeepSeek | Perplexity |
|---|---|---|---|---|---|
| Answer / plan / review | read files only | read-only sandbox | approvals off, so it can only read | read files only | web only, no files |
| Build / fix | auto-accepts **file edits only** (no shell) | `workspace-write` sandbox (project folder only) | **off by default** | file edits only (no shell) | never |

`agy` has no "edits only" mode; letting it build means skipping *all* its approvals, shell included. To allow it anyway, run `ensemble config` and set `"agents": { "gemini": { "allowWrite": true } }`.

## Where things go

- **Build runs:** your project gets a branch `ensemble/<task>-<id>` with one commit. Plans, reviews, diffs and every prompt/answer are in `<project>/.ensemble/` (kept out of git automatically). If the folder wasn't a git repo, Ensemble creates one with a baseline commit first. It refuses to start on uncommitted changes unless you allow it.
- **Council runs:** `~/.ensemble/council/<id>/` (`final.md` is the answer).
- **Research notes:** `<project>/.ensemble/RESEARCH.md`, also passed to the planner.
- **Config:** `~/.ensemble/config.json` – change default roles, timeouts, or a CLI's flags if a vendor changes them.

## Dashboard security

The dashboard listens only on `127.0.0.1`, rejects requests addressed to any other host name, and requires a random token that's printed when it starts. Other websites open in your browser can't start runs.

## Tests

```bash
npm test
```

Uses a fake CLI (`test/mock-agent.js`) and a fake Perplexity server to exercise council, research → build → review → fix, branch/commit handling, dirty-repo protection, fallback when a plan runs out of usage or credit, key handling, and keeping read-only models out of file-editing roles.

## License

MIT
