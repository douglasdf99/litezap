// Chat actions in the Conversas tab (marco 9): archive, pin, mute, unread, delete, and the
// contact's picture / about text / block on demand (lookups spend the instance quota).
import * as api from './api.js';
import { h, mount, toast, errMsg, modal, phoneOf, confirmDanger } from './ui.js';

// chatMarks renders the state markers of a chat in the list.
export function chatMarks(c) {
  return [
    c.pinned ? h('span', { class: 'tag', title: 'Fixada' }, 'fixada') : null,
    c.archived ? h('span', { class: 'tag', title: 'Arquivada' }, 'arquivada') : null,
    c.muted ? h('span', { class: 'tag', title: c.muted_until ? `Silenciada até ${new Date(c.muted_until).toLocaleString('pt-BR')}` : 'Silenciada' }, 'silenciada') : null,
    c.unread ? h('span', { class: 'tag', title: 'Marcada como não lida' }, 'não lida') : null,
  ];
}

const MUTE_OPTIONS = [['8 horas', 8 * 3600], ['1 semana', 7 * 86400], ['Sempre', 0]];

// chatActions returns the action bar of an open chat. onChange(chatView) is called after
// a change; onDeleted() after the chat was deleted.
export function chatActions(ctx, jid, { onChange, onDeleted }) {
  const bar = h('div', { class: 'chat-actions row' });
  const path = `${ctx.base}/chats/${encodeURIComponent(jid)}`;
  const isGroup = jid.endsWith('@g.us');
  let chat = null;

  const patch = async (body, okMsg) => {
    try {
      chat = await api.patch(path, body);
      toast(okMsg);
      render();
      if (onChange) onChange(chat);
    } catch (e) {
      toast(e.code === 'app_state_not_synced'
        ? 'O celular ainda não compartilhou as chaves das conversas. Deixe-o online e tente de novo em instantes.'
        : errMsg(e), 'error');
    }
  };

  const btn = (label, fn, cls = 'btn small') => {
    const b = h('button', { class: cls, type: 'button' }, label);
    b.addEventListener('click', async () => { b.disabled = true; try { await fn(); } finally { b.disabled = false; } });
    return b;
  };

  const muteMenu = () => {
    const sel = h('select', { 'aria-label': 'Silenciar por' }, MUTE_OPTIONS.map(([l, s]) => h('option', { value: String(s) }, l)));
    modal('Silenciar conversa', h('div', null,
      h('p', { class: 'small muted' }, 'As notificações desta conversa param no celular e nos outros aparelhos da conta.'), sel), [
      { label: 'Cancelar' },
      { label: 'Silenciar', primary: true, onclick: () => patch(Number(sel.value) ? { muted: true, mute_seconds: Number(sel.value) } : { muted: true }, 'Conversa silenciada') },
    ]);
  };

  const contactInfo = async () => {
    const body = h('div', null, h('p', { class: 'muted' }, 'Consultando o WhatsApp…'));
    modal(isGroup ? 'Grupo' : 'Contato', body);
    const rows = [h('p', { class: 'mono small' }, jid)];
    try {
      const blob = await api.get(`${ctx.base}/contacts/${encodeURIComponent(jid)}/picture?download=1`, { blob: true });
      rows.unshift(h('img', { class: 'avatar-big', alt: 'Foto de perfil', src: URL.createObjectURL(blob) }));
    } catch (e) {
      rows.push(h('p', { class: 'small muted' }, e.status === 404 ? 'Sem foto (ou escondida pela privacidade do contato).' : errMsg(e)));
    }
    if (!isGroup) {
      try {
        const p = await api.get(`${ctx.base}/contacts/${encodeURIComponent(jid)}/profile`);
        if (p.verified_name) rows.push(h('p', null, h('b', null, 'Empresa: '), p.verified_name));
        rows.push(h('p', null, h('b', null, 'Recado: '), p.about || '—'));
        if (p.business) {
          if (p.business.email) rows.push(h('p', { class: 'small' }, 'E-mail: ', p.business.email));
          if (p.business.address) rows.push(h('p', { class: 'small' }, 'Endereço: ', p.business.address));
          if ((p.business.categories || []).length) rows.push(h('p', { class: 'small' }, 'Categorias: ', p.business.categories.join(', ')));
        }
      } catch (e) { rows.push(h('p', { class: 'small error' }, errMsg(e))); }
      rows.push(h('p', { class: 'small muted' }, 'Cada consulta gasta a cota de consultas da instância (30 por minuto).'));
    }
    mount(body, rows);
  };

  const block = async (on) => {
    if (on && !(await confirmDanger(`Bloquear ${phoneOf(jid)}? O contato não consegue mais te mandar mensagens nem ligar (vale também no celular).`))) return;
    try {
      await api.post(`${ctx.base}/contacts/${encodeURIComponent(jid)}/${on ? 'block' : 'unblock'}`);
      toast(on ? 'Contato bloqueado' : 'Contato desbloqueado');
    } catch (e) { toast(errMsg(e), 'error'); }
  };

  const remove = async () => {
    if (!(await confirmDanger('Apagar esta conversa no WhatsApp (celular e aparelhos vinculados)? O histórico continua no LiteZap.'))) return;
    try {
      await api.del(path);
      toast('Conversa apagada');
      if (onDeleted) onDeleted();
    } catch (e) { toast(errMsg(e), 'error'); }
  };

  function render() {
    if (!chat) { mount(bar); return; }
    mount(bar,
      btn(chat.archived ? 'Desarquivar' : 'Arquivar', () => patch({ archived: !chat.archived }, chat.archived ? 'Desarquivada' : 'Arquivada')),
      btn(chat.pinned ? 'Desafixar' : 'Fixar', () => patch({ pinned: !chat.pinned }, chat.pinned ? 'Desafixada' : 'Fixada')),
      chat.muted ? btn('Reativar som', () => patch({ muted: false }, 'Som reativado')) : btn('Silenciar…', muteMenu),
      btn(chat.unread ? 'Marcar como lida' : 'Marcar como não lida', () => patch({ unread: !chat.unread }, chat.unread ? 'Marcada como lida' : 'Marcada como não lida')),
      btn(isGroup ? 'Foto do grupo' : 'Perfil', contactInfo, 'btn small ghost'),
      isGroup ? null : btn('Bloquear', () => block(true), 'btn small ghost danger'),
      isGroup ? null : btn('Desbloquear', () => block(false), 'btn small ghost'),
      btn('Apagar', remove, 'btn small danger'));
  }

  const load = async () => {
    try { chat = await api.get(path); render(); } catch { mount(bar); /* chat not in the log yet */ }
  };
  load();
  return { el: bar, reload: load, set(c) { chat = c; render(); } };
}
