// "Atendente IA" tab: agent hook config (spec 4.3), per-chat override, recent calls.
import * as api from './api.js';
import { h, mount, clear, toast, errMsg, modal, secretBox, field, fmtTime, phoneOf, confirmDanger } from './ui.js';

const STATUS_CLASS = { ok: 'd-delivered', failed: 'd-failed' };
const STATUS_LABEL = { ok: 'ok', failed: 'falhou', skipped: 'pulada', discarded: 'descartada', paused: 'pausa (humano)' };

// Go durations ("30s", "1h0m0s") <-> numbers in the given unit.
function durSeconds(d) {
  if (typeof d === 'number') return d;
  let s = 0;
  for (const [, n, u] of String(d || '').matchAll(/([\d.]+)(h|ms|m|s)/g)) {
    s += Number(n) * ({ h: 3600, m: 60, s: 1, ms: 0.001 }[u]);
  }
  return s;
}

export function agentPanel(ctx) {
  const base = `${ctx.base}/agent-hook`;
  const cfgBox = h('div', { class: 'card settings' }, h('p', { class: 'muted' }, 'Carregando…'));
  const chatsBox = h('div', { class: 'card' });
  const callsBody = h('tbody');
  let alive = true;

  // ---------- config ----------
  const renderConfig = (c) => {
    const url = h('input', { type: 'url', class: 'mono', value: c.url, placeholder: 'https://meu-agente.exemplo.com/litezap' });
    const enabled = h('input', { type: 'checkbox', checked: c.enabled });
    const groups = h('input', { type: 'checkbox', checked: c.groups_enabled });
    const timeout = h('input', { type: 'number', min: 1, max: 120, value: durSeconds(c.timeout) });
    const contextN = h('input', { type: 'number', min: 0, max: 100, value: c.context_messages });
    const pause = h('input', { type: 'number', min: 0, max: 10080, value: durSeconds(c.human_pause) / 60 });
    const maxReplies = h('input', { type: 'number', min: 1, max: 1000, value: c.max_replies_per_window });
    const windowMin = h('input', { type: 'number', min: 1, max: 1440, value: durSeconds(c.reply_window) / 60 });
    const ignore = h('textarea', { rows: 3, class: 'mono', placeholder: 'um número ou JID por linha' });
    ignore.value = (c.ignore || []).join('\n');
    const secret = h('input', { class: 'mono', placeholder: c.secret_set ? 'definido (deixe vazio para manter)' : 'vazio = gerado', autocomplete: 'off' });
    const save = h('button', { class: 'btn primary', type: 'submit' }, c.configured ? 'Salvar' : 'Criar hook');
    const del = c.configured ? h('button', {
      class: 'btn danger', type: 'button',
      onclick: async () => {
        if (!confirmDanger('Remover o hook de agente desta instância? O atendente para de responder.')) return;
        try { await api.del(base); toast('Hook removido'); loadConfig(); } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, 'Remover') : null;
    const rotate = c.configured ? h('button', {
      class: 'btn', type: 'button',
      onclick: async () => {
        if (!confirmDanger('Gerar um novo segredo? O agente precisa ser atualizado com ele, senão as assinaturas falham.')) return;
        try {
          // An empty secret makes the server generate one and show it once.
          const v = await api.put(base, { secret: '' });
          showSecret(v.secret);
          renderConfig(v);
        } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, 'Gerar novo segredo') : null;

    const form = h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        const num = (input) => Number(input.value);
        const p = {
          url: url.value.trim(), enabled: enabled.checked, groups_enabled: groups.checked,
          timeout: `${num(timeout)}s`, context_messages: num(contextN), human_pause: `${num(pause)}m`,
          max_replies_per_window: num(maxReplies), reply_window: `${num(windowMin)}m`,
          ignore: ignore.value.split(/[\n,]/).map((s) => s.trim()).filter(Boolean),
        };
        if (secret.value.trim()) p.secret = secret.value.trim();
        save.disabled = true;
        try {
          const v = await api.put(base, p);
          toast('Atendente salvo');
          if (v.secret) showSecret(v.secret);
          renderConfig(v);
        } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('p', { class: 'small muted' },
      'Cada mensagem recebida (fora de grupos, chats ignorados ou pausados) vira um POST assinado ',
      '(X-LiteZap-Signature, igual aos webhooks) para a URL do seu agente; a resposta { reply, actions } ',
      'sai pela fila normal, com ritmo, janela e rampa. Erro, timeout ou resposta inválida não enviam nada.'),
    h('fieldset', null, h('legend', null, 'Agente'),
      field('URL do agente', url, 'Só http(s). Endereços locais (127.0.0.1, rede interna) exigem webhook_allow_private_urls no servidor.'),
      h('div', { class: 'row' },
        h('label', { class: 'check' }, enabled, ' Responder automaticamente (padrão para todas as conversas)'),
        h('label', { class: 'check' }, groups, ' Também em grupos')),
      field('Segredo de assinatura', secret, 'Use o mesmo valor no agente (LITEZAP_AGENT_SECRET no exemplo). 16 a 256 caracteres.')),
    h('fieldset', null, h('legend', null, 'Comportamento'),
      h('div', { class: 'grid3' },
        field('Timeout (s)', timeout, 'Sem resposta nesse prazo: nada é enviado.'),
        field('Mensagens de contexto', contextN, 'Últimas N mensagens do chat enviadas junto.'),
        field('Pausa quando um humano responde (min)', pause, 'Você respondeu pelo celular: o agente para nesse chat. 0 = não pausar.')),
      h('div', { class: 'grid3' },
        field('Máx. respostas por chat', maxReplies, 'Proteção contra loop entre robôs.'),
        field('… em quantos minutos', windowMin, 'Estourou: o chat é pausado e um agent.failed (loop_guard) é emitido.')),
      field('Ignorar (nunca responder)', ignore)),
    h('div', { class: 'row end' }, rotate, del, save));
    mount(cfgBox, form);
  };

  const showSecret = (s) => modal('Segredo do atendente',
    secretBox(s, 'Segredo de assinatura (X-LiteZap-Signature): ele só aparece agora. Configure-o no seu agente.'));

  const loadConfig = async () => {
    try { const c = await api.get(base); if (alive) renderConfig(c); } catch (e) { mount(cfgBox, h('p', { class: 'error' }, errMsg(e))); }
  };

  // ---------- per-chat override ----------
  const setChat = async (jid, enabled) => {
    try {
      await api.put(`${ctx.base}/chats/${encodeURIComponent(jid)}/agent`, { enabled });
      toast(enabled === null ? 'Conversa volta ao padrão (e sai da pausa)' : enabled ? 'Atendente ligado nesta conversa' : 'Atendente desligado nesta conversa');
      loadChats();
    } catch (e) { toast(errMsg(e), 'error'); }
  };

  const loadChats = async () => {
    let list = [];
    let recent = [];
    try {
      [list, recent] = await Promise.all([
        api.get(`${base}/chats`).then((r) => r.chats || []),
        api.get(`${ctx.base}/chats?limit=50`).then((r) => r.chats || []).catch(() => []),
      ]);
    } catch (e) { mount(chatsBox, h('p', { class: 'error' }, errMsg(e))); return; }
    if (!alive) return;
    const pick = h('select', { 'aria-label': 'Conversa' },
      h('option', { value: '' }, 'escolha uma conversa recente…'),
      recent.map((c) => h('option', { value: c.jid }, `${c.name || phoneOf(c.jid)}${c.is_group ? ' (grupo)' : ''}`)));
    const jidIn = h('input', { class: 'mono', placeholder: 'ou digite número/JID' });
    const target = () => jidIn.value.trim() || pick.value;
    const btn = (label, value) => h('button', {
      class: 'btn small', type: 'button',
      onclick: () => { const j = target(); if (!j) { toast('Escolha uma conversa', 'error'); return; } setChat(j, value); },
    }, label);
    const rows = list.map((c) => {
      const state = c.enabled === null || c.enabled === undefined ? 'padrão' : c.enabled ? 'ligado' : 'desligado';
      return h('tr', null,
        h('td', null, c.name || phoneOf(c.jid), h('div', { class: 'small muted mono' }, c.jid)),
        h('td', null, state),
        h('td', { class: 'small' }, c.paused_until ? `pausado até ${fmtTime(c.paused_until)}` : ''),
        h('td', null, h('div', { class: 'actions' },
          h('button', { class: 'btn small', type: 'button', onclick: () => setChat(c.jid, null) }, 'Reativar (padrão)'),
          c.enabled !== true ? h('button', { class: 'btn small', type: 'button', onclick: () => setChat(c.jid, true) }, 'Ligar') : null,
          c.enabled !== false ? h('button', { class: 'btn small danger', type: 'button', onclick: () => setChat(c.jid, false) }, 'Desligar') : null)));
    });
    mount(chatsBox,
      h('h2', null, 'Por conversa'),
      h('p', { class: 'small muted' }, 'Ligue ou desligue o atendente numa conversa específica. "Reativar" volta ao padrão da instância e encerra pausas (após handoff ou resposta humana).'),
      h('div', { class: 'row' }, pick, jidIn, btn('Ligar', true), btn('Desligar', false), btn('Padrão', null)),
      rows.length
        ? h('table', { class: 'table small' },
          h('thead', null, h('tr', null, h('th', null, 'Conversa'), h('th', null, 'Atendente'), h('th', null, 'Pausa'), h('th', null, ''))),
          h('tbody', null, rows))
        : h('p', { class: 'muted small' }, 'Nenhuma conversa com regra própria ou pausada.'));
  };

  // ---------- recent calls ----------
  const loadCalls = async () => {
    try {
      const res = await api.get(`${base}/calls?limit=50`);
      if (!alive) return;
      clear(callsBody);
      for (const c of res.calls || []) {
        callsBody.appendChild(h('tr', null,
          h('td', null, fmtTime(c.created_at)),
          h('td', null, phoneOf(c.chat)),
          h('td', null, h('span', { class: `badge ${STATUS_CLASS[c.status] || 'd-pending'}` }, STATUS_LABEL[c.status] || c.status)),
          h('td', { class: 'small' }, c.reason || '', c.http_status ? ` · HTTP ${c.http_status}` : '', c.duration_ms ? ` · ${c.duration_ms} ms` : ''),
          h('td', { class: 'small' }, c.replied ? 'respondeu' : '', (c.actions || []).length ? ` · ${(c.actions || []).join(', ')}` : ''),
          h('td', { class: 'small muted' }, c.detail || '')));
      }
      if (!callsBody.firstChild) callsBody.appendChild(h('tr', null, h('td', { colspan: 6, class: 'muted small' }, 'Nenhuma chamada ainda.')));
    } catch (e) { toast(errMsg(e), 'error'); }
  };

  // Live refresh: hook outcomes and incoming messages change the call log.
  let timer = null;
  const off = ctx.on((type) => {
    if (!type.startsWith('agent.') && type !== 'message.received' && type !== 'message.sent') return;
    clearTimeout(timer);
    timer = setTimeout(() => { loadCalls(); if (type !== 'message.received') loadChats(); }, 800);
  });

  loadConfig();
  loadChats();
  loadCalls();
  const el = h('div', { class: 'agent' },
    cfgBox,
    chatsBox,
    h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h2', null, 'Chamadas recentes'),
        h('button', { class: 'btn small ghost', type: 'button', onclick: loadCalls }, 'Atualizar')),
      h('div', { class: 'table-card' },
        h('table', { class: 'table small' },
          h('thead', null, h('tr', null, h('th', null, 'Quando'), h('th', null, 'Conversa'), h('th', null, 'Status'),
            h('th', null, 'Motivo'), h('th', null, 'Ações'), h('th', null, 'Detalhe'))),
          callsBody))));
  return { el, cleanup() { alive = false; off(); clearTimeout(timer); } };
}
