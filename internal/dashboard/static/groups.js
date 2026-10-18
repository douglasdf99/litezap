// "Grupos" tab (marco 8): groups the number is in, metadata, participants and invite link.
import * as api from './api.js';
import { h, mount, clear, toast, errMsg, modal, fmtTime, phoneOf, confirmDanger } from './ui.js';

const STATUS_LABEL = {
  ok: 'ok', not_on_whatsapp: 'sem WhatsApp', unknown_contact: 'contato desconhecido', privacy_invite_required: 'só aceita convite',
  recently_left: 'saiu há pouco', already_participant: 'já participa', not_participant: 'não participa', group_full: 'grupo cheio',
};
const EPHEMERAL = { 0: 'desligadas', 86400: '24 horas', 604800: '7 dias', 7776000: '90 dias' };

function resultsText(results) {
  return (results || []).map((r) => `${phoneOf(r.participant)}: ${STATUS_LABEL[r.status] || r.status}`).join('\n');
}

export function groupsPanel(ctx, rest) {
  const g = (jid, ...more) => `${ctx.base}/groups/${encodeURIComponent(jid)}${more.length ? '/' + more.join('/') : ''}`;
  const list = h('ul', { class: 'chat-list' });
  const pane = h('div', { class: 'conv' });
  let current = rest && rest[0] ? rest[0] : null;
  let alive = true;

  const item = (v) => h('li', null, h('a', { href: ctx.href('groups', v.jid), class: current === v.jid ? 'active' : '' },
    h('span', { class: 'chat-name' }, v.name || v.jid, v.community ? h('span', { class: 'tag' }, 'comunidade') : null,
      v.parent ? h('span', { class: 'tag' }, 'na comunidade') : null),
    h('span', { class: 'chat-meta' }, `${v.participant_count || 0} participantes`)));

  const loadList = async () => {
    try {
      const res = await api.get(`${ctx.base}/groups`);
      if (!alive) return;
      const groups = (res.groups || []).sort((a, b) => (a.name || '').localeCompare(b.name || ''));
      mount(list, groups.length ? groups.map(item) : h('li', { class: 'muted small pad' }, 'Este número não está em nenhum grupo.'));
    } catch (e) {
      mount(list, h('li', { class: 'muted small pad' }, e.code === 'instance_not_connected' ? 'Instância desconectada: conecte para ver os grupos.' : errMsg(e)));
    }
  };

  const action = (label, fn, cls = 'btn small') => {
    const b = h('button', { class: cls, type: 'button' }, label);
    b.addEventListener('click', async () => {
      b.disabled = true;
      try { await fn(); } catch (e) { toast(errMsg(e), 'error'); } finally { b.disabled = false; }
    });
    return b;
  };

  const participantsOp = async (jid, act, who) => {
    const res = await api.post(g(jid, 'participants'), { action: act, participants: who });
    const failed = (res.results || []).filter((r) => r.status !== 'ok');
    if (failed.length) modal('Resultado por participante', h('pre', { class: 'mono small' }, resultsText(res.results)));
    else toast('Feito');
    showGroup(jid);
  };

  const showGroup = async (jid) => {
    current = jid;
    for (const a of list.querySelectorAll('a')) a.classList.toggle('active', a.getAttribute('href') === ctx.href('groups', jid));
    mount(pane, h('div', { class: 'conv-empty' }, 'Carregando…'));
    let v;
    try { v = await api.get(g(jid)); } catch (e) { mount(pane, h('div', { class: 'conv-empty' }, errMsg(e))); return; }
    if (!alive || current !== jid) return;
    const s = v.settings || {};
    const invite = h('div', { class: 'row' });
    const showLink = (link) => mount(invite, h('input', { class: 'mono', readonly: true, value: link }),
      action('Copiar', async () => { await navigator.clipboard.writeText(link); toast('Copiado'); }),
      action('Redefinir', async () => {
        if (!confirmDanger('Redefinir o link? O link atual para de funcionar para todo mundo.')) return;
        showLink((await api.post(g(jid, 'invite', 'reset'))).link);
      }));
    mount(invite, action('Mostrar link de convite', async () => showLink((await api.get(g(jid, 'invite'))).link)));

    const rows = (v.participants || []).map((p) => h('tr', null,
      h('td', null, phoneOf(p.jid), p.lid && p.jid !== p.lid ? h('span', { class: 'muted small' }, ` ${p.lid}`) : null),
      h('td', null, p.super_admin ? 'dono' : p.admin ? 'admin' : ''),
      h('td', { class: 'actions' }, h('div', { class: 'row' },
        p.admin ? action('Rebaixar', () => participantsOp(jid, 'demote', [p.jid])) : action('Promover', () => participantsOp(jid, 'promote', [p.jid])),
        p.super_admin ? null : action('Remover', async () => {
          if (!confirmDanger(`Remover ${phoneOf(p.jid)} do grupo? Todos os participantes veem.`)) return;
          await participantsOp(jid, 'remove', [p.jid]);
        }, 'btn small danger')))));

    const addBox = h('input', { placeholder: 'número com DDI, ex.: 5511988887777', class: 'mono' });
    mount(pane,
      h('div', { class: 'conv-head' }, h('strong', null, v.name || v.jid), h('span', { class: 'mono muted small' }, v.jid),
        v.community ? h('span', { class: 'tag' }, 'comunidade') : null),
      h('div', { class: 'pad group-detail' },
        v.description ? h('p', { class: 'small prewrap' }, v.description) : null,
        h('p', { class: 'small muted' },
          `Dono: ${phoneOf(v.owner) || '—'} · criado em ${fmtTime(v.created_at) || '—'} · ${v.participant_count} participantes`),
        h('p', { class: 'small' },
          s.announce ? h('span', { class: 'tag' }, 'só admins enviam') : null,
          s.locked ? h('span', { class: 'tag' }, 'só admins editam') : null,
          s.approval_required ? h('span', { class: 'tag' }, 'entrada com aprovação') : null,
          s.admins_add_members ? h('span', { class: 'tag' }, 'só admins adicionam') : null,
          h('span', { class: 'tag' }, `mensagens temporárias: ${EPHEMERAL[s.ephemeral_seconds] || s.ephemeral_seconds + 's'}`)),
        invite,
        h('h3', null, 'Participantes'),
        h('div', { class: 'table-card' }, h('table', { class: 'table small' },
          h('thead', null, h('tr', null, h('th', null, 'Número'), h('th', null, 'Papel'), h('th', null, ''))), h('tbody', null, rows))),
        h('form', {
          class: 'row group-add',
          onsubmit: async (ev) => {
            ev.preventDefault();
            const who = addBox.value.split(/[\s,;]+/).filter(Boolean);
            if (!who.length) return;
            try { await participantsOp(jid, 'add', who); addBox.value = ''; } catch (e) { toast(errMsg(e), 'error'); }
          },
        }, addBox, h('button', { class: 'btn small', type: 'submit' }, 'Adicionar')),
        h('p', { class: 'small muted' }, 'Adicione só quem já conversa com este número: adicionar desconhecidos é motivo de banimento. O LiteZap pula números sem conversa que não confirmam no WhatsApp e limita as adições por hora e por dia.'),
        h('div', { class: 'row end' }, action('Sair do grupo', async () => {
          if (!confirmDanger('Sair deste grupo? Para voltar, será preciso um convite.')) return;
          await api.post(g(jid, 'leave'));
          toast('Você saiu do grupo');
          location.hash = ctx.href('groups');
          loadList();
        }, 'btn danger'))));
  };

  const createForm = () => {
    const name = h('input', { placeholder: 'Nome do novo grupo', maxlength: 100 });
    const btn = h('button', { class: 'btn small', type: 'submit' }, 'Criar');
    return h('form', {
      class: 'row new-chat',
      onsubmit: async (ev) => {
        ev.preventDefault();
        if (!name.value.trim()) return;
        btn.disabled = true;
        try {
          const res = await api.post(`${ctx.base}/groups`, { name: name.value.trim() });
          toast('Grupo criado');
          name.value = '';
          await loadList();
          location.hash = ctx.href('groups', res.group.jid);
        } catch (e) { toast(errMsg(e), 'error'); } finally { btn.disabled = false; }
      },
    }, name, btn);
  };

  const off = ctx.on((type, ev) => {
    if (type !== 'group.updated') return;
    loadList();
    if (current && ev.data && ev.data.chat === current) showGroup(current);
  });

  const el = h('div', { class: 'chats card' },
    h('aside', { class: 'chat-side' }, createForm(), list),
    pane);
  loadList();
  if (current) showGroup(current);
  else mount(pane, h('div', { class: 'conv-empty' }, 'Escolha um grupo para ver os participantes e o link de convite.'));

  return {
    el,
    update(r) { const jid = r && r[0]; if (jid && jid !== current) showGroup(jid); else if (!jid) { current = null; clear(pane); mount(pane, h('div', { class: 'conv-empty' }, 'Escolha um grupo.')); } },
    cleanup() { alive = false; off(); },
  };
}
