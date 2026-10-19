// Marco 11: instance name/tags/IP allowlist editor (the pairing-link generator lives in
// the connect panel, wizard.js).
import * as api from './api.js';
import { h, toast, errMsg, modal, field } from './ui.js';

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
