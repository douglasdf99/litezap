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

// api performs a request with the stored token. opts.token overrides it (login check);
// opts.blob returns a Blob instead of JSON.
export async function api(method, path, body, opts = {}) {
  const token = opts.token ?? getToken();
  const headers = { Authorization: `Bearer ${token}` };
  const init = { method, headers, cache: 'no-store', credentials: 'omit' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (!res.ok) {
    const err = await errorFrom(res);
    if (res.status === 401 && !opts.token) unauthorizedHandler();
    throw err;
  }
  if (res.status === 204) return null;
  if (opts.blob) return res.blob();
  return res.json();
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

// subscribe opens an SSE stream with fetch (EventSource cannot send Authorization) and
// reconnects with backoff. onEvent(type, envelope); onState('open'|'closed'). Returns stop().
export function subscribe(path, onEvent, onState = () => {}) {
  let stopped = false;
  let ctrl = null;
  (async () => {
    let delay = 1000;
    while (!stopped) {
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
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += value.replace(/\r\n?/g, '\n');
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
      onState('closed');
      await sleep(delay);
      delay = Math.min(delay * 2, 15000);
    }
  })();
  return () => { stopped = true; if (ctrl) ctrl.abort(); };
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
