// Cross-platform process launching without dependencies.
// On Windows, npm-installed CLIs are .cmd shims, which Node can't start
// directly; they must go through cmd.exe with careful escaping.

import { spawn as nodeSpawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const isWin = process.platform === 'win32';
const META = /([()\][%!^"`<>&|;, *?])/g;

export function resolveCommand(cmd, env = process.env) {
  if (!isWin) return cmd;
  const exts = (env.PATHEXT || '.COM;.EXE;.BAT;.CMD').split(';').filter(Boolean);
  const hasExt = path.extname(cmd) !== '';
  const candidates = (dir) => (hasExt ? [path.join(dir, cmd)] : exts.map((e) => path.join(dir, cmd + e.toLowerCase())));
  if (cmd.includes('\\') || cmd.includes('/')) {
    return candidates(path.dirname(cmd)).map((c) => path.join(path.dirname(cmd), path.basename(c))).find((c) => fs.existsSync(c)) || cmd;
  }
  const dirs = [process.cwd(), ...(env.PATH || env.Path || '').split(path.delimiter).filter(Boolean)];
  for (const d of dirs) {
    const hit = candidates(d).find((c) => { try { return fs.statSync(c).isFile(); } catch { return false; } });
    if (hit) return hit;
  }
  return cmd;
}

export function isBatchFile(resolved) {
  return isWin && /\.(cmd|bat)$/i.test(resolved);
}

function escapeArg(arg, doubleEscape) {
  let a = String(arg).replace(/(\\*)"/g, '$1$1\\"').replace(/(\\*)$/, '$1$1');
  a = `"${a}"`.replace(META, '^$1');
  if (doubleEscape) a = a.replace(META, '^$1');
  return a;
}

export function spawn(command, args = [], options = {}) {
  const resolved = resolveCommand(command, options.env);
  if (!isBatchFile(resolved)) return nodeSpawn(resolved, args, options);
  const dbl = /node_modules[\\/]\.bin[\\/][^\\/]+\.cmd$/i.test(resolved);
  const line = [resolved.replace(META, '^$1'), ...args.map((a) => escapeArg(a, dbl))].join(' ');
  return nodeSpawn(process.env.comspec || 'cmd.exe', ['/d', '/s', '/c', `"${line}"`], { ...options, windowsVerbatimArguments: true });
}
