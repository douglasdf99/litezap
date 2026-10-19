// "Conectar a IA": writes the LiteZap MCP entry into Claude Desktop, Claude Code and
// Cursor on this computer. Only the tray app enables it (GET api/info -> ai_clients).
import * as api from './api.js';
import { h, mount, toast, errMsg, modal } from './ui.js';

const BASE = '/app/api/ai-clients';

function statusBadge(c) {
  if (c.error) return h('span', { class: 'badge st-failed', title: c.error }, 'configuração inválida');
  if (c.connected) return h('span', { class: 'badge st-connected' }, 'conectado');
  if (c.present) return h('span', { class: 'badge st-qr_pending', title: 'A entrada do LiteZap aponta para outro programa' }, 'precisa atualizar');
  if (!c.installed) return h('span', { class: 'badge' }, 'não encontrado');
  return h('span', { class: 'badge' }, 'não conectado');
}

export function aiClientsView({ root, shell }) {
  const list = h('div', { class: 'ai-list' }, h('div', { class: 'skel skel-card' }), h('div', { class: 'skel skel-card' }));

  const act = async (c, action, btn) => {
    btn.disabled = true;
    try {
      const res = await api.post(`${BASE}/${encodeURIComponent(c.id)}/${action}`, {});
      if (action === 'connect') {
        modal(`Conectado ao ${c.name}`, h('div', null,
          h('p', null, h('b', null, res.message)),
          h('p', { class: 'muted' }, 'Depois, peça por exemplo: "liste minhas conversas do WhatsApp" ou "manda um oi pro Fulano".'),
          res.result && res.result.backup
            ? h('p', { class: 'small muted' }, 'Cópia do arquivo original: ', h('code', null, res.result.backup))
            : null));
      } else {
        toast(res.message);
      }
    } catch (e) {
      toast(errMsg(e), 'error');
    } finally {
      btn.disabled = false;
      load();
    }
  };

  const card = (c) => {
    const actions = [];
    if (!c.connected) {
      const b = h('button', { class: 'btn primary', onclick: () => act(c, 'connect', b) }, c.present ? 'Atualizar' : 'Conectar');
      actions.push(b);
    }
    if (c.present) {
      const b = h('button', { class: 'btn danger', onclick: () => act(c, 'disconnect', b) }, 'Desconectar');
      actions.push(b);
    }
    return h('div', { class: 'card ai-card' },
      h('div', { class: 'ai-head' }, h('h3', null, c.name), statusBadge(c)),
      h('p', { class: 'small muted' }, 'Arquivo: ', h('code', null, c.path)),
      c.error ? h('p', { class: 'error small' }, c.error) : null,
      h('div', { class: 'row' }, actions));
  };

  const load = async () => {
    try {
      const res = await api.get(BASE);
      mount(list, (res.clients || []).map(card));
    } catch (e) { toast(errMsg(e), 'error'); }
  };

  load();
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, 'Conectar a IA'),
      h('p', { class: 'lead' }, 'Use o seu WhatsApp pelo Claude ou pelo Cursor (MCP). Um clique adiciona o LiteZap à configuração do programa, ',
        'sem copiar tokens. Os outros servidores configurados não mudam e o arquivo original ganha uma cópia (.litezap.bak).'))),
    h('p', { class: 'secret-note' }, 'O LiteZap precisa estar aberto para a IA usar o WhatsApp. Depois de conectar, reinicie o programa de IA.'),
    list), 'ai'));
  return () => {};
}
