// Instance pages: header + tabs (chats, pairing, settings, webhooks, live events), sharing
// one SSE stream per open instance.
import * as api from './api.js';
import { h, mount, clear, toast, errMsg, stateBadge, field, fmtTime, phoneOf, confirmDanger } from './ui.js';
import { webhooksPanel } from './webhooks.js';

const ACTIONS = { start: 'Iniciar', stop: 'Parar', restart: 'Reiniciar', logout: 'Deslogar' };

export async function instanceAction(id, action) {
  if (action === 'logout' && !confirmDanger('Deslogar desconecta o número deste aparelho. Para voltar, será preciso parear de novo. Continuar?')) return null;
  const v = await api.post(`/v1/instances/${encodeURIComponent(id)}/${action}`);
  toast(`${ACTIONS[action]}: ok`);
  return v;
}

export function availableActions(state) {
  switch (state) {
    case 'created': case 'stopped': case 'logged_out': case 'failed': return ['start'];
    case 'banned': return [];
    default: return ['stop', 'restart', 'logout'];
  }
}

const TABS = [
  ['chats', 'Conversas'], ['pair', 'Parear'], ['settings', 'Configurações'],
  ['webhooks', 'Webhooks'], ['events', 'Eventos ao vivo'],
];

export function instanceView(id, { root, shell, setLive }) {
  const base = `/v1/instances/${encodeURIComponent(id)}`;
  const ctx = {
    id, base, inst: null, events: [], listeners: new Set(), settingsCache: null,
    on(fn) { this.listeners.add(fn); return () => this.listeners.delete(fn); },
    async settings(force) {
      if (!this.settingsCache || force) this.settingsCache = await api.get(`${base}/settings`);
      return this.settingsCache;
    },
    href(tab, ...rest) { return `#/i/${[id, tab, ...rest].map(encodeURIComponent).join('/')}`; },
  };

  const header = h('div', { class: 'inst-head card' }, h('p', { class: 'muted' }, 'Carregando…'));
  const tabs = h('nav', { class: 'tabs' });
  const body = h('div', { class: 'tab-body' });
  let current = null; // {name, el, cleanup, update}

  const renderHeader = () => {
    const inst = ctx.inst;
    if (!inst) return;
    const acts = availableActions(inst.state).map((a) => {
      const b = h('button', {
        class: 'btn small',
        onclick: async () => {
          b.disabled = true;
          try { const v = await instanceAction(id, a); if (v) { ctx.inst = v; renderHeader(); } } catch (e) { toast(errMsg(e), 'error'); } finally { b.disabled = false; }
        },
      }, ACTIONS[a]);
      return b;
    });
    mount(header,
      h('div', { class: 'inst-title' },
        h('a', { href: '#/', class: 'back', 'aria-label': 'Voltar' }, '←'),
        h('h1', null, inst.name || inst.id),
        stateBadge(inst.state, inst.state_reason),
        inst.phone ? h('span', { class: 'phone' }, phoneOf(inst.phone)) : null),
      h('div', { class: 'inst-sub' }, h('span', { class: 'mono muted small' }, inst.id), h('div', { class: 'actions' }, acts)));
  };

  const refresh = async () => {
    try {
      ctx.inst = await api.get(base);
      renderHeader();
    } catch (e) {
      if (e.status === 404) { toast('Instância não encontrada', 'error'); location.hash = '#/'; } else toast(errMsg(e), 'error');
    }
  };

  // chat.presence is opt-in (not part of "*"): ask for it so the live feed shows typing.
  const stop = api.subscribe(`${base}/events?types=*,chat.presence`, (type, ev) => {
    ctx.events.unshift(ev);
    if (ctx.events.length > 300) ctx.events.length = 300;
    if (type === 'instance.status') {
      // The event has state/reason; the phone shows up after pairing, so refetch.
      refresh();
    }
    for (const l of ctx.listeners) l(type, ev);
  }, setLive);

  mount(root, shell(h('section', null, header, tabs, body), 'instances'));
  refresh();

  return {
    id,
    show(tab, rest) {
      if (!TABS.some(([t]) => t === tab)) tab = 'chats';
      mount(tabs, TABS.map(([t, label]) => h('a', { href: ctx.href(t), class: t === tab ? 'active' : '' }, label)));
      if (current && current.name === tab && current.update) { current.update(rest); return; }
      if (current && current.cleanup) current.cleanup();
      const make = { chats: chatsTab, pair: pairTab, settings: settingsTab, webhooks: webhooksTab, events: eventsTab }[tab];
      current = { name: tab, ...make(ctx, rest) };
      mount(body, current.el);
    },
    close() {
      stop();
      if (current && current.cleanup) current.cleanup();
      current = null;
    },
  };
}

// ---------- pairing ----------

function pairTab(ctx) {
  const el = h('div', { class: 'card pair' });
  const qrImg = h('img', { class: 'qr', alt: 'QR de pareamento', width: 280, height: 280 });
  const qrNote = h('p', { class: 'muted small' });
  let objectURL = null;
  let pairCode = null;
  let lastState = null;

  const loadQR = async () => {
    try {
      const blob = await api.get(`${ctx.base}/qr?format=png`, { blob: true });
      if (objectURL) URL.revokeObjectURL(objectURL);
      objectURL = URL.createObjectURL(blob);
      qrImg.src = objectURL;
      qrNote.textContent = `QR atualizado às ${new Date().toLocaleTimeString('pt-BR')}. Ele muda sozinho a cada ~20s.`;
    } catch (e) {
      qrNote.textContent = e.code === 'instance_not_pairing' ? 'Aguardando um QR do WhatsApp…' : errMsg(e);
    }
  };

  const phone = h('input', { inputmode: 'numeric', placeholder: '5511999999999', 'aria-label': 'Número com DDI' });
  const codeOut = h('div', { class: 'pair-code', hidden: true });
  const showCode = (code) => {
    pairCode = code;
    const pretty = code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
    mount(codeOut, h('div', { class: 'code mono' }, pretty),
      h('p', { class: 'small' }, 'No celular: WhatsApp > Aparelhos conectados > Conectar um aparelho > "Conectar com número de telefone" e digite o código.'));
    codeOut.hidden = false;
  };
  const pairBtn = h('button', { class: 'btn', type: 'submit' }, 'Gerar código');
  const pairForm = h('form', {
    class: 'row',
    onsubmit: async (ev) => {
      ev.preventDefault();
      pairBtn.disabled = true;
      try { showCode((await api.post(`${ctx.base}/pair-phone`, { phone: phone.value.replace(/\D/g, '') })).code); } catch (e) { toast(errMsg(e), 'error'); } finally { pairBtn.disabled = false; }
    },
  }, phone, pairBtn);

  const render = () => {
    const st = ctx.inst ? ctx.inst.state : null;
    if (st === lastState) return;
    lastState = st;
    if (!st) { mount(el, h('p', { class: 'muted' }, 'Carregando…')); return; }
    if (st === 'connected') {
      mount(el, h('div', { class: 'pair-done' },
        h('div', { class: 'big-check', 'aria-hidden': 'true' }, '✓'),
        h('h2', null, 'Conectado'),
        h('p', null, ctx.inst.phone ? `Número ${phoneOf(ctx.inst.phone)} pareado.` : 'Número pareado.'),
        h('a', { class: 'btn primary', href: ctx.href('chats') }, 'Ver conversas')));
      return;
    }
    if (st === 'banned') {
      mount(el, h('p', { class: 'error' }, 'Este número foi banido pelo WhatsApp. Exclua a instância.'));
      return;
    }
    if (['created', 'stopped', 'logged_out', 'failed'].includes(st)) {
      const b = h('button', {
        class: 'btn primary',
        onclick: async () => {
          b.disabled = true;
          try { ctx.inst = await api.post(`${ctx.base}/start`); lastState = null; render(); } catch (e) { toast(errMsg(e), 'error'); b.disabled = false; }
        },
      }, 'Iniciar pareamento');
      mount(el, h('h2', null, 'Parear um número'),
        h('p', null, 'A instância está parada. Inicie para gerar o QR.'), b);
      return;
    }
    if (st === 'qr_pending') {
      mount(el,
        h('div', { class: 'pair-grid' },
          h('div', null,
            h('h2', null, 'Escaneie o QR'),
            h('ol', { class: 'steps' },
              h('li', null, 'Abra o WhatsApp no celular do número que vai conectar.'),
              h('li', null, 'Toque em Aparelhos conectados > Conectar um aparelho.'),
              h('li', null, 'Aponte a câmera para o QR ao lado.')),
            h('h3', null, 'Ou parear pelo número'),
            h('p', { class: 'small muted' }, 'Digite o número com DDI e DDD (só dígitos) para receber um código de 8 caracteres.'),
            pairForm, codeOut),
          h('div', { class: 'qr-box' }, qrImg, qrNote)));
      if (pairCode) showCode(pairCode);
      loadQR();
      return;
    }
    mount(el, h('div', { class: 'waiting' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }),
      h('p', null, st === 'reconnecting'
        ? `Reconectando ao WhatsApp${ctx.inst.state_reason ? ` (${ctx.inst.state_reason})` : ''}…`
        : 'Iniciando o worker… o QR aparece aqui em instantes.')));
  };

  const off = ctx.on((type, ev) => {
    if (type === 'instance.qr' && lastState === 'qr_pending') loadQR();
    else if (type === 'instance.pair_code' && ev.data && ev.data.code) showCode(ev.data.code);
  });
  // ctx.inst changes on instance.status (refetched by the view); poll it cheaply.
  const tick = setInterval(render, 700);
  const qrTimer = setInterval(() => { if (lastState === 'qr_pending') loadQR(); }, 20000);
  render();
  return {
    el,
    cleanup() { off(); clearInterval(tick); clearInterval(qrTimer); if (objectURL) URL.revokeObjectURL(objectURL); },
  };
}

// ---------- chats ----------

const STATUS = {
  queued: ['⏱', 'na fila'], in_flight: ['…', 'enviando'], pending: ['…', 'enviando'],
  sent: ['✓', 'enviada'], delivered: ['✓✓', 'entregue'], read: ['✓✓', 'lida'], played: ['✓✓', 'reproduzida'], failed: ['✗', 'falhou'],
};

const RANK = { queued: 1, in_flight: 2, pending: 2, sent: 3, delivered: 4, read: 5, played: 6, failed: 7 };
const rank = (st) => RANK[st] || 0;

function statusMark(m) {
  const [icon, label] = STATUS[m.status] || ['', m.status || ''];
  return h('span', { class: `status s-${m.status}`, title: m.error ? `${label}: ${m.error}` : label },
    icon, ' ', label, m.status === 'failed' && m.error ? `: ${m.error}` : '');
}

const TYPE_LABEL = {
  image: 'imagem', video: 'vídeo', audio: 'áudio', document: 'documento', sticker: 'figurinha',
  location: 'localização', contact: 'contato', reaction: 'reação', button_reply: 'resposta de botão',
};

function clockIn(tz) {
  try {
    return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: tz || undefined }).format(new Date());
  } catch { return null; }
}

function queueNotice(set, inst) {
  const win = set.send_window || {};
  const hasWindow = win.start || win.end;
  const now = clockIn(set.timezone);
  const outside = hasWindow && now && (now < win.start || now >= win.end);
  const tz = set.timezone || 'fuso do servidor';
  return h('div', { class: 'notice' },
    h('b', null, 'Mensagem na fila.'), ' Ela sai quando:',
    h('ul', null,
      inst && inst.state !== 'connected'
        ? h('li', { class: 'warn' }, `a instância conectar (agora: ${inst.state}); nada sai antes disso;`) : null,
      h('li', null, 'passar a carência de 15–30s depois de conectar (protege o número logo após o login);'),
      h('li', null, `o ritmo permitir: pelo menos ${(set.min_interval_ms / 1000).toFixed(1)}s (+ até ${(set.jitter_ms / 1000).toFixed(1)}s) entre envios, até ${set.max_per_minute}/min`,
        set.ramp && set.ramp.enabled ? `, e o limite diário da rampa de número novo (${set.ramp.start_per_day}→${set.ramp.end_per_day} em ${set.ramp.days} dias)` : '', ';'),
      hasWindow
        ? h('li', null, `se o contato não te mandou nada nas últimas 24h, dentro da janela de envio ${win.start}–${win.end} (${tz}). Respostas a conversas ativas saem a qualquer hora.`,
          outside ? h('b', null, ` Agora (${now}) está fora da janela: a mensagem espera a próxima abertura.`) : null)
        : h('li', null, 'sem janela de envio configurada: sai a qualquer hora.')),
    h('p', { class: 'small' }, 'A primeira mensagem para um número ainda passa pela verificação de que ele tem WhatsApp. Ajuste janela e ritmo na aba Configurações.'));
}

function chatsTab(ctx, rest) {
  const list = h('ul', { class: 'chat-list' });
  const more = h('button', { class: 'btn small ghost', hidden: true }, 'Carregar mais');
  const pane = h('div', { class: 'conv' });
  let cursor = '';
  let current = null; // {jid, bubbles: Map, box, notice}
  const chats = new Map();

  const chatItem = (c) => h('li', null, h('a', {
    href: ctx.href('chats', c.jid),
    class: current && current.jid === c.jid ? 'active' : '',
  },
  h('span', { class: 'chat-name' }, c.name || phoneOf(c.jid), c.is_group ? h('span', { class: 'tag' }, 'grupo') : null),
  h('span', { class: 'chat-meta' }, fmtTime(c.last_message_at),
    c.unread_count ? h('span', { class: 'unread' }, String(c.unread_count)) : null)));

  const loadChats = async (append) => {
    try {
      const page = await api.get(`${ctx.base}/chats?limit=50${append && cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
      if (!append) { chats.clear(); clear(list); }
      for (const c of page.chats || []) { chats.set(c.jid, c); list.appendChild(chatItem(c)); }
      cursor = page.next_cursor || '';
      more.hidden = !cursor;
      if (!chats.size) list.appendChild(h('li', { class: 'muted small pad' }, 'Nenhuma conversa ainda. As conversas aparecem quando chegam mensagens (ou depois do history sync).'));
    } catch (e) {
      mount(list, h('li', { class: 'muted small pad' }, e.code === 'instance_not_connected' ? 'Instância desconectada.' : errMsg(e)));
    }
  };
  more.addEventListener('click', () => loadChats(true));
  let reloadTimer = null;
  const reloadChats = () => { clearTimeout(reloadTimer); reloadTimer = setTimeout(() => loadChats(false), 800); };

  const bubble = (m, isGroup) => {
    const media = m.media;
    let content;
    if (m.type === 'text') content = h('div', { class: 'text' }, m.text || '');
    else if (m.type === 'reaction') content = h('div', { class: 'text muted' }, m.text ? `reagiu ${m.text}` : 'removeu a reação');
    else {
      content = h('div', { class: 'text' },
        h('span', { class: 'tag' }, TYPE_LABEL[m.type] || m.type),
        m.text ? ` ${m.text}` : '',
        media && media.url && media.id ? downloadButton(ctx, media) : null);
    }
    return h('div', { class: `msg ${m.from_me ? 'out' : 'in'}` },
      !m.from_me && isGroup ? h('div', { class: 'sender' }, m.push_name || phoneOf(m.sender)) : null,
      content,
      h('div', { class: 'meta' }, fmtTime(m.timestamp), m.from_me ? statusMark(m) : null));
  };

  const upsert = (m, prepend) => {
    if (!current) return;
    const key = m.id || m.wa_message_id;
    const known = current.bubbles.get(m.id) || (m.wa_message_id && current.bubbles.get(m.wa_message_id));
    const merged = known ? { ...known.m, ...Object.fromEntries(Object.entries(m).filter(([, v]) => v !== undefined && v !== '')) } : m;
    // Events can beat the POST response: never move a status backwards.
    if (known && rank(known.m.status) > rank(merged.status)) merged.status = known.m.status;
    const el = bubble(merged, current.isGroup);
    const entry = { m: merged, el };
    if (known) known.el.replaceWith(el);
    else if (prepend) current.box.insertBefore(el, current.box.firstChild.nextSibling);
    else {
      const atBottom = current.box.scrollHeight - current.box.scrollTop - current.box.clientHeight < 80;
      current.box.appendChild(el);
      if (atBottom || m.from_me) current.box.scrollTop = current.box.scrollHeight;
    }
    if (key) current.bubbles.set(key, entry);
    if (merged.id) current.bubbles.set(merged.id, entry);
    if (merged.wa_message_id) current.bubbles.set(merged.wa_message_id, entry);
  };

  const openChat = async (jid) => {
    for (const a of list.querySelectorAll('a')) a.classList.toggle('active', a.getAttribute('href') === ctx.href('chats', jid));
    if (!jid) {
      current = null;
      mount(pane, h('div', { class: 'conv-empty' },
        h('p', null, 'Escolha uma conversa ou comece uma nova pelo número.')));
      return;
    }
    const chat = chats.get(jid);
    const olderBtn = h('button', { class: 'btn small ghost older', hidden: true }, 'Carregar anteriores');
    const box = h('div', { class: 'messages' }, olderBtn);
    const notice = h('div', { class: 'notice-slot' });
    const text = h('textarea', { rows: 2, placeholder: 'Escreva uma mensagem… (Enter envia, Shift+Enter quebra linha)', 'aria-label': 'Mensagem' });
    const sendBtn = h('button', { class: 'btn primary', type: 'submit' }, 'Enviar');
    current = { jid, bubbles: new Map(), box, notice, isGroup: chat ? chat.is_group : jid.endsWith('@g.us') };
    const me = current;
    let older = '';

    const send = async (ev) => {
      if (ev) ev.preventDefault();
      const body = text.value;
      if (!body.trim()) return;
      sendBtn.disabled = true;
      try {
        const res = await api.post(`${ctx.base}/messages`, {
          to: jid, type: 'text', text: body, client_message_id: crypto.randomUUID(),
        });
        text.value = '';
        upsert({ ...res, from_me: true, type: 'text', text: res.text || body, timestamp: res.timestamp || new Date().toISOString() });
        if (res.status === 'queued') {
          try { mount(notice, queueNotice(await ctx.settings(), ctx.inst)); } catch { /* settings unavailable */ }
        } else clear(notice);
      } catch (e) {
        toast(errMsg(e), 'error');
      } finally { sendBtn.disabled = false; text.focus(); }
    };
    text.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) send(e); });

    mount(pane,
      h('div', { class: 'conv-head' },
        h('b', null, chat && chat.name ? chat.name : phoneOf(jid)),
        h('span', { class: 'mono muted small' }, jid)),
      box, notice,
      h('form', { class: 'composer', onsubmit: send }, text, sendBtn));

    const loadPage = async (initial) => {
      try {
        const page = await api.get(`${ctx.base}/chats/${encodeURIComponent(jid)}/messages?limit=40${older ? `&cursor=${encodeURIComponent(older)}` : ''}`);
        if (current !== me) return;
        const msgs = page.messages || [];
        // The API returns newest first.
        if (initial) for (const m of msgs.reverse()) upsert(m);
        else for (const m of msgs) upsert(m, true);
        older = page.next_cursor || '';
        olderBtn.hidden = !older;
        if (initial) box.scrollTop = box.scrollHeight;
        if (initial && !msgs.length) box.appendChild(h('p', { class: 'muted small center' }, 'Sem mensagens no histórico desta conversa.'));
      } catch (e) {
        if (e.status === 404) box.appendChild(h('p', { class: 'muted small center' }, 'Nova conversa: ainda não há histórico.'));
        else toast(errMsg(e), 'error');
      }
    };
    olderBtn.addEventListener('click', () => loadPage(false));
    if (jid.includes('@')) await loadPage(true);
    else box.appendChild(h('p', { class: 'muted small center' }, `Nova conversa com +${jid}. A resposta aparece em Conversas sob o JID que o WhatsApp devolver (com ou sem o 9º dígito).`));
    text.focus();
  };

  const toInput = h('input', { inputmode: 'tel', placeholder: 'Número com DDI (5511…)', 'aria-label': 'Número' });
  const newForm = h('form', {
    class: 'row new-chat',
    onsubmit: (ev) => {
      ev.preventDefault();
      const v = toInput.value.trim();
      const to = v.includes('@') ? v : v.replace(/\D/g, '');
      if (!to) return;
      toInput.value = '';
      location.hash = ctx.href('chats', to);
    },
  }, toInput, h('button', { class: 'btn small', type: 'submit' }, 'Abrir'));

  const off = ctx.on((type, ev) => {
    const d = ev.data || {};
    if (type === 'message.received' || type === 'message.sent' || type === 'message.reaction') {
      reloadChats();
      if (current && (d.chat === current.jid || current.bubbles.has(d.id))) upsert(d);
    } else if (type === 'message.status') {
      if (current && (current.bubbles.has(d.id) || (d.wa_message_id && current.bubbles.has(d.wa_message_id)))) {
        upsert({ id: d.id, wa_message_id: d.wa_message_id, status: d.status, error: d.error });
      }
    }
  });

  const el = h('div', { class: 'chats card' },
    h('aside', { class: 'chat-side' }, newForm, list, more),
    pane);
  loadChats(false).then(() => openChat(rest[0] || null));
  return {
    el,
    update(r) { openChat(r[0] || null); },
    cleanup() { off(); clearTimeout(reloadTimer); },
  };
}

function downloadButton(ctx, media) {
  const b = h('button', { class: 'btn small ghost', type: 'button' }, 'Baixar');
  b.addEventListener('click', async () => {
    b.disabled = true;
    try {
      const blob = await api.get(`${ctx.base}/media/${encodeURIComponent(media.id)}`, { blob: true });
      const url = URL.createObjectURL(blob);
      const a = h('a', { href: url, download: media.file_name || `${media.kind || 'midia'}-${media.id}` });
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (e) { toast(errMsg(e), 'error'); } finally { b.disabled = false; }
  });
  return b;
}

// ---------- settings ----------

function settingsTab(ctx) {
  const el = h('div', { class: 'card settings' }, h('p', { class: 'muted' }, 'Carregando…'));
  const num = (v, attrs = {}) => h('input', { type: 'number', value: v, ...attrs });

  const render = (s) => {
    const minI = num(s.min_interval_ms, { min: 0, step: 100 });
    const jitter = num(s.jitter_ms, { min: 0, step: 100 });
    const maxMin = num(s.max_per_minute, { min: 1 });
    const typing = h('input', { type: 'checkbox', checked: s.typing_simulation });
    const rampOn = h('input', { type: 'checkbox', checked: s.ramp.enabled });
    const rampStart = num(s.ramp.start_per_day, { min: 1 });
    const rampEnd = num(s.ramp.end_per_day, { min: 1 });
    const rampDays = num(s.ramp.days, { min: 1, max: 365 });
    const noWin = h('input', { type: 'checkbox', checked: !s.send_window.start && !s.send_window.end });
    const winStart = h('input', { type: 'time', value: s.send_window.start || '08:00' });
    const winEnd = h('input', { type: 'time', value: s.send_window.end || '20:00' });
    const syncWin = () => { winStart.disabled = winEnd.disabled = noWin.checked; };
    noWin.addEventListener('change', syncWin);
    syncWin();
    const tzList = h('datalist', { id: 'tz-list' });
    try { for (const z of Intl.supportedValuesOf('timeZone')) tzList.appendChild(h('option', { value: z })); } catch { /* old browser */ }
    const tz = h('input', { value: s.timezone, list: 'tz-list', placeholder: 'vazio = fuso do servidor (ex.: America/Sao_Paulo)' });
    const calls = s.calls || {};
    const autoReject = h('input', { type: 'checkbox', checked: !!calls.auto_reject });
    const rejectMsg = h('textarea', { rows: 2, maxlength: 1000, placeholder: 'vazio = rejeitar sem mandar mensagem' }, calls.reject_message || '');
    const online = h('input', { type: 'checkbox', checked: !!(s.presence && s.presence.online) });
    const unsafe = h('input', { type: 'checkbox', checked: s.unsafe_pacing });
    const proxy = h('input', { class: 'mono', value: s.proxy_url, placeholder: 'vazio = proxy padrão do servidor (ou conexão direta)', autocomplete: 'off' });
    // Settings the server's operator manages (hosted plans) are read-only here.
    const locked = new Set(s.locked || []);
    proxy.disabled = locked.has('proxy_url');
    unsafe.disabled = locked.has('unsafe_pacing');
    const save = h('button', { class: 'btn primary', type: 'submit' }, 'Salvar');

    const form = h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        const p = {};
        // An emptied number field means "keep the current value", never 0.
        const n = (input, cur) => (input.value.trim() === '' ? cur : Number(input.value));
        if (n(minI, s.min_interval_ms) !== s.min_interval_ms) p.min_interval_ms = n(minI);
        if (n(jitter, s.jitter_ms) !== s.jitter_ms) p.jitter_ms = n(jitter);
        if (n(maxMin, s.max_per_minute) !== s.max_per_minute) p.max_per_minute = n(maxMin);
        if (typing.checked !== s.typing_simulation) p.typing_simulation = typing.checked;
        const ramp = {};
        if (rampOn.checked !== s.ramp.enabled) ramp.enabled = rampOn.checked;
        if (n(rampStart, s.ramp.start_per_day) !== s.ramp.start_per_day) ramp.start_per_day = n(rampStart);
        if (n(rampEnd, s.ramp.end_per_day) !== s.ramp.end_per_day) ramp.end_per_day = n(rampEnd);
        if (n(rampDays, s.ramp.days) !== s.ramp.days) ramp.days = n(rampDays);
        if (Object.keys(ramp).length) p.ramp = ramp;
        const win = noWin.checked ? { start: '', end: '' } : { start: winStart.value, end: winEnd.value };
        if (win.start !== s.send_window.start || win.end !== s.send_window.end) p.send_window = win;
        if (tz.value.trim() !== s.timezone) p.timezone = tz.value.trim();
        const cp = {};
        if (autoReject.checked !== !!calls.auto_reject) cp.auto_reject = autoReject.checked;
        if (rejectMsg.value.trim() !== (calls.reject_message || '')) cp.reject_message = rejectMsg.value.trim();
        if (Object.keys(cp).length) p.calls = cp;
        if (online.checked !== !!(s.presence && s.presence.online)) p.presence = { online: online.checked };
        if (!unsafe.disabled && unsafe.checked !== s.unsafe_pacing) p.unsafe_pacing = unsafe.checked;
        // The server masks the proxy password: send it only when the user changed it.
        if (!proxy.disabled && proxy.value.trim() !== s.proxy_url) {
          if (proxy.value.includes(':xxxxx@')) {
            toast('O proxy mudou mas a senha continua mascarada (xxxxx): digite a senha real de novo.', 'error');
            return;
          }
          p.proxy_url = proxy.value.trim();
        }
        if (!Object.keys(p).length) { toast('Nada mudou'); return; }
        if (p.proxy_url !== undefined && !confirmDanger('Trocar o proxy reinicia o worker da instância. Continuar?')) return;
        save.disabled = true;
        try {
          const next = await api.put(`${ctx.base}/settings`, p);
          ctx.settingsCache = next;
          toast('Configurações salvas');
          render(next);
        } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('fieldset', null, h('legend', null, 'Ritmo de envio'),
      h('div', { class: 'grid3' },
        field('Intervalo mínimo (ms)', minI, 'Mínimo 1000 sem "ritmo inseguro".'),
        field('Variação aleatória (ms)', jitter, 'Somada ao intervalo a cada envio.'),
        field('Máximo por minuto', maxMin, 'Até 60 sem "ritmo inseguro".')),
      h('label', { class: 'check' }, typing, ' Simular "digitando…" antes de enviar')),
    h('fieldset', null, h('legend', null, 'Rampa de número novo'),
      h('label', { class: 'check' }, rampOn, ' Limitar envios por dia, subindo aos poucos'),
      h('div', { class: 'grid3' },
        field('Início (por dia)', rampStart), field('Fim (por dia)', rampEnd), field('Dias', rampDays))),
    h('fieldset', null, h('legend', null, 'Janela de envio'),
      h('p', { class: 'small muted' }, 'Mensagens para quem não te mandou nada nas últimas 24h só saem dentro da janela; respostas a conversas ativas saem a qualquer hora.'),
      h('label', { class: 'check' }, noWin, ' Sem janela (enviar a qualquer hora)'),
      h('div', { class: 'grid3' }, field('Início', winStart), field('Fim', winEnd), field('Fuso horário', tz)), tzList),
    h('fieldset', null, h('legend', null, 'Chamadas e presença'),
      h('label', { class: 'check' }, autoReject, ' Rejeitar chamadas individuais automaticamente'),
      field('Mensagem ao rejeitar', rejectMsg, 'Vai pela fila (ritmo e janela valem), no máximo uma por contato a cada 10 min; só em chamadas individuais.'),
      h('label', { class: 'check' }, online, ' Ficar online (recebe "digitando…" e presença dos contatos)'),
      h('p', { class: 'small muted' }, 'Atenção: com a conta online o WhatsApp para de mostrar notificações no celular. Deixe desligado se alguém usa o celular para atender.')),
    h('fieldset', null, h('legend', null, 'Somente admin'),
      locked.size ? h('p', { class: 'small muted' }, 'Campos bloqueados são gerenciados pelo provedor deste servidor.') : null,
      field('Proxy (socks5:// ou http://)', proxy, 'A senha aparece mascarada; só é enviada se você alterar o campo. Vazio volta ao proxy padrão. Trocar reinicia o worker; se o proxy não responder, a instância fica "reconectando" (nunca cai para conexão direta).'),
      h('label', { class: 'check' }, unsafe, ' Ritmo inseguro (libera os pisos de intervalo e máximo por minuto; aumenta o risco de banimento)')),
    h('div', { class: 'row end' }, save));
    mount(el, form);
  };

  ctx.settings(true).then(render).catch((e) => mount(el, h('p', { class: 'error' }, errMsg(e))));
  return { el };
}

// ---------- webhooks ----------

function webhooksTab(ctx) {
  const panel = webhooksPanel(ctx.id);
  return { el: panel.el, cleanup: panel.cleanup };
}

// ---------- live events ----------

function summary(ev) {
  const d = ev.data || {};
  switch (ev.type) {
    case 'instance.status': return `${d.state}${d.reason ? ` (${d.reason})` : ''}`;
    case 'instance.qr': return 'novo QR de pareamento';
    case 'instance.pair_code': return 'código de pareamento gerado';
    case 'message.received': case 'message.sent': case 'message.reaction': case 'message.button_reply':
      return `${phoneOf(d.chat)}: ${d.type === 'text' ? (d.text || '').slice(0, 80) : `[${TYPE_LABEL[d.type] || d.type}] ${(d.text || '').slice(0, 60)}`}`;
    case 'message.status': return `${d.id || ''} → ${d.status}${d.error ? ` (${d.error})` : ''}`;
    case 'message.edited': return `${phoneOf(d.chat)}: editada → ${(d.text || '').slice(0, 60)}`;
    case 'message.revoked': return `${phoneOf(d.chat)}: apagada`;
    case 'chat.presence': return `${phoneOf(d.sender || d.chat)}: ${PRESENCE_LABEL[d.state] || d.state}`;
    case 'call.received': return `${d.is_group ? 'chamada em grupo' : 'chamada'} de ${phoneOf(d.caller)}${d.media === 'video' ? ' (vídeo)' : ''}${d.rejected ? ' — rejeitada' : ''}`;
    case 'call.accepted': return `chamada de ${phoneOf(d.caller)} atendida`;
    case 'call.ended': return `chamada de ${phoneOf(d.caller)} encerrada${d.missed ? ' (perdida)' : d.reason ? ` (${d.reason})` : ''}`;
    case 'history.synced': return `${d.messages} mensagens em ${d.chats} blocos (${d.new_messages} novas)`;
    default: return '';
  }
}

const PRESENCE_LABEL = { composing: 'digitando…', recording: 'gravando áudio…', paused: 'parou de digitar', online: 'online', offline: 'offline' };

function eventsTab(ctx) {
  const list = h('ul', { class: 'events' });
  const filter = h('input', { placeholder: 'filtrar por tipo (ex.: message.)', 'aria-label': 'Filtro' });
  const pause = h('input', { type: 'checkbox' });
  const item = (ev) => {
    const pre = h('pre', { class: 'json', hidden: true }, JSON.stringify(ev, null, 2));
    return h('li', { 'data-type': ev.type },
      h('button', { class: 'ev-line', type: 'button', onclick: () => { pre.hidden = !pre.hidden; } },
        h('span', { class: 'ev-time' }, fmtTime(ev.timestamp)),
        h('span', { class: 'ev-type' }, ev.type),
        h('span', { class: 'ev-sum' }, summary(ev))),
      pre);
  };
  const visible = (ev) => !filter.value || ev.type.includes(filter.value.trim());
  const redraw = () => {
    mount(list, ctx.events.filter(visible).map(item));
    if (!list.firstChild) list.appendChild(h('li', { class: 'muted small pad' }, 'Nenhum evento ainda. Eles aparecem aqui ao vivo (sem replay de eventos antigos).'));
  };
  filter.addEventListener('input', redraw);
  const off = ctx.on((type, ev) => {
    if (pause.checked || !visible(ev)) return;
    if (list.firstChild && list.firstChild.classList.contains('pad')) clear(list);
    list.insertBefore(item(ev), list.firstChild);
    while (list.children.length > 300) list.lastChild.remove();
  });
  redraw();
  return {
    el: h('div', { class: 'card' },
      h('div', { class: 'row' }, filter,
        h('label', { class: 'check' }, pause, ' Pausar'),
        h('button', { class: 'btn small ghost', type: 'button', onclick: () => { ctx.events.length = 0; redraw(); } }, 'Limpar')),
      h('p', { class: 'small muted' }, 'Mesmos eventos que os webhooks recebem. Clique numa linha para ver o JSON.'),
      list),
    cleanup: off,
  };
}
