// API keys for pay-as-you-go agents (Perplexity, DeepSeek).
// Read from environment variables first, then ~/.ensemble/keys.json
// (created with owner-only permissions). Keys are only ever handed to the
// vendor's own endpoint; they never go into prompts or run logs.

import fs from 'node:fs';
import path from 'node:path';
import { HOME_DIR, ensureHome } from './config.js';

const KEYS_FILE = path.join(HOME_DIR, 'keys.json');

export const KEY_ENV = {
  perplexity: 'PERPLEXITY_API_KEY',
  deepseek: 'DEEPSEEK_API_KEY',
};

function readFile() {
  try { return JSON.parse(fs.readFileSync(KEYS_FILE, 'utf8')); } catch { return {}; }
}

export function getKey(name) {
  return process.env[KEY_ENV[name] || `${name.toUpperCase()}_API_KEY`] || readFile()[name] || null;
}

export function setKey(name, value) {
  if (!KEY_ENV[name]) throw new Error(`Unknown key "${name}". Use one of: ${Object.keys(KEY_ENV).join(', ')}.`);
  const v = String(value || '').trim();
  if (!v || /\s/.test(v) || v.length < 10) throw new Error('That does not look like an API key.');
  ensureHome();
  const all = readFile();
  all[name] = v;
  fs.writeFileSync(KEYS_FILE, JSON.stringify(all, null, 2), { mode: 0o600 });
  try { fs.chmodSync(KEYS_FILE, 0o600); } catch { /* Windows: file is already in the user's own profile */ }
}

export function removeKey(name) {
  const all = readFile();
  delete all[name];
  if (fs.existsSync(KEYS_FILE)) fs.writeFileSync(KEYS_FILE, JSON.stringify(all, null, 2), { mode: 0o600 });
}

export function keySource(name) {
  if (process.env[KEY_ENV[name]]) return `environment (${KEY_ENV[name]})`;
  if (readFile()[name]) return 'saved key';
  return null;
}
