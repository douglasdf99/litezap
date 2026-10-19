// "Perfil" tab (marco 9): the account's name, about text and picture, the privacy
// settings, the blocklist and the auto-read option.
import * as api from './api.js';
import { h, mount, toast, errMsg, field, phoneOf, confirmDanger } from './ui.js';

const PRIVACY = [
  ['last_seen', 'Visto por último', ['all', 'contacts', 'contact_blacklist', 'none']],
  ['online', 'Online', ['all', 'match_last_seen']],
  ['profile_photo', 'Foto do perfil', ['all', 'contacts', 'contact_blacklist', 'none']],
  ['about', 'Recado', ['all', 'contacts', 'contact_blacklist', 'none']],
  ['groups_add', 'Quem pode me adicionar em grupos', ['all', 'contacts', 'contact_blacklist', 'none']],
  ['read_receipts', 'Confirmação de leitura', ['all', 'none']],
  ['calls', 'Quem pode me ligar', ['all', 'known']],
  ['messages', 'Quem pode me mandar mensagem', ['all', 'contacts']],
];
const VALUE_LABEL = {
  all: 'Todos', contacts: 'Meus contatos', contact_blacklist: 'Meus contatos, exceto…', none: 'Ninguém',
  match_last_seen: 'Igual ao visto por último', known: 'Contatos e números conhecidos',
};
const TIMERS = [[0, 'Desligado'], [86400, '24 horas'], [604800, '7 dias'], [7776000, '90 dias']];

const fileToBase64 = (file) => new Promise((resolve, reject) => {
  const r = new FileReader();
  r.onload = () => resolve(String(r.result).split(',')[1] || '');
  r.onerror = () => reject(r.error);
  r.readAsDataURL(file);
});

export function profilePanel(ctx) {
  const profileBox = h('div', { class: 'card settings' }, h('p', { class: 'muted' }, 'Carregando…'));
  const privacyBox = h('div', { class: 'card settings' }, h('p', { class: 'muted' }, 'Carregando…'));
  const blockBox = h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Carregando…'));
  const readBox = h('div', { class: 'card settings' });
  let alive = true;

  const notConnected = (e) => (e.code === 'instance_not_connected' ? 'Instância desconectada: conecte para ver e alterar o perfil.' : errMsg(e));

  // ---------- profile ----------
  const renderProfile = (p) => {
    const name = h('input', { value: p.name || '', maxlength: 25 });
    const about = h('textarea', { rows: 2, maxlength: 139 }, p.about || '');
    const file = h('input', { type: 'file', accept: 'image/jpeg,image/png' });
    const save = h('button', { class: 'btn primary', type: 'submit' }, 'Salvar');
    // Through the API (the page never loads WhatsApp's CDN directly).
    const pic = h('div', { class: 'avatar-big empty' }, p.picture ? '…' : 'sem foto');
    if (p.picture) {
      api.get(`${ctx.base}/profile/picture?download=1`, { blob: true })
        .then((b) => pic.replaceWith(h('img', { class: 'avatar-big', alt: 'Foto do perfil', src: URL.createObjectURL(b) })))
        .catch(() => { pic.textContent = 'sem foto'; });
    }
    const upload = h('button', {
      class: 'btn small', type: 'button',
      onclick: async () => {
        const f = file.files && file.files[0];
        if (!f) { toast('Escolha uma imagem JPEG ou PNG'); return; }
        if (f.size > 10 * 1024 * 1024) { toast('A imagem passa de 10 MB', 'error'); return; }
        upload.disabled = true;
        try {
          const v = await api.put(`${ctx.base}/profile/picture`, { base64: await fileToBase64(f) });
          toast('Foto atualizada');
          renderProfile(v);
        } catch (e) { toast(errMsg(e), 'error'); upload.disabled = false; }
      },
    }, 'Enviar foto');
    const removePic = p.picture ? h('button', {
      class: 'btn small danger', type: 'button',
      onclick: async () => {
        if (!(await confirmDanger('Remover a foto do perfil?'))) return;
        try { await api.del(`${ctx.base}/profile/picture`); toast('Foto removida'); loadProfile(); } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, 'Remover foto') : null;
    mount(profileBox, h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        const body = {};
        if (name.value.trim() !== (p.name || '')) body.name = name.value.trim();
        if (about.value !== (p.about || '')) body.about = about.value;
        if (!Object.keys(body).length) { toast('Nada mudou'); return; }
        save.disabled = true;
        try { renderProfile(await api.patch(`${ctx.base}/profile`, body)); toast('Perfil salvo'); } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('h2', null, 'Perfil'),
    h('div', { class: 'profile-head' }, pic,
      h('div', null,
        h('p', null, h('b', null, phoneOf(p.jid))),
        p.business_name ? h('p', { class: 'small' }, 'Empresa: ', p.business_name) : null,
        p.platform ? h('p', { class: 'small muted' }, 'Celular: ', p.platform) : null)),
    h('div', { class: 'row' }, file, upload, removePic),
    h('p', { class: 'small muted' }, 'A foto é recortada no centro e reduzida a 640×640 (JPEG).'),
    field('Nome (aparece para quem não salvou seu número)', name, 'Até 25 caracteres.'),
    field('Recado', about, 'Até 139 caracteres.'),
    h('div', { class: 'row end' }, save)));
  };
  const loadProfile = () => api.get(`${ctx.base}/profile`).then((p) => alive && renderProfile(p))
    .catch((e) => alive && mount(profileBox, h('h2', null, 'Perfil'), h('p', { class: 'muted' }, notConnected(e))));

  // ---------- privacy ----------
  const renderPrivacy = (pv) => {
    const selects = PRIVACY.map(([key, label, values]) => {
      const sel = h('select', { 'aria-label': label }, values.map((v) => h('option', { value: v }, VALUE_LABEL[v] || v)));
      // A value the form does not offer (or none) shows as "(atual)" and is never sent back.
      if (!values.includes(pv[key])) sel.insertBefore(h('option', { value: '' }, pv[key] ? `(atual: ${pv[key]})` : '(atual)'), sel.firstChild);
      sel.value = values.includes(pv[key]) ? pv[key] : '';
      return [key, label, sel, sel.value];
    });
    const timer = h('select', { 'aria-label': 'Mensagens temporárias' }, TIMERS.map(([s, l]) => h('option', { value: String(s) }, l)));
    timer.value = '-';
    const save = h('button', { class: 'btn primary', type: 'submit' }, 'Salvar privacidade');
    mount(privacyBox, h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        const body = {};
        for (const [key, , sel, initial] of selects) if (sel.value !== initial && sel.value !== '') body[key] = sel.value;
        if (timer.value !== '' && timer.value !== '-') body.default_disappearing_seconds = Number(timer.value);
        if (!Object.keys(body).length) { toast('Nada mudou'); return; }
        save.disabled = true;
        try { renderPrivacy(await api.patch(`${ctx.base}/privacy`, body)); toast('Privacidade salva'); } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('h2', null, 'Privacidade'),
    h('p', { class: 'small muted' }, 'Vale para a conta toda (o celular mostra o mesmo). "Exceto…" é editado no celular.'),
    h('div', { class: 'grid2' }, selects.map(([, label, sel]) => field(label, sel))),
    field('Mensagens temporárias em conversas novas', timer, '"(não mudar)" mantém o valor atual (o WhatsApp não informa qual é).'),
    h('div', { class: 'row end' }, save)));
    timer.insertBefore(h('option', { value: '-' }, '(não mudar)'), timer.firstChild);
    timer.value = '-';
  };
  const loadPrivacy = () => api.get(`${ctx.base}/privacy`).then((p) => alive && renderPrivacy(p))
    .catch((e) => alive && mount(privacyBox, h('h2', null, 'Privacidade'), h('p', { class: 'muted' }, notConnected(e))));

  // ---------- blocklist ----------
  const renderBlocked = (list) => {
    const input = h('input', { inputmode: 'tel', placeholder: 'Número com DDI (5511…)', 'aria-label': 'Número a bloquear' });
    mount(blockBox,
      h('h2', null, 'Bloqueados'),
      list.length ? h('ul', { class: 'plain' }, list.map((jid) => h('li', { class: 'row' },
        h('span', { class: 'mono' }, phoneOf(jid)),
        h('button', {
          class: 'btn small', onclick: async () => {
            try { renderBlocked((await api.post(`${ctx.base}/contacts/${encodeURIComponent(jid)}/unblock`)).blocked || []); toast('Desbloqueado'); } catch (e) { toast(errMsg(e), 'error'); }
          },
        }, 'Desbloquear')))) : h('p', { class: 'muted small' }, 'Ninguém bloqueado.'),
      h('form', {
        class: 'row', onsubmit: async (ev) => {
          ev.preventDefault();
          const v = input.value.trim();
          const to = v.includes('@') ? v : v.replace(/\D/g, '');
          if (!to || !(await confirmDanger(`Bloquear ${phoneOf(to)}? O contato não consegue mais te mandar mensagens nem ligar.`))) return;
          try { renderBlocked((await api.post(`${ctx.base}/contacts/${encodeURIComponent(to)}/block`)).blocked || []); toast('Bloqueado'); } catch (e) { toast(errMsg(e), 'error'); }
        },
      }, input, h('button', { class: 'btn small danger', type: 'submit' }, 'Bloquear')));
  };
  const loadBlocked = () => api.get(`${ctx.base}/blocklist`).then((r) => alive && renderBlocked(r.blocked || []))
    .catch((e) => alive && mount(blockBox, h('h2', null, 'Bloqueados'), h('p', { class: 'muted' }, notConnected(e))));

  // ---------- auto read ----------
  const renderRead = (s) => {
    const ar = s.auto_read || {};
    const on = h('input', { type: 'checkbox', checked: !!ar.enabled });
    const groups = h('input', { type: 'checkbox', checked: !!ar.groups });
    const save = h('button', { class: 'btn primary', type: 'submit' }, 'Salvar');
    mount(readBox, h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        save.disabled = true;
        try {
          const next = await api.put(`${ctx.base}/settings`, { auto_read: { enabled: on.checked, groups: groups.checked } });
          ctx.settingsCache = next;
          toast('Leitura automática salva');
          renderRead(next);
        } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('h2', null, 'Leitura automática'),
    h('label', { class: 'check' }, on, ' Marcar mensagens recebidas como lidas automaticamente'),
    h('label', { class: 'check' }, groups, ' Também em grupos'),
    h('p', { class: 'small muted' }, 'Lê depois de alguns segundos, em lote, no máximo uma confirmação por segundo. Com a confirmação de leitura desligada na privacidade, o contato não vê os tiques azuis.'),
    h('div', { class: 'row end' }, save)));
  };
  ctx.settings(true).then((s) => alive && renderRead(s)).catch(() => {});

  loadProfile();
  loadPrivacy();
  loadBlocked();
  const el = h('div', { class: 'profile' }, profileBox, readBox, privacyBox, blockBox);
  return { el, cleanup() { alive = false; } };
}
