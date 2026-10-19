// LiteZap dashboard: boot, login, routing, the app shell and the instances list.
import * as api from './api.js';
import {
  h, mount, toast, errMsg, stateBadge, field, confirmDanger, phoneOf, icon, skeleton, emptyState,
} from './ui.js';
import { tagChips } from './platform.js';
import { instanceView, instanceAction, availableActions } from './instance.js';
import { webhooksPanel } from './webhooks.js';
import { aiClientsView } from './aiclients.js';
import { openWizard, initials } from './wizard.js';

const DOCS_URL = 'https://litezap.com/docs/';
const root = document.getElementById('root');
let teardown = null; // cleanup of the current view (SSE streams, timers)
let liveEl = null;
let info = null; // GET api/info, fetched once

function setLive(state) {
  if (!liveEl) return;
  const on = state === 'open';
  liveEl.className = `live ${state === 'paused' ? 'idle' : on ? 'on' : 'off'}`;
  liveEl.title = on ? 'Atualizações em tempo real ligadas' : 'Reconectando as atualizações em tempo real…';
  liveEl.lastChild.textContent = on ? 'Tempo real' : 'Reconectando…';
}

api.onUnauthorized(() => {
  api.clearToken();
  if (api.nativeRelogin()) return; // the app reloads us with a fresh one-click code
  toast('Sessão expirada ou token inválido. Entre de novo.', 'error');
  route();
});

function logo(cls = '') {
  return [
    h('img', { src: 'brand/logo-full.png', alt: 'LiteZap', class: `logo-full logo-light ${cls}`, width: 111, height: 30 }),
    h('img', { src: 'brand/logo-full-white.png', alt: 'LiteZap', class: `logo-full logo-dark ${cls}`, width: 111, height: 30 }),
    h('img', { src: 'brand/logo-mark.png', alt: 'LiteZap', class: 'logo-mark', width: 34, height: 34 }),
  ];
}

function shell(content, active) {
  liveEl = h('div', { class: 'live idle', title: 'Tempo real' }, h('span', { class: 'dot', 'aria-hidden': 'true' }), h('span', null, ''));
  const link = (href, key, ic, label) => h('a', {
    href, class: active === key ? 'active' : '', 'aria-current': active === key ? 'page' : null, title: label,
  }, icon(ic, 18), h('span', null, label));
  return h('div', { class: 'shell' },
    h('aside', { class: 'sidebar' },
      h('a', { class: 'brand', href: '#/', 'aria-label': 'LiteZap: início' }, logo()),
      h('nav', { class: 'nav', 'aria-label': 'Principal' },
        h('div', { class: 'nav-label' }, 'Painel'),
        link('#/', 'instances', 'phone', 'Instâncias'),
        link('#/webhooks', 'webhooks', 'webhook', 'Webhooks globais'),
        info && info.ai_clients ? link('#/ia', 'ai', 'sparkles', 'Conectar a IA') : null,
        h('div', { class: 'nav-label' }, 'Ajuda'),
        h('a', { href: DOCS_URL, target: '_blank', rel: 'noopener noreferrer', title: 'Documentação da API' },
          icon('book', 18), h('span', null, 'Documentação'))),
      h('div', { class: 'sidebar-foot' }, liveEl,
        h('button', {
          class: 'btn ghost', type: 'button', title: 'Sair',
          onclick: () => { api.clearToken(); route(); },
        }, icon('logout', 18), h('span', null, 'Sair')))),
    h('main', { class: 'main', id: 'main' }, h('div', { class: 'content' }, content)));
}

// ---------- login ----------

async function loginView() {
  // Inside the macOS app: ask for a fresh one-click code instead of showing the form.
  if (api.nativeRelogin()) {
    mount(root, h('div', { class: 'login-wrap' }, h('div', { class: 'card login-card center' },
      h('div', { class: 'login-brand' }, logo()), h('p', { class: 'muted' }, 'Entrando…'))));
    return;
  }
  const info = await api.dashboardInfo();
  const input = h('input', {
    type: 'password', class: 'mono', autocomplete: 'off', spellcheck: 'false',
    placeholder: 'lza_…', required: true,
  });
  const remember = h('input', { type: 'checkbox' });
  const btn = h('button', { class: 'btn primary large block', type: 'submit' }, 'Entrar');
  const err = h('p', { class: 'error small', hidden: true, role: 'alert' });
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
  h('div', { class: 'login-brand' }, logo()),
  h('div', null, h('h1', null, 'Entrar no painel'), h('p', { class: 'muted' }, 'Use o token admin do seu servidor LiteZap.')),
  info.one_click_login
    ? h('p', { class: 'secret-note' }, icon('sparkles', 16), h('span', null, 'Usa o app do LiteZap? Clique em "Abrir painel" no ícone dele: o login é automático.'))
    : null,
  field('Token admin', input),
  h('label', { class: 'check small' }, remember, ' Lembrar neste navegador'),
  err,
  btn,
  h('details', { class: 'help' },
    h('summary', null, 'Onde encontro o token?'),
    h('ul', null,
      h('li', null, h('code', null, 'litezap serve'), ': arquivo ', h('code', null, 'data/admin_token'),
        ' na pasta do servidor (ou em ', h('code', null, 'LITEZAP_DATA_DIR'), '), a menos que ', h('code', null, 'LITEZAP_ADMIN_TOKEN'), ' esteja definido.'),
      h('li', null, 'App no macOS: ', h('code', null, '~/Library/Application Support/LiteZap/data/admin_token')),
      h('li', null, 'Windows: ', h('code', null, '%APPDATA%\\LiteZap\\data\\admin_token')),
      h('li', null, 'Linux: ', h('code', null, '~/.local/share/litezap/data/admin_token'))),
    h('p', { class: 'muted small' }, '"Lembrar" guarda o token neste navegador para este endereço. Não marque em computador compartilhado. "Sair" apaga o token.')));
  mount(root, h('div', { class: 'login-wrap' }, form));
  input.focus();
}

// ---------- instances ----------

const ACTION_META = {
  stop: ['Parar', 'power'], restart: ['Reiniciar', 'refresh'], logout: ['Desconectar o número', 'logout'], start: ['Iniciar', 'power'],
};

function instancesView() {
  const grid = h('div', { class: 'inst-grid', 'aria-busy': 'true' }, skeleton(3));
  const stats = h('div', { class: 'stats' });
  const cards = new Map();
  let list = null;

  const wizard = (inst) => openWizard({ inst, aiClients: !!(info && info.ai_clients), onChange: () => load() });

  const newBtn = () => h('button', { class: 'btn primary', type: 'button', onclick: () => wizard(null) }, icon('plus', 18), 'Nova instância');

  const render = () => {
    grid.removeAttribute('aria-busy');
    cards.clear();
    if (!list.length) {
      mount(stats);
      mount(grid, h('div', { class: 'card empty-card', style: null },
        emptyState({
          iconName: 'phone', title: 'Nenhuma instância ainda',
          text: 'Crie uma instância e conecte um número de WhatsApp em menos de um minuto. Depois é só usar a API, os webhooks ou a IA.',
          action: h('button', { class: 'btn primary large', type: 'button', onclick: () => wizard(null) }, icon('plus', 18), 'Criar a primeira instância'),
        })));
      grid.firstChild.style.gridColumn = '1 / -1';
      return;
    }
    const connected = list.filter((i) => i.state === 'connected').length;
    mount(stats,
      h('span', { class: 'stat' }, h('b', null, String(list.length)), list.length === 1 ? ' instância' : ' instâncias'),
      h('span', { class: 'stat' }, h('span', { class: 'stat-dot', 'aria-hidden': 'true' }), h('b', null, String(connected)), connected === 1 ? ' conectada' : ' conectadas'));
    mount(grid, list.map((inst) => { const c = card(inst); cards.set(inst.id, c); return c; }));
  };

  const card = (inst) => {
    const href = `#/i/${encodeURIComponent(inst.id)}`;
    const needsConnect = !['connected', 'banned', 'reconnecting'].includes(inst.state);
    const foot = [];
    if (needsConnect) {
      foot.push(h('button', { class: 'btn primary small', type: 'button', onclick: () => wizard(inst) }, icon('qr', 15), 'Conectar'));
    } else {
      foot.push(h('a', { class: 'btn small', href: `${href}/chats` }, icon('chat', 15), 'Conversas'));
    }
    foot.push(h('span', { class: 'grow' }));
    foot.push(h('a', { class: 'icon-btn', href: `${href}/settings`, title: 'Configurações', 'aria-label': `Configurações: ${inst.name}` }, icon('settings', 17)));
    for (const a of availableActions(inst.state)) {
      if (a === 'start') continue; // "Conectar" starts it
      if (a === 'logout' && inst.state !== 'connected') continue; // nothing paired yet
      const [label, ic] = ACTION_META[a];
      const b = h('button', { class: 'icon-btn', type: 'button', title: label, 'aria-label': `${label}: ${inst.name}` }, icon(ic, 17));
      b.addEventListener('click', async () => {
        b.disabled = true;
        try { const v = await instanceAction(inst.id, a); if (v) update(v); } catch (e) { toast(errMsg(e), 'error'); } finally { b.disabled = false; }
      });
      foot.push(b);
    }
    const del = h('button', { class: 'icon-btn danger', type: 'button', title: 'Excluir', 'aria-label': `Excluir ${inst.name}` }, icon('trash', 17));
    del.addEventListener('click', async () => {
      if (!(await confirmDanger(`Excluir a instância "${inst.name}"? A sessão do WhatsApp, o histórico e a fila dela serão apagados.`, { ok: 'Excluir' }))) return;
      del.disabled = true;
      try {
        await api.del(`/v1/instances/${encodeURIComponent(inst.id)}`);
        toast('Instância excluída');
        load();
      } catch (e) { toast(errMsg(e), 'error'); del.disabled = false; }
    });
    foot.push(del);
    return h('article', { class: 'card inst-card' },
      h('div', { class: 'inst-card-top' },
        h('div', { class: 'avatar', 'aria-hidden': 'true' }, initials(inst.name)),
        h('div', { class: 'inst-card-name' },
          h('a', { href }, inst.name || inst.id),
          h('div', { class: 'mono' }, inst.id))),
      h('div', { class: 'inst-card-meta' },
        stateBadge(inst.state, inst.state_reason),
        inst.phone ? h('span', { class: 'phone' }, icon('phone', 14), phoneOf(inst.phone)) : null),
      (inst.tags || []).length ? h('div', null, tagChips(inst)) : null,
      needsConnect && inst.state !== 'banned'
        ? h('div', { class: 'resume-hint' }, icon('alert', 14), 'Falta conectar o WhatsApp. Continue de onde parou.')
        : null,
      h('div', { class: 'inst-card-foot' }, foot));
  };

  const update = (inst) => {
    if (!list) return;
    const i = list.findIndex((x) => x.id === inst.id);
    if (i < 0) { load(); return; }
    list[i] = inst;
    render();
  };

  const load = async () => {
    try {
      list = (await api.get('/v1/instances')).instances || [];
      render();
    } catch (e) {
      grid.removeAttribute('aria-busy');
      mount(grid, h('div', { class: 'card', style: null }, emptyState({
        iconName: 'alert', title: 'Não deu para carregar as instâncias', text: errMsg(e),
        action: h('button', { class: 'btn', type: 'button', onclick: load }, icon('refresh', 16), 'Tentar de novo'),
      })));
    }
  };

  // Throttled reload for events about instances this page does not know yet.
  let pending = null;
  const stop = api.subscribe('/v1/events?types=instance.status', (type, ev) => {
    if (!cards.has(ev.instance_id)) { clearTimeout(pending); pending = setTimeout(load, 500); return; }
    api.get(`/v1/instances/${encodeURIComponent(ev.instance_id)}`).then(update).catch(() => {});
  }, (s) => { setLive(s); if (s === 'open' && list) load(); });

  load();
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' },
      h('div', null, h('h1', null, 'Instâncias'), h('p', null, 'Cada instância é um número de WhatsApp conectado ao LiteZap.')),
      h('div', { class: 'page-head-actions' }, newBtn())),
    stats, grid), 'instances'));
  return () => { stop(); clearTimeout(pending); };
}

function globalWebhooksView() {
  const panel = webhooksPanel(null);
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, 'Webhooks globais'),
      h('p', null, 'Recebem eventos de todas as instâncias. Os de uma instância ficam na aba Webhooks dela; aqui aparecem todos.'))),
    panel.el), 'webhooks'));
  return panel.cleanup;
}

// ---------- routing ----------

let instanceCtx = null; // kept while navigating between tabs of the same instance

function parseRoute() {
  try {
    return location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  } catch {
    return []; // malformed escape in a hand-edited URL
  }
}

async function route() {
  await consumeLaunchCode();
  if (!info) info = await api.dashboardInfo();
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
      instanceCtx = instanceView(parts[1], { root, shell, setLive, openWizard: (inst) => openWizard({ inst, aiClients: !!info.ai_clients }) });
    }
    instanceCtx.show(parts[2] || 'chats', parts.slice(3));
    return;
  }
  if (instanceCtx) { instanceCtx.close(); instanceCtx = null; }
  if (teardown) { teardown(); teardown = null; }
  if (parts[0] === 'ia' && info.ai_clients) teardown = aiClientsView({ root, shell });
  else teardown = parts[0] === 'webhooks' ? globalWebhooksView() : instancesView();
  const main = document.getElementById('main');
  if (main) main.scrollTop = 0;
}

// One-click login from the tray app: #code=... (the fragment never reaches the server).
// Handled on load and on hashchange (the tray may reopen an already open tab).
async function consumeLaunchCode() {
  const m = location.hash.match(/^#code=([A-Za-z0-9_-]{16,128})$/);
  if (!m) return;
  history.replaceState(null, '', `${location.pathname}#/`);
  try {
    api.setToken(await api.redeemLaunchCode(m[1]), false);
  } catch (e) {
    toast(`Login automático falhou: ${errMsg(e)}. Use "Abrir painel" no app de novo ou cole o token.`, 'error');
  }
}

window.addEventListener('hashchange', route);
route();
