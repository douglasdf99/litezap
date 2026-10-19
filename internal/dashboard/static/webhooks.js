// Webhooks panel: list/create/toggle/delete, deliveries with retry. instanceId null = all
// webhooks (create makes a global one).
import * as api from './api.js';
import { h, mount, clear, toast, errMsg, modal, secretBox, field, fmtTime, confirmDanger, emptyState } from './ui.js';

export function webhooksPanel(instanceId) {
  const listEl = h('div', { class: 'wh-list' });
  let alive = true;

  const load = async () => {
    try {
      const q = instanceId ? `?instance_id=${encodeURIComponent(instanceId)}` : '';
      const res = await api.get(`/v1/webhooks${q}`);
      if (!alive) return;
      const list = res.webhooks || [];
      mount(listEl, list.length ? list.map(card) : h('div', { class: 'card' }, emptyState({ iconName: 'webhook', title: 'Nenhum webhook ainda', text: 'Cadastre uma URL acima para receber mensagens e eventos no seu sistema.' })));
    } catch (e) { mount(listEl, h('p', { class: 'error' }, errMsg(e))); }
  };

  const card = (w) => {
    const enabled = h('input', { type: 'checkbox', checked: w.enabled });
    enabled.addEventListener('change', async () => {
      enabled.disabled = true;
      try { await api.patch(`/v1/webhooks/${encodeURIComponent(w.id)}`, { enabled: enabled.checked }); toast(enabled.checked ? 'Webhook ligado' : 'Webhook desligado'); } catch (e) { enabled.checked = !enabled.checked; toast(errMsg(e), 'error'); } finally { enabled.disabled = false; }
    });
    const deliveries = h('div', { class: 'deliveries', hidden: true });
    const delBtn = h('button', {
      class: 'btn small danger', type: 'button',
      onclick: async () => {
        if (!(await confirmDanger(`Excluir o webhook ${w.url}? Entregas pendentes dele são descartadas.`))) return;
        try { await api.del(`/v1/webhooks/${encodeURIComponent(w.id)}`); toast('Webhook excluído'); load(); } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, 'Excluir');
    return h('div', { class: 'wh card-inner' },
      h('div', { class: 'wh-head' },
        h('div', null,
          h('div', { class: 'mono wh-url' }, w.url),
          h('div', { class: 'small muted' },
            w.instance_id ? `instância ${w.instance_id}` : 'global (todas as instâncias)',
            ' · eventos: ', (w.events || []).join(', ') || '*',
            w.filters ? ` · filtros: ${JSON.stringify(w.filters)}` : '', ' · ', h('span', { class: 'mono' }, w.id))),
        h('div', { class: 'actions' },
          h('label', { class: 'check' }, enabled, ' ligado'),
          h('button', {
            class: 'btn small', type: 'button',
            onclick: () => {
              deliveries.hidden = !deliveries.hidden;
              if (!deliveries.hidden) deliveriesView(w, deliveries);
            },
          }, 'Entregas'),
          delBtn)),
      deliveries);
  };

  const url = h('input', { type: 'url', class: 'mono', placeholder: 'https://exemplo.com/litezap', required: true });
  const evs = h('input', { value: '*', placeholder: 'message.*, instance.status' });
  const secret = h('input', { class: 'mono', placeholder: 'opcional (16–256 caracteres); vazio = gerado', autocomplete: 'off' });
  const createBtn = h('button', { class: 'btn primary', type: 'submit' }, 'Criar webhook');
  const form = h('form', {
    class: 'card wh-form',
    onsubmit: async (ev) => {
      ev.preventDefault();
      const body = {
        url: url.value.trim(),
        events: evs.value.split(',').map((s) => s.trim()).filter(Boolean),
      };
      if (instanceId) body.instance_id = instanceId;
      if (secret.value.trim()) body.secret = secret.value.trim();
      createBtn.disabled = true;
      try {
        const w = await api.post('/v1/webhooks', body);
        url.value = ''; secret.value = '';
        load();
        modal('Webhook criado', secretBox(w.secret, 'Segredo de assinatura (X-LiteZap-Signature): ele só aparece agora. Guarde-o no seu receptor.'));
      } catch (e) { toast(errMsg(e), 'error'); } finally { createBtn.disabled = false; }
    },
  },
  h('h2', null, instanceId ? 'Novo webhook desta instância' : 'Novo webhook global'),
  h('div', { class: 'grid3' },
    field('URL', url, 'Só http(s). Endereços locais/privados exigem webhook_allow_private_urls no servidor.'),
    field('Eventos', evs, 'Separados por vírgula: tipos exatos, famílias (message.*) ou *.'),
    field('Segredo', secret)),
  h('div', { class: 'row end' }, createBtn));

  load();
  return { el: h('div', { class: 'webhooks' }, form, listEl), cleanup() { alive = false; } };
}

function deliveriesView(w, box) {
  const status = h('select', { 'aria-label': 'Status' },
    h('option', { value: '' }, 'todas'), h('option', { value: 'pending' }, 'pendentes'),
    h('option', { value: 'delivered' }, 'entregues'), h('option', { value: 'failed' }, 'dead-letter (falhas)'));
  const tbody = h('tbody');
  const more = h('button', { class: 'btn small ghost', type: 'button', hidden: true }, 'Carregar mais');
  let cursor = '';

  const row = (d) => {
    const pre = h('pre', { class: 'json', hidden: true }, JSON.stringify(d.event, null, 2));
    const retry = d.status === 'failed' ? h('button', {
      class: 'btn small', type: 'button',
      onclick: async () => {
        try {
          await api.post(`/v1/webhooks/${encodeURIComponent(w.id)}/deliveries/${encodeURIComponent(d.id)}/retry`);
          toast('Entrega recolocada na fila');
          load(false);
        } catch (e) { toast(errMsg(e), 'error'); }
      },
    }, 'Reenviar') : null;
    return [
      h('tr', null,
        h('td', null, fmtTime(d.created_at)),
        h('td', null, d.event_type),
        h('td', null, h('span', { class: `badge d-${d.status}` }, d.status)),
        h('td', null, String(d.attempts)),
        h('td', { class: 'small' }, d.last_status_code ? `HTTP ${d.last_status_code} ` : '', d.last_error || ''),
        h('td', null, h('div', { class: 'actions' },
          h('button', { class: 'btn small ghost', type: 'button', onclick: () => { pre.hidden = !pre.hidden; } }, 'Evento'),
          retry))),
      h('tr', { class: 'json-row' }, h('td', { colspan: 6 }, pre)),
    ];
  };

  const load = async (append) => {
    try {
      const qs = new URLSearchParams({ limit: '25' });
      if (status.value) qs.set('status', status.value);
      if (append && cursor) qs.set('cursor', cursor);
      const page = await api.get(`/v1/webhooks/${encodeURIComponent(w.id)}/deliveries?${qs}`);
      if (!append) clear(tbody);
      for (const d of page.deliveries || []) for (const r of row(d)) tbody.appendChild(r);
      if (!tbody.firstChild) tbody.appendChild(h('tr', null, h('td', { colspan: 6, class: 'muted small' }, 'Nenhuma entrega.')));
      cursor = page.next_cursor || '';
      more.hidden = !cursor;
    } catch (e) { toast(errMsg(e), 'error'); }
  };
  status.addEventListener('change', () => load(false));
  more.addEventListener('click', () => load(true));
  mount(box,
    h('div', { class: 'row' }, status, h('button', { class: 'btn small ghost', type: 'button', onclick: () => load(false) }, 'Atualizar')),
    h('table', { class: 'table small' },
      h('thead', null, h('tr', null, h('th', null, 'Criada'), h('th', null, 'Evento'), h('th', null, 'Status'), h('th', null, 'Tentativas'), h('th', null, 'Último erro'), h('th', null, ''))),
      tbody),
    more);
  load(false);
}
