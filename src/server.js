// Local dashboard. Binds to 127.0.0.1 only and requires a per-launch token,
// so other websites in your browser can't trigger runs on your machine.

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { agentStatus, runs, startCouncil, startBuild, clearStatusCache } from './engine.js';
import { setKey, removeKey } from './keys.js';
import { loadConfig, HOME_DIR, ensureHome } from './config.js';

const UI_FILE = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'ui', 'index.html');
const RECENT_FILE = path.join(HOME_DIR, 'recent-folders.json');

function readRecent() {
  try { return JSON.parse(fs.readFileSync(RECENT_FILE, 'utf8')); } catch { return []; }
}
function addRecent(dir) {
  ensureHome();
  const list = [dir, ...readRecent().filter((d) => d !== dir)].slice(0, 8);
  fs.writeFileSync(RECENT_FILE, JSON.stringify(list, null, 2));
}

function send(res, code, body, type = 'application/json') {
  res.writeHead(code, { 'Content-Type': type, 'Cache-Control': 'no-store' });
  res.end(type === 'application/json' ? JSON.stringify(body) : body);
}

async function readJson(req) {
  let raw = '';
  for await (const c of req) {
    raw += c;
    if (raw.length > 5_000_000) throw new Error('Request too large.');
  }
  return raw ? JSON.parse(raw) : {};
}

export function createServer({ port, token = crypto.randomBytes(16).toString('hex') } = {}) {
  const config = loadConfig();
  port = port || config.port;
  const allowedHosts = new Set([`127.0.0.1:${port}`, `localhost:${port}`]);

  const server = http.createServer(async (req, res) => {
    try {
      // Block DNS-rebinding: only accept requests addressed to localhost.
      if (!allowedHosts.has(req.headers.host)) return send(res, 403, { error: 'Bad host' });
      const url = new URL(req.url, `http://${req.headers.host}`);

      if (req.method === 'GET' && url.pathname === '/') {
        return send(res, 200, fs.readFileSync(UI_FILE, 'utf8'), 'text/html; charset=utf-8');
      }

      const given = req.headers['x-ensemble-token'] || url.searchParams.get('token');
      if (given !== token) return send(res, 401, { error: 'Missing or wrong token. Open the link Ensemble printed when it started.' });

      if (req.method === 'GET' && url.pathname === '/api/agents') {
        const cfg = loadConfig();
        const status = await agentStatus({ refresh: url.searchParams.has('refresh') });
        return send(res, 200, { agents: status, roles: cfg.roles, defaults: { councilRounds: cfg.councilRounds, maxReviewRounds: cfg.maxReviewRounds }, recent: readRecent() });
      }

      if (req.method === 'POST' && url.pathname === '/api/keys') {
        const { name, key, remove } = await readJson(req);
        if (remove) removeKey(name); else setKey(name, key);
        clearStatusCache();
        return send(res, 200, { ok: true });
      }

      if (req.method === 'GET' && url.pathname === '/api/runs') {
        return send(res, 200, [...runs.values()].map((r) => r.summary()).reverse());
      }

      if (req.method === 'POST' && url.pathname === '/api/council') {
        const body = await readJson(req);
        const run = await startCouncil(body);
        return send(res, 200, run.summary());
      }

      if (req.method === 'POST' && url.pathname === '/api/build') {
        const body = await readJson(req);
        const run = await startBuild(body);
        addRecent(run.cwd);
        return send(res, 200, run.summary());
      }

      const m = url.pathname.match(/^\/api\/runs\/([\w-]+)\/(events|cancel)$/);
      if (m) {
        const run = runs.get(m[1]);
        if (!run) return send(res, 404, { error: 'Unknown run' });
        if (m[2] === 'cancel' && req.method === 'POST') {
          run.cancel();
          return send(res, 200, { ok: true });
        }
        if (m[2] === 'events') {
          res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', Connection: 'keep-alive' });
          const write = (e) => res.write(`data: ${JSON.stringify(e)}\n\n`);
          write({ type: 'hello', run: run.summary() });
          run.events.forEach(write);
          if (run.status !== 'running') return res.end();
          const onEvent = (e) => {
            write(e);
            if (e.type === 'done' || e.type === 'error') res.end();
          };
          run.on('event', onEvent);
          const ping = setInterval(() => res.write(': ping\n\n'), 20000);
          req.on('close', () => { clearInterval(ping); run.off('event', onEvent); });
          return;
        }
      }

      send(res, 404, { error: 'Not found' });
    } catch (e) {
      if (!res.headersSent) send(res, 400, { error: e.message });
      else res.end();
    }
  });

  return {
    server,
    token,
    port,
    url: `http://127.0.0.1:${port}/?token=${token}`,
    listen: () => new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(port, '127.0.0.1', () => resolve());
    }),
  };
}
