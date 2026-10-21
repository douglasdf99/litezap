// "Perfil da empresa" (marco 12): edits the WhatsApp Business profile of the account
// (description, address, map pin, e-mail, sites, categories and business hours). Shown in
// the "Perfil" tab only for Business accounts. No external calls: the map links open in a
// new tab only when the user clicks them.
import * as api from './api.js';
import { h, mount, toast, errMsg, field } from './ui.js';

const DAYS = [['sun', 'Domingo'], ['mon', 'Segunda'], ['tue', 'Terça'], ['wed', 'Quarta'], ['thu', 'Quinta'], ['fri', 'Sexta'], ['sat', 'Sábado']];
const MODES = [['open', 'Aberto'], ['closed', 'Fechado'], ['24h', '24 horas'], ['appointment', 'Com agendamento']];

const encode = (s) => encodeURIComponent(s).replace(/[!'()*]/g, (c) => '%' + c.charCodeAt(0).toString(16).toUpperCase());
const num = (v) => (v.trim() === '' ? null : Number(v.replace(',', '.')));
const sameList = (a, b) => a.length === b.length && a.every((v, i) => v === b[i]);

function pinURL(lat, lng) {
  return `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lng}#map=17/${lat}/${lng}`;
}

// One weekday: mode plus open/close; split hours (intervals) are kept while the row is
// left as it was.
function dayRow(key, label, orig) {
  const mode = h('select', { 'aria-label': `${label}: situação` }, MODES.map(([v, l]) => h('option', { value: v }, l)));
  mode.value = orig.mode || 'closed';
  const first = (orig.intervals && orig.intervals[0]) || orig;
  const open = h('input', { type: 'time', value: first.open || '09:00', 'aria-label': `${label}: abre` });
  const close = h('input', { type: 'time', value: first.close === '24:00' ? '23:59' : (first.close || '18:00'), 'aria-label': `${label}: fecha` });
  const times = h('span', { class: 'row' }, open, h('span', { class: 'muted small' }, 'às'), close);
  const extra = orig.intervals && orig.intervals.length > 1
    ? h('small', { class: 'muted' }, `Turnos: ${orig.intervals.map((i) => `${i.open}–${i.close}`).join(', ')} (mantidos se este dia não mudar)`) : null;
  const sync = () => { times.style.display = mode.value === 'open' ? '' : 'none'; };
  mode.addEventListener('change', sync);
  sync();
  const untouched = () => mode.value === (orig.mode || 'closed') && open.value === (first.open || '09:00') &&
    close.value === (first.close === '24:00' ? '23:59' : (first.close || '18:00'));
  return {
    el: h('div', { class: 'biz-day' }, h('span', { class: 'biz-day-name' }, label), mode, times, extra),
    changed: () => !untouched(),
    read() {
      if (untouched() && orig.mode) return orig; // exactly as WhatsApp has it (split hours too)
      if (mode.value !== 'open') return { mode: mode.value };
      return { mode: 'open', open: open.value, close: close.value === '23:59' && first.close === '24:00' ? '24:00' : close.value };
    },
    key,
  };
}

export function businessProfileBox(ctx) {
  const box = h('div', { class: 'card settings' });
  box.style.display = 'none';
  let alive = true;

  const render = (p) => {
    const desc = h('textarea', { rows: 3, maxlength: 512 }, p.description || '');
    const address = h('input', { value: p.address || '', maxlength: 256 });
    const email = h('input', { type: 'email', value: p.email || '', maxlength: 128 });
    const sites = [0, 1].map((i) => h('input', { type: 'url', placeholder: 'https://', value: (p.websites || [])[i] || '', 'aria-label': `Site ${i + 1}` }));
    const loc = p.location || null;
    const lat = h('input', { inputmode: 'decimal', value: loc ? String(loc.latitude) : '', placeholder: '-23.5505', 'aria-label': 'Latitude' });
    const lng = h('input', { inputmode: 'decimal', value: loc ? String(loc.longitude) : '', placeholder: '-46.6333', 'aria-label': 'Longitude' });
    // Pasting "lat, lng" (as maps copy it) into the latitude fills both.
    lat.addEventListener('paste', (ev) => {
      const t = (ev.clipboardData && ev.clipboardData.getData('text')) || '';
      const m = t.match(/^\s*(-?\d+(?:\.\d+)?)\s*[,; ]\s*(-?\d+(?:\.\d+)?)\s*$/);
      if (m) { ev.preventDefault(); lat.value = m[1]; lng.value = m[2]; refreshMap(); }
    });
    const mapLink = h('a', { class: 'btn small', target: '_blank', rel: 'noopener noreferrer' }, 'Ver no mapa');
    const searchLink = h('a', { class: 'btn small', target: '_blank', rel: 'noopener noreferrer' }, 'Usar endereço');
    const refreshMap = () => {
      const la = num(lat.value); const lo = num(lng.value);
      const ok = la !== null && lo !== null && Number.isFinite(la) && Number.isFinite(lo) && Math.abs(la) <= 90 && Math.abs(lo) <= 180;
      mapLink.style.display = ok ? '' : 'none';
      if (ok) mapLink.setAttribute('href', pinURL(la, lo));
      const q = address.value.trim();
      searchLink.style.display = q ? '' : 'none';
      if (q) searchLink.setAttribute('href', `https://www.openstreetmap.org/search?query=${encode(q)}`);
    };
    lat.addEventListener('input', refreshMap);
    lng.addEventListener('input', refreshMap);
    address.addEventListener('input', refreshMap);
    refreshMap();

    // Categories: the current ones (id + name), removable; more by id.
    let cats = (p.category_list || []).map((c) => ({ id: c.id, name: c.name }));
    const origCats = cats.map((c) => c.id);
    const catList = h('div', { class: 'row' });
    const catInput = h('input', { inputmode: 'numeric', placeholder: 'id da categoria', 'aria-label': 'Id da categoria' });
    const drawCats = () => mount(catList, cats.length ? cats.map((c, i) => h('span', { class: 'biz-chip' }, c.name || c.id,
      h('button', { type: 'button', class: 'biz-chip-x', 'aria-label': `Remover ${c.name || c.id}`, onclick: () => { cats.splice(i, 1); drawCats(); } }, '×')))
      : h('span', { class: 'muted small' }, 'Nenhuma categoria.'));
    drawCats();
    const addCat = h('button', {
      type: 'button', class: 'btn small', onclick: () => {
        const id = catInput.value.trim();
        if (!/^\d{1,32}$/.test(id)) { toast('O id da categoria tem só números', 'error'); return; }
        if (cats.length >= 3) { toast('No máximo 3 categorias', 'error'); return; }
        if (!cats.some((c) => c.id === id)) cats.push({ id, name: `#${id}` });
        catInput.value = '';
        drawCats();
      },
    }, 'Adicionar');

    // Hours.
    const week = p.business_hours || { timezone: '', days: {} };
    const tz = h('input', { value: week.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || 'America/Sao_Paulo', 'aria-label': 'Fuso horário' });
    const rows = DAYS.map(([k, l]) => dayRow(k, l, (week.days || {})[k] || { mode: 'closed' }));

    const save = h('button', { class: 'btn primary', type: 'submit' }, 'Salvar perfil da empresa');
    mount(box, h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        const body = {};
        if (desc.value !== (p.description || '')) body.description = desc.value;
        if (address.value.trim() !== (p.address || '')) body.address = address.value.trim();
        if (email.value.trim() !== (p.email || '')) body.email = email.value.trim();
        const ws = sites.map((s) => s.value.trim()).filter(Boolean);
        if (!sameList(ws, p.websites || [])) body.websites = ws;
        const la = num(lat.value); const lo = num(lng.value);
        if ((la === null) !== (lo === null)) { toast('Preencha latitude e longitude juntas', 'error'); return; }
        if (la !== null && (!loc || la !== loc.latitude || lo !== loc.longitude)) {
          if (!Number.isFinite(la) || !Number.isFinite(lo)) { toast('Latitude e longitude são números', 'error'); return; }
          body.latitude = la; body.longitude = lo;
        }
        const ids = cats.map((c) => c.id);
        if (!sameList(ids, origCats)) {
          if (!ids.length) { toast('Escolha ao menos uma categoria', 'error'); return; }
          body.categories = ids;
        }
        if (rows.some((r) => r.changed()) || tz.value.trim() !== (week.timezone || '')) {
          const days = {};
          for (const r of rows) days[r.key] = r.read();
          body.hours = { timezone: tz.value.trim(), days };
        }
        if (!Object.keys(body).length) { toast('Nada mudou'); return; }
        save.disabled = true;
        try {
          const v = await api.patch(`${ctx.base}/business/profile`, body);
          toast('Perfil da empresa salvo');
          if (alive) render(v);
        } catch (e) { toast(e, 'error'); save.disabled = false; }
      },
    },
    h('h2', null, 'Perfil da empresa'),
    h('p', { class: 'small muted' }, 'É o que os clientes veem no seu perfil do WhatsApp Business. Só os campos alterados são enviados.'),
    field('Descrição', desc, 'Até 512 caracteres.'),
    field('Endereço', address),
    h('div', { class: 'grid2' }, field('Latitude', lat), field('Longitude', lng)),
    h('div', { class: 'row' }, mapLink, searchLink),
    h('p', { class: 'small muted' }, '"Usar endereço" abre o endereço no OpenStreetMap numa nova aba: clique no local, copie as coordenadas e cole aqui (colar "lat, long" na latitude preenche as duas). Nada é enviado a serviços de mapa sem você clicar.'),
    h('div', { class: 'grid2' }, field('E-mail', email), field('Site 1', sites[0]), field('Site 2', sites[1])),
    field('Categorias', h('div', null, catList, h('div', { class: 'row' }, catInput, addCat)), 'Até 3. Para adicionar, use o id da categoria (o WhatsApp não tem busca de categorias pela API).'),
    h('h3', null, 'Horário de funcionamento'),
    field('Fuso horário', tz, 'Nome IANA, como America/Sao_Paulo.'),
    h('div', { class: 'biz-week' }, rows.map((r) => r.el)),
    h('div', { class: 'row end' }, save)));
  };

  const load = () => api.get(`${ctx.base}/business/profile`)
    .then((p) => { if (!alive) return; box.style.display = ''; render(p); })
    .catch((e) => {
      if (!alive) return;
      if (e.code === 'business_account_required' || e.code === 'not_implemented') { box.style.display = 'none'; return; }
      box.style.display = '';
      mount(box, h('h2', null, 'Perfil da empresa'), h('p', { class: 'muted' }, e.code === 'instance_not_connected'
        ? 'Instância desconectada: conecte para ver e alterar o perfil da empresa.' : errMsg(e)));
    });

  return { el: box, load, cleanup() { alive = false; } };
}
