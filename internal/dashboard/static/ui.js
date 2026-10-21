// Tiny DOM helpers and shared components. Data only ever reaches the page as text nodes or
// attribute values (never parsed as HTML), so names, messages and event payloads cannot
// inject markup.

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k.startsWith('on')) { if (typeof v === 'function') el.addEventListener(k.slice(2), v); continue; }
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

// External pages the free-AI setup links to (exact URLs, nothing built from data).
const EXTERNAL = new Set(['https://ollama.com/download', 'https://aistudio.google.com/apikey', 'https://console.groq.com/keys']);

// OpenStreetMap pages the business profile links to (opened by the user in a new tab; the
// dashboard itself never calls a map or geocoding service): a pin and an address search.
const OSM_PIN = /^https:\/\/www\.openstreetmap\.org\/\?mlat=-?\d{1,3}(\.\d+)?&mlon=-?\d{1,3}(\.\d+)?#map=17\/-?\d{1,3}(\.\d+)?\/-?\d{1,3}(\.\d+)?$/;
const OSM_SEARCH = /^https:\/\/www\.openstreetmap\.org\/search\?query=[A-Za-z0-9%._~-]*$/;

// safeHref allows only in-app hash routes, blob: URLs we created, the public docs, the
// few external setup pages above and the map links.
function safeHref(v) {
  const s = String(v);
  return /^(#|blob:|https:\/\/litezap\.com\/)/.test(s) || EXTERNAL.has(s) || OSM_PIN.test(s) || OSM_SEARCH.test(s) ? s : '#';
}

export function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

export function mount(el, ...children) { clear(el); append(el, children); return el; }

// ---------- icons (inline SVG built with DOM calls, 24px stroke set) ----------

const SVG = 'http://www.w3.org/2000/svg';
const ICONS = {
  plus: ['M12 5v14', 'M5 12h14'],
  phone: ['M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z', 'M11 18h2'],
  qr: ['M4 4h6v6H4z', 'M14 4h6v6h-6z', 'M4 14h6v6H4z', 'M14 14h2v2h-2z', 'M18 14h2', 'M14 18h2', 'M18 18h2v2'],
  link: ['M10 13a5 5 0 0 0 7.07 0l3-3a5 5 0 0 0-7.07-7.07l-1.5 1.5', 'M14 11a5 5 0 0 0-7.07 0l-3 3a5 5 0 0 0 7.07 7.07l1.5-1.5'],
  check: ['M5 12.5l4.5 4.5L19 7.5'],
  copy: ['M9 9h11v11H9z', 'M5 15H4V4h11v1'],
  eye: ['M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z', 'M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z'],
  eyeOff: ['M3 3l18 18', 'M10.6 5.1A10.7 10.7 0 0 1 12 5c6.5 0 10 7 10 7a17 17 0 0 1-3.2 4.1', 'M6.6 6.6A17 17 0 0 0 2 12s3.5 7 10 7a10 10 0 0 0 5.4-1.6', 'M9.9 9.9a3 3 0 0 0 4.2 4.2'],
  x: ['M6 6l12 12', 'M18 6L6 18'],
  chat: ['M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12z'],
  users: ['M16 20v-1a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v1', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', 'M22 20v-1a4 4 0 0 0-3-3.9', 'M16 3.1a4 4 0 0 1 0 7.8'],
  webhook: ['M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2', 'M6 17l3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06', 'M12 6l3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8'],
  bot: ['M12 8V4H8', 'M4 8h16v12H4z', 'M2 14h2', 'M20 14h2', 'M9 13v2', 'M15 13v2'],
  sparkles: ['M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z', 'M19 17l.8 2.2L22 20l-2.2.8L19 23l-.8-2.2L16 20l2.2-.8z'],
  settings: ['M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z'],
  logout: ['M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4', 'M16 17l5-5-5-5', 'M21 12H9'],
  book: ['M4 19.5A2.5 2.5 0 0 1 6.5 17H20V3H6.5A2.5 2.5 0 0 0 4 5.5z', 'M4 19.5A2.5 2.5 0 0 0 6.5 22H20v-5'],
  send: ['M22 2L11 13', 'M22 2l-7 20-4-9-9-4z'],
  back: ['M15 18l-6-6 6-6'],
  next: ['M9 18l6-6-6-6'],
  refresh: ['M21 12a9 9 0 1 1-2.6-6.4L21 8', 'M21 3v5h-5'],
  trash: ['M3 6h18', 'M8 6V4h8v2', 'M6 6l1 14h10l1-14'],
  power: ['M12 2v10', 'M18.4 6.6a9 9 0 1 1-12.8 0'],
  user: ['M20 21v-1a5 5 0 0 0-5-5H9a5 5 0 0 0-5 5v1', 'M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z'],
  inbox: ['M22 12h-6l-2 3h-4l-2-3H2', 'M5.5 5h13L22 12v7H2v-7z'],
  alert: ['M12 9v4', 'M12 17h.01', 'M10.3 3.9L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z'],
  activity: ['M22 12h-4l-3 9L9 3l-3 9H2'],
  edit: ['M12 20h9', 'M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z'],
  key: ['M21 2l-2 2', 'M15.5 7.5l3 3L22 7l-3-3', 'M11.4 12.6a5 5 0 1 1-7 7 5 5 0 0 1 7-7z', 'M11.4 12.6L19 5'],
  menu: ['M3 6h18', 'M3 12h18', 'M3 18h18'],
};

export function icon(name, size = 18) {
  const s = document.createElementNS(SVG, 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('width', size);
  s.setAttribute('height', size);
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '2');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  s.setAttribute('aria-hidden', 'true');
  s.setAttribute('class', 'icon');
  for (const d of ICONS[name] || []) {
    const p = document.createElementNS(SVG, 'path');
    p.setAttribute('d', d);
    s.appendChild(p);
  }
  return s;
}

// ---------- formatting ----------

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

// ---------- toasts ----------

let toastBox = null;
// toast shows msg: a string, a node, or an error (rendered with errView).
export function toast(msg, kind = 'info') {
  if (msg && typeof msg === 'object' && !(msg instanceof Node)) msg = errView(msg);
  if (!toastBox || !toastBox.isConnected) {
    toastBox = h('div', { class: 'toasts', role: 'status', 'aria-live': 'polite' });
    document.body.appendChild(toastBox);
  }
  const t = h('div', { class: `toast toast-${kind}` },
    icon(kind === 'error' ? 'alert' : 'check', 18),
    h('span', { class: 'toast-msg' }, msg),
    h('button', { class: 'toast-x', type: 'button', 'aria-label': 'Fechar aviso', onclick: () => t.remove() }, icon('x', 14)));
  toastBox.appendChild(t);
  setTimeout(() => t.remove(), kind === 'error' ? 8000 : 3500);
}

// ---------- errors ----------
// The API answers {error:{code,message,retryable}} with English messages (by design).
// The dashboard shows a pt-BR text for the code (and, for validation_failed, for the most
// common messages) and keeps the raw code and message in a small muted line for debugging.

const FRIENDLY = {
  unauthorized: 'Sessão expirada ou token inválido. Entre de novo.',
  forbidden: 'Sem permissão para isso.',
  not_found: 'Não encontrado: talvez tenha sido apagado. Recarregue a página.',
  instance_not_found: 'Instância não encontrada: talvez tenha sido apagada. Recarregue a página.',
  group_not_found: 'Grupo não encontrado (ou este número não participa mais dele).',
  conflict: 'Conflito: isso já existe ou mudou enquanto você editava. Recarregue e tente de novo.',
  rate_limited: 'Muitos pedidos em pouco tempo. Espere alguns segundos e tente de novo.',
  instance_not_pairing: 'A instância não está esperando pareamento agora.',
  instance_not_connected: 'A instância não está conectada. Conecte o WhatsApp e tente de novo.',
  instance_banned: 'Este número foi bloqueado pelo WhatsApp.',
  sending_suspended: 'Os envios desta instância estão pausados. Retome os envios nas configurações da instância.',
  daily_cap: 'Limite diário de envios atingido. Tente amanhã ou ajuste o limite nas configurações da instância.',
  limit_reached: 'Limite atingido: apague algo que não usa mais ou ajuste o limite nas configurações.',
  plan_limit: 'Limite do plano atingido.',
  pin_limit: 'Limite de conversas fixadas atingido: desafixe uma antes.',
  worker_timeout: 'O WhatsApp demorou para responder. Tente de novo em instantes.',
  worker_error: 'O WhatsApp não aceitou o pedido. Tente de novo em instantes.',
  shutting_down: 'O LiteZap está sendo encerrado. Abra-o de novo e tente outra vez.',
  internal: 'Erro interno do LiteZap. Tente de novo; se continuar, veja o log (menu do app > Ver log).',
  not_implemented: 'Isso ainda não é suportado pelo LiteZap.',
  not_supported: 'Isso não é suportado pelo WhatsApp neste caso.',
  not_configured: 'Este recurso não está configurado neste LiteZap.',
  media_too_large: 'Arquivo grande demais para o WhatsApp.',
  media_fetch_failed: 'Não deu para baixar a mídia do link informado. Confira se o link abre no navegador.',
  media_unavailable: 'Esta mídia não está mais disponível no WhatsApp.',
  unsupported_media_type: 'Tipo de arquivo não suportado.',
  upload_failed: 'Falha ao enviar o arquivo para o WhatsApp. Tente de novo.',
  invalid_number: 'Número inválido ou sem WhatsApp. Use DDI + DDD + número, ex.: 5511988887777.',
  app_state_not_synced: 'O WhatsApp ainda está sincronizando as conversas. Tente de novo em alguns minutos.',
  revoke_window_expired: 'Passou do prazo para apagar esta mensagem para todos.',
  edit_window_expired: 'Passou do prazo de 15 minutos para editar esta mensagem.',
  message_expired: 'A mensagem expirou na fila sem ser enviada.',
  message_canceled: 'A mensagem foi cancelada.',
  link_expired: 'Este link expirou. Gere um novo.',
  invite_invalid: 'Convite inválido. Confira o link.',
  invite_revoked: 'Este convite foi revogado. Peça um link novo.',
  group_forbidden: 'Só administradores do grupo podem fazer isso.',
  community_not_allowed: 'Isso não é permitido em comunidades.',
  business_account_required: 'Isso precisa de uma conta do WhatsApp Business.',
  agent_active: 'Um agente de IA está atendendo esta conversa. Pause o agente antes.',
  server_unreachable: 'Servidor indisponível. Confira se o LiteZap está aberto.',
};

// Codes whose message already is pt-BR (the desktop app's own errors, local network errors).
const PT_MESSAGE = new Set(['timeout', 'network', 'config_malformed', 'app_not_installed', 'ai_client_error', 'extension_unavailable', 'app_not_running']);

const FIELD = { name: 'O nome', about: 'O recado', description: 'A descrição', reason: 'O motivo' };

// Common validation_failed messages: [pattern, pt-BR text or (match) => text]. First match wins.
const VALIDATION = [
  [/digits including country code|mentions must be phone numbers/i, 'Número inválido: use DDI + DDD + número, ex.: 5511988887777.'],
  [/phones must have 1 to 10/i, 'Informe de 1 a 10 números.'],
  [/invalid JSON body/i, 'Os dados enviados são inválidos. Recarregue a página e tente de novo.'],
  [/^text(?: \(the new text\))? is required/i, 'Escreva a mensagem.'],
  [/each message is limited to (\d+) characters/i, (m) => `Cada mensagem aceita no máximo ${m[1]} caracteres.`],
  [/^(name|about|description|reason) is limited to (\d+) characters/i, (m) => `${FIELD[m[1].toLowerCase()]} aceita no máximo ${m[2]} caracteres.`],
  [/name must have 1 to (\d+) characters/i, (m) => `O nome precisa ter de 1 a ${m[1]} caracteres.`],
  [/^name is required/i, 'Informe um nome.'],
  [/points to a private or local address/i, 'O link da mídia aponta para um endereço local ou privado: use um link público.'],
  [/must be an absolute http\(s\) URL|invalid upload_url/i, 'Use um link completo, começando com https://.'],
  [/base64 or url|url or base64/i, 'Envie um arquivo ou informe um link.'],
  [/base64 is not valid/i, 'O arquivo enviado está corrompido. Escolha-o de novo.'],
  [/a sticker must be a WebP/i, 'Figurinhas precisam ser imagens WebP.'],
  [/a gif must be an MP4/i, 'GIFs precisam ser vídeos MP4.'],
  [/secret must have 16 to 256/i, 'O segredo precisa ter de 16 a 256 caracteres (ou deixe em branco para gerar um).'],
  [/tags must be 1-40 characters/i, 'Etiquetas: de 1 a 40 caracteres (letras, números e _ . : -), começando com letra ou número.'],
  [/expires_after must be a duration/i, 'A validade precisa ficar entre 1 minuto e 7 dias.'],
  [/no instance exists yet/i, 'Crie uma instância primeiro.'],
  [/invalid group JID|a group JID ends with @g\.us/i, 'Grupo inválido.'],
  [/participants must list at least one/i, 'Informe pelo menos um participante (número com DDI e DDD).'],
  [/poll\.options needs 2 to 12/i, 'A enquete precisa de 2 a 12 opções.'],
  [/only text messages can be edited/i, 'Só mensagens de texto podem ser editadas.'],
  [/only your own messages can be edited/i, 'Só dá para editar as suas próprias mensagens.'],
  [/only your own messages can be deleted/i, 'Só dá para apagar as suas próprias mensagens (em grupos, admins podem apagar as dos outros).'],
  [/has not been sent yet/i, 'A mensagem ainda não foi enviada.'],
  [/the message was deleted/i, 'A mensagem foi apagada.'],
  [/nothing to (change|update)/i, 'Nada para mudar.'],
  [/emoji must be a single emoji/i, 'Use um único emoji.'],
];

function validationText(msg) {
  const s = String(msg || '');
  for (const [re, out] of VALIDATION) {
    const m = re.exec(s);
    if (m) return typeof out === 'function' ? out(m) : out;
  }
  return 'Dados inválidos: confira os campos e tente de novo.';
}

function byStatus(status) {
  if (status === 400) return 'Pedido inválido: confira os campos e tente de novo.';
  if (status === 401) return FRIENDLY.unauthorized;
  if (status === 403) return FRIENDLY.forbidden;
  if (status === 404) return FRIENDLY.not_found;
  if (status === 409) return FRIENDLY.conflict;
  if (status === 413) return FRIENDLY.media_too_large;
  if (status === 429) return FRIENDLY.rate_limited;
  if (status >= 500) return 'Erro no servidor do LiteZap. Tente de novo em instantes.';
  return '';
}

// errMsg is the pt-BR text for an error (ApiError, any Error or a string).
export function errMsg(e) {
  if (!e) return 'Erro desconhecido';
  if (typeof e === 'string') return e;
  const code = e.code || '';
  if (PT_MESSAGE.has(code) && e.message) return e.message;
  if (code === 'validation_failed') return validationText(e.message);
  if (FRIENDLY[code]) return FRIENDLY[code];
  if (typeof e.status === 'number') {
    const s = byStatus(e.status);
    if (s) return s;
  }
  return e.message || String(e);
}

// errDetail is the raw code and message for the muted line ('' when it adds nothing).
export function errDetail(e) {
  if (!e || typeof e !== 'object' || !e.code || PT_MESSAGE.has(e.code)) return '';
  return e.message && e.message !== e.code ? `${e.code}: ${e.message}` : e.code;
}

// errView is errMsg plus the muted detail line, as DOM.
export function errView(e) {
  const detail = errDetail(e);
  return h('span', { class: 'err-view' }, h('span', null, errMsg(e)), detail ? h('span', { class: 'err-detail' }, detail) : null);
}

// ---------- dialogs ----------

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// trapFocus keeps Tab inside el (the native <dialog> leaves it free to reach the browser UI).
export function trapFocus(el) {
  el.addEventListener('keydown', (e) => {
    if (e.key !== 'Tab') return;
    const items = [...el.querySelectorAll(FOCUSABLE)].filter((n) => n.offsetParent !== null || n === document.activeElement);
    if (!items.length) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  });
}

// dialog opens an empty modal shell: {dlg, close}. Esc and the X call onClose (or close).
export function dialog({ label, wide, onClose } = {}) {
  const opener = document.activeElement;
  const dlg = h('dialog', { class: `modal${wide ? ' modal-wide' : ''}`, 'aria-label': label || null });
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    try { dlg.close(); } catch { /* already closed */ }
    dlg.remove();
    if (opener && opener.isConnected && typeof opener.focus === 'function') opener.focus();
  };
  dlg.addEventListener('cancel', (e) => { e.preventDefault(); (onClose || close)(); });
  dlg.addEventListener('mousedown', (e) => { if (e.target === dlg) dlg.dataset.down = '1'; });
  dlg.addEventListener('click', (e) => {
    // Click on the backdrop (the dialog box itself, outside the content).
    if (e.target === dlg && dlg.dataset.down) (onClose || close)();
    delete dlg.dataset.down;
  });
  trapFocus(dlg);
  document.body.appendChild(dlg);
  dlg.showModal();
  return { dlg, close };
}

// modal shows content in a dialog; returns close().
export function modal(title, body, actions = []) {
  const { dlg, close } = dialog({ label: title });
  mount(dlg,
    h('div', { class: 'modal-head' }, h('h2', null, title),
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Fechar', onclick: close }, icon('x'))),
    h('div', { class: 'modal-body' }, body),
    h('div', { class: 'modal-actions' },
      actions.length ? actions.map((a) => {
        const b = h('button', {
          class: a.primary ? 'btn primary' : a.danger ? 'btn danger-solid' : 'btn',
          type: 'button',
          onclick: async () => {
            b.disabled = true;
            let r;
            try { r = a.onclick ? await a.onclick() : undefined; } finally { b.disabled = false; }
            if (r === false) return;
            close();
          },
        }, a.label);
        return b;
      }) : h('button', { class: 'btn primary', type: 'button', onclick: close }, 'Fechar')));
  const focus = dlg.querySelector('.modal-body ' + FOCUSABLE) || dlg.querySelector('.modal-actions .btn.primary') || dlg.querySelector('.modal-actions .btn');
  if (focus) focus.focus();
  return close;
}

// confirmDanger asks in an in-page dialog (window.confirm is a no-op in some webviews).
export function confirmDanger(text, { ok = 'Confirmar', danger = true } = {}) {
  return new Promise((resolve) => {
    let answered = false;
    const done = (v) => { if (!answered) { answered = true; resolve(v); } };
    const { dlg, close } = dialog({ label: 'Confirmar', onClose: () => { done(false); close(); } });
    const yes = h('button', { class: danger ? 'btn danger-solid' : 'btn primary', type: 'button', onclick: () => { done(true); close(); } }, ok);
    const no = h('button', { class: 'btn', type: 'button', onclick: () => { done(false); close(); } }, 'Cancelar');
    mount(dlg,
      h('div', { class: 'confirm' },
        h('div', { class: `confirm-icon${danger ? ' danger' : ''}` }, icon('alert', 22)),
        h('p', null, text)),
      h('div', { class: 'modal-actions' }, no, yes));
    no.focus();
  });
}

// promptText asks for one line of text; resolves null on cancel.
export function promptText(title, label, value = '') {
  return new Promise((resolve) => {
    let answered = false;
    const done = (v) => { if (!answered) { answered = true; resolve(v); } };
    const { dlg, close } = dialog({ label: title, onClose: () => { done(null); close(); } });
    const input = h('input', { value });
    const form = h('form', {
      onsubmit: (e) => { e.preventDefault(); done(input.value); close(); },
    },
    h('div', { class: 'modal-head' }, h('h2', null, title)),
    h('div', { class: 'modal-body' }, field(label, input)),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', type: 'button', onclick: () => { done(null); close(); } }, 'Cancelar'),
      h('button', { class: 'btn primary', type: 'submit' }, 'Salvar')));
    mount(dlg, form);
    input.focus();
    input.select();
  });
}

// ---------- clipboard and secrets ----------

// copyText copies with the async clipboard API, falling back to execCommand (webviews).
export async function copyText(value) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value);
      return true;
    }
  } catch { /* fall back */ }
  const ta = h('textarea', { class: 'offscreen', readonly: true });
  ta.value = value;
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  ta.remove();
  return ok;
}

export function copyButton(value, label = 'Copiar', cls = 'btn') {
  const b = h('button', { class: cls, type: 'button' }, icon('copy', 16), h('span', null, label));
  b.addEventListener('click', async () => {
    if (await copyText(typeof value === 'function' ? value() : value)) {
      b.classList.add('copied');
      mount(b, icon('check', 16), h('span', null, 'Copiado'));
      setTimeout(() => { b.classList.remove('copied'); mount(b, icon('copy', 16), h('span', null, label)); }, 1800);
    } else toast('Não deu para copiar. Selecione o texto e copie manualmente.', 'error');
  });
  return b;
}

export function mask(value) {
  const s = String(value || '');
  if (s.length <= 10) return '•'.repeat(s.length);
  return `${s.slice(0, 6)}${'•'.repeat(Math.min(24, s.length - 10))}${s.slice(-4)}`;
}

// secretBox shows a value masked, with reveal and copy (instance token, webhook secret).
export function secretBox(value, note) {
  let shown = false;
  const out = h('code', { class: 'secret-value' }, mask(value));
  const eye = h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mostrar', title: 'Mostrar' }, icon('eye'));
  eye.addEventListener('click', () => {
    shown = !shown;
    out.textContent = shown ? value : mask(value);
    mount(eye, icon(shown ? 'eyeOff' : 'eye'));
    eye.setAttribute('aria-label', shown ? 'Ocultar' : 'Mostrar');
  });
  return h('div', { class: 'secret-wrap' },
    note ? h('p', { class: 'secret-note' }, icon('key', 16), h('span', null, note)) : null,
    h('div', { class: 'secret-box' }, out, eye, copyButton(value, 'Copiar', 'btn small')));
}

// ---------- states, fields, misc ----------

const STATE_LABEL = {
  created: 'Criada', starting: 'Iniciando', qr_pending: 'Aguardando conexão', connecting: 'Conectando',
  connected: 'Conectada', reconnecting: 'Reconectando', logged_out: 'Desconectada', banned: 'Banida',
  failed: 'Com erro', stopped: 'Parada',
};

const REASON_LABEL = {
  proxy_required: 'precisa de proxy', qr_timeout: 'QR expirou',
};

export function stateLabel(state) { return STATE_LABEL[state] || state; }

export function stateTone(state) {
  if (state === 'connected') return 'ok';
  if (['qr_pending', 'starting', 'connecting', 'reconnecting', 'created'].includes(state)) return 'warn';
  if (['failed', 'banned', 'logged_out'].includes(state)) return 'danger';
  return 'neutral';
}

export function stateBadge(state, reason) {
  const r = reason ? (REASON_LABEL[reason] || reason) : '';
  return h('span', { class: `badge tone-${stateTone(state)} st-${state}`, title: reason || state },
    h('span', { class: 'dot', 'aria-hidden': 'true' }), STATE_LABEL[state] || state, r ? ` · ${r}` : '');
}

export function field(label, input, hint) {
  return h('label', { class: 'field' }, h('span', { class: 'field-label' }, label), input,
    hint ? h('small', { class: 'hint' }, hint) : null);
}

export function busy(btn, fn) {
  return async (ev) => {
    if (ev) ev.preventDefault();
    btn.disabled = true;
    try { await fn(); } catch (e) { toast(e, 'error'); } finally { btn.disabled = false; }
  };
}

export function skeleton(n = 3, cls = 'skel-card') {
  return Array.from({ length: n }, () => h('div', { class: `skel ${cls}`, 'aria-hidden': 'true' }));
}

export function emptyState({ iconName = 'inbox', title, text, action }) {
  return h('div', { class: 'empty-state' },
    h('div', { class: 'empty-icon' }, icon(iconName, 28)),
    h('h2', null, title),
    text ? h('p', null, text) : null,
    action || null);
}

export function spinner(label) {
  return h('span', { class: 'spinner', role: label ? 'status' : null, 'aria-label': label || null });
}
