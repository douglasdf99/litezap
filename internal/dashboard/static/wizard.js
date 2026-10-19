// Guided flow to create and connect an instance: a modal stepper (Nova instância ->
// Conectar o WhatsApp -> Pronto) plus the connect panel reused by the "Conectar" tab.
//
// Live state comes from the instance SSE stream; while the stream is not open the panel
// polls the instance every few seconds, so it never sits waiting on an event that cannot
// arrive (the old flow froze when the browser had no free connection for the stream).
import * as api from './api.js';
import {
  h, mount, clear, toast, errMsg, icon, dialog, secretBox, copyButton, field, phoneOf,
  confirmDanger, spinner, fmtTime,
} from './ui.js';

const DOCS_URL = 'https://litezap.com/docs/';
const enc = encodeURIComponent;

// Tokens are shown only at creation; keep them for this page load so the "Pronto" step
// can still show them after the user closes and resumes the wizard. Never persisted.
const tokens = new Map();

const uuid = () => (crypto.randomUUID ? crypto.randomUUID()
  : Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join(''));

export function initials(name) {
  const parts = String(name || '?').trim().split(/[\s_\-.]+/).filter(Boolean);
  const s = parts.length > 1 ? parts[0][0] + parts[1][0] : (parts[0] || '?')[0];
  return s.toUpperCase();
}

// ---------- connection status ----------

function describe(inst) {
  const st = inst ? inst.state : null;
  const reason = inst && inst.state_reason;
  switch (st) {
    case null: case undefined: return { tone: 'busy', title: 'Carregando…', sub: '' };
    case 'created': case 'starting': return { tone: 'busy', title: 'Preparando o QR…', sub: 'Iniciando a conexão com o WhatsApp.' };
    case 'qr_pending': return { tone: 'wait', title: 'Aguardando leitura', sub: 'Escaneie o QR ou use o código pelo número.' };
    case 'connecting': return { tone: 'busy', title: 'Conectando…', sub: 'Leitura feita. Finalizando o pareamento.' };
    case 'reconnecting': return { tone: 'busy', title: 'Reconectando…', sub: reason || 'Tentando falar com o WhatsApp de novo.' };
    case 'connected': return { tone: 'ok', title: 'Conectado', sub: inst.phone ? phoneOf(inst.phone) : 'Número pareado.' };
    case 'banned': return { tone: 'err', title: 'Número banido', sub: 'O WhatsApp baniu este número. Exclua a instância.' };
    case 'stopped':
      if (reason === 'proxy_required') return { tone: 'err', title: 'Precisa de proxy', sub: 'Este servidor exige proxy. Defina um em Configurações e conecte de novo.', retry: true };
      return { tone: 'err', title: 'Conexão parada', sub: 'Gere um novo QR para tentar de novo.', retry: true };
    case 'logged_out': return { tone: 'err', title: 'Desconectada', sub: 'O aparelho foi desconectado. Gere um novo QR.', retry: true };
    case 'failed': return { tone: 'err', title: 'Algo deu errado', sub: reason ? `Motivo: ${reason}` : 'Tente gerar um novo QR.', retry: true };
    default: return { tone: 'busy', title: st, sub: reason || '' };
  }
}

const TONE_ICON = { busy: null, wait: 'qr', ok: 'check', err: 'alert' };

function statusBox(inst, onRetry) {
  const d = describe(inst);
  const ic = TONE_ICON[d.tone];
  let retry = null;
  if (d.retry && onRetry) {
    retry = h('button', { class: 'btn small primary', type: 'button' }, icon('refresh', 16), 'Gerar novo QR');
    retry.addEventListener('click', async () => {
      retry.disabled = true;
      try { await onRetry(); } catch (e) { toast(errMsg(e), 'error'); } finally { retry.disabled = false; }
    });
  }
  return h('div', { class: `status-box tone-${d.tone}`, role: 'status', 'aria-live': 'polite' },
    h('span', { class: 's-icon' }, ic ? icon(ic, 18) : spinner()),
    h('div', { style: null }, h('b', null, d.title), d.sub ? h('small', null, d.sub) : null),
    retry ? h('span', { class: 'grow' }) : null, retry);
}

// ---------- connect panel ----------

// connectPanel shows QR / pairing code / pairing link for an instance and tracks its
// state live. onState(inst) fires on every change; returns {el, cleanup, inst()}.
export function connectPanel(id, { onState = () => {}, compact = false } = {}) {
  const base = `/v1/instances/${enc(id)}`;
  let inst = null;
  let alive = true;
  let sseOpen = false;
  let mode = 'qr';

  // --- QR ---
  const qrImg = h('img', { alt: 'QR code para conectar o WhatsApp', width: 248, height: 248 });
  const qrMark = h('img', { class: 'qr-mark', src: 'brand/logo-mark.png', alt: '' });
  const qrOverlay = h('div', { class: 'qr-overlay' });
  const qrFrame = h('div', { class: 'qr-frame stale' }, qrImg, qrMark, qrOverlay);
  const timerText = h('span', null, '');
  const timerBar = h('span', { style: null });
  const timer = h('div', { class: 'qr-timer', hidden: true }, icon('refresh', 14), timerText, h('div', { class: 'bar' }, timerBar));
  let objectURL = null;
  let qrAt = 0;
  let qrTTL = 20000;
  let qrLoading = false;

  const overlay = (content) => {
    qrFrame.classList.toggle('stale', !!content);
    if (content) mount(qrOverlay, content); else clear(qrOverlay);
    qrMark.hidden = !!content;
  };

  const loadQR = async (ttl) => {
    if (qrLoading || !alive) return;
    qrLoading = true;
    try {
      const blob = await api.get(`${base}/qr?format=png`, { blob: true });
      if (!alive) return;
      if (objectURL) URL.revokeObjectURL(objectURL);
      objectURL = URL.createObjectURL(blob);
      qrImg.src = objectURL;
      qrAt = Date.now();
      qrTTL = ttl || qrTTL || 20000;
      overlay(null);
      timer.hidden = false;
      tickTimer();
    } catch (e) {
      if (!alive) return;
      timer.hidden = true;
      overlay(h('div', null, spinner(), e.code === 'instance_not_pairing' ? 'Gerando um QR novo…' : errMsg(e)));
    } finally { qrLoading = false; }
  };

  const tickTimer = () => {
    if (!qrAt || timer.hidden) return;
    const left = Math.max(0, qrTTL - (Date.now() - qrAt));
    const s = Math.ceil(left / 1000);
    timerText.textContent = s > 0 ? `Novo QR em ${s}s` : 'Atualizando o QR…';
    timerBar.style.width = `${Math.round((left / qrTTL) * 100)}%`;
    // The server pushes instance.qr on rotation; if that is missed, refetch shortly after.
    if (left === 0 && Date.now() - qrAt > qrTTL + 2500 && inst && inst.state === 'qr_pending') loadQR();
  };

  const qrPane = h('div', { class: 'connect-main' },
    h('div', { class: 'qr-stage' }, qrFrame),
    timer);

  // --- pairing code ---
  const phone = h('input', { inputmode: 'tel', autocomplete: 'off', placeholder: '55 11 99999-9999', 'aria-label': 'Número com DDI e DDD' });
  const codeOut = h('div', { hidden: true });
  const showCode = (code) => {
    const pretty = code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
    mount(codeOut, h('div', { class: 'code-out' },
      h('div', { class: 'small muted' }, 'Seu código'),
      h('div', { class: 'code', 'aria-label': `Código ${code.split('').join(' ')}` }, pretty),
      copyButton(code, 'Copiar código', 'btn small'),
      h('p', { class: 'pane-note' }, 'No celular: ', h('span', { class: 'path' }, 'Aparelhos conectados > Conectar um aparelho > Conectar com número de telefone'), ' e digite o código.')));
    codeOut.hidden = false;
  };
  const codeBtn = h('button', { class: 'btn primary', type: 'submit' }, 'Gerar código');
  const codePane = h('form', {
    class: 'connect-main',
    onsubmit: async (ev) => {
      ev.preventDefault();
      const digits = phone.value.replace(/\D/g, '');
      if (digits.length < 10) { toast('Digite o número com DDI e DDD, ex.: 55 11 99999-9999.', 'error'); phone.focus(); return; }
      codeBtn.disabled = true;
      try { showCode((await api.post(`${base}/pair-phone`, { phone: digits })).code); } catch (e) { toast(errMsg(e), 'error'); } finally { codeBtn.disabled = false; }
    },
  },
  h('p', { class: 'pane-note' }, 'Sem câmera por perto? Receba um código de 8 caracteres e digite no celular.'),
  field('Número do WhatsApp (com DDI e DDD)', phone),
  h('div', { class: 'row' }, codeBtn),
  codeOut);

  // --- pairing link ---
  const ttl = h('select', { 'aria-label': 'Validade do link' },
    h('option', { value: '600' }, 'Vale 10 minutos'),
    h('option', { value: '300' }, 'Vale 5 minutos'),
    h('option', { value: '120' }, 'Vale 2 minutos'));
  const linkOut = h('div', { class: 'link-out', hidden: true });
  const genLink = h('button', { class: 'btn primary', type: 'button' }, icon('link', 16), 'Gerar link');
  genLink.addEventListener('click', async () => {
    genLink.disabled = true;
    try {
      const link = await api.post(`${base}/pairing-links`, { ttl_seconds: Number(ttl.value) });
      mount(linkOut,
        h('code', { class: 'link-url' }, link.url),
        h('div', { class: 'row' }, copyButton(link.url, 'Copiar link', 'btn small')),
        h('p', { class: 'pane-note' }, `Vale até ${fmtTime(link.expires_at)} ou até o primeiro pareamento. Quem tiver o link pode conectar esta instância: envie só para quem vai parear.`));
      linkOut.hidden = false;
    } catch (e) {
      toast(e.status === 404 ? 'Este servidor não oferece link de pareamento.' : errMsg(e), 'error');
    } finally { genLink.disabled = false; }
  });
  const revoke = h('button', { class: 'btn ghost small', type: 'button' }, 'Revogar links');
  revoke.addEventListener('click', async () => {
    revoke.disabled = true;
    try {
      const r = await api.del(`${base}/pairing-links`);
      clear(linkOut); linkOut.hidden = true;
      toast(`Links revogados: ${r && r.revoked !== undefined ? r.revoked : 0}`);
    } catch (e) { toast(errMsg(e), 'error'); } finally { revoke.disabled = false; }
  });
  const linkPane = h('div', { class: 'connect-main' },
    h('p', { class: 'pane-note' }, 'Gere uma página com o QR e o código para enviar a quem está com o celular. O token não é exposto.'),
    h('div', { class: 'row' }, ttl, genLink, revoke),
    linkOut);

  // --- layout ---
  const panes = { qr: qrPane, code: codePane, link: linkPane };
  const tabs = [['qr', 'QR code', 'qr'], ['code', 'Código', 'phone'], ['link', 'Link', 'link']];
  const seg = h('div', { class: 'seg', role: 'tablist', 'aria-label': 'Forma de conectar' });
  const paneBox = h('div', { role: 'tabpanel' });
  const setMode = (m, focus) => {
    mode = m;
    mount(seg, tabs.map(([k, label, ic]) => {
      const b = h('button', { type: 'button', role: 'tab', 'aria-selected': k === m ? 'true' : 'false', tabindex: k === m ? '0' : '-1' }, icon(ic, 15), label);
      b.addEventListener('click', () => setMode(k, true));
      b.addEventListener('keydown', (e) => {
        const i = tabs.findIndex(([x]) => x === k);
        if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
          e.preventDefault();
          setMode(tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length][0], true);
        }
      });
      return b;
    }));
    mount(paneBox, panes[m]);
    if (focus) { const sel = seg.querySelector('[aria-selected="true"]'); if (sel) sel.focus(); }
  };
  setMode('qr');

  const statusSlot = h('div');
  const how = h('div', { class: 'how' },
    h('h3', null, 'Conecte pelo celular'),
    h('ol', { class: 'steps-list' },
      h('li', null, h('span', null, 'Abra o ', h('b', null, 'WhatsApp'), ' no celular do número que vai usar.')),
      h('li', null, h('span', null, 'Toque em ', h('span', { class: 'path' }, 'Aparelhos conectados > Conectar um aparelho'), '.')),
      h('li', null, h('span', null, 'Aponte a câmera para o QR. Prefere digitar? Use a aba ', h('b', null, 'Código'), '.'))),
    statusSlot,
    compact ? null : h('p', { class: 'pane-note' }, 'O QR muda sozinho a cada ~20 segundos. Pode fechar esta janela: a conexão continua e você retoma pelo card da instância.'));

  const el = h('div', { class: 'connect' }, h('div', { class: 'connect-main' }, seg, paneBox), how);

  // --- state tracking ---
  const start = async () => {
    inst = await api.post(`${base}/start`);
    apply(inst);
  };
  let lastState = undefined;
  const apply = (v) => {
    inst = v;
    mount(statusSlot, statusBox(inst, start));
    const st = inst ? inst.state : null;
    if (st !== lastState) {
      lastState = st;
      if (st === 'qr_pending') loadQR();
      else {
        timer.hidden = true;
        const d = describe(inst);
        overlay(h('div', null, d.tone === 'err' ? icon('alert', 28) : spinner(), h('div', null, d.title)));
      }
    }
    onState(inst);
  };
  const refresh = async () => {
    try { apply(await api.get(base)); } catch (e) { if (e.status === 404) { alive = false; toast('Instância não encontrada', 'error'); } }
  };

  const stop = api.subscribe(`${base}/events?types=instance.status,instance.qr,instance.pair_code`, (type, ev) => {
    const d = ev.data || {};
    if (type === 'instance.status') {
      // The phone appears after pairing, so refetch instead of patching.
      refresh();
    } else if (type === 'instance.qr') {
      if (inst && inst.state !== 'qr_pending') inst = { ...inst, state: 'qr_pending' };
      loadQR(d.timeout_ms ? Number(d.timeout_ms) : 20000);
    } else if (type === 'instance.pair_code' && d.code) {
      showCode(d.code);
    }
  }, (s) => {
    const was = sseOpen;
    sseOpen = s === 'open';
    if (sseOpen && !was) refresh(); // anything missed while the stream was down
  });

  const tick = setInterval(tickTimer, 1000);
  // Fallback polling: only when the live stream is not connected.
  const poll = setInterval(() => { if (!sseOpen && alive && !document.hidden) refresh(); }, 3000);
  refresh();

  return {
    el,
    inst: () => inst,
    cleanup() {
      alive = false;
      stop();
      clearInterval(tick);
      clearInterval(poll);
      if (objectURL) URL.revokeObjectURL(objectURL);
    },
  };
}

// ---------- wizard ----------

const STEPS = ['Nova instância', 'Conectar o WhatsApp', 'Pronto'];

// openWizard opens the guided flow. Without inst it starts at "Nova instância"; with an
// instance it resumes at the right step. opts.aiClients shows the "Conectar a IA" action.
export function openWizard({ inst = null, aiClients = false, onChange = () => {} } = {}) {
  let step = !inst ? 0 : inst.state === 'connected' ? 2 : 1;
  let current = inst;
  let cleanupStep = null;
  const title = h('h2', null, '');
  const stepper = h('ol', { class: 'stepper', 'aria-label': 'Etapas' });
  const body = h('div', { class: 'wiz-body' });
  const foot = h('div', { class: 'wiz-foot' });

  const close = () => {
    if (cleanupStep) { cleanupStep(); cleanupStep = null; }
    shell.close();
    onChange(current);
  };
  const shell = dialog({ label: 'Nova instância', wide: true, onClose: close });
  shell.dlg.classList.add('wizard');
  mount(shell.dlg,
    h('div', { class: 'wiz-head' }, title,
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Fechar', title: 'Fechar (Esc)', onclick: close }, icon('x'))),
    stepper, body, foot);

  const go = (n) => {
    if (cleanupStep) { cleanupStep(); cleanupStep = null; }
    step = n;
    title.textContent = current ? (n === 2 ? 'Tudo pronto' : current.name) : 'Nova instância';
    mount(stepper, STEPS.map((label, i) => h('li', {
      class: i < n ? 'done' : i === n ? 'current' : '',
      'aria-current': i === n ? 'step' : null,
    }, h('span', { class: 'n' }, i < n ? icon('check', 14) : String(i + 1)), label)));
    const make = [createStep, connectStep, doneStep][n];
    cleanupStep = make() || null;
  };

  // --- 1. create ---
  function createStep() {
    const name = h('input', { name: 'name', maxlength: 64, required: true, autocomplete: 'off', placeholder: 'ex.: Vendas, Suporte, Loja SP' });
    const proxy = h('input', { class: 'mono', autocomplete: 'off', spellcheck: 'false', placeholder: 'socks5://usuario:senha@host:porta' });
    const btn = h('button', { class: 'btn primary', type: 'submit', form: 'wiz-create' }, 'Criar instância', icon('next', 16));
    const form = h('form', {
      id: 'wiz-create', class: 'wiz-create',
      onsubmit: async (ev) => {
        ev.preventDefault();
        const n = name.value.trim();
        if (!n) { name.focus(); return; }
        const px = proxy.value.trim();
        if (px && !/^(socks5|http|https):\/\//i.test(px)) { toast('O proxy precisa começar com socks5://, http:// ou https://', 'error'); proxy.focus(); return; }
        btn.disabled = true;
        name.disabled = proxy.disabled = true;
        const prog = progress(['Criando a instância', px ? 'Aplicando o proxy' : null, 'Iniciando a conexão'].filter(Boolean));
        form.appendChild(prog.el);
        try {
          const res = await api.post('/v1/instances', { name: n });
          current = res.instance;
          tokens.set(current.id, res.token);
          prog.next();
          if (px) {
            await api.put(`/v1/instances/${enc(current.id)}/settings`, { proxy_url: px });
            prog.next();
            if (['stopped', 'failed'].includes(current.state)) current = await api.post(`/v1/instances/${enc(current.id)}/start`);
          }
          prog.next();
          onChange(current);
          go(1);
        } catch (e) {
          prog.el.remove();
          toast(errMsg(e), 'error');
          btn.disabled = false;
          name.disabled = proxy.disabled = false;
          // Created but a later step failed: continue from the connect step anyway.
          if (current) go(1);
        }
      },
    },
    h('div', { class: 'wiz-intro' },
      h('div', { class: 'empty-icon' }, icon('phone', 22)),
      h('div', null, h('b', null, 'Cada instância é um número de WhatsApp.'),
        h('p', null, 'Dê um nome para reconhecer depois. No próximo passo você conecta o celular.'))),
    field('Nome da instância', name),
    h('details', { class: 'adv' }, h('summary', null, 'Avançado'),
      field('Proxy (opcional)', proxy, 'Use se este número precisa sair por um IP específico. Vazio usa o padrão do servidor.')));
    mount(body, form);
    mount(foot, h('button', { class: 'btn ghost', type: 'button', onclick: close }, 'Cancelar'), h('span', { class: 'grow' }), btn);
    name.focus();
    return null;
  }

  // --- 2. connect ---
  function connectStep() {
    const panel = connectPanel(current.id, {
      onState: (v) => {
        if (!v) return;
        current = v;
        if (v.state === 'connected') setTimeout(() => { if (step === 1) { onChange(current); go(2); } }, 600);
      },
    });
    mount(body, panel.el);
    mount(foot,
      h('button', { class: 'btn ghost', type: 'button', onclick: close }, 'Continuar depois'),
      h('span', { class: 'grow' }),
      h('span', { class: 'small muted' }, 'Avança sozinho quando conectar'));
    const first = body.querySelector('[role="tab"]');
    if (first) first.focus();
    return panel.cleanup;
  }

  // --- 3. done ---
  function doneStep() {
    const id = current.id;
    const tokenSlot = h('div');
    const renderToken = () => {
      const tok = tokens.get(id);
      if (tok) {
        mount(tokenSlot, secretBox(tok, 'Este é o token da instância para a sua integração. Guarde em local seguro: ele não aparece de novo depois que você recarregar o painel.'));
        return;
      }
      const rot = h('button', { class: 'btn small', type: 'button' }, icon('key', 15), 'Gerar novo token');
      rot.addEventListener('click', async () => {
        if (!(await confirmDanger('Gerar um novo token invalida o anterior. Integrações que usam o token antigo param de funcionar. Continuar?', { ok: 'Gerar novo token' }))) return;
        rot.disabled = true;
        try {
          const r = await api.post(`/v1/instances/${enc(id)}/token/rotate`);
          tokens.set(id, r.token);
          renderToken();
          toast('Novo token gerado');
        } catch (e) { toast(errMsg(e), 'error'); rot.disabled = false; }
      });
      mount(tokenSlot, h('div', { class: 'stack' },
        h('p', { class: 'small muted' }, 'Por segurança, o token só aparece quando é criado. Se você não guardou, gere um novo.'),
        h('div', null, rot)));
    };
    renderToken();

    const testBox = h('div', { hidden: true });
    const quickTest = h('button', { type: 'button' },
      h('span', { class: 'q-icon' }, icon('send', 16)), h('b', null, 'Mensagem de teste'), h('small', null, 'Envie um "oi" para confirmar que está tudo certo.'));
    quickTest.addEventListener('click', () => {
      if (!testBox.hidden) { testBox.hidden = true; return; }
      const to = h('input', { inputmode: 'tel', placeholder: '55 11 99999-9999', autocomplete: 'off' });
      const txt = h('textarea', { rows: 2 }, 'Oi! Mensagem de teste do LiteZap.');
      const send = h('button', { class: 'btn primary small', type: 'submit' }, icon('send', 14), 'Enviar');
      mount(testBox, h('form', {
        class: 'test-form',
        onsubmit: async (ev) => {
          ev.preventDefault();
          const digits = to.value.replace(/\D/g, '');
          if (digits.length < 10) { toast('Digite o número com DDI e DDD.', 'error'); to.focus(); return; }
          send.disabled = true;
          try {
            const r = await api.post(`/v1/instances/${enc(id)}/messages`, { to: digits, type: 'text', text: txt.value, client_message_id: uuid() });
            toast(r.status === 'queued' ? 'Mensagem na fila: sai em instantes (o LiteZap respeita um ritmo seguro).' : 'Mensagem enviada');
          } catch (e) { toast(errMsg(e), 'error'); } finally { send.disabled = false; }
        },
      }, field('Para (com DDI e DDD)', to), field('Mensagem', txt), h('div', { class: 'row end' }, send)));
      testBox.hidden = false;
      to.focus();
    });
    const nav = (href) => () => { close(); location.hash = href; };
    const quick = h('div', { class: 'quick' },
      quickTest,
      h('button', { type: 'button', onclick: nav(`#/i/${enc(id)}/webhooks`) },
        h('span', { class: 'q-icon' }, icon('webhook', 16)), h('b', null, 'Configurar webhook'), h('small', null, 'Receba mensagens e eventos no seu sistema.')),
      aiClients
        ? h('button', { type: 'button', onclick: nav('#/ia') },
          h('span', { class: 'q-icon' }, icon('sparkles', 16)), h('b', null, 'Conectar a IA'), h('small', null, 'Use este WhatsApp pelo Claude ou Cursor.'))
        : h('button', { type: 'button', onclick: nav(`#/i/${enc(id)}/chats`) },
          h('span', { class: 'q-icon' }, icon('chat', 16)), h('b', null, 'Ver conversas'), h('small', null, 'Acompanhe e responda pelo painel.')),
      h('a', { href: DOCS_URL, target: '_blank', rel: 'noopener noreferrer' },
        h('span', { class: 'q-icon' }, icon('book', 16)), h('b', null, 'Documentação da API'), h('small', null, 'Exemplos para enviar e receber mensagens.')));

    mount(body,
      h('div', { class: 'done-hero' },
        h('div', { class: 'big-check pop' }, icon('check', 30)),
        h('h3', null, `${current.name} está conectada`),
        h('p', null, current.phone ? `Número ${phoneOf(current.phone)}` : 'Número pareado')),
      h('div', { class: 'done-grid' },
        h('div', null, h('p', { class: 'section-title' }, 'Token da instância'), tokenSlot,
          h('p', { class: 'pane-note' }, 'Use no cabeçalho ', h('code', null, 'Authorization: Bearer …'), ' das chamadas à API desta instância.')),
        h('div', null, h('p', { class: 'section-title' }, 'Próximos passos'), quick, testBox)));
    const doneBtn = h('button', { class: 'btn primary', type: 'button', onclick: () => { close(); location.hash = `#/i/${enc(id)}/chats`; } }, 'Abrir instância');
    mount(foot, h('button', { class: 'btn ghost', type: 'button', onclick: close }, 'Fechar'), h('span', { class: 'grow' }), doneBtn);
    doneBtn.focus();
    return null;
  }

  go(step);
  return { close };
}

// progress renders a checklist that advances with next().
function progress(labels) {
  let i = 0;
  const items = labels.map((l) => h('li', null, h('span', { class: 'p-ic' }), l));
  const render = () => items.forEach((li, k) => {
    li.className = k < i ? 'done' : '';
    mount(li.firstChild, k < i ? icon('check', 16) : k === i ? spinner() : h('span', { class: 'icon', style: null }, '·'));
  });
  render();
  return { el: h('ul', { class: 'progress-list', 'aria-live': 'polite' }, items), next() { i++; render(); } };
}

