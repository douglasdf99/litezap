// REST and SSE client. The admin token travels only in the Authorization header (never in
// URLs); it lives in sessionStorage, or localStorage when the user picks "remember".
const KEY = 'litezap.adminToken';

let unauthorizedHandler = () => {};
export function onUnauthorized(fn) { unauthorizedHandler = fn; }

function storage(kind) {
  try { return kind === 'local' ? window.localStorage : window.sessionStorage; } catch { return null; }
}

export function getToken() {
  return storage('session')?.getItem(KEY) || storage('local')?.getItem(KEY) || '';
}

export function setToken(token, remember) {
  clearToken();
  storage(remember ? 'local' : 'session')?.setItem(KEY, token);
}

export function clearToken() {
  storage('session')?.removeItem(KEY);
  storage('local')?.removeItem(KEY);
}

export class ApiError extends Error {
  constructor(status, code, message, retryable) {
    super(message || code || `HTTP ${status}`);
    this.status = status;
    this.code = code || '';
    this.retryable = !!retryable;
  }
}

async function errorFrom(res) {
  let body = null;
  try { body = await res.json(); } catch { /* not JSON */ }
  const e = body && body.error;
  return new ApiError(res.status, e && e.code, e && e.message, e && e.retryable);
}

// Requests that take longer than this fail with a clear error instead of leaving the UI
// waiting forever (e.g. when the browser has no free connection to this server).
const TIMEOUT_MS = 30000;

// api performs a request with the stored token. opts.token overrides it (login check);
// opts.blob returns a Blob instead of JSON.
export async function api(method, path, body, opts = {}) {
  const token = opts.token ?? getToken();
  const headers = { Authorization: `Bearer ${token}` };
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), opts.timeout || (opts.blob ? 4 * TIMEOUT_MS : TIMEOUT_MS));
  const init = { method, headers, cache: 'no-store', credentials: 'omit', signal: ctrl.signal };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  try {
    let res;
    try {
      res = await fetch(path, init);
    } catch (e) {
      if (ctrl.signal.aborted) throw new ApiError(0, 'timeout', 'O servidor do LiteZap não respondeu. Feche abas antigas do painel e tente de novo.', true);
      throw new ApiError(0, 'network', 'Sem conexão com o servidor do LiteZap. Ele está aberto?', true);
    }
    if (!res.ok) {
      const err = await errorFrom(res);
      if (res.status === 401 && !opts.token) unauthorizedHandler();
      throw err;
    }
    if (res.status === 204) return null;
    if (opts.blob) return await res.blob();
    return await res.json();
  } finally {
    clearTimeout(timer);
  }
}

export const get = (p, o) => api('GET', p, undefined, o);
export const post = (p, b, o) => api('POST', p, b, o);
export const put = (p, b) => api('PUT', p, b);
export const patch = (p, b) => api('PATCH', p, b);
export const del = (p) => api('DELETE', p);

// redeemLaunchCode trades a one-click login code (from the tray app) for the admin token.
export async function redeemLaunchCode(code) {
  const res = await fetch('api/session', {
    method: 'POST', cache: 'no-store', credentials: 'omit',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()).token;
}

export async function dashboardInfo() {
  try {
    const res = await fetch('api/info', { cache: 'no-store', credentials: 'omit' });
    return res.ok ? await res.json() : {};
  } catch { return {}; }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Every open stream holds one of the ~6 HTTP/1.1 connections a browser allows per host.
// Streams of hidden tabs/windows are closed and reopened when they show again, so a few
// forgotten dashboard tabs cannot starve the others (requests would queue forever).
const streams = new Set();
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    for (const s of streams) (document.hidden ? s.pause : s.resume)();
  });
}

// subscribe opens an SSE stream with fetch (EventSource cannot send Authorization) and
// reconnects with backoff. onEvent(type, envelope); onState('open'|'closed'|'paused').
// After every (re)open, callers should refetch state they may have missed. Returns stop().
export function subscribe(path, onEvent, onState = () => {}) {
  let stopped = false;
  let paused = typeof document !== 'undefined' && document.hidden;
  let ctrl = null;
  let wake = null; // resolves the backoff/pause wait early
  const wait = (ms) => new Promise((r) => { wake = r; if (ms !== Infinity) setTimeout(r, ms); });
  const handle = {
    pause() { if (stopped || paused) return; paused = true; if (ctrl) ctrl.abort(); onState('paused'); },
    resume() { if (stopped || !paused) return; paused = false; if (wake) wake(); },
  };
  streams.add(handle);
  (async () => {
    let delay = 1000;
    while (!stopped) {
      if (paused) { await wait(Infinity); continue; }
      ctrl = new AbortController();
      try {
        const res = await fetch(path, {
          headers: { Authorization: `Bearer ${getToken()}`, Accept: 'text/event-stream' },
          signal: ctrl.signal, cache: 'no-store', credentials: 'omit',
        });
        if (res.status === 401) { unauthorizedHandler(); return; }
        if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);
        onState('open');
        delay = 1000;
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true }).replace(/\r\n?/g, '\n');
          let i;
          while ((i = buf.indexOf('\n\n')) >= 0) {
            const block = buf.slice(0, i);
            buf = buf.slice(i + 2);
            dispatch(block, onEvent);
          }
        }
      } catch {
        if (stopped) return;
      }
      if (stopped) return;
      if (paused) continue;
      onState('closed');
      await wait(delay);
      delay = Math.min(delay * 2, 15000);
    }
  })();
  return () => { stopped = true; streams.delete(handle); if (ctrl) ctrl.abort(); if (wake) wake(); };
}

// The macOS app shows the dashboard in a WKWebView. When the admin session is missing or
// rejected there, ask the app (once per page load) to reload with a fresh one-click code.
let reloginAsked = false;
export function nativeRelogin() {
  if (reloginAsked) return false;
  const bridge = typeof window !== 'undefined' && window.webkit && window.webkit.messageHandlers
    && window.webkit.messageHandlers.litezap;
  if (!bridge || typeof bridge.postMessage !== 'function') return false;
  reloginAsked = true;
  try { bridge.postMessage('relogin'); return true; } catch { return false; }
}

function dispatch(block, onEvent) {
  let type = 'message';
  const data = [];
  for (const line of block.split('\n')) {
    if (line === '' || line.startsWith(':')) continue;
    const idx = line.indexOf(':');
    const field = idx < 0 ? line : line.slice(0, idx);
    let value = idx < 0 ? '' : line.slice(idx + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') type = value;
    else if (field === 'data') data.push(value);
  }
  if (!data.length) return;
  let payload;
  try { payload = JSON.parse(data.join('\n')); } catch { return; }
  if (type === 'error') return; // slow_consumer: the server closes the stream and we reconnect
  try { onEvent(type, payload); } catch (e) { console.error(e); }
}
