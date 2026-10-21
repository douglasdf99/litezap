// "Cardápio" tab: LiteZap's own catalog (kept in LiteZap, never written to Meta or
// WhatsApp). Categories on the side, item cards with photo, price and an esgotado switch,
// an item editor with options, three import paths (spreadsheet, WhatsApp catalog copy,
// "Montar com IA" with review before saving) and "Enviar cardápio para…". Every value
// reaches the page as text (h() builds text nodes), photos load as blobs through the API.
import * as api from './api.js';
import { h, mount, toast, errMsg, errView, field, emptyState, dialog, confirmDanger, promptText, skeleton, icon } from './ui.js';

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

// caption mirrors the server's WhatsApp text of an item (menu.Caption).
function caption(it) {
  let s = `*${it.name || 'Item'}*`;
  if (it.price_cents > 0) s += ` — ${brl(it.price_cents)}`;
  if (it.available === false) s += ' (esgotado)';
  if (it.description) s += `\n${it.description}`;
  for (const o of it.options || []) s += `\n${optionLine(o)}`;
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

function itemEditor(ctx, menu, images, item, onSaved) {
  const it = item || { name: '', description: '', price_cents: 0, available: true, options: [], tags: [], category_id: '' };
  const name = h('input', { value: it.name, maxlength: 120, required: true });
  const cat = h('select', null, h('option', { value: '' }, 'Sem categoria'),
    menu.categories.map((c) => h('option', { value: c.id }, c.name)));
  cat.value = it.category_id || (ctx.menuFilter && ctx.menuFilter !== '_all' ? ctx.menuFilter : '');
  const desc = h('textarea', { rows: 3, maxlength: 1000 }, it.description || '');
  const price = h('input', { value: centsText(it.price_cents), placeholder: '0,00', inputmode: 'decimal' });
  const avail = h('input', { type: 'checkbox', class: 'ag-switch', checked: it.available !== false });
  const sku = h('input', { value: it.sku || '', maxlength: 64, placeholder: 'Opcional' });
  const tags = h('input', { value: (it.tags || []).join(', '), placeholder: 'vegano, picante' });
  const opts = optionsEditor(it.options);
  // Photo: a file (sent as base64, resized by the server), a link, or none.
  let photoFile = null;
  let removePhoto = false;
  const img = h('img', { class: 'mn-photo', alt: '' });
  if (it.image_path) images.load(it.image_path, img); else img.classList.add('cat-img-missing');
  const file = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/gif', 'aria-label': 'Foto' });
  const url = h('input', { type: 'url', placeholder: 'https://… (link de uma foto)', 'aria-label': 'Link da foto' });
  file.addEventListener('change', () => {
    photoFile = file.files[0] || null;
    removePhoto = false;
    if (photoFile) { img.classList.remove('cat-img-missing'); img.src = URL.createObjectURL(photoFile); }
  });
  const preview = h('div', { class: 'ag-bubble prewrap mn-preview' });
  const read = () => ({ name: name.value.trim(), category_id: cat.value, description: desc.value.trim(), price: price.value.trim() || '0',
    available: avail.checked, sku: sku.value.trim(), tags: tags.value.split(',').map((s) => s.trim()).filter(Boolean), options: opts.read() });
  const refresh = () => {
    const v = read();
    const cents = parseBRL(v.price);
    mount(preview, caption({ ...v, price_cents: Number.isNaN(cents) ? 0 : cents }));
  };
  const form = h('div', { class: 'mn-editor' },
    h('div', { class: 'stack' },
      field('Nome', name),
      h('div', { class: 'grid2' }, field('Preço (R$)', price, 'Ex.: 45,90'), field('Categoria', cat)),
      field('Descrição', desc),
      h('label', { class: 'row' }, avail, h('span', { class: 'ag-switch-label' }, 'Disponível'), h('span', { class: 'hint' }, 'desligado = esgotado')),
      h('div', { class: 'grid2' }, field('SKU', sku), field('Tags', tags, 'Separadas por vírgula')),
      h('h3', null, 'Opções'), opts.el),
    h('div', { class: 'stack' },
      h('h3', null, 'Foto'), img, file, url,
      it.image_path ? h('button', { class: 'btn small ghost danger', type: 'button', onclick: () => { removePhoto = true; photoFile = null; img.classList.add('cat-img-missing'); } }, icon('trash', 14), 'Remover foto') : null,
      h('p', { class: 'hint' }, 'JPEG, PNG ou GIF até 8 MB e 10 megapixels; o LiteZap reduz para 1080 px.'),
      h('h3', null, 'Prévia no WhatsApp'), preview));
  form.addEventListener('input', refresh);
  form.addEventListener('click', () => setTimeout(refresh, 0));
  refresh();
  sheet(item ? `Editar ${it.name}` : 'Novo item', form, [
    { label: 'Cancelar' },
    {
      label: item ? 'Salvar' : 'Criar item', primary: true, onclick: async () => {
        const v = read();
        if (!v.name) { toast('Informe o nome do item', 'error'); return false; }
        if (Number.isNaN(parseBRL(v.price))) { toast('Preço inválido: use 45,90', 'error'); return false; }
        for (const g of v.options) {
          if (g.choices.some((c) => Number.isNaN(c.price_delta_cents))) { toast(`Acréscimo inválido em "${g.name}"`, 'error'); return false; }
        }
        try {
          let saved = item ? await api.patch(`${ctx.base}/menu/items/${encodeURIComponent(item.id)}`, v) : await api.post(`${ctx.base}/menu/items`, v);
          if (photoFile) {
            saved = await api.put(`${ctx.base}/menu/items/${encodeURIComponent(saved.id)}/image`, { base64: await readFile(photoFile) });
          } else if (url.value.trim()) {
            saved = await api.put(`${ctx.base}/menu/items/${encodeURIComponent(saved.id)}/image`, { url: url.value.trim() });
          } else if (removePhoto && item) {
            saved = await api.del(`${ctx.base}/menu/items/${encodeURIComponent(saved.id)}/image`);
          }
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
      preview.errors.length ? h('div', { class: 'error' }, preview.errors.join('; ')) : null,
      rowsTable(preview.rows).table);
  };
  sheet('Importar planilha (CSV)', h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'No Excel ou Google Planilhas: Arquivo > Fazer download > CSV. Colunas: categoria, nome, descricao, preco (45,90 ou R$ 1.234,50), disponivel (sim/não/esgotado), imagem_url e opcoes (opcional: "Tamanho: P | M +5,00; Borda (opcional): Catupiry +8").'),
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
    h('p', { class: 'small muted' }, 'Lê os produtos do catálogo deste número no WhatsApp (só leitura: nada é alterado no WhatsApp) e copia nome, descrição, preço, disponibilidade e a primeira foto para o Cardápio do LiteZap. Produtos ocultos ou em análise ficam de fora; os já copiados são pulados.'),
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

function aiDialog(ctx, reload) {
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
        ? 'Configure um provedor de IA na aba Atendente IA (há opções gratuitas, como o Ollama no computador).' : errView(e)));
    } finally { if (btn) btn.disabled = false; }
  };
  const buildBtn = h('button', { class: 'btn', type: 'button' }, icon('sparkles', 15), 'Montar itens');
  buildBtn.addEventListener('click', () => build(buildBtn));
  sheet('Montar com IA', h('div', { class: 'stack' },
    h('p', { class: 'small muted' }, 'Cole o cardápio como texto livre. A IA configurada no Atendente IA (qualquer provedor, inclusive o Ollama gratuito) organiza em itens para você revisar; nada é salvo sem a sua confirmação.'),
    text, h('div', null, buildBtn), out), [
    { label: 'Cancelar' },
    {
      label: 'Salvar itens marcados', primary: true, onclick: async () => {
        if (!review) { toast('Monte os itens primeiro', 'error'); return false; }
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
  ]);
  text.focus();
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
    h('p', { class: 'small muted' }, 'Vai pela fila de envio, com as mesmas pausas anti-ban das outras mensagens. Itens esgotados ficam de fora.'),
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

// ---------- panel ----------

export function menuPanel(ctx) {
  let alive = true;
  const images = imageLoader();
  let menu = { categories: [], uncategorized: [], item_count: 0, limits: {} };
  ctx.menuFilter = ctx.menuFilter || '_all';
  const side = h('aside', { class: 'mn-side card' });
  const main = h('div', { class: 'mn-main stack' });
  const head = h('div', { class: 'row card pad mn-head' });

  const reload = async () => {
    try { menu = await api.get(`${ctx.base}/menu`); } catch (e) {
      if (alive) mount(main, h('p', { class: 'error pad' }, errView(e)));
      return;
    }
    if (alive) draw();
  };
  const sections = () => [...menu.categories, { id: '', name: 'Outros', items: menu.uncategorized }];

  const importMenu = () => h('div', { class: 'row' },
    h('button', { class: 'btn small', type: 'button', onclick: () => csvDialog(ctx, reload) }, icon('book', 14), 'Planilha (CSV)'),
    h('button', { class: 'btn small', type: 'button', onclick: () => catalogDialog(ctx, reload) }, icon('copy', 14), 'Copiar do catálogo do WhatsApp'),
    h('button', { class: 'btn small', type: 'button', onclick: () => aiDialog(ctx, reload) }, icon('sparkles', 14), 'Montar com IA'));

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

  let dragged = null; // {id, from}
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
    const move = (d) => {
      const ids = list.map((x) => x.id);
      const j = index + d;
      if (j < 0 || j >= ids.length) return;
      [ids[index], ids[j]] = [ids[j], ids[index]];
      reorderItems(section.id, ids);
    };
    const el = h('article', { class: `cat-card mn-card${it.available ? '' : ' mn-soldout'}`, draggable: 'true' },
      img,
      h('div', { class: 'cat-info' },
        h('h3', { class: 'cat-name' }, it.name),
        h('p', { class: 'cat-price' }, it.price_cents ? it.price_text : h('span', { class: 'muted' }, 'Sem preço')),
        it.options.length ? h('p', { class: 'small muted' }, `${it.options.length} ${it.options.length === 1 ? 'grupo de opções' : 'grupos de opções'}`) : null,
        h('label', { class: 'row' }, sw, state),
        h('div', { class: 'row end' },
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mover para trás', disabled: index === 0, onclick: () => move(-1) }, '◀'),
          h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Mover para frente', disabled: index === list.length - 1, onclick: () => move(1) }, '▶'),
          h('button', { class: 'btn small ghost', type: 'button', onclick: () => itemEditor(ctx, menu, images, it, reload) }, icon('edit', 14), 'Editar'),
          h('button', { class: 'btn small ghost', type: 'button', 'aria-label': `Enviar ${it.name}`, disabled: !it.available, onclick: () => sendDialog(ctx, menu, { item: it }) }, icon('send', 14)),
          h('button', { class: 'btn small ghost danger', type: 'button', 'aria-label': `Apagar ${it.name}`, onclick: async () => {
            if (!(await confirmDanger(`Apagar "${it.name}" do cardápio?`))) return;
            try { await api.del(`${ctx.base}/menu/items/${encodeURIComponent(it.id)}`); toast('Item apagado'); reload(); } catch (e) { toast(e, 'error'); }
          } }, icon('trash', 14)))));
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
        icon('copy', 22), h('strong', null, 'Copiar do catálogo do WhatsApp'), h('span', { class: 'small muted' }, 'Um clique, com as fotos. Só leitura no WhatsApp.')),
      h('button', { class: 'mn-path card', type: 'button', onclick: () => aiDialog(ctx, reload) },
        icon('sparkles', 22), h('strong', null, 'Montar com IA'), h('span', { class: 'small muted' }, '"pizza calabresa 45, coca 2L 12": você revisa antes de salvar.')),
      h('button', { class: 'btn small', type: 'button', onclick: () => itemEditor(ctx, menu, images, null, reload) }, icon('plus', 14), 'Ou crie um item à mão')),
  });

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
    if (!menu.item_count && !menu.categories.length) {
      mount(side);
      side.hidden = true;
      mount(main, empty());
      return;
    }
    side.hidden = false;
    drawSide();
    const shown = sections().filter((s) => ctx.menuFilter === '_all' ? (s.items.length || s.id) : s.id === ctx.menuFilter);
    mount(main, shown.map((s) => h('section', { class: 'stack' },
      h('h3', { class: 'mn-sec' }, s.name, h('span', { class: 'muted small' }, ` · ${s.items.length}`)),
      s.items.length ? h('div', { class: 'cat-grid' }, s.items.map((it, i) => card(it, s, i, s.items)))
        : h('p', { class: 'muted small' }, 'Sem itens nesta categoria. Crie um item ou arraste um para cá pelo editor.'))));
  };

  const el = h('div', { class: 'stack' }, head, h('div', { class: 'mn-layout' }, side, main));
  mount(main, skeleton(4));
  reload();
  return { el, cleanup() { alive = false; images.clear(); } };
}
