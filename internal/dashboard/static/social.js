// Marco 10 tabs: "Status" (stories), "Canais" (channels) and "Etiquetas" (WhatsApp
// Business labels).
import * as api from './api.js';
import { h, mount, toast, errMsg, fmtTime, phoneOf, confirmDanger, field } from './ui.js';

// WhatsApp's label palette (color index 0-19), approximately as the apps show it.
const LABEL_COLORS = ['#ff9485', '#64c4ff', '#ffd429', '#dfaef0', '#99b6c1', '#55ccb3', '#ff9dff', '#d3a91d', '#6d7cce', '#d7e752',
  '#00d0e2', '#ffc5c7', '#93ceac', '#f74848', '#00a0f2', '#83e422', '#ffaf04', '#b5ebff', '#9ba6ff', '#9368cf'];

const STATUS_TEXT = { contacts: 'todos os contatos', blacklist: 'contatos, exceto alguns', whitelist: 'só alguns contatos' };

function button(label, fn, cls = 'btn small') {
  const b = h('button', { class: cls, type: 'button' }, label);
  b.addEventListener('click', async () => {
    b.disabled = true;
    try { await fn(); } catch (e) { toast(errMsg(e), 'error'); } finally { b.disabled = false; }
  });
  return b;
}

function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(',')[1] || '');
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

function disconnected(e) {
  return e.code === 'instance_not_connected' ? 'Instância desconectada: conecte para usar esta aba.' : errMsg(e);
}

// ---- Status ----

export function statusPanel(ctx) {
  let alive = true;
  const list = h('div', { class: 'status-list' });
  const privacy = h('p', { class: 'small muted' });

  const type = h('select', null, h('option', { value: 'text' }, 'Texto'), h('option', { value: 'image' }, 'Imagem'), h('option', { value: 'video' }, 'Vídeo'));
  const text = h('textarea', { rows: 3, maxlength: 700, placeholder: 'Texto do status (ou legenda da mídia)' });
  const color = h('input', { type: 'color', value: '#1e6e4f' });
  const file = h('input', { type: 'file', accept: 'image/*,video/mp4' });
  const send = h('button', { class: 'btn', type: 'submit' }, 'Publicar status');
  const sync = () => {
    const isText = type.value === 'text';
    color.closest('label').hidden = !isText;
    file.closest('label').hidden = isText;
  };
  type.addEventListener('change', sync);

  const form = h('form', {
    class: 'card pad stack',
    onsubmit: async (ev) => {
      ev.preventDefault();
      if (!confirmDanger('Publicar este status? Ele vai para todos os contatos permitidos pela privacidade do status no celular.')) return;
      send.disabled = true;
      try {
        const body = { type: type.value };
        if (type.value === 'text') {
          if (!text.value.trim()) throw new Error('Escreva o texto do status.');
          body.text = text.value;
          body.background_color = color.value;
        } else {
          const f = file.files[0];
          if (!f) throw new Error('Escolha o arquivo.');
          body.media = { base64: await fileToBase64(f), mime: f.type };
          if (text.value.trim()) body.caption = text.value;
        }
        const v = await api.post(`${ctx.base}/status?wait=true&timeout=60s`, body);
        toast(v.status === 'sent' ? 'Status publicado' : 'Status na fila de envio');
        text.value = '';
        file.value = '';
        load();
      } catch (e) { toast(errMsg(e), 'error'); } finally { send.disabled = false; }
    },
  },
  h('h3', null, 'Novo status'),
  h('div', { class: 'row' }, field('Tipo', type), field('Fundo', color), field('Arquivo', file)),
  text,
  h('div', { class: 'row' }, send),
  privacy,
  h('p', { class: 'small muted' }, 'O status passa pela fila de envio (ritmo e janela de horário) e tem limite de 30 por dia por instância.'));

  const card = (m) => {
    const media = m.media && m.media.id ? button('Ver mídia', async () => {
      const blob = await api.get(`${ctx.base}/media/${encodeURIComponent(m.media.id)}`, { blob: true });
      window.open(URL.createObjectURL(blob), '_blank', 'noopener');
    }) : null;
    const reply = h('input', { placeholder: 'Responder…' });
    return h('div', { class: 'card pad status-item' },
      h('div', { class: 'row' },
        h('strong', null, m.from_me ? 'Você' : phoneOf(m.sender)),
        h('span', { class: 'tag' }, m.type),
        h('span', { class: 'muted small' }, fmtTime(m.timestamp)),
        m.revoked_at ? h('span', { class: 'tag' }, 'apagado') : null),
      m.text ? h('p', { class: 'prewrap' }, m.text) : null,
      h('div', { class: 'row' }, media,
        m.from_me && !m.revoked_at && m.status !== 'pending' ? button('Apagar', async () => {
          if (!confirmDanger('Apagar este status para todos?')) return;
          await api.del(`${ctx.base}/status/${encodeURIComponent(m.id)}?wait=true`);
          toast('Status apagado');
          load();
        }, 'btn small danger') : null,
        m.from_me ? null : h('form', {
          class: 'row',
          onsubmit: async (ev) => {
            ev.preventDefault();
            if (!reply.value.trim()) return;
            try {
              await api.post(`${ctx.base}/status/${encodeURIComponent(m.id)}/reply`, { text: reply.value });
              reply.value = '';
              toast('Resposta na fila');
            } catch (e) { toast(errMsg(e), 'error'); }
          },
        }, reply, h('button', { class: 'btn small', type: 'submit' }, 'Enviar'))));
  };

  const load = async () => {
    try {
      const page = await api.get(`${ctx.base}/status?limit=50`);
      if (!alive) return;
      const msgs = page.messages || [];
      mount(list, msgs.length ? msgs.map(card) : h('p', { class: 'muted small pad' },
        'Nenhum status ainda. Os status dos contatos aparecem aqui conforme chegam (com a instância conectada).'));
    } catch (e) { mount(list, h('p', { class: 'muted small pad' }, disconnected(e))); }
  };
  const loadPrivacy = async () => {
    try {
      const p = await api.get(`${ctx.base}/status/privacy`);
      if (alive) privacy.textContent = `Quem vê seus status: ${STATUS_TEXT[p.type] || p.type}${p.list && p.list.length ? ` (${p.list.length})` : ''} — altere no celular.`;
    } catch { privacy.textContent = ''; }
  };

  const off = ctx.on((type) => { if (type === 'status.received') load(); });
  const el = h('div', { class: 'stack' }, form, h('h3', null, 'Status recentes'), list);
  sync();
  load();
  loadPrivacy();
  return { el, cleanup() { alive = false; off(); } };
}

// ---- Canais ----

export function channelsPanel(ctx, rest) {
  let alive = true;
  let current = rest && rest[0] ? rest[0] : null;
  const c = (jid, ...more) => `${ctx.base}/channels/${encodeURIComponent(jid)}${more.length ? '/' + more.join('/') : ''}`;
  const list = h('ul', { class: 'chat-list' });
  const pane = h('div', { class: 'conv' });

  const item = (v) => h('li', null, h('a', { href: ctx.href('channels', v.jid), class: current === v.jid ? 'active' : '' },
    h('span', { class: 'chat-name' }, v.name || v.jid, v.role && v.role !== 'subscriber' ? h('span', { class: 'tag' }, v.role === 'owner' ? 'dono' : v.role) : null),
    h('span', { class: 'chat-meta' }, `${v.subscribers || 0} seguidores${v.muted ? ' · silenciado' : ''}`)));

  const loadList = async () => {
    try {
      const res = await api.get(`${ctx.base}/channels`);
      if (!alive) return;
      const chans = (res.channels || []).sort((a, b) => (a.name || '').localeCompare(b.name || ''));
      mount(list, chans.length ? chans.map(item) : h('li', { class: 'muted small pad' }, 'Nenhum canal seguido.'));
    } catch (e) { mount(list, h('li', { class: 'muted small pad' }, disconnected(e))); }
  };

  const showChannel = async (jid) => {
    current = jid;
    for (const a of list.querySelectorAll('a')) a.classList.toggle('active', a.getAttribute('href') === ctx.href('channels', jid));
    mount(pane, h('div', { class: 'conv-empty' }, 'Carregando…'));
    let v; let posts = [];
    try {
      v = await api.get(c(jid));
      posts = (await api.get(c(jid, 'messages') + '?count=30')).messages || [];
    } catch (e) { mount(pane, h('div', { class: 'conv-empty' }, errMsg(e))); return; }
    if (!alive || current !== jid) return;
    const admin = v.role === 'owner' || v.role === 'admin';
    const following = v.role === 'subscriber' || admin;
    const postText = h('textarea', { rows: 3, placeholder: 'Texto da publicação' });
    const postFile = h('input', { type: 'file', accept: 'image/*,video/mp4' });
    const publish = admin ? h('form', {
      class: 'stack card pad',
      onsubmit: async (ev) => {
        ev.preventDefault();
        const f = postFile.files[0];
        const body = f ? { type: f.type.startsWith('video/') ? 'video' : 'image', media: { base64: await fileToBase64(f), mime: f.type }, caption: postText.value || undefined }
          : { type: 'text', text: postText.value };
        if (!f && !postText.value.trim()) return;
        if (!confirmDanger('Publicar no canal? Todos os seguidores recebem.')) return;
        try {
          await api.post(c(jid, 'messages') + '?wait=true&timeout=60s', body);
          toast('Publicado');
          postText.value = ''; postFile.value = '';
          showChannel(jid);
        } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, h('h3', null, 'Publicar'), postText, h('div', { class: 'row' }, postFile, h('button', { class: 'btn small', type: 'submit' }, 'Publicar'))) : null;

    const postRow = (p) => h('div', { class: 'card pad' },
      h('div', { class: 'row' }, h('span', { class: 'tag' }, p.type), h('span', { class: 'muted small' }, fmtTime(p.timestamp)),
        h('span', { class: 'muted small' }, `${p.views || 0} visualizações`),
        p.reactions ? h('span', { class: 'small' }, Object.entries(p.reactions).map(([k, n]) => `${k} ${n}`).join('  ')) : null),
      p.text ? h('p', { class: 'prewrap small' }, p.text) : null,
      h('div', { class: 'row' }, button('👍', async () => { await api.post(c(jid, 'reactions'), { server_id: p.server_id, emoji: '👍' }); toast('Reação enviada'); }),
        button('Remover reação', async () => { await api.post(c(jid, 'reactions'), { server_id: p.server_id, emoji: '' }); toast('Reação removida'); })));

    mount(pane,
      h('div', { class: 'conv-head' }, h('strong', null, v.name || v.jid), h('span', { class: 'mono muted small' }, v.jid),
        v.verified ? h('span', { class: 'tag' }, 'verificado') : null),
      h('div', { class: 'pad stack' },
        v.description ? h('p', { class: 'small prewrap' }, v.description) : null,
        h('p', { class: 'small muted' }, `${v.subscribers || 0} seguidores · criado em ${fmtTime(v.created_at) || '—'}`),
        v.invite_link ? h('p', { class: 'small mono' }, v.invite_link) : null,
        h('div', { class: 'row' },
          admin ? null : following ? button('Deixar de seguir', async () => {
            if (!confirmDanger('Deixar de seguir este canal?')) return;
            await api.post(c(jid, 'unfollow')); toast('Você deixou de seguir'); loadList(); showChannel(jid);
          }, 'btn small danger') : button('Seguir', async () => { await api.post(c(jid, 'follow')); toast('Seguindo'); loadList(); showChannel(jid); }),
          following ? button(v.muted ? 'Reativar notificações' : 'Silenciar', async () => {
            await api.post(c(jid, v.muted ? 'unmute' : 'mute')); loadList(); showChannel(jid);
          }) : null),
        publish,
        h('h3', null, 'Publicações recentes'),
        posts.length ? h('div', { class: 'stack' }, posts.map(postRow)) : h('p', { class: 'muted small' }, 'Sem publicações.')));
  };

  const findForm = () => {
    const invite = h('input', { placeholder: 'Link de convite do canal' });
    return h('form', {
      class: 'row new-chat',
      onsubmit: async (ev) => {
        ev.preventDefault();
        if (!invite.value.trim()) return;
        try {
          const v = await api.post(`${ctx.base}/channels/preview`, { invite: invite.value.trim() });
          invite.value = '';
          location.hash = ctx.href('channels', v.jid);
        } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, invite, h('button', { class: 'btn small', type: 'submit' }, 'Abrir'));
  };
  const createForm = () => {
    const name = h('input', { placeholder: 'Nome do novo canal', maxlength: 100 });
    return h('form', {
      class: 'row new-chat',
      onsubmit: async (ev) => {
        ev.preventDefault();
        if (!name.value.trim()) return;
        try {
          const v = await api.post(`${ctx.base}/channels`, { name: name.value.trim() });
          toast('Canal criado'); name.value = '';
          await loadList();
          location.hash = ctx.href('channels', v.jid);
        } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, name, h('button', { class: 'btn small', type: 'submit' }, 'Criar'));
  };

  const el = h('div', { class: 'chats card' }, h('aside', { class: 'chat-side' }, findForm(), createForm(), list), pane);
  loadList();
  if (current) showChannel(current);
  else mount(pane, h('div', { class: 'conv-empty' }, 'Escolha um canal, ou abra um pelo link de convite.'));
  return {
    el,
    update(r) { const jid = r && r[0]; if (jid && jid !== current) showChannel(jid); },
    cleanup() { alive = false; },
  };
}

// ---- Etiquetas ----

export function labelsPanel(ctx) {
  let alive = true;
  const body = h('div', { class: 'stack' });

  const swatch = (i) => {
    const el = h('span', { class: 'label-swatch' });
    el.style.backgroundColor = LABEL_COLORS[i] || '#ccc'; // CSSOM: allowed by the CSP (no inline style attribute)
    return el;
  };
  const colorSelect = (value) => h('select', null, LABEL_COLORS.map((_, i) => h('option', { value: String(i), selected: i === value }, `cor ${i}`)));

  const load = async () => {
    let labels;
    try { labels = (await api.get(`${ctx.base}/labels`)).labels || []; } catch (e) {
      if (!alive) return;
      mount(body, h('p', { class: 'muted pad' }, e.code === 'business_account_required'
        ? 'Etiquetas são do WhatsApp Business: este número é uma conta pessoal.' : disconnected(e)));
      return;
    }
    if (!alive) return;
    const name = h('input', { placeholder: 'Nova etiqueta', maxlength: 100 });
    const color = colorSelect(0);
    const rows = labels.map((l) => {
      const chat = h('input', { placeholder: 'número ou JID da conversa', class: 'mono' });
      return h('tr', null,
        h('td', null, swatch(l.color), ' ', l.name, h('span', { class: 'muted small' }, ` #${l.id}`)),
        h('td', { class: 'small' }, (l.chats || []).map((jid) => h('span', { class: 'tag' }, phoneOf(jid),
          button('×', async () => { await api.del(`${ctx.base}/labels/${encodeURIComponent(l.id)}/chats/${encodeURIComponent(jid)}`); load(); }, 'btn tiny')))),
        h('td', { class: 'actions' }, h('form', {
          class: 'row',
          onsubmit: async (ev) => {
            ev.preventDefault();
            if (!chat.value.trim()) return;
            try { await api.put(`${ctx.base}/labels/${encodeURIComponent(l.id)}/chats/${encodeURIComponent(chat.value.trim())}`); load(); } catch (e) { toast(errMsg(e), 'error'); }
          },
        }, chat, h('button', { class: 'btn small', type: 'submit' }, 'Etiquetar')),
        button('Renomear', async () => {
          const n = window.prompt('Novo nome', l.name);
          if (!n) return;
          await api.patch(`${ctx.base}/labels/${encodeURIComponent(l.id)}`, { name: n });
          load();
        }),
        button('Apagar', async () => {
          if (!confirmDanger(`Apagar a etiqueta "${l.name}"? Ela sai de todas as conversas, também no celular.`)) return;
          await api.del(`${ctx.base}/labels/${encodeURIComponent(l.id)}`);
          load();
        }, 'btn small danger')));
    });
    let profile = null;
    try {
      const p = await api.get(`${ctx.base}/business/profile`);
      profile = h('div', { class: 'card pad small' }, h('h3', null, 'Perfil comercial'),
        h('p', null, h('strong', null, p.business_name || phoneOf(p.jid))),
        p.categories && p.categories.length ? h('p', null, p.categories.join(', ')) : null,
        p.address ? h('p', null, p.address) : null, p.email ? h('p', null, p.email) : null,
        p.options && p.options.website ? h('p', null, p.options.website) : null);
    } catch { /* optional */ }
    if (!alive) return;
    mount(body,
      profile,
      h('form', {
        class: 'row card pad',
        onsubmit: async (ev) => {
          ev.preventDefault();
          if (!name.value.trim()) return;
          try { await api.post(`${ctx.base}/labels`, { name: name.value.trim(), color: Number(color.value) }); toast('Etiqueta criada'); load(); } catch (e) { toast(errMsg(e), 'error'); }
        },
      }, name, color, h('button', { class: 'btn small', type: 'submit' }, 'Criar')),
      labels.length ? h('div', { class: 'table-card' }, h('table', { class: 'table small' },
        h('thead', null, h('tr', null, h('th', null, 'Etiqueta'), h('th', null, 'Conversas'), h('th', null, ''))),
        h('tbody', null, rows))) : h('p', { class: 'muted small' }, 'Nenhuma etiqueta sincronizada ainda (as do celular chegam pelo app state logo após o pareamento).'));
  };

  const off = ctx.on((type) => { if (type === 'label.updated') load(); });
  load();
  return { el: body, cleanup() { alive = false; off(); } };
}
