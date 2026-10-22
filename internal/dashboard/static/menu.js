// "Cardápio" tab: LiteZap's own catalog (kept in LiteZap, never written to Meta or
// WhatsApp), on par with the WhatsApp catalog. Three views:
// - Itens: categories on the side, a search/filter bar, item cards (photo, price with the
//   sale struck through, esgotado switch, hidden badge), bulk actions on the selected items
//   (with a preview for price adjustments), duplicate, and an item editor with options, a
//   photo gallery (up to 10, ordered, main photo), sale price, link, SKU, hidden and
//   collections;
// - Coleções: create, edit, pick and order items, cover, send;
// - Pedidos montados: the order drafts built in chats, with status.
// Plus three import paths (spreadsheet, WhatsApp catalog copy, "Montar com IA": first
// with the user's own Claude/ChatGPT over MCP, then the Atendente IA provider when one is
// ready; review before saving), the CSV export and "Enviar cardápio para…". Every value
// reaches the page as text (h() builds text nodes), photos load as blobs through the API.
import * as api from './api.js';
import { h, mount, toast, errMsg, errView, field, emptyState, dialog, confirmDanger, promptText, skeleton, icon, copyButton, phoneOf } from './ui.js';

// brl formats cents as R$.
export function brl(cents) {
  try {
    return new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' }).format((cents || 0) / 100);
  } catch {
    return `R$ ${((cents || 0) / 100).toFixed(2).replace('.', ',')}`;
  }
}

// parseBRL reads "45,90", "R$ 1.234,50" or "45.9" as cents (NaN when it is not a price).
export function parseBRL(text) {
  let s = String(text || '').replace(/R\$/i, '').replace(/\s/g, '');
  if (!s) return 0;
  const neg = s.startsWith('-');
  s = s.replace(/^[+-]/, '');
  if (!/^[\d.,]+$/.test(s)) return NaN;
  if (s.includes(',')) s = s.replace(/\./g, '').replace(',', '.');
  else if (/^\d{1,3}(\.\d{3})+$/.test(s)) s = s.replace(/\./g, '');
  const n = Math.round(parseFloat(s) * 100);
  return Number.isFinite(n) ? (neg ? -n : n) : NaN;
}

const centsText = (c) => (c ? (c / 100).toFixed(2).replace('.', ',') : '');

function optionLine(o) {
  const choices = (o.choices || []).map((c) => c.price_delta_cents ? `${c.name} (${c.price_delta_cents > 0 ? '+' : '-'}${brl(Math.abs(c.price_delta_cents))})` : c.name);
  let label = o.name;
  if (!o.required && o.max > 1) label += ` (opcional, até ${o.max})`;
  else if (!o.required) label += ' (opcional)';
  else if (o.max > 1) label += ` (escolha até ${o.max})`;
  return `${label}: ${choices.join(' · ')}`;
}

// caption mirrors the server's WhatsApp text of an item (menu.Caption): a sale shows the
// old price struck through and the sale price in bold.
function caption(it) {
  let s = `*${it.name || 'Item'}*`;
  if (it.on_sale) s += ` — ~${brl(it.price_cents)}~ *${brl(it.sale_price_cents)}*`;
  else if (it.price_cents > 0) s += ` — ${brl(it.price_cents)}`;
  if (it.available === false) s += ' (esgotado)';
  if (it.description) s += `\n${it.description}`;
  for (const o of it.options || []) s += `\n${optionLine(o)}`;
  if (it.link_url) s += `\n${it.link_url}`;
  return s;
}

// A tiny pool: at most 3 photo downloads at once.
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

// sheet is a wide dialog with a title, a body and action buttons; returns close().
function sheet(title, body, actions) {
  const { dlg, close } = dialog({ label: title, wide: true });
  dlg.classList.add('mn-sheet');
  mount(dlg,
    h('div', { class: 'modal-head' }, h('h2', null, title),
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Fechar', onclick: close }, icon('x'))),
    h('div', { class: 'modal-body' }, body),
    h('div', { class: 'modal-actions' }, actions.map((a) => {
      const b = h('button', { class: a.primary ? 'btn primary' : a.danger ? 'btn danger' : 'btn', type: 'button' }, a.label);
      b.addEventListener('click', async () => {
        b.disabled = true;
        let r;
        try { r = a.onclick ? await a.onclick(b) : undefined; } finally { b.disabled = false; }
        if (r !== false) close();
      });
      if (a.ref) a.ref(b);
      return b;
    })));
  return close;
}

function readFile(file, asText) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(asText ? r.result : String(r.result).replace(/^data:[^,]*,/, ''));
    r.onerror = () => reject(new Error('Não deu para ler o arquivo'));
    if (asText) r.readAsText(file); else r.readAsDataURL(file);
  });
}

// ---------- item editor ----------

function optionsEditor(initial) {
  const list = h('div', { class: 'stack mn-opts' });
  const groups = [];
  const addGroup = (o = { name: '', required: true, max: 1, choices: [{ name: '', price_delta_cents: 0 }] }) => {
    const name = h('input', { value: o.name || '', placeholder: 'Ex.: Tamanho', maxlength: 60, 'aria-label': 'Nome do grupo' });
    const required = h('input', { type: 'checkbox', checked: o.required !== false });
    const maxSel = h('input', { type: 'number', min: 1, max: 20, value: o.max || 1, class: 'mn-num', 'aria-label': 'Escolhas no máximo' });
    const rows = h('div', { class: 'stack mn-choices' });
    const addChoice = (c = { name: '', price_delta_cents: 0 }) => {
      const cn = h('input', { value: c.name || '', placeholder: 'Ex.: Grande', maxlength: 60, 'aria-label': 'Escolha' });
      const cd = h('input', { value: c.price_delta_cents ? (c.price_delta_cents < 0 ? '-' : '') + centsText(Math.abs(c.price_delta_cents)) : '', placeholder: '+0,00', inputmode: 'decimal', class: 'mn-num', 'aria-label': 'Acréscimo' });
      const row = h('div', { class: 'row' }, cn, cd,
        h('button', { class: 'btn small ghost', type: 'button', 'aria-label': 'Remover escolha', onclick: () => row.remove() }, icon('x', 14)));
      row.read = () => ({ name: cn.value.trim(), price_delta_cents: parseBRL(cd.value) });
      rows.appendChild(row);
    };
    (o.choices && o.choices.length ? o.choices : [{}]).forEach(addChoice);
    const box = h('div', { class: 'card-inner stack' },
      h('div', { class: 'row' }, name,
        h('label', { class: 'row small' }, required, 'Obrigatório'),
        h('label', { class: 'row small' }, 'Até', maxSel),
        h('button', { class: 'btn small ghost danger', type: 'button', onclick: () => { box.remove(); groups.splice(groups.indexOf(box), 1); } }, icon('trash', 14), 'Grupo')),
      rows,
      h('div', null, h('button', { class: 'btn small', type: 'button', onclick: () => addChoice() }, icon('plus', 14), 'Escolha')));
    box.read = () => ({ name: name.value.trim(), required: required.checked, min: required.checked ? 1 : 0, max: Number(maxSel.value) || 1,
      choices: [...rows.children].map((r) => r.read()).filter((c) => c.name) });
    groups.push(box);
    list.appendChild(box);
  };
  (initial || []).forEach(addGroup);
  const el = h('div', { class: 'stack' }, list,
    h('div', null, h('button', { class: 'btn small', type: 'button', onclick: () => addGroup() }, icon('plus', 14), 'Grupo de opções')),
    h('p', { class: 'hint' }, 'Ex.: "Tamanho" obrigatório com P, M (+5,00) e G (+10,00); "Borda" opcional. O acréscimo soma ao preço do item.'));
  return { el, read: () => groups.map((g) => g.read()).filter((g) => g.name), list };
}

// photosEditor is the gallery of an item: thumbnails in order (the first is the main
// photo), with "make main", move and remove. Changes to a saved item go to the server at
// once; for a new item the chosen files are uploaded after it is created.
function photosEditor(ctx, images, item, onChange) {
  const box = h('div', { class: 'mn-gallery' });
  const pending = []; // {file} or {url} for a new item (or queued uploads)
  let current = (item && item.images) || [];
  const max = 10;
  const path = (id) => `${ctx.base}/menu/items/${encodeURIComponent(item.id)}/images/${encodeURIComponent(id)}`;
  const apply = (v) => { current = v.images || []; if (onChange) onChange(v); draw(); };
  const act = async (fn) => { try { apply(await fn()); } catch (e) { toast(e, 'error'); } };
  const draw = () => {
    const thumbs = current.map((im, i) => {
      const img = h('img', { class: 'mn-thumb', alt: i === 0 ? 'Foto principal' : `Foto ${i + 1}` });
      images.load(im.path, img);
      return h('figure', { class: `mn-thumb-box${i === 0 ? ' main' : ''}` }, img,
        h('figcaption', { class: 'row mn-thumb-tools' },
          i === 0 ? h('span', { class: 'badge tone-ok small' }, 'Principal')
            : h('button', { class: 'icon-btn', type: 'button', title: 'Tornar principal', 'aria-label': 'Tornar principal',
              onclick: () => act(() => api.put(`${path(im.id)}/main`, {})) }, '★'),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mover para trás', disabled: i === 0, onclick: () => {
            const ids = current.map((x) => x.id);
            [ids[i - 1], ids[i]] = [ids[i], ids[i - 1]];
            act(() => api.put(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}/images/order`, { ids }));
          } }, '◀'),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mover para frente', disabled: i === current.length - 1, onclick: () => {
            const ids = current.map((x) => x.id);
            [ids[i], ids[i + 1]] = [ids[i + 1], ids[i]];
            act(() => api.put(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}/images/order`, { ids }));
          } }, '▶'),
          h('button', { class: 'icon-btn danger', type: 'button', 'aria-label': 'Remover foto', onclick: async () => {
            if (!(await confirmDanger('Remover esta foto do item?'))) return;
            act(() => api.del(path(im.id)));
          } }, icon('trash', 14))));
    });
    const queued = pending.map((p, i) => h('figure', { class: 'mn-thumb-box pending' },
      p.file ? (() => { const img = h('img', { class: 'mn-thumb', alt: 'Nova foto' }); img.src = URL.createObjectURL(p.file); return img; })()
        : h('div', { class: 'mn-thumb mn-thumb-url small muted' }, 'link'),
      h('figcaption', { class: 'row mn-thumb-tools' }, h('span', { class: 'small muted' }, 'ao salvar'),
        h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Tirar da fila', onclick: () => { pending.splice(i, 1); draw(); } }, icon('x', 14)))));
    mount(box, thumbs.length || queued.length ? h('div', { class: 'mn-thumbs' }, thumbs, queued) : h('p', { class: 'small muted' }, 'Sem fotos ainda.'));
  };
  const file = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/gif,image/webp', multiple: true, 'aria-label': 'Adicionar fotos' });
  const url = h('input', { type: 'url', placeholder: 'https://… (link de uma foto)', 'aria-label': 'Link da foto' });
  const room = () => max - current.length - pending.length;
  const addFiles = async (files) => {
    for (const f of files) {
      if (room() <= 0) { toast(`No máximo ${max} fotos por item`, 'error'); break; }
      if (item) await act(async () => api.post(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}/images`, { base64: await readFile(f) }));
      else pending.push({ file: f });
    }
    draw();
  };
  file.addEventListener('change', () => { addFiles([...file.files]); file.value = ''; });
  const addURL = h('button', { class: 'btn small', type: 'button', onclick: async () => {
    const u = url.value.trim();
    if (!u) return;
    if (room() <= 0) { toast(`No máximo ${max} fotos por item`, 'error'); return; }
    if (item) await act(() => api.post(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}/images`, { url: u }));
    else { pending.push({ url: u }); draw(); }
    url.value = '';
  } }, icon('plus', 14), 'Link');
  draw();
  return {
    el: h('div', { class: 'stack' }, box, file, h('div', { class: 'row' }, url, addURL),
      h('p', { class: 'hint' }, `Até ${max} fotos (JPEG, PNG, GIF ou WebP até 8 MB). A primeira é a principal: vai junto quando o item é enviado.`)),
    // upload sends the queued photos of a just-created item.
    async upload(id) {
      for (const p of pending.splice(0)) {
        await api.post(`${ctx.base}/menu/items/${encodeURIComponent(id)}/images`, p.file ? { base64: await readFile(p.file) } : { url: p.url });
      }
    },
  };
}

// salePreview shows the price as customers see it: the old price struck through and the
// sale price in bold.
function priceNode(it) {
  if (it.on_sale) {
    return h('span', { class: 'mn-price' }, h('s', { class: 'muted' }, brl(it.price_cents)), ' ', h('strong', null, brl(it.sale_price_cents)));
  }
  return it.price_cents ? h('span', { class: 'mn-price' }, brl(it.price_cents)) : h('span', { class: 'muted' }, 'Sem preço');
}

function today() { return new Date().toISOString().slice(0, 10); }

function itemEditor(ctx, menu, images, item, onSaved) {
  const it = item || { name: '', description: '', price_cents: 0, available: true, options: [], tags: [], category_id: '', collection_ids: [], images: [] };
  const name = h('input', { value: it.name, maxlength: 120, required: true });
  const cat = h('select', null, h('option', { value: '' }, 'Sem categoria'),
    menu.categories.map((c) => h('option', { value: c.id }, c.name)));
  cat.value = it.category_id || (ctx.menuFilter && ctx.menuFilter !== '_all' ? ctx.menuFilter : '');
  const desc = h('textarea', { rows: 3, maxlength: 1000 }, it.description || '');
  const price = h('input', { value: centsText(it.price_cents), placeholder: '0,00', inputmode: 'decimal' });
  const sale = h('input', { value: centsText(it.sale_price_cents), placeholder: 'Opcional', inputmode: 'decimal', 'aria-label': 'Preço promocional' });
  const until = h('input', { type: 'date', value: it.sale_until || '', 'aria-label': 'Promoção até' });
  const avail = h('input', { type: 'checkbox', class: 'ag-switch', checked: it.available !== false });
  const hidden = h('input', { type: 'checkbox', class: 'ag-switch', checked: !!it.hidden });
  const sku = h('input', { value: it.sku || '', maxlength: 64, placeholder: 'Opcional' });
  const link = h('input', { type: 'url', value: it.link_url || '', maxlength: 2048, placeholder: 'https://… (página do item)' });
  const tags = h('input', { value: (it.tags || []).join(', '), placeholder: 'vegano, picante' });
  const cols = (menu.collections || []).map((c) => {
    const cb = h('input', { type: 'checkbox', checked: (it.collection_ids || []).includes(c.id) });
    cb.dataset.id = c.id;
    return h('label', { class: 'row small' }, cb, c.name, c.active ? null : h('span', { class: 'muted' }, ' (inativa)'));
  });
  const opts = optionsEditor(it.options);
  let saved = item;
  const gallery = photosEditor(ctx, images, item, (v) => { saved = v; });
  const preview = h('div', { class: 'ag-bubble prewrap mn-preview' });
  const saleHint = h('p', { class: 'hint' });
  const read = () => ({ name: name.value.trim(), category_id: cat.value, description: desc.value.trim(), price: price.value.trim() || '0',
    sale_price: sale.value.trim(), sale_until: sale.value.trim() ? until.value : '', available: avail.checked, hidden: hidden.checked,
    sku: sku.value.trim(), link_url: link.value.trim(), tags: tags.value.split(',').map((s) => s.trim()).filter(Boolean), options: opts.read(),
    collection_ids: cols.map((l) => l.querySelector('input')).filter((cb) => cb.checked).map((cb) => cb.dataset.id) });
  const refresh = () => {
    const v = read();
    const cents = parseBRL(v.price);
    const saleCents = v.sale_price ? parseBRL(v.sale_price) : 0;
    const onSale = saleCents > 0 && saleCents < cents && (!v.sale_until || v.sale_until >= today());
    mount(preview, caption({ ...v, price_cents: Number.isNaN(cents) ? 0 : cents, sale_price_cents: saleCents, on_sale: onSale }));
    if (Number.isNaN(saleCents)) mount(saleHint, h('span', { class: 'error' }, 'Preço promocional inválido: use 39,90'));
    else if (saleCents > 0 && saleCents >= cents) mount(saleHint, h('span', { class: 'error' }, 'O preço promocional precisa ser menor que o preço.'));
    else if (saleCents > 0) mount(saleHint, 'No WhatsApp: ', h('s', null, brl(cents)), ' ', h('strong', null, brl(saleCents)), v.sale_until ? ` até ${v.sale_until.split('-').reverse().join('/')}` : '');
    else mount(saleHint, 'Deixe vazio para não ter promoção.');
  };
  const form = h('div', { class: 'mn-editor' },
    h('div', { class: 'stack' },
      field('Nome', name),
      h('div', { class: 'grid2' }, field('Preço (R$)', price, 'Ex.: 45,90'), field('Categoria', cat)),
      h('div', { class: 'grid2' }, field('Preço promocional (R$)', sale), field('Promoção até', until, 'Último dia (opcional)')),
      saleHint,
      field('Descrição', desc),
      h('label', { class: 'row' }, avail, h('span', { class: 'ag-switch-label' }, 'Disponível'), h('span', { class: 'hint' }, 'desligado = esgotado (aparece marcado)')),
      h('label', { class: 'row' }, hidden, h('span', { class: 'ag-switch-label' }, 'Oculto'), h('span', { class: 'hint' }, 'nunca aparece para clientes, atendente ou agentes')),
      h('div', { class: 'grid2' }, field('SKU', sku), field('Tags', tags, 'Separadas por vírgula')),
      field('Link', link, 'Vai na legenda quando o item é enviado.'),
      cols.length ? h('fieldset', { class: 'mn-cols' }, h('legend', null, 'Coleções'), cols) : null,
      h('h3', null, 'Opções'), opts.el),
    h('div', { class: 'stack' },
      h('h3', null, 'Fotos'), gallery.el,
      h('h3', null, 'Prévia no WhatsApp'), preview));
  form.addEventListener('input', refresh);
  form.addEventListener('click', () => setTimeout(refresh, 0));
  refresh();
  sheet(item ? `Editar ${it.name}` : 'Novo item', form, [
    { label: item ? 'Fechar' : 'Cancelar', onclick: () => { if (saved !== item) onSaved(); return true; } },
    {
      label: item ? 'Salvar' : 'Criar item', primary: true, onclick: async () => {
        const v = read();
        if (!v.name) { toast('Informe o nome do item', 'error'); return false; }
        if (Number.isNaN(parseBRL(v.price))) { toast('Preço inválido: use 45,90', 'error'); return false; }
        if (v.sale_price && Number.isNaN(parseBRL(v.sale_price))) { toast('Preço promocional inválido: use 39,90', 'error'); return false; }
        for (const g of v.options) {
          if (g.choices.some((c) => Number.isNaN(c.price_delta_cents))) { toast(`Acréscimo inválido em "${g.name}"`, 'error'); return false; }
        }
        if (!(menu.collections || []).length) delete v.collection_ids;
        try {
          const out = item ? await api.patch(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}`, v) : await api.post(`${ctx.base}/menu/items`, v);
          if (!item) await gallery.upload(out.id);
          toast(item ? 'Item salvo' : 'Item criado');
          onSaved();
          return true;
        } catch (e) { toast(e, 'error'); onSaved(); return false; }
      },
    },
  ]);
  name.focus();
}

// ---------- imports ----------

function rowsTable(rows, { editable } = {}) {
  const keep = new Map();
  const body = h('tbody');
  for (const r of rows) {
    const ok = !(r.errors && r.errors.length);
    const check = h('input', { type: 'checkbox', checked: ok, disabled: !ok || !editable, 'aria-label': 'Incluir' });
    const name = editable ? h('input', { value: r.name, maxlength: 120, 'aria-label': 'Nome' }) : null;
    const price = editable ? h('input', { value: centsText(r.price_cents), class: 'mn-num', inputmode: 'decimal', 'aria-label': 'Preço' }) : null;
    keep.set(r, { check, name, price });
    body.appendChild(h('tr', { class: ok ? '' : 'mn-bad' },
      h('td', null, check),
      h('td', null, r.category || h('span', { class: 'muted' }, '—')),
      h('td', null, name || r.name || h('span', { class: 'muted' }, '(sem nome)'),
        (r.options || []).length ? h('div', { class: 'small muted' }, r.options.map(optionLine).join(' · ')) : null,
        r.available === false ? h('span', { class: 'badge tone-danger' }, 'Esgotado') : null),
      h('td', null, price || r.price_text || brl(r.price_cents)),
      h('td', { class: 'small' },
        (r.errors || []).map((e) => h('div', { class: 'error' }, e)),
        (r.warnings || []).map((w) => h('div', { class: 'muted' }, `⚠ ${w}`)),
        r.image_url ? h('div', { class: 'muted' }, 'com foto (link)') : null)));
  }
  const table = h('div', { class: 'table-card mn-rows' }, h('table', { class: 'table small' },
    h('thead', null, h('tr', null, h('th', null, ''), h('th', null, 'Categoria'), h('th', null, 'Item'), h('th', null, 'Preço'), h('th', null, 'Observações'))),
    body));
  const selected = () => rows.filter((r) => keep.get(r).check.checked).map((r) => {
    const k = keep.get(r);
    const out = { ...r };
    delete out.errors; delete out.warnings; delete out.line; delete out.price_text;
    if (k.name) out.name = k.name.value.trim();
    if (k.price) out.price_cents = parseBRL(k.price.value);
    return out;
  });
  return { table, selected };
}

function summary(v) {
  const parts = [`${v.valid} ${v.valid === 1 ? 'item válido' : 'itens válidos'}`];
  if (v.invalid) parts.push(`${v.invalid} com erro`);
  if (v.skipped) parts.push(`${v.skipped} já no cardápio`);
  return parts.join(' · ');
}

function savedToast(v) {
  let msg = `${v.items_created} ${v.items_created === 1 ? 'item salvo' : 'itens salvos'}`;
  if (v.images_saved) msg += `, ${v.images_saved} fotos`;
  if (v.images_pending) msg += ` (faltam ${v.images_pending} fotos: rode de novo para buscá-las)`;
  toast(msg);
  if (v.image_errors && v.image_errors.length) toast(`Fotos não salvas: ${v.image_errors.slice(0, 3).join('; ')}`, 'error');
}

function csvDialog(ctx, reload) {
  const file = h('input', { type: 'file', accept: '.csv,text/csv,text/plain', 'aria-label': 'Planilha CSV' });
  const text = h('textarea', { rows: 6, class: 'mono', placeholder: 'categoria;nome;descricao;preco;disponivel;imagem_url;opcoes\nPizzas;Calabresa;Molho e calabresa;45,90;sim;;Tamanho: P | G +10,00' });
  const replace = h('input', { type: 'checkbox' });
  const out = h('div', { class: 'stack' });
  let preview = null;
  file.addEventListener('change', async () => { if (file.files[0]) text.value = await readFile(file.files[0], true); });
  const check = async () => {
    try {
      preview = await api.post(`${ctx.base}/menu/import/csv`, { csv: text.value, dry_run: true });
    } catch (e) { mount(out, h('p', { class: 'error' }, errView(e))); return; }
    mount(out, h('p', null, h('strong', null, summary(preview))),
      (preview.file_errors || []).length ? h('div', { class: 'error' }, preview.file_errors.join('; ')) : null,
      rowsTable(preview.rows).table);
  };
  sheet('Importar planilha (CSV)', h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'No Excel ou Google Planilhas: Arquivo > Fazer download > CSV. Colunas: categoria, nome, descricao, preco (45,90 ou R$ 1.234,50), disponivel (sim/não/esgotado), imagem_url e, opcionais: opcoes ("Tamanho: P | M +5,00; Borda (opcional): Catupiry +8"), preco_promocional, promocao_ate (31/12/2026), sku, link, tags, colecoes (Promoções|Mais vendidos), fotos (links separados por |) e oculto (sim/não). "Exportar CSV" gera uma planilha nesse formato.'),
    file, text,
    h('label', { class: 'row small' }, replace, 'Substituir o cardápio atual (apaga os itens e categorias de hoje)'),
    h('div', null, h('button', { class: 'btn', type: 'button', onclick: check }, icon('eye', 15), 'Pré-visualizar')),
    out), [
    { label: 'Cancelar' },
    {
      label: 'Salvar itens válidos', primary: true, onclick: async () => {
        if (!preview) { await check(); if (!preview) return false; }
        if (!preview.valid) { toast('Nenhum item válido para salvar', 'error'); return false; }
        if (replace.checked && !(await confirmDanger('Substituir apaga todo o cardápio atual antes de importar. Continuar?'))) return false;
        try {
          const v = await api.post(`${ctx.base}/menu/import/csv`, { csv: text.value, skip_invalid: true, replace: replace.checked }, { timeout: 150000 });
          savedToast(v);
          reload();
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ]);
}

function catalogDialog(ctx, reload) {
  const out = h('div', { class: 'stack' }, skeleton(2));
  let preview = null;
  sheet('Copiar do catálogo do WhatsApp', h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'Lê os produtos do catálogo deste número no WhatsApp (só leitura: nada é alterado no WhatsApp) e copia nome, descrição, preço, preço promocional, disponibilidade, link, código (retailer_id, vira o SKU) e todas as fotos para o Cardápio do LiteZap; as coleções do WhatsApp viram coleções aqui. Produtos ocultos ou em análise ficam de fora; os já copiados são pulados (e ganham as fotos que faltarem).'),
    out), [
    { label: 'Cancelar' },
    {
      label: 'Copiar para o cardápio', primary: true, onclick: async () => {
        if (!preview) return false;
        try {
          const v = await api.post(`${ctx.base}/menu/import/whatsapp-catalog`, {}, { timeout: 150000 });
          savedToast(v);
          reload();
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ]);
  api.post(`${ctx.base}/menu/import/whatsapp-catalog`, { dry_run: true }, { timeout: 60000 }).then((v) => {
    preview = v;
    mount(out, h('p', null, h('strong', null, summary(v))), v.rows.length ? rowsTable(v.rows).table : h('p', { class: 'muted' }, 'Nada novo para copiar.'));
  }).catch((e) => {
    mount(out, h('p', { class: 'error' }, e.code === 'catalog_not_found'
      ? 'Este número não tem catálogo no WhatsApp (ou não é uma conta Business).' : errView(e)));
  });
}

// menuPrompt is the ready-made request for the user's own assistant (Claude or ChatGPT).
export function menuPrompt(inst) {
  let p = 'Monte o cardápio do LiteZap a partir deste texto: <cole aqui>. Use import_menu_rows com dry_run primeiro e me mostre antes de salvar.';
  if (inst && inst.id) p += ` Use a instância "${inst.name || inst.id}" (${inst.id}).`;
  return p;
}

// connectedClient reports whether an AI client (Claude Desktop/Code, Cursor) has LiteZap.
export function connectedClient(clients) {
  return (clients || []).some((c) => (c.extension_installed && !c.extension_disabled) || c.connected);
}

// subscriptionPath is "Montar com IA" with the user's own Claude or ChatGPT subscription:
// LiteZap gives the tools (import_menu_rows with a preview), the assistant reads the text,
// photo or spreadsheet. clients is null when this server cannot tell (not the tray app).
function subscriptionPath(ctx, clients, reload, close) {
  const prompt = menuPrompt(ctx.inst);
  const linked = clients === null || connectedClient(clients);
  const connect = clients === null
    ? h('a', { href: 'https://litezap.com/docs/mcp/' }, 'Como conectar a IA')
    : h('a', { href: '#/ia', onclick: () => close() }, 'Conectar a IA');
  return h('section', { class: 'card pad stack' },
    h('h3', null, icon('sparkles', 16), ' Monte com o seu Claude ou ChatGPT (assinatura)'),
    h('p', { class: 'small muted' }, 'Sem chave de API e sem Ollama: a IA que você já assina lê o seu cardápio (texto, foto do cardápio impresso ou planilha) e usa as ferramentas do LiteZap. Ela mostra uma prévia e só salva depois que você confirmar.'),
    linked ? null : h('p', { class: 'small' }, h('b', null, 'Nenhuma IA conectada ao LiteZap ainda. '), connect, '.'),
    h('ol', { class: 'small stack' },
      h('li', null, 'Abra o ', h('b', null, 'Claude Desktop'), ' (com a extensão LiteZap instalada) ou o ', h('b', null, 'Claude Code'),
        ' (com o plugin LiteZap: use ', h('code', null, '/litezap:montar-cardapio'), '). No ', h('b', null, 'ChatGPT'),
        ', use o conector MCP do LiteZap (precisa de um endereço HTTPS público; veja o guia).'),
      h('li', null, 'Cole o pedido abaixo, trocando <cole aqui> pelo seu cardápio, ou anexe a foto ou o CSV.'),
      h('li', null, 'Confira a prévia que a IA mostrar, confirme, e clique em "Atualizar" aqui para ver os itens.')),
    h('div', { class: 'row ai-cmd' }, h('code', { class: 'prewrap' }, prompt), copyButton(prompt, 'Copiar pedido', 'btn small')),
    h('p', { class: 'small' }, linked ? connect : null, linked ? ' · ' : null,
      h('a', { href: 'https://litezap.com/docs/guias/cardapio/#montar-com-a-sua-assinatura-claudechatgpt' }, 'Guia com exemplos')),
    h('div', null, h('button', { class: 'btn small', type: 'button', onclick: () => reload() }, icon('refresh', 14), 'Atualizar')));
}

async function aiDialog(ctx, reload) {
  const [att, clients] = await Promise.all([
    api.get(`${ctx.base}/attendant`).catch(() => null),
    api.get('/app/api/ai-clients').then((r) => r.clients || []).catch(() => null),
  ]);
  const ready = !!(att && att.ready);
  const text = h('textarea', { rows: 6, maxlength: 12000, placeholder: 'Ex.: pizza calabresa 45, marguerita 42, coca 2L 12, suco de laranja 9 (esgotado)' });
  const out = h('div', { class: 'stack' });
  let review = null;
  let rows = [];
  const build = async (btn) => {
    if (!text.value.trim()) { toast('Escreva ou cole o cardápio', 'error'); return; }
    if (btn) btn.disabled = true;
    mount(out, h('p', { class: 'muted' }, 'A IA está montando os itens…'));
    try {
      const v = await api.post(`${ctx.base}/menu/import/ai`, { text: text.value }, { timeout: 120000 });
      rows = v.rows;
      review = rowsTable(rows, { editable: true });
      mount(out, h('p', null, h('strong', null, summary(v)), ' — confira e ajuste antes de salvar. Nada foi salvo ainda.'), review.table);
    } catch (e) {
      review = null;
      mount(out, h('p', { class: 'error' }, e.code === 'ai_not_configured'
        ? 'O provedor do Atendente IA não está pronto. Use o seu Claude ou ChatGPT (acima).' : errView(e)));
    } finally { if (btn) btn.disabled = false; }
  };
  const buildBtn = h('button', { class: 'btn', type: 'button' }, icon('sparkles', 15), 'Montar itens');
  buildBtn.addEventListener('click', () => build(buildBtn));
  let closeSheet = () => {};
  const primary = subscriptionPath(ctx, clients, reload, () => closeSheet());
  // With a ready Atendente IA provider, building here stays as the secondary path.
  const inApp = ready ? h('details', { class: 'adv' },
    h('summary', null, 'Ou monte aqui, com o provedor do Atendente IA'),
    h('div', { class: 'stack' },
      h('p', { class: 'small muted' }, 'Cole o cardápio como texto livre. O provedor configurado no Atendente IA organiza em itens para você revisar; nada é salvo sem a sua confirmação.'),
      text, h('div', null, buildBtn), out)) : null;
  const actions = ready ? [
    { label: 'Fechar', onclick: () => { reload(); return true; } },
    {
      label: 'Salvar itens marcados', primary: true, onclick: async () => {
        if (!review) { toast('Monte os itens aqui primeiro (ou feche, se usou o seu Claude/ChatGPT)', 'error'); return false; }
        const sel = review.selected();
        if (!sel.length) { toast('Marque ao menos um item', 'error'); return false; }
        if (sel.some((r) => Number.isNaN(r.price_cents))) { toast('Há um preço inválido', 'error'); return false; }
        try {
          const v = await api.post(`${ctx.base}/menu/import/rows`, { rows: sel });
          savedToast(v);
          reload();
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ] : [{ label: 'Fechar', onclick: () => { reload(); return true; } }];
  closeSheet = sheet('Montar com IA', h('div', { class: 'stack' }, primary, inApp), actions);
}

// ---------- send ----------

function sendDialog(ctx, menu, preset) {
  const to = h('input', { placeholder: '5511999999999', inputmode: 'tel', 'aria-label': 'Para' });
  const cat = h('select', null, h('option', { value: '' }, 'Cardápio completo'), menu.categories.map((c) => h('option', { value: c.id }, c.name)));
  if (preset && preset.category) cat.value = preset.category;
  const prev = h('div', { class: 'stack mn-send-preview' });
  const item = preset && preset.item;
  let chunks = [];
  const load = async () => {
    if (item) { mount(prev, h('div', { class: 'ag-bubble prewrap' }, caption(item))); return; }
    try {
      const q = cat.value ? `?category_id=${encodeURIComponent(cat.value)}` : '';
      chunks = (await api.get(`${ctx.base}/menu/text${q}`)).chunks || [];
      mount(prev, chunks.length ? [h('p', { class: 'small muted' }, chunks.length === 1 ? '1 mensagem' : `${chunks.length} mensagens`),
        ...chunks.map((c) => h('div', { class: 'ag-bubble prewrap' }, c))] : h('p', { class: 'muted' }, 'Nenhum item disponível para enviar.'));
    } catch (e) { mount(prev, h('p', { class: 'error' }, errView(e))); }
  };
  cat.addEventListener('change', load);
  sheet(item ? `Enviar "${item.name}"` : 'Enviar cardápio', h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'Vai pela fila de envio, com as mesmas pausas anti-ban das outras mensagens. Itens esgotados e ocultos ficam de fora; promoções aparecem com o preço antigo riscado.'),
    field('Para', to, 'Número com DDI e DDD, ou o JID da conversa.'),
    item ? null : field('O que enviar', cat),
    h('h3', null, 'Prévia'), prev), [
    { label: 'Cancelar' },
    {
      label: 'Enviar', primary: true, onclick: async () => {
        if (!to.value.trim()) { toast('Informe o destinatário', 'error'); return false; }
        try {
          if (item) {
            const v = await api.post(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}/send`, { to: to.value.trim() });
            toast(v.status === 'sent' ? 'Item enviado' : 'Item na fila de envio');
          } else {
            const body = { to: to.value.trim() };
            if (cat.value) body.category_id = cat.value;
            const v = await api.post(`${ctx.base}/menu/send`, body);
            toast(`Cardápio na fila: ${v.messages.length} ${v.messages.length === 1 ? 'mensagem' : 'mensagens'}`);
          }
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ]);
  load();
  to.focus();
}

// ---------- bulk changes ----------

// bulkPriceDialog previews a price adjustment (dry_run) and saves it after the user saw
// every before/after.
function bulkPriceDialog(ctx, ids, reload) {
  const mode = h('select', { 'aria-label': 'Tipo de reajuste' }, h('option', { value: 'percent' }, 'Percentual (%)'), h('option', { value: 'amount' }, 'Valor (R$)'));
  const val = h('input', { placeholder: 'Ex.: 10 ou -15', inputmode: 'decimal', 'aria-label': 'Reajuste' });
  const out = h('div', { class: 'stack' });
  let preview = null;
  const body = () => {
    const raw = val.value.trim().replace(',', '.');
    if (mode.value === 'percent') return { percent: Number(raw) };
    const c = parseBRL(val.value);
    return { amount_cents: c };
  };
  const check = async () => {
    preview = null;
    const b = body();
    if ((b.percent !== undefined && (!Number.isFinite(b.percent) || b.percent === 0)) || (b.amount_cents !== undefined && (!b.amount_cents || Number.isNaN(b.amount_cents)))) {
      mount(out, h('p', { class: 'error' }, 'Informe um reajuste diferente de zero: 10, -15 ou 2,00.'));
      return;
    }
    try {
      preview = await api.post(`${ctx.base}/menu/items/bulk`, { item_ids: ids, action: 'adjust_price', dry_run: true, ...b });
    } catch (e) { mount(out, h('p', { class: 'error' }, errView(e))); return; }
    mount(out, h('p', null, h('strong', null, `${preview.affected} ${preview.affected === 1 ? 'item muda' : 'itens mudam'}`), ' — prévia, nada foi salvo ainda.'),
      preview.errors.length ? h('div', { class: 'error' }, preview.errors.join('; ')) : null,
      h('div', { class: 'table-card mn-rows' }, h('table', { class: 'table small' },
        h('thead', null, h('tr', null, h('th', null, 'Item'), h('th', null, 'Antes'), h('th', null, 'Depois'))),
        h('tbody', null, preview.changes.map((c) => h('tr', { class: c.error ? 'mn-bad' : '' },
          h('td', null, c.name), h('td', null, c.before), h('td', null, c.after, c.error ? h('div', { class: 'error' }, c.error) : null)))))));
  };
  mode.addEventListener('change', () => { preview = null; mount(out); });
  val.addEventListener('input', () => { preview = null; });
  sheet(`Reajustar preço de ${ids.length} ${ids.length === 1 ? 'item' : 'itens'}`, h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'O preço promocional (quando houver) muda do mesmo jeito. Veja a prévia antes de salvar.'),
    h('div', { class: 'row' }, mode, val, h('button', { class: 'btn', type: 'button', onclick: check }, icon('eye', 15), 'Pré-visualizar')),
    out), [
    { label: 'Cancelar' },
    {
      label: 'Salvar reajuste', primary: true, onclick: async () => {
        if (!preview) { await check(); return false; }
        if (preview.errors.length) { toast('Há itens que não aceitam esse reajuste', 'error'); return false; }
        try {
          const v = await api.post(`${ctx.base}/menu/items/bulk`, { item_ids: ids, action: 'adjust_price', ...body() });
          toast(`Preço reajustado em ${v.affected} ${v.affected === 1 ? 'item' : 'itens'}`);
          reload();
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ]);
  val.focus();
}

// pickDialog asks for one entry of a list (a category or a collection).
function pickDialog(title, label, options) {
  return new Promise((resolve) => {
    const sel = h('select', { 'aria-label': label }, options.map((o) => h('option', { value: o.id }, o.name)));
    sheet(title, field(label, sel), [
      { label: 'Cancelar', onclick: () => { resolve(null); return true; } },
      { label: 'Aplicar', primary: true, onclick: () => { resolve(sel.value); return true; } },
    ]);
  });
}

async function runBulk(ctx, ids, action, extra, reload, label) {
  try {
    const v = await api.post(`${ctx.base}/menu/items/bulk`, { item_ids: ids, action, ...extra });
    toast(`${label}: ${v.affected} ${v.affected === 1 ? 'item' : 'itens'}`);
    reload();
    return true;
  } catch (e) { toast(e, 'error'); return false; }
}

// ---------- collections ----------

function collectionEditor(ctx, menu, images, col, reload) {
  const all = allItems(menu);
  const name = h('input', { value: col ? col.name : '', maxlength: 60, placeholder: 'Ex.: Promoções', 'aria-label': 'Nome' });
  const desc = h('textarea', { rows: 2, maxlength: 500, placeholder: 'Opcional: aparece embaixo do nome quando é enviada' }, col ? col.description : '');
  const active = h('input', { type: 'checkbox', class: 'ag-switch', checked: !col || col.active });
  // The cover: a photo of one of the items.
  const cover = h('select', { 'aria-label': 'Capa' }, h('option', { value: '' }, 'Sem capa'));
  for (const it of all) {
    (it.images || []).forEach((im, i) => cover.appendChild(h('option', { value: im.id }, `${it.name}${i ? ` (foto ${i + 1})` : ''}`)));
  }
  cover.value = (col && col.cover_image_id) || '';
  const coverImg = h('img', { class: 'mn-thumb mn-cover', alt: 'Capa' });
  const showCover = () => {
    const id = cover.value;
    const owner = all.find((it) => (it.images || []).some((im) => im.id === id));
    const im = owner && owner.images.find((x) => x.id === id);
    if (im) { coverImg.hidden = false; images.load(im.path, coverImg); } else coverImg.hidden = true;
  };
  cover.addEventListener('change', showCover);
  showCover();
  // The items: a picker with search, and the chosen ones in order.
  let chosen = col ? [...col.item_ids] : [];
  const byID = new Map(all.map((it) => [it.id, it]));
  const q = h('input', { type: 'search', placeholder: 'Buscar item', 'aria-label': 'Buscar item' });
  const pool = h('div', { class: 'mn-picker' });
  const list = h('ol', { class: 'mn-order' });
  const count = h('h3');
  const draw = () => {
    const term = fold(q.value);
    mount(pool, all.filter((it) => !term || fold(`${it.name} ${(it.tags || []).join(' ')}`).includes(term)).slice(0, 200).map((it) => {
      const cb = h('input', { type: 'checkbox', checked: chosen.includes(it.id), 'aria-label': it.name });
      cb.addEventListener('change', () => {
        if (cb.checked) { if (chosen.length >= 100) { cb.checked = false; toast('No máximo 100 itens por coleção', 'error'); return; } chosen.push(it.id); }
        else chosen = chosen.filter((x) => x !== it.id);
        draw();
      });
      return h('label', { class: 'row small' }, cb, it.name, it.hidden ? h('span', { class: 'badge' }, 'Oculto') : null,
        it.available === false ? h('span', { class: 'badge tone-danger' }, 'Esgotado') : null);
    }));
    mount(count, `Na coleção (${chosen.length}), nesta ordem`);
    mount(list, chosen.map((id, i) => h('li', { class: 'row small' }, h('span', { class: 'grow' }, (byID.get(id) || { name: id }).name),
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Subir', disabled: i === 0, onclick: () => { [chosen[i - 1], chosen[i]] = [chosen[i], chosen[i - 1]]; draw(); } }, '▲'),
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Descer', disabled: i === chosen.length - 1, onclick: () => { [chosen[i], chosen[i + 1]] = [chosen[i + 1], chosen[i]]; draw(); } }, '▼'),
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Tirar da coleção', onclick: () => { chosen.splice(i, 1); draw(); } }, icon('x', 14)))));
  };
  q.addEventListener('input', draw);
  draw();
  sheet(col ? `Coleção ${col.name}` : 'Nova coleção', h('div', { class: 'mn-editor' },
    h('div', { class: 'stack' }, field('Nome', name), field('Descrição', desc),
      h('label', { class: 'row' }, active, h('span', { class: 'ag-switch-label' }, 'Ativa'), h('span', { class: 'hint' }, 'inativa = guardada, mas não é oferecida a clientes')),
      field('Capa', cover, 'Uma foto de um dos itens.'), coverImg),
    h('div', { class: 'stack' }, h('h3', null, 'Itens'), q, pool, count, list)), [
    { label: 'Cancelar' },
    {
      label: col ? 'Salvar' : 'Criar coleção', primary: true, onclick: async () => {
        if (!name.value.trim()) { toast('Informe o nome da coleção', 'error'); return false; }
        const body = { name: name.value.trim(), description: desc.value.trim(), active: active.checked, cover_image_id: cover.value };
        try {
          if (!col) {
            await api.post(`${ctx.base}/menu/collections`, { ...body, item_ids: chosen });
          } else {
            const base = `${ctx.base}/menu/collections/${encodeURIComponent(col.id)}`;
            await api.patch(base, body);
            const gone = col.item_ids.filter((id) => !chosen.includes(id));
            const added = chosen.filter((id) => !col.item_ids.includes(id));
            if (gone.length) await api.post(`${base}/items/remove`, { item_ids: gone });
            if (added.length) await api.post(`${base}/items`, { item_ids: added });
            // The server keeps the old order and appends the new items: fix it when it differs.
            const after = col.item_ids.filter((id) => chosen.includes(id)).concat(added);
            if (chosen.length && after.join() !== chosen.join()) await api.put(`${base}/items/order`, { ids: chosen });
          }
          toast(col ? 'Coleção salva' : 'Coleção criada');
          reload();
          return true;
        } catch (e) { toast(e, 'error'); reload(); return false; }
      },
    },
  ]);
  name.focus();
}

function sendCollectionDialog(ctx, col) {
  const to = h('input', { placeholder: '5511999999999', inputmode: 'tel', 'aria-label': 'Para' });
  const photos = h('input', { type: 'number', min: 0, max: 10, value: 0, class: 'mn-num', 'aria-label': 'Fotos' });
  const prev = h('div', { class: 'stack mn-send-preview' }, skeleton(1));
  api.get(`${ctx.base}/menu/collections/${encodeURIComponent(col.id)}/text`).then((v) => {
    mount(prev, (v.chunks || []).length ? (v.chunks || []).map((c) => h('div', { class: 'ag-bubble prewrap' }, c))
      : h('p', { class: 'muted' }, 'Nenhum item disponível nesta coleção.'));
  }).catch((e) => mount(prev, h('p', { class: 'error' }, errView(e))));
  sheet(`Enviar "${col.name}"`, h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'Vai pela fila de envio, com as pausas anti-ban. Itens ocultos ficam de fora; as fotos são dos primeiros itens disponíveis com foto (cada foto é uma mensagem).'),
    field('Para', to, 'Número com DDI e DDD, ou o JID da conversa.'),
    field('Fotos dos primeiros itens', photos, '0 a 10'),
    h('h3', null, 'Prévia do texto'), prev), [
    { label: 'Cancelar' },
    {
      label: 'Enviar', primary: true, onclick: async () => {
        if (!to.value.trim()) { toast('Informe o destinatário', 'error'); return false; }
        try {
          const v = await api.post(`${ctx.base}/menu/collections/${encodeURIComponent(col.id)}/send`, { to: to.value.trim(), photos: Number(photos.value) || 0 });
          toast(`Coleção na fila: ${v.messages.length} ${v.messages.length === 1 ? 'mensagem' : 'mensagens'}`);
          return true;
        } catch (e) { toast(e, 'error'); return false; }
      },
    },
  ]);
  to.focus();
}

function collectionsView(ctx, menu, images, reload) {
  const cols = menu.collections || [];
  const byID = new Map(allItems(menu).map((it) => [it.id, it]));
  const move = async (i, d) => {
    const ids = cols.map((c) => c.id);
    const j = i + d;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    try { await api.put(`${ctx.base}/menu/collections/order`, { ids }); reload(); } catch (e) { toast(e, 'error'); }
  };
  const head = h('div', { class: 'row' },
    h('p', { class: 'small muted grow' }, `Coleções agrupam itens de qualquer categoria ("Promoções", "Mais vendidos"). ${cols.length} de ${menu.limits.max_collections || 50}, até ${menu.limits.max_collection_items || 100} itens cada.`),
    h('button', { class: 'btn small primary', type: 'button', onclick: () => collectionEditor(ctx, menu, images, null, reload) }, icon('plus', 14), 'Nova coleção'));
  if (!cols.length) {
    return h('div', { class: 'stack' }, head, emptyState({ iconName: 'book', title: 'Nenhuma coleção ainda',
      text: 'Crie "Promoções" ou "Mais vendidos" e escolha os itens. Dá para enviar a coleção inteira para um cliente, com fotos.' }));
  }
  return h('div', { class: 'stack' }, head, h('div', { class: 'cat-grid' }, cols.map((c, i) => {
    const img = h('img', { class: 'cat-img', alt: c.name, loading: 'lazy' });
    if (c.cover_path) images.load(c.cover_path, img); else img.classList.add('cat-img-missing');
    const names = c.item_ids.slice(0, 4).map((id) => (byID.get(id) || {}).name).filter(Boolean);
    return h('article', { class: `cat-card${c.active ? '' : ' mn-soldout'}` }, img,
      h('div', { class: 'cat-info' },
        h('h3', { class: 'cat-name' }, c.name),
        h('p', { class: 'small muted' }, `${c.item_count} ${c.item_count === 1 ? 'item' : 'itens'}`, c.active ? '' : ' · inativa'),
        names.length ? h('p', { class: 'small' }, names.join(', '), c.item_count > names.length ? '…' : '') : null,
        h('div', { class: 'row end' },
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': `Subir ${c.name}`, disabled: i === 0, onclick: () => move(i, -1) }, '◀'),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': `Descer ${c.name}`, disabled: i === cols.length - 1, onclick: () => move(i, 1) }, '▶'),
          h('button', { class: 'btn small ghost', type: 'button', onclick: () => collectionEditor(ctx, menu, images, c, reload) }, icon('edit', 14), 'Editar'),
          h('button', { class: 'btn small ghost', type: 'button', 'aria-label': `Enviar ${c.name}`, disabled: !c.active || !c.item_count, onclick: () => sendCollectionDialog(ctx, c) }, icon('send', 14)),
          h('button', { class: 'btn small ghost danger', type: 'button', 'aria-label': `Apagar ${c.name}`, onclick: async () => {
            if (!(await confirmDanger(`Apagar a coleção "${c.name}"? Os itens continuam no cardápio.`))) return;
            try { await api.del(`${ctx.base}/menu/collections/${encodeURIComponent(c.id)}`); toast('Coleção apagada'); reload(); } catch (e) { toast(e, 'error'); }
          } }, icon('trash', 14)))));
  })));
}

// ---------- order drafts ----------

const ORDER_STATUS = { draft: ['Rascunho', ''], sent: ['Enviado', 'tone-warn'], confirmed: ['Confirmado', 'tone-ok'], cancelled: ['Cancelado', 'tone-danger'] };
const ORDER_SOURCE = { api: 'API', mcp: 'Assistente (MCP)', dashboard: 'Painel', agent: 'Agente', attendant: 'Atendente IA' };

function orderDialog(ctx, d, reload) {
  const actions = [{ label: 'Fechar' }];
  const setStatus = (status, label) => async () => {
    try { await api.patch(`${ctx.base}/menu/orders/${encodeURIComponent(d.id)}`, { status }); toast(label); reload(); return true; } catch (e) { toast(e, 'error'); return false; }
  };
  if (d.status !== 'cancelled') {
    actions.push({ label: 'Cancelar pedido', danger: true, onclick: async () => {
      if (!(await confirmDanger('Cancelar este pedido? Não dá para desfazer.'))) return false;
      return setStatus('cancelled', 'Pedido cancelado')();
    } });
  }
  if (d.status === 'draft' || d.status === 'sent') actions.push({ label: 'Marcar confirmado', onclick: setStatus('confirmed', 'Pedido confirmado') });
  if (d.status !== 'cancelled' && d.valid && d.chat) {
    actions.push({ label: d.status === 'draft' ? 'Enviar resumo' : 'Reenviar resumo', primary: true, onclick: async () => {
      try {
        const v = await api.post(`${ctx.base}/menu/orders/${encodeURIComponent(d.id)}/send`, {});
        toast(v.message.status === 'sent' ? 'Resumo enviado' : 'Resumo na fila de envio');
        reload();
        return true;
      } catch (e) { toast(e, 'error'); return false; }
    } });
  }
  const [label, tone] = ORDER_STATUS[d.status] || [d.status, ''];
  sheet(`Pedido de ${d.customer_name || phoneOf(d.chat) || 'cliente'}`, h('div', { class: 'stack' },
    h('p', { class: 'row small' }, h('span', { class: `badge ${tone}` }, label), h('span', { class: 'muted' }, ORDER_SOURCE[d.source] || d.source),
      d.chat ? h('span', { class: 'muted' }, phoneOf(d.chat)) : null, h('span', { class: 'muted' }, new Date(d.created_at).toLocaleString('pt-BR'))),
    d.problems && d.problems.length ? h('div', { class: 'error small' }, d.problems.join('; ')) : null,
    h('div', { class: 'ag-bubble prewrap' }, d.summary),
    h('p', { class: 'small muted' }, 'Nomes, endereços e observações vêm da conversa com o cliente.')), actions);
}

function ordersView(ctx, reload) {
  const status = h('select', { 'aria-label': 'Status' }, h('option', { value: '' }, 'Todos'),
    Object.entries(ORDER_STATUS).map(([k, [l]]) => h('option', { value: k }, l)));
  status.value = ctx.orderFilter || '';
  const body = h('div', { class: 'stack' }, skeleton(2));
  const load = async () => {
    ctx.orderFilter = status.value;
    let v;
    try { v = await api.get(`${ctx.base}/menu/orders?limit=100${status.value ? `&status=${encodeURIComponent(status.value)}` : ''}`); } catch (e) {
      mount(body, h('p', { class: 'error' }, errView(e)));
      return;
    }
    if (!v.drafts.length) {
      mount(body, emptyState({ iconName: 'inbox', title: 'Nenhum pedido montado',
        text: 'Pedidos montados numa conversa aparecem aqui: pelo Atendente IA, pelos agentes ou pelo seu assistente (create_order_draft). O total é sempre calculado pelo LiteZap com o cardápio.' }));
      return;
    }
    mount(body, h('div', { class: 'table-card' }, h('table', { class: 'table small' },
      h('thead', null, h('tr', null, h('th', null, 'Quando'), h('th', null, 'Cliente'), h('th', null, 'Itens'), h('th', null, 'Total'), h('th', null, 'Status'), h('th', null, 'Origem'), h('th', null, ''))),
      h('tbody', null, v.drafts.map((d) => {
        const [label, tone] = ORDER_STATUS[d.status] || [d.status, ''];
        const n = d.items.reduce((s, l) => s + (l.quantity || 0), 0);
        return h('tr', { class: d.valid ? '' : 'mn-bad' },
          h('td', null, new Date(d.created_at).toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' })),
          h('td', null, d.customer_name || phoneOf(d.chat) || h('span', { class: 'muted' }, '—')),
          h('td', null, String(n).replace('.', ',')),
          h('td', null, d.total_text),
          h('td', null, h('span', { class: `badge ${tone}` }, label), d.valid ? null : h('span', { class: 'error' }, ' com problema')),
          h('td', null, ORDER_SOURCE[d.source] || d.source),
          h('td', null, h('button', { class: 'btn small ghost', type: 'button', onclick: () => orderDialog(ctx, d, () => { load(); }) }, icon('eye', 14), 'Ver')));
      })))));
  };
  status.addEventListener('change', load);
  load();
  return h('div', { class: 'stack' },
    h('div', { class: 'row' }, h('p', { class: 'small muted grow' }, 'Carrinhos montados nas conversas, com o total calculado pelo cardápio (opções, promoções e disponibilidade).'),
      field('Status', status), h('button', { class: 'btn small', type: 'button', onclick: load }, icon('refresh', 14), 'Atualizar')),
    body);
}

// ---------- panel ----------

function allItems(menu) {
  return [...menu.categories.flatMap((c) => c.items), ...menu.uncategorized];
}

function fold(s) {
  return String(s || '').toLowerCase().normalize('NFD').replace(/[̀-ͯ]/g, '');
}

async function exportCSV(ctx) {
  try {
    const b = await api.get(`${ctx.base}/menu/export.csv`, { blob: true });
    const u = URL.createObjectURL(b);
    const a = h('a', { href: u, download: 'cardapio.csv' });
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(u), 10000);
  } catch (e) { toast(e, 'error'); }
}

export function menuPanel(ctx) {
  let alive = true;
  const images = imageLoader();
  let menu = { categories: [], uncategorized: [], collections: [], item_count: 0, limits: {} };
  ctx.menuFilter = ctx.menuFilter || '_all';
  ctx.menuView = ctx.menuView || 'items';
  ctx.menuQuery = ctx.menuQuery || { q: '', state: '', collection: '', sale: false };
  const selected = new Set();
  const side = h('aside', { class: 'mn-side card' });
  const main = h('div', { class: 'mn-main stack' });
  const head = h('div', { class: 'row card pad mn-head' });
  const tabs = h('nav', { class: 'mn-tabs row', 'aria-label': 'Seções do cardápio' });

  const reload = async () => {
    try { menu = await api.get(`${ctx.base}/menu`); } catch (e) {
      if (alive) mount(main, h('p', { class: 'error pad' }, errView(e)));
      return;
    }
    const ids = new Set(allItems(menu).map((it) => it.id));
    for (const id of [...selected]) if (!ids.has(id)) selected.delete(id);
    if (alive) draw();
  };
  const sections = () => [...menu.categories, { id: '', name: 'Outros', items: menu.uncategorized }];

  const importMenu = () => h('div', { class: 'row' },
    h('button', { class: 'btn small', type: 'button', onclick: () => csvDialog(ctx, reload) }, icon('book', 14), 'Planilha (CSV)'),
    h('button', { class: 'btn small', type: 'button', onclick: () => catalogDialog(ctx, reload) }, icon('copy', 14), 'Copiar do catálogo do WhatsApp'),
    h('button', { class: 'btn small', type: 'button', onclick: () => aiDialog(ctx, reload) }, icon('sparkles', 14), 'Montar com IA'),
    h('button', { class: 'btn small ghost', type: 'button', onclick: () => exportCSV(ctx) }, icon('book', 14), 'Exportar CSV'));

  const moveCategory = async (i, d) => {
    const ids = menu.categories.map((c) => c.id);
    const j = i + d;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    try { menu = await api.put(`${ctx.base}/menu/categories/order`, { ids }); draw(); } catch (e) { toast(e, 'error'); }
  };
  const reorderItems = async (categoryID, ids) => {
    try { menu = await api.put(`${ctx.base}/menu/items/order`, { category_id: categoryID, ids }); draw(); } catch (e) { toast(e, 'error'); reload(); }
  };

  const drawSide = () => {
    const entry = (id, label, count, extra) => h('li', { class: `mn-cat${ctx.menuFilter === id ? ' active' : ''}` },
      h('button', { class: 'mn-cat-btn', type: 'button', onclick: () => { ctx.menuFilter = id; draw(); } },
        h('span', { class: 'grow' }, label), h('span', { class: 'badge' }, String(count))),
      extra || null);
    mount(side,
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Categorias')),
      h('ul', { class: 'mn-cats' },
        entry('_all', 'Todos os itens', menu.item_count),
        menu.categories.map((c, i) => entry(c.id, c.name, c.items.length, h('span', { class: 'mn-cat-tools' },
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': `Subir ${c.name}`, disabled: i === 0, onclick: () => moveCategory(i, -1) }, '▲'),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': `Descer ${c.name}`, disabled: i === menu.categories.length - 1, onclick: () => moveCategory(i, 1) }, '▼'),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': `Renomear ${c.name}`, onclick: async () => {
            const n = await promptText('Renomear categoria', 'Nome', c.name);
            if (!n || !n.trim()) return;
            try { await api.patch(`${ctx.base}/menu/categories/${encodeURIComponent(c.id)}`, { name: n.trim() }); reload(); } catch (e) { toast(e, 'error'); }
          } }, icon('edit', 14)),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': `Apagar ${c.name}`, onclick: async () => {
            if (!(await confirmDanger(`Apagar a categoria "${c.name}"? Os itens dela ficam em "Outros".`))) return;
            try { await api.del(`${ctx.base}/menu/categories/${encodeURIComponent(c.id)}`); if (ctx.menuFilter === c.id) ctx.menuFilter = '_all'; reload(); } catch (e) { toast(e, 'error'); }
          } }, icon('trash', 14))))),
        menu.uncategorized.length ? entry('', 'Outros', menu.uncategorized.length) : null),
      h('button', { class: 'btn small block', type: 'button', onclick: async () => {
        const n = await promptText('Nova categoria', 'Nome', '');
        if (!n || !n.trim()) return;
        try { await api.post(`${ctx.base}/menu/categories`, { name: n.trim() }); reload(); } catch (e) { toast(e, 'error'); }
      } }, icon('plus', 14), 'Nova categoria'));
  };

  // matches applies the search/filter bar.
  const matches = (it) => {
    const f = ctx.menuQuery;
    if (f.q) {
      const hay = fold(`${it.name} ${it.description} ${it.sku || ''} ${(it.tags || []).join(' ')}`);
      if (!fold(f.q).split(/\s+/).filter(Boolean).every((w) => hay.includes(w))) return false;
    }
    switch (f.state) {
      case 'available': if (!it.available || it.hidden) return false; break;
      case 'soldout': if (it.available) return false; break;
      case 'hidden': if (!it.hidden) return false; break;
      default:
    }
    if (f.collection && !(it.collection_ids || []).includes(f.collection)) return false;
    if (f.sale && !it.on_sale) return false;
    return true;
  };
  const filtering = () => { const f = ctx.menuQuery; return !!(f.q || f.state || f.collection || f.sale); };

  const toolbar = () => {
    const q = h('input', { type: 'search', value: ctx.menuQuery.q, placeholder: 'Buscar por nome, descrição, SKU ou tag', 'aria-label': 'Buscar itens' });
    const state = h('select', { 'aria-label': 'Situação' }, h('option', { value: '' }, 'Todos'), h('option', { value: 'available' }, 'Disponíveis'),
      h('option', { value: 'soldout' }, 'Esgotados'), h('option', { value: 'hidden' }, 'Ocultos'));
    state.value = ctx.menuQuery.state;
    const col = h('select', { 'aria-label': 'Coleção' }, h('option', { value: '' }, 'Qualquer coleção'), (menu.collections || []).map((c) => h('option', { value: c.id }, c.name)));
    col.value = ctx.menuQuery.collection;
    const sale = h('input', { type: 'checkbox', checked: ctx.menuQuery.sale });
    const apply = () => { ctx.menuQuery = { q: q.value.trim(), state: state.value, collection: col.value, sale: sale.checked }; drawItems(); };
    let timer = null;
    q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(apply, 200); });
    for (const el of [state, col, sale]) el.addEventListener('change', apply);
    return h('div', { class: 'row mn-toolbar' }, q, state, (menu.collections || []).length ? col : null, h('label', { class: 'row small' }, sale, 'Em promoção'));
  };

  const bulkBar = h('div', { class: 'row card pad mn-bulk', hidden: true });
  const drawBulk = () => {
    bulkBar.hidden = selected.size === 0;
    if (!selected.size) { mount(bulkBar); return; }
    const ids = [...selected];
    const n = ids.length;
    mount(bulkBar,
      h('strong', null, `${n} ${n === 1 ? 'selecionado' : 'selecionados'}`),
      h('button', { class: 'btn small', type: 'button', onclick: () => runBulk(ctx, ids, 'set_available', { available: true }, reload, 'Disponível') }, 'Disponível'),
      h('button', { class: 'btn small', type: 'button', onclick: () => runBulk(ctx, ids, 'set_available', { available: false }, reload, 'Esgotado') }, 'Esgotado'),
      h('button', { class: 'btn small', type: 'button', onclick: () => runBulk(ctx, ids, 'set_hidden', { hidden: true }, reload, 'Ocultos') }, icon('eyeOff', 14), 'Ocultar'),
      h('button', { class: 'btn small', type: 'button', onclick: () => runBulk(ctx, ids, 'set_hidden', { hidden: false }, reload, 'Visíveis') }, icon('eye', 14), 'Mostrar'),
      h('button', { class: 'btn small', type: 'button', onclick: async () => {
        const id = await pickDialog('Mover para a categoria', 'Categoria', [{ id: '', name: 'Sem categoria (Outros)' }, ...menu.categories]);
        if (id !== null) runBulk(ctx, ids, 'move_category', { category_id: id }, reload, 'Movidos');
      } }, 'Mover…'),
      (menu.collections || []).length ? h('button', { class: 'btn small', type: 'button', onclick: async () => {
        const id = await pickDialog('Adicionar à coleção', 'Coleção', menu.collections);
        if (id) runBulk(ctx, ids, 'add_to_collection', { collection_id: id }, reload, 'Na coleção');
      } }, 'Coleção…') : null,
      h('button', { class: 'btn small', type: 'button', onclick: () => bulkPriceDialog(ctx, ids, reload) }, 'Reajustar preço…'),
      h('button', { class: 'btn small', type: 'button', onclick: () => runBulk(ctx, ids, 'clear_sale', {}, reload, 'Sem promoção') }, 'Tirar promoção'),
      h('button', { class: 'btn small danger', type: 'button', onclick: async () => {
        if (!(await confirmDanger(`Apagar ${n} ${n === 1 ? 'item' : 'itens'} do cardápio, com as fotos? Não dá para desfazer.`))) return;
        if (await runBulk(ctx, ids, 'delete', {}, reload, 'Apagados')) selected.clear();
      } }, icon('trash', 14), 'Apagar'),
      h('button', { class: 'btn small ghost', type: 'button', onclick: () => { selected.clear(); drawItems(); } }, 'Limpar seleção'));
  };

  let dragged = null; // {id}
  const card = (it, section, index, list) => {
    const img = h('img', { class: 'cat-img', alt: it.name, loading: 'lazy' });
    if (it.image_path) images.load(it.image_path, img); else img.classList.add('cat-img-missing');
    const sw = h('input', { type: 'checkbox', class: 'ag-switch', checked: it.available, 'aria-label': `${it.name} disponível` });
    sw.addEventListener('change', async () => {
      sw.disabled = true;
      try {
        await api.put(`${ctx.base}/menu/items/${encodeURIComponent(it.id)}/available`, { available: sw.checked });
        it.available = sw.checked;
        el.classList.toggle('mn-soldout', !it.available);
        mount(state, it.available ? 'Disponível' : 'Esgotado');
      } catch (e) { sw.checked = !sw.checked; toast(e, 'error'); } finally { sw.disabled = false; }
    });
    const state = h('span', { class: 'ag-switch-label small' }, it.available ? 'Disponível' : 'Esgotado');
    const pick = h('input', { type: 'checkbox', class: 'mn-pick', checked: selected.has(it.id), 'aria-label': `Selecionar ${it.name}` });
    pick.addEventListener('change', () => { if (pick.checked) selected.add(it.id); else selected.delete(it.id); el.classList.toggle('mn-selected', pick.checked); drawBulk(); });
    const move = (d) => {
      const ids = list.map((x) => x.id);
      const j = index + d;
      if (j < 0 || j >= ids.length) return;
      [ids[index], ids[j]] = [ids[j], ids[index]];
      reorderItems(section.id, ids);
    };
    const canMove = !filtering();
    const el = h('article', { class: `cat-card mn-card${it.available ? '' : ' mn-soldout'}${it.hidden ? ' mn-hidden' : ''}${selected.has(it.id) ? ' mn-selected' : ''}`, draggable: canMove ? 'true' : 'false' },
      h('div', { class: 'mn-card-media' }, img, pick,
        h('div', { class: 'mn-card-badges' },
          it.on_sale ? h('span', { class: 'badge tone-ok' }, 'Promoção') : null,
          it.hidden ? h('span', { class: 'badge' }, icon('eyeOff', 12), 'Oculto') : null,
          (it.images || []).length > 1 ? h('span', { class: 'badge' }, `${it.images.length} fotos`) : null)),
      h('div', { class: 'cat-info' },
        h('h3', { class: 'cat-name' }, it.name),
        h('p', { class: 'cat-price' }, priceNode(it)),
        it.options.length ? h('p', { class: 'small muted' }, `${it.options.length} ${it.options.length === 1 ? 'grupo de opções' : 'grupos de opções'}`) : null,
        h('label', { class: 'row' }, sw, state),
        h('div', { class: 'row end' },
          canMove ? h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mover para trás', disabled: index === 0, onclick: () => move(-1) }, '◀') : null,
          canMove ? h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mover para frente', disabled: index === list.length - 1, onclick: () => move(1) }, '▶') : null,
          h('button', { class: 'btn small ghost', type: 'button', onclick: () => itemEditor(ctx, menu, images, it, reload) }, icon('edit', 14), 'Editar'),
          h('button', { class: 'btn small ghost', type: 'button', 'aria-label': `Duplicar ${it.name}`, title: 'Duplicar (para fazer uma variação)', onclick: async () => {
            try { const v = await api.post(`${ctx.base}/menu/items/${encodeURIComponent(it.id)}/duplicate`, {}); toast(`Cópia criada: ${v.name}`); reload(); } catch (e) { toast(e, 'error'); }
          } }, icon('copy', 14)),
          h('button', { class: 'btn small ghost', type: 'button', 'aria-label': `Enviar ${it.name}`, disabled: !it.available || it.hidden, onclick: () => sendDialog(ctx, menu, { item: it }) }, icon('send', 14)),
          h('button', { class: 'btn small ghost danger', type: 'button', 'aria-label': `Apagar ${it.name}`, onclick: async () => {
            if (!(await confirmDanger(`Apagar "${it.name}" do cardápio?`))) return;
            try { await api.del(`${ctx.base}/menu/items/${encodeURIComponent(it.id)}`); selected.delete(it.id); toast('Item apagado'); reload(); } catch (e) { toast(e, 'error'); }
          } }, icon('trash', 14)))));
    if (canMove) {
      el.addEventListener('dragstart', (ev) => { dragged = { id: it.id }; el.classList.add('mn-dragging'); ev.dataTransfer.effectAllowed = 'move'; ev.dataTransfer.setData('text/plain', it.id); });
      el.addEventListener('dragend', () => { el.classList.remove('mn-dragging'); dragged = null; });
      el.addEventListener('dragover', (ev) => { if (dragged) { ev.preventDefault(); el.classList.add('mn-over'); } });
      el.addEventListener('dragleave', () => el.classList.remove('mn-over'));
      el.addEventListener('drop', (ev) => {
        ev.preventDefault();
        el.classList.remove('mn-over');
        if (!dragged || dragged.id === it.id) return;
        const ids = list.map((x) => x.id).filter((id) => id !== dragged.id);
        ids.splice(ids.indexOf(it.id), 0, dragged.id);
        reorderItems(section.id, ids);
      });
    }
    return el;
  };

  const empty = () => emptyState({
    iconName: 'book',
    title: 'Monte o cardápio do seu negócio',
    text: 'O Cardápio fica guardado no LiteZap (não mexe no WhatsApp nem na Meta). O Atendente IA e os agentes usam os itens, preços e opções para responder e montar pedidos. Comece por um destes caminhos:',
    action: h('div', { class: 'mn-paths' },
      h('button', { class: 'mn-path card', type: 'button', onclick: () => csvDialog(ctx, reload) },
        icon('book', 22), h('strong', null, 'Importar planilha'), h('span', { class: 'small muted' }, 'CSV do Excel ou Google Planilhas, com pré-visualização.')),
      h('button', { class: 'mn-path card', type: 'button', onclick: () => catalogDialog(ctx, reload) },
        icon('copy', 22), h('strong', null, 'Copiar do catálogo do WhatsApp'), h('span', { class: 'small muted' }, 'Um clique, com fotos e coleções. Só leitura no WhatsApp.')),
      h('button', { class: 'mn-path card', type: 'button', onclick: () => aiDialog(ctx, reload) },
        icon('sparkles', 22), h('strong', null, 'Montar com IA'), h('span', { class: 'small muted' }, 'Com o seu Claude ou ChatGPT (assinatura): texto ou foto do cardápio, você revisa antes de salvar.')),
      h('button', { class: 'btn small', type: 'button', onclick: () => itemEditor(ctx, menu, images, null, reload) }, icon('plus', 14), 'Ou crie um item à mão')),
  });

  const itemsBox = h('div', { class: 'stack' });
  const drawItems = () => {
    const shown = sections()
      .filter((s) => ctx.menuFilter === '_all' ? (s.items.length || s.id) : s.id === ctx.menuFilter)
      .map((s) => ({ s, items: s.items.filter(matches) }))
      .filter((x) => !filtering() || x.items.length);
    mount(itemsBox, shown.length ? shown.map(({ s, items }) => h('section', { class: 'stack' },
      h('h3', { class: 'mn-sec' }, s.name, h('span', { class: 'muted small' }, ` · ${items.length}`)),
      items.length ? h('div', { class: 'cat-grid' }, items.map((it, i) => card(it, s, i, s.items)))
        : h('p', { class: 'muted small' }, 'Sem itens nesta categoria. Crie um item ou mova um para cá pelo editor.')))
      : h('p', { class: 'muted' }, 'Nenhum item com esses filtros.'));
    drawBulk();
  };

  const drawTabs = () => {
    const tab = (id, label, count) => h('button', { class: `chip${ctx.menuView === id ? ' active' : ''}`, type: 'button', 'aria-pressed': ctx.menuView === id ? 'true' : 'false',
      onclick: () => { ctx.menuView = id; draw(); } }, label, count === undefined ? null : h('span', { class: 'muted' }, ` ${count}`));
    mount(tabs, tab('items', 'Itens', menu.item_count), tab('collections', 'Coleções', (menu.collections || []).length), tab('orders', 'Pedidos montados'));
  };

  const draw = () => {
    if (ctx.menuFilter !== '_all' && ctx.menuFilter !== '' && !menu.categories.some((c) => c.id === ctx.menuFilter)) ctx.menuFilter = '_all';
    mount(head,
      h('div', { class: 'stack grow' },
        h('h2', null, 'Cardápio'),
        h('p', { class: 'small muted' }, `Catálogo próprio do LiteZap: ${menu.item_count} de ${menu.limits.max_items || 500} itens. Nada daqui é enviado para a Meta; o WhatsApp só recebe as mensagens que você (ou o atendente) enviar.`)),
      h('div', { class: 'row' },
        h('button', { class: 'btn small primary', type: 'button', onclick: () => itemEditor(ctx, menu, images, null, reload) }, icon('plus', 14), 'Novo item'),
        menu.item_count ? h('button', { class: 'btn small', type: 'button', onclick: () => sendDialog(ctx, menu, { category: ctx.menuFilter !== '_all' ? ctx.menuFilter : '' }) }, icon('send', 14), 'Enviar cardápio para…') : null),
      menu.item_count ? h('div', { class: 'row mn-import' }, h('span', { class: 'small muted' }, 'Importar:'), importMenu()) : null);
    if (!menu.item_count && !menu.categories.length && !(menu.collections || []).length && ctx.menuView === 'items') {
      mount(side);
      side.hidden = true;
      layout.classList.add('mn-no-side');
      mount(tabs);
      mount(main, empty());
      return;
    }
    drawTabs();
    layout.classList.toggle('mn-no-side', ctx.menuView !== 'items');
    if (ctx.menuView === 'collections') {
      side.hidden = true;
      mount(main, tabs, collectionsView(ctx, menu, images, reload));
      return;
    }
    if (ctx.menuView === 'orders') {
      side.hidden = true;
      mount(main, tabs, ordersView(ctx, reload));
      return;
    }
    side.hidden = false;
    drawSide();
    mount(main, tabs, toolbar(), bulkBar, itemsBox);
    drawItems();
  };

  const layout = h('div', { class: 'mn-layout' }, side, main);
  const el = h('div', { class: 'stack' }, head, layout);
  mount(main, skeleton(4));
  reload();
  return { el, cleanup() { alive = false; images.clear(); } };
}
