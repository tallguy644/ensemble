// Agent definitions and user config loading.
//
// Every agent is an OFFICIAL vendor CLI that you sign into with your own plan.
// Ensemble only launches the binary and reads what it prints. It never reads,
// copies or reuses the CLIs' login tokens, and never automates a website.

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

export const HOME_DIR = process.env.ENSEMBLE_HOME || path.join(os.homedir(), '.ensemble');
export const CONFIG_PATH = path.join(HOME_DIR, 'config.json');

// "{prompt}" in args means the prompt is passed as an argument.
// No "{prompt}" means the prompt is piped through stdin.
export const DEFAULT_AGENTS = {
  claude: {
    label: 'Claude',
    membership: 'Claude Pro / Max',
    command: 'claude',
    versionArgs: ['--version'],
    install: 'npm install -g @anthropic-ai/claude-code',
    login: 'Run `claude` once and choose your Claude Pro/Max account.',
    modes: {
      // Plain print mode: anything needing approval is refused, so it can only read.
      read: ['-p', '--output-format', 'text'],
      // Auto-accepts file edits only. Shell commands still need approval and are refused.
      write: ['-p', '--output-format', 'text', '--permission-mode', 'acceptEdits'],
    },
    enabled: true,
  },
  codex: {
    label: 'ChatGPT (Codex)',
    membership: 'ChatGPT Plus / Pro',
    command: 'codex',
    versionArgs: ['--version'],
    install: 'npm install -g @openai/codex',
    login: 'Run `codex login` and choose "Sign in with ChatGPT".',
    modes: {
      read: ['exec', '--skip-git-repo-check', '-s', 'read-only', '-'],
      write: ['exec', '--skip-git-repo-check', '-s', 'workspace-write', '-'],
    },
    enabled: true,
  },
  gemini: {
    label: 'Gemini (Antigravity)',
    membership: 'Google AI Pro / Ultra',
    command: 'agy',
    versionArgs: ['--version'],
    install: 'PowerShell: irm https://antigravity.google/cli/install.ps1 | iex',
    login: 'Run `agy` once and sign in with the Google account that has AI Pro.',
    modes: {
      read: ['-p', '{prompt}', '--print-timeout', '20m'],
      // agy has no "edits only" mode, so writing means skipping all approvals,
      // shell included. Off unless you set "allowWrite": true for gemini.
      write: ['-p', '{prompt}', '--print-timeout', '20m', '--dangerously-skip-permissions'],
    },
    // agy only takes the prompt as an argument; very long prompts are moved into a file.
    maxArgPrompt: 6000,
    allowWrite: false,
    enabled: true,
  },
  perplexity: {
    label: 'Perplexity',
    membership: 'Perplexity API (pay-as-you-go; Pro/Max plans do not include API use)',
    type: 'perplexity',
    keyName: 'perplexity',
    endpoint: 'https://api.perplexity.ai/v1/agent',
    // "low" = fast web-searching model with sources (~$0.05–0.10 per request).
    // Try "medium" for deeper research at higher cost.
    request: { preset: 'low' },
    // Searches the web but can't see your project folder.
    noFiles: true,
    install: 'Create an API key at perplexity.ai/account/api and add credit.',
    login: 'Run `ensemble key perplexity <key>` or click the chip in the dashboard.',
    allowWrite: false,
    enabled: true,
  },
  deepseek: {
    label: 'DeepSeek',
    membership: 'DeepSeek API (pay-as-you-go; there is no DeepSeek membership)',
    // DeepSeek's own documented setup: Claude Code running on DeepSeek models.
    // It gets its own settings folder so it never touches your Claude login.
    command: 'claude',
    versionArgs: ['--version'],
    keyName: 'deepseek',
    unsetEnv: ['ANTHROPIC_API_KEY'],
    env: {
      ANTHROPIC_BASE_URL: 'https://api.deepseek.com/anthropic',
      ANTHROPIC_AUTH_TOKEN: '{key}',
      // Values from DeepSeek's "Integrate with Claude Code" guide (Oct 2026).
      // For the bigger model, set ANTHROPIC_MODEL to "deepseek-v4-pro" in your config.
      ANTHROPIC_MODEL: 'deepseek-flash[1m]',
      ANTHROPIC_DEFAULT_OPUS_MODEL: 'deepseek-flash[1m]',
      ANTHROPIC_DEFAULT_SONNET_MODEL: 'deepseek-flash[1m]',
      ANTHROPIC_DEFAULT_HAIKU_MODEL: 'deepseek-flash',
      CLAUDE_CODE_SUBAGENT_MODEL: 'deepseek-flash',
      CLAUDE_CODE_EFFORT_LEVEL: 'max',
      CLAUDE_CODE_AUTO_COMPACT_WINDOW: '786432',
      CLAUDE_CONFIG_DIR: '{home}/deepseek-claude',
    },
    install: 'Needs Claude Code installed (npm install -g @anthropic-ai/claude-code) plus a key from platform.deepseek.com with some credit.',
    login: 'Run `ensemble key deepseek <key>` or click the chip in the dashboard.',
    modes: {
      read: ['-p', '--output-format', 'text'],
      write: ['-p', '--output-format', 'text', '--permission-mode', 'acceptEdits'],
    },
    enabled: true,
  },
};

export const DEFAULT_ROLES = {
  // Build workflow
  researcher: ['perplexity'],
  planner: ['claude', 'gemini', 'codex', 'deepseek'],
  builder: ['codex', 'claude', 'deepseek', 'gemini'],
  reviewer: ['gemini', 'deepseek', 'claude', 'codex'],
  fixer: ['claude', 'codex', 'deepseek', 'gemini'],
  // Council workflow
  chair: ['claude', 'gemini', 'codex', 'deepseek'],
};

export const DEFAULTS = {
  port: 4317,
  timeoutMinutes: 20,
  councilRounds: 2,
  maxReviewRounds: 2,
};

function deepMerge(base, extra) {
  if (!extra || typeof extra !== 'object' || Array.isArray(extra)) return extra ?? base;
  const out = { ...base };
  for (const [k, v] of Object.entries(extra)) {
    out[k] = base && typeof base[k] === 'object' && !Array.isArray(base[k]) ? deepMerge(base[k], v) : v;
  }
  return out;
}

export function loadConfig() {
  let user = {};
  if (fs.existsSync(CONFIG_PATH)) {
    try {
      user = JSON.parse(fs.readFileSync(CONFIG_PATH, 'utf8'));
    } catch (e) {
      throw new Error(`Could not parse ${CONFIG_PATH}: ${e.message}`);
    }
  }
  const config = {
    agents: deepMerge(structuredClone(DEFAULT_AGENTS), user.agents || {}),
    roles: structuredClone(DEFAULT_ROLES),
    ...(user.roles ? { roles: { ...structuredClone(DEFAULT_ROLES), ...user.roles } } : {}),
    ...DEFAULTS,
    ...Object.fromEntries(Object.entries(user).filter(([k]) => !['agents', 'roles'].includes(k))),
  };
  // Test hook: point every agent at the mock CLI.
  if (process.env.ENSEMBLE_MOCK) {
    for (const [id, a] of Object.entries(config.agents)) {
      a.command = process.execPath;
      a.versionArgs = [process.env.ENSEMBLE_MOCK, id, '--version'];
      a.modes = {
        read: [process.env.ENSEMBLE_MOCK, id, 'read'],
        write: [process.env.ENSEMBLE_MOCK, id, 'write'],
      };
      a.allowWrite = id !== 'perplexity';
      delete a.maxArgPrompt;
      delete a.type;
      delete a.keyName;
      delete a.env;
    }
  }
  return config;
}

export function ensureHome() {
  fs.mkdirSync(HOME_DIR, { recursive: true });
  return HOME_DIR;
}
