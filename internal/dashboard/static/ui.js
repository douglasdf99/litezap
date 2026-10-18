// Tiny DOM helpers. Data only ever reaches the page as text nodes or attribute values
// (never parsed as HTML), so names, messages and event payloads cannot inject markup.

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'class') el.className = v;
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'href') el.setAttribute('href', safeHref(v));
      else el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children) {
    if (c === undefined || c === null || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

// safeHref allows only in-app hash routes and blob: URLs we created.
function safeHref(v) {
  const s = String(v);
  return /^(#|blob:)/.test(s) ? s : '#';
}

export function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

export function mount(el, ...children) { clear(el); append(el, children); return el; }

export function fmtTime(ts) {
  if (!ts) return '';
  const d = new Date(ts);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  return d.toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'medium' });
}

export function phoneOf(jid) {
  if (!jid) return '';
  const [user, server] = String(jid).split('@');
  if (server === 's.whatsapp.net') return '+' + user.split(':')[0];
  if (!server && /^\d{8,15}$/.test(user)) return '+' + user;
  return jid;
}

let toastBox = null;
export function toast(msg, kind = 'info') {
  if (!toastBox) {
    toastBox = h('div', { class: 'toasts', role: 'status', 'aria-live': 'polite' });
    document.body.appendChild(toastBox);
  }
  const t = h('div', { class: `toast toast-${kind}` }, msg);
  toastBox.appendChild(t);
  setTimeout(() => t.remove(), kind === 'error' ? 7000 : 3500);
}

export function errMsg(e) {
  if (!e) return 'Erro desconhecido';
  const code = e.code ? ` (${e.code})` : '';
  return `${e.message || String(e)}${code}`;
}

// modal shows content in a dialog; returns close().
export function modal(title, body, actions = []) {
  const dlg = h('dialog', { class: 'modal' });
  const close = () => { dlg.close(); dlg.remove(); };
  mount(dlg,
    h('h2', null, title),
    h('div', { class: 'modal-body' }, body),
    h('div', { class: 'modal-actions' },
      actions.length ? actions.map((a) => h('button', {
        class: a.primary ? 'btn primary' : 'btn',
        onclick: async () => { if (a.onclick && (await a.onclick()) === false) return; close(); },
      }, a.label)) : h('button', { class: 'btn primary', onclick: close }, 'Fechar')));
  dlg.addEventListener('cancel', (e) => { e.preventDefault(); close(); });
  document.body.appendChild(dlg);
  dlg.showModal();
  return close;
}

// secretBox shows a value once with a copy button (instance token, webhook secret).
export function secretBox(value, note) {
  const input = h('input', { class: 'mono secret', readonly: true, value });
  return h('div', null,
    h('p', { class: 'warn' }, note),
    h('div', { class: 'row' }, input,
      h('button', {
        class: 'btn', type: 'button',
        onclick: async () => {
          try { await navigator.clipboard.writeText(value); toast('Copiado'); } catch { input.select(); }
        },
      }, 'Copiar')));
}

export function confirmDanger(text) { return window.confirm(text); }

const STATE_LABEL = {
  created: 'criada', starting: 'iniciando', qr_pending: 'aguardando QR', connecting: 'conectando',
  connected: 'conectada', reconnecting: 'reconectando', logged_out: 'deslogada', banned: 'banida',
  failed: 'falhou', stopped: 'parada',
};

export function stateBadge(state, reason) {
  return h('span', { class: `badge st-${state}`, title: reason || state },
    STATE_LABEL[state] || state, reason ? ` · ${reason}` : '');
}

export function field(label, input, hint) {
  return h('label', { class: 'field' }, h('span', { class: 'field-label' }, label), input,
    hint ? h('small', { class: 'hint' }, hint) : null);
}

export function busy(btn, fn) {
  return async (ev) => {
    if (ev) ev.preventDefault();
    btn.disabled = true;
    try { await fn(); } catch (e) { toast(errMsg(e), 'error'); } finally { btn.disabled = false; }
  };
}
