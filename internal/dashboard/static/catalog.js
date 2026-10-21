// "Catálogo" tab (marco 12, catalog phase 1): the WhatsApp Business catalog of the number
// (or of another business), read only; send a product to a chat; recent orders. Shown
// only for Business accounts. Images load through the API (and the instance's proxy) as
// blobs: the CSP allows no external images, and the browser never talks to Meta's CDN.
import * as api from './api.js';
import { h, mount, toast, errMsg, field, emptyState, modal, copyButton, fmtTime, phoneOf, skeleton, icon } from './ui.js';

const REVIEW = { pending: ['Em análise', 'tone-warn'], rejected: ['Rejeitado', 'tone-danger'] };

// money formats a price in thousandths of the unit (WhatsApp's price/1000 convention).
export function money(amount1000, currency) {
  if (!amount1000) return '';
  const cur = currency || 'BRL';
  try {
    return new Intl.NumberFormat('pt-BR', { style: 'currency', currency: cur }).format(amount1000 / 1000);
  } catch {
    return `${cur} ${(amount1000 / 1000).toFixed(2)}`;
  }
}

// A tiny pool: at most 3 image downloads at once (the API limits image reads per minute).
function imageLoader() {
  const urls = new Map();
  const queue = [];
  let active = 0;
  const next = () => {
    while (active < 3 && queue.length) {
      const { path, el } = queue.shift();
      active++;
      api.get(path, { blob: true })
        .then((b) => { const u = URL.createObjectURL(b); urls.set(path, u); if (el.isConnected) el.src = u; })
        .catch(() => { el.classList.add('cat-img-missing'); })
        .finally(() => { active--; next(); });
    }
  };
  return {
    load(path, el) {
      if (urls.has(path)) { el.src = urls.get(path); return; }
      queue.push({ path, el });
      next();
    },
    clear() { for (const u of urls.values()) URL.revokeObjectURL(u); urls.clear(); queue.length = 0; },
  };
}

function sendDialog(ctx, p, owner) {
  const to = h('input', { placeholder: '5511999999999', inputmode: 'tel', 'aria-label': 'Para' });
  const body = h('textarea', { rows: 3, maxlength: 1024, placeholder: 'Texto opcional abaixo do produto' });
  const footer = h('input', { maxlength: 60, placeholder: 'Rodapé opcional' });
  modal(`Enviar "${p.name}"`, h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'Vai como cartão de produto (foto, nome, preço e botão "Ver"), pela fila de envio com as mesmas pausas anti-ban das outras mensagens.'),
    field('Para', to, 'Número com DDI e DDD, ou o JID da conversa.'),
    field('Texto', body),
    field('Rodapé', footer)), [
    { label: 'Cancelar' },
    {
      label: 'Enviar', primary: true, onclick: async () => {
        if (!to.value.trim()) { toast('Informe o destinatário', 'error'); return false; }
        const msg = { type: 'product', to: to.value.trim(), product_id: p.id };
        if (owner) msg.catalog_owner = owner;
        if (body.value.trim()) msg.body = body.value.trim();
        if (footer.value.trim()) msg.footer = footer.value.trim();
        try {
          const v = await api.post(`${ctx.base}/messages`, msg);
          toast(v.status === 'sent' ? 'Produto enviado' : 'Produto na fila de envio');
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ]);
  to.focus();
}

function productCard(ctx, p, owner, images) {
  const img = h('img', { class: 'cat-img', alt: p.name, loading: 'lazy' });
  if (p.image_path) images.load(p.image_path, img); else img.classList.add('cat-img-missing');
  const badges = [];
  if (p.hidden) badges.push(h('span', { class: 'badge tone-warn' }, 'Oculto'));
  const rv = REVIEW[p.review_status];
  if (rv) badges.push(h('span', { class: `badge ${rv[1]}` }, rv[0]));
  if (p.availability === 'out_of_stock') badges.push(h('span', { class: 'badge tone-danger' }, 'Esgotado'));
  const price = money(p.price_amount_1000, p.currency);
  const sale = p.sale_price_amount_1000 && p.sale_price_amount_1000 < p.price_amount_1000 ? money(p.sale_price_amount_1000, p.currency) : '';
  const send = h('button', { class: 'btn small', type: 'button', onclick: () => sendDialog(ctx, p, owner) }, icon('send', 14), 'Enviar para…');
  if (p.hidden || !p.image_path) {
    send.disabled = true;
    send.title = p.hidden ? 'Produto oculto: o cliente não conseguiria abrir' : 'Produto sem foto';
  }
  return h('article', { class: 'cat-card' },
    img,
    h('div', { class: 'cat-info' },
      h('h3', { class: 'cat-name' }, p.name),
      h('p', { class: 'cat-price' }, sale ? [h('s', { class: 'muted' }, price), ' ', sale] : (price || h('span', { class: 'muted' }, 'Sem preço'))),
      badges.length ? h('div', { class: 'row' }, badges) : null,
      p.retailer_id ? h('p', { class: 'small muted mono' }, p.retailer_id) : null,
      h('div', { class: 'row end' }, send)));
}

function emptyCatalog(link) {
  return emptyState({
    iconName: 'inbox',
    title: 'Catálogo vazio',
    text: 'Crie os produtos no app WhatsApp Business do celular, em Ferramentas comerciais > Catálogo. ' +
      'Eles aparecem aqui em alguns minutos (o WhatsApp revisa cada produto novo). ' +
      'O LiteZap ainda não cria nem edita produtos pela API: no WhatsApp, isso exige um fluxo não documentado com token de conta de anúncios, ' +
      'que pode restringir o número. Por isso, por enquanto, só leitura e envio.',
    action: link ? h('div', { class: 'row' }, copyButton(link, 'Copiar link do catálogo', 'btn small')) : null,
  });
}

export function catalogPanel(ctx) {
  let alive = true;
  const images = imageLoader();
  const ownerInput = h('input', { placeholder: 'Outro número (opcional)', inputmode: 'tel', 'aria-label': 'Ver o catálogo de outro número' });
  const head = h('div', { class: 'row card pad' });
  const filters = h('div', { class: 'row cat-filters' });
  const grid = h('div', { class: 'cat-grid' });
  const more = h('div', { class: 'row' });
  const orders = h('div', { class: 'card pad' });
  let owner = '';
  let products = [];
  let collections = [];
  let filter = null; // collection id
  let cursor = '';

  const draw = () => {
    const ids = filter ? new Set((collections.find((c) => c.id === filter) || { products: [] }).products.map((p) => p.id)) : null;
    const list = ids ? products.filter((p) => ids.has(p.id)) : products;
    mount(grid, list.map((p) => productCard(ctx, p, owner, images)));
    mount(filters, collections.length ? [
      h('button', { class: `chip${filter ? '' : ' active'}`, type: 'button', onclick: () => { filter = null; draw(); } }, 'Todos'),
      ...collections.map((c) => h('button', {
        class: `chip${filter === c.id ? ' active' : ''}`, type: 'button', onclick: () => { filter = c.id; draw(); },
      }, c.name, c.review_status && c.review_status !== 'approved' ? h('span', { class: 'muted small' }, ' (em análise)') : null)),
    ] : []);
  };

  const loadPage = async (first) => {
    const q = new URLSearchParams();
    if (owner) q.set('jid', owner);
    if (!first && cursor) q.set('cursor', cursor);
    const v = await api.get(`${ctx.base}/catalog?${q}`);
    if (!alive) return null;
    products = first ? v.products : products.concat(v.products);
    cursor = v.next_cursor || '';
    mount(more, cursor ? h('button', { class: 'btn small', type: 'button', onclick: () => loadPage(false).then(draw).catch((e) => toast(e, 'error')) }, 'Carregar mais') : null);
    return v;
  };

  const load = async () => {
    mount(grid, skeleton(4));
    mount(filters);
    mount(more);
    filter = null;
    let v;
    try { v = await loadPage(true); } catch (e) {
      if (!alive) return;
      mount(head, ownerControls());
      mount(grid, e.code === 'catalog_not_found'
        ? (owner ? h('p', { class: 'muted pad' }, 'Este número não tem catálogo no WhatsApp.') : emptyCatalog(''))
        : h('p', { class: 'muted pad' }, e.code === 'upstream_changed'
          ? `O WhatsApp mudou o serviço de catálogo: ${errMsg(e)}` : errMsg(e)));
      return;
    }
    if (!v) return;
    mount(head, h('div', { class: 'stack grow' },
      h('h2', null, owner ? `Catálogo de ${phoneOf(v.owner)}` : 'Catálogo do WhatsApp'),
      h('p', { class: 'small muted' }, 'Produtos lidos do WhatsApp (atualiza a cada 5 minutos). Para criar ou editar, use o app WhatsApp Business no celular.')),
    h('div', { class: 'row' }, v.catalog_link ? copyButton(v.catalog_link, 'Copiar link do catálogo', 'btn small') : null), ownerControls());
    if (!v.products.length) { mount(grid, emptyCatalog(owner ? '' : v.catalog_link)); return; }
    try { collections = (await api.get(`${ctx.base}/catalog/collections${owner ? `?jid=${encodeURIComponent(owner)}` : ''}`)).collections || []; } catch { collections = []; }
    if (alive) draw();
  };

  const ownerControls = () => h('form', {
    class: 'row',
    onsubmit: (ev) => { ev.preventDefault(); owner = ownerInput.value.replace(/\D/g, ''); images.clear(); load(); },
  }, ownerInput, h('button', { class: 'btn small', type: 'submit' }, owner ? 'Trocar' : 'Ver'),
  owner ? h('button', { class: 'btn small ghost', type: 'button', onclick: () => { owner = ''; ownerInput.value = ''; images.clear(); load(); } }, 'Meu catálogo') : null);

  const loadOrders = async () => {
    let list;
    try { list = (await api.get(`${ctx.base}/orders?limit=10`)).orders || []; } catch (e) {
      if (alive) mount(orders, h('h2', null, 'Pedidos recentes'), h('p', { class: 'muted small' }, errMsg(e)));
      return;
    }
    if (!alive) return;
    mount(orders, h('h2', null, 'Pedidos recentes'),
      list.length ? h('div', { class: 'table-card' }, h('table', { class: 'table small' },
        h('thead', null, h('tr', null, h('th', null, 'Quando'), h('th', null, 'Cliente'), h('th', null, 'Itens'), h('th', null, 'Total'), h('th', null, 'Pedido'))),
        h('tbody', null, list.map((o) => h('tr', null,
          h('td', null, fmtTime(o.received_at)),
          h('td', null, phoneOf(o.chat)),
          h('td', null, String(o.item_count)),
          h('td', null, money(o.total_amount_1000, o.currency) || '—'),
          h('td', null, h('button', { class: 'btn tiny', type: 'button', onclick: () => showOrder(o.order_id) }, o.order_id)))))))
        : h('p', { class: 'muted small' }, 'Nenhum pedido ainda. Pedidos feitos pelo carrinho do catálogo aparecem aqui (evento order.received).'));
  };

  const showOrder = async (id) => {
    let d;
    try { d = await api.get(`${ctx.base}/orders/${encodeURIComponent(id)}`); } catch (e) { toast(e, 'error'); return; }
    modal(`Pedido ${d.order_id}`, h('div', { class: 'stack' },
      h('p', null, `Cliente: ${phoneOf(d.chat)}`),
      d.note ? h('p', { class: 'prewrap' }, d.note) : null,
      d.items.length ? h('table', { class: 'table small' },
        h('thead', null, h('tr', null, h('th', null, 'Produto'), h('th', null, 'Qtd.'), h('th', null, 'Preço'))),
        h('tbody', null, d.items.map((it) => h('tr', null, h('td', null, it.name || it.product_id), h('td', null, String(it.quantity)),
          h('td', null, money(it.price_amount_1000, it.currency || d.currency)))))) : h('p', { class: 'muted small' }, 'O WhatsApp não devolveu os itens.'),
      h('p', null, h('strong', null, `Total: ${money(d.total_amount_1000, d.currency) || '—'}`)),
      h('p', { class: 'small muted' }, 'Aceitar ou recusar o pedido é feito no app WhatsApp Business.')));
  };

  const off = ctx.on((type) => { if (type === 'order.received') loadOrders(); });
  const el = h('div', { class: 'stack' }, head, filters, grid, more, orders);
  load();
  loadOrders();
  return { el, cleanup() { alive = false; off(); images.clear(); } };
}
