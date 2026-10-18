// LiteZap dashboard: boot, login, routing and the instances list.
import * as api from './api.js';
import { h, mount, toast, errMsg, modal, secretBox, stateBadge, field, confirmDanger, phoneOf } from './ui.js';
import { instanceView, instanceAction, availableActions } from './instance.js';
import { webhooksPanel } from './webhooks.js';

const root = document.getElementById('root');
let teardown = null; // cleanup of the current view (SSE streams, timers)
let liveDot = null;

function setLive(state) {
  if (!liveDot) return;
  liveDot.className = `live ${state === 'open' ? 'on' : 'off'}`;
  liveDot.title = state === 'open' ? 'Tempo real conectado' : 'Tempo real reconectando…';
}

api.onUnauthorized(() => {
  api.clearToken();
  toast('Sessão expirada ou token inválido. Entre de novo.', 'error');
  route();
});

function logo() {
  return h('img', { src: 'logo.svg', alt: '', class: 'logo', width: 28, height: 28 });
}

function shell(content, active) {
  liveDot = h('span', { class: 'live idle', title: 'Tempo real' });
  return h('div', { class: 'shell' },
    h('header', { class: 'topbar' },
      h('a', { class: 'brand', href: '#/' }, logo(), h('span', null, 'Lite', h('b', null, 'Zap'))),
      h('nav', null,
        h('a', { href: '#/', class: active === 'instances' ? 'active' : '' }, 'Instâncias'),
        h('a', { href: '#/webhooks', class: active === 'webhooks' ? 'active' : '' }, 'Webhooks globais')),
      h('div', { class: 'topbar-right' }, liveDot,
        h('button', {
          class: 'btn ghost', onclick: () => { api.clearToken(); route(); },
        }, 'Sair'))),
    h('main', { class: 'content' }, content));
}

// ---------- login ----------

async function loginView() {
  const info = await api.dashboardInfo();
  const input = h('input', {
    type: 'password', class: 'mono', autocomplete: 'off', spellcheck: 'false',
    placeholder: 'lza_…', required: true, 'aria-label': 'Token admin',
  });
  const remember = h('input', { type: 'checkbox' });
  const btn = h('button', { class: 'btn primary', type: 'submit' }, 'Entrar');
  const err = h('p', { class: 'error', hidden: true });
  const form = h('form', {
    class: 'card login-card',
    onsubmit: async (ev) => {
      ev.preventDefault();
      err.hidden = true;
      btn.disabled = true;
      const token = input.value.trim();
      try {
        await api.get('/v1/instances', { token });
        api.setToken(token, remember.checked);
        location.hash = '#/';
        route();
      } catch (e) {
        err.textContent = e.status === 403
          ? 'Esse é um token de instância. O painel precisa do token admin.'
          : e.status === 401 ? 'Token inválido.' : errMsg(e);
        err.hidden = false;
      } finally { btn.disabled = false; }
    },
  },
  h('div', { class: 'login-brand' }, logo(), h('h1', null, 'Lite', h('b', null, 'Zap'))),
  h('p', { class: 'muted' }, 'Entre com o token admin do servidor.'),
  field('Token admin', input),
  h('label', { class: 'check' }, remember, ' Lembrar neste navegador'),
  err,
  btn,
  h('details', { class: 'help' },
    h('summary', null, 'Onde está o token?'),
    info.one_click_login
      ? h('p', null, 'Rodando pelo app de bandeja? Clique em "Abrir painel" no ícone do LiteZap: o login é automático.')
      : null,
    h('ul', null,
      h('li', null, h('code', null, 'litezap serve'), ': arquivo ', h('code', null, 'data/admin_token'),
        ' na pasta onde o servidor roda (ou em ', h('code', null, 'LITEZAP_DATA_DIR'), '), a menos que você defina ',
        h('code', null, 'LITEZAP_ADMIN_TOKEN'), '.'),
      h('li', null, 'App de bandeja no macOS: ', h('code', null, '~/Library/Application Support/LiteZap/data/admin_token')),
      h('li', null, 'Windows: ', h('code', null, '%APPDATA%\\LiteZap\\data\\admin_token')),
      h('li', null, 'Linux: ', h('code', null, '~/.local/share/litezap/data/admin_token'))),
    h('p', { class: 'muted' }, 'O token fica só neste navegador (na aba, ou guardado se marcar "lembrar"). Use "Sair" para apagá-lo.')));
  mount(root, h('div', { class: 'login-wrap' }, form));
  input.focus();
}

// ---------- instances ----------

const ACTIONS = { start: 'Iniciar', stop: 'Parar', restart: 'Reiniciar', logout: 'Deslogar' };

function instancesView() {
  const tbody = h('tbody');
  const rows = new Map();
  const empty = h('p', { class: 'muted empty', hidden: true }, 'Nenhuma instância ainda. Crie a primeira acima.');

  const render = (list) => {
    rows.clear();
    mount(tbody, list.map((inst) => {
      const tr = row(inst);
      rows.set(inst.id, tr);
      return tr;
    }));
    empty.hidden = list.length > 0;
  };

  const row = (inst) => {
    const tr = h('tr', null,
      h('td', null, h('a', { href: `#/i/${encodeURIComponent(inst.id)}` }, inst.name || inst.id),
        h('div', { class: 'mono muted small' }, inst.id)),
      h('td', { class: 'state' }, stateBadge(inst.state, inst.state_reason)),
      h('td', null, inst.phone ? phoneOf(inst.phone) : h('span', { class: 'muted' }, '—')),
      h('td', null, h('div', { class: 'actions' }, actionsFor(inst))));
    return tr;
  };

  const actionsFor = (inst) => {
    const out = [];
    if (['created', 'stopped', 'logged_out', 'failed', 'qr_pending', 'starting'].includes(inst.state)) {
      out.push(h('a', { class: 'btn small primary', href: `#/i/${encodeURIComponent(inst.id)}/pair` }, 'Parear'));
    }
    for (const a of availableActions(inst.state)) {
      if (a === 'start') continue; // "Parear" starts it
      const b = h('button', {
        class: 'btn small',
        onclick: async () => {
          b.disabled = true;
          try { const v = await instanceAction(inst.id, a); if (v) update(v); } catch (e) { toast(errMsg(e), 'error'); } finally { b.disabled = false; }
        },
      }, ACTIONS[a]);
      out.push(b);
    }
    const del = h('button', {
      class: 'btn small danger',
      onclick: async () => {
        if (!confirmDanger(`Excluir a instância "${inst.name}"? A sessão do WhatsApp, o histórico e a fila dela serão apagados.`)) return;
        del.disabled = true;
        try {
          await api.del(`/v1/instances/${encodeURIComponent(inst.id)}`);
          toast('Instância excluída');
          load();
        } catch (e) { toast(errMsg(e), 'error'); del.disabled = false; }
      },
    }, 'Excluir');
    out.push(del);
    return out;
  };

  const update = (inst) => {
    const old = rows.get(inst.id);
    if (!old) { load(); return; }
    const tr = row(inst);
    old.replaceWith(tr);
    rows.set(inst.id, tr);
  };

  const load = async () => {
    try { render((await api.get('/v1/instances')).instances || []); } catch (e) { toast(errMsg(e), 'error'); }
  };

  const name = h('input', { placeholder: 'ex.: vendas', maxlength: 64, required: true, 'aria-label': 'Nome' });
  const createBtn = h('button', { class: 'btn primary', type: 'submit' }, 'Criar instância');
  const form = h('form', {
    class: 'row create',
    onsubmit: async (ev) => {
      ev.preventDefault();
      createBtn.disabled = true;
      try {
        const res = await api.post('/v1/instances', { name: name.value.trim() });
        name.value = '';
        await load();
        modal('Instância criada', h('div', null,
          secretBox(res.token, 'Token da instância: ele só aparece agora. Guarde-o para integrar seu sistema (o painel usa o token admin e não precisa dele).')),
        [{ label: 'Parear agora', primary: true, onclick: () => { location.hash = `#/i/${encodeURIComponent(res.instance.id)}/pair`; } },
          { label: 'Fechar' }]);
      } catch (e) { toast(errMsg(e), 'error'); } finally { createBtn.disabled = false; }
    },
  }, name, createBtn);

  // Throttled reload for events about instances this page does not know yet.
  let pending = null;
  const stop = api.subscribe('/v1/events?types=instance.status', (type, ev) => {
    const tr = rows.get(ev.instance_id);
    if (!tr) { clearTimeout(pending); pending = setTimeout(load, 500); return; }
    api.get(`/v1/instances/${encodeURIComponent(ev.instance_id)}`).then(update).catch(() => {});
  }, setLive);

  load();
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' }, h('h1', null, 'Instâncias'), form),
    h('div', { class: 'card table-card' },
      h('table', { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Nome'), h('th', null, 'Estado'), h('th', null, 'Número'), h('th', null, ''))),
        tbody),
      empty)), 'instances'));
  return () => { stop(); clearTimeout(pending); };
}

function globalWebhooksView() {
  const panel = webhooksPanel(null);
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' }, h('h1', null, 'Webhooks globais')),
    h('p', { class: 'muted' }, 'Webhooks globais recebem eventos de todas as instâncias. Os de uma instância só ficam na aba Webhooks dela; aqui aparecem todos.'),
    panel.el), 'webhooks'));
  return panel.cleanup;
}

// ---------- routing ----------

let instanceCtx = null; // kept while navigating between tabs of the same instance

function parseRoute() {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  return parts;
}

async function route() {
  const parts = parseRoute();
  if (!api.getToken()) {
    if (teardown) { teardown(); teardown = null; }
    if (instanceCtx) { instanceCtx.close(); instanceCtx = null; }
    await loginView();
    return;
  }
  if (parts[0] === 'i' && parts[1]) {
    if (teardown) { teardown(); teardown = null; }
    if (!instanceCtx || instanceCtx.id !== parts[1]) {
      if (instanceCtx) instanceCtx.close();
      instanceCtx = instanceView(parts[1], { root, shell, setLive });
    }
    instanceCtx.show(parts[2] || 'chats', parts.slice(3));
    return;
  }
  if (instanceCtx) { instanceCtx.close(); instanceCtx = null; }
  if (teardown) { teardown(); teardown = null; }
  teardown = parts[0] === 'webhooks' ? globalWebhooksView() : instancesView();
}

async function boot() {
  // One-click login from the tray app: #code=... (the fragment never reaches the server).
  const m = location.hash.match(/^#code=([A-Za-z0-9_-]{16,128})$/);
  if (m) {
    history.replaceState(null, '', location.pathname + '#/');
    try {
      api.setToken(await api.redeemLaunchCode(m[1]), false);
    } catch (e) {
      toast(`Login automático falhou: ${errMsg(e)}`, 'error');
    }
  }
  window.addEventListener('hashchange', route);
  route();
}

boot();
