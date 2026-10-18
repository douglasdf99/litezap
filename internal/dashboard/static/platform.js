// Marco 11: instance name/tags/IP allowlist editor and the hosted pairing-link generator.
import * as api from './api.js';
import { h, mount, toast, errMsg, modal, field, secretBox, fmtTime } from './ui.js';

const splitList = (s) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

// tagChips renders an instance's tags.
export function tagChips(inst) {
  return (inst.tags || []).map((t) => h('span', { class: 'tag' }, t));
}

// editInstance opens a dialog to rename the instance and set its tags and IP allowlist.
export function editInstance(inst, onSaved) {
  const name = h('input', { value: inst.name || '', maxlength: 100, required: true });
  const tags = h('input', { value: (inst.tags || []).join(', '), placeholder: 'vip, loja-sp' });
  const allow = h('textarea', { rows: 3, placeholder: '203.0.113.7\n10.0.0.0/8', class: 'mono' });
  allow.value = (inst.ip_allowlist || []).join('\n');
  modal('Editar instância', h('div', { class: 'form' },
    field('Nome', name),
    field('Etiquetas', tags, 'Separe por vírgula. Letras minúsculas, dígitos e _ . : - (até 20). Servem para filtrar e para operações em lote.'),
    field('IPs permitidos para o token da instância', allow,
      'Um IP ou CIDR por linha. Vazio: qualquer endereço. Atrás de proxy reverso, o servidor precisa de trusted_proxies.')), [
    { label: 'Cancelar' },
    {
      label: 'Salvar', primary: true,
      onclick: async () => {
        try {
          // Only the fields that changed (tags and ip_allowlist need the admin token).
          const body = {};
          const same = (a, b) => a.join(',') === (b || []).join(',');
          if (name.value.trim() !== inst.name) body.name = name.value.trim();
          if (!same(splitList(tags.value).map((t) => t.toLowerCase()), inst.tags)) body.tags = splitList(tags.value);
          if (!same(splitList(allow.value), inst.ip_allowlist)) body.ip_allowlist = splitList(allow.value);
          if (!Object.keys(body).length) return true;
          const v = await api.patch(`/v1/instances/${encodeURIComponent(inst.id)}`, body);
          toast('Instância atualizada');
          onSaved(v);
        } catch (e) { toast(errMsg(e), 'error'); return false; }
        return true;
      },
    },
  ]);
}

// pairingLinkCard generates a hosted pairing link to send to the customer.
export function pairingLinkCard(ctx) {
  const out = h('div');
  const ttl = h('select', null,
    h('option', { value: '600' }, '10 minutos'),
    h('option', { value: '300' }, '5 minutos'),
    h('option', { value: '120' }, '2 minutos'));
  const gen = h('button', {
    class: 'btn primary', type: 'button',
    onclick: async () => {
      gen.disabled = true;
      try {
        const link = await api.post(`${ctx.base}/pairing-links`, { ttl_seconds: Number(ttl.value) });
        mount(out, secretBox(link.url, `Vale até ${fmtTime(link.expires_at)} ou até o primeiro pareamento. Quem tiver o link pode parear esta instância; envie só para o cliente.`));
      } catch (e) { toast(errMsg(e), 'error'); } finally { gen.disabled = false; }
    },
  }, 'Gerar link');
  const revoke = h('button', {
    class: 'btn danger', type: 'button',
    onclick: async () => {
      revoke.disabled = true;
      try {
        const r = await api.del(`${ctx.base}/pairing-links`);
        mount(out);
        toast(`Links revogados: ${r && r.revoked !== undefined ? r.revoked : 0}`);
      } catch (e) { toast(errMsg(e), 'error'); } finally { revoke.disabled = false; }
    },
  }, 'Revogar links');
  return h('div', { class: 'card pair-link' },
    h('h3', null, 'Link de pareamento para o cliente'),
    h('p', { class: 'small muted' }, 'Uma página hospedada com o QR e o código por telefone, sem expor o token. Pode ser embutida no seu sistema (pairing_frame_ancestors).'),
    h('div', { class: 'row' }, ttl, gen, revoke),
    out);
}
