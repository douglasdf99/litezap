// "Qual IA usar?": free options first (Ollama on this computer, Gemini and Groq free
// tiers), then the paid provider. Shared by the Atendente IA provider block and the
// Agentes tab when no provider is configured. Only the loopback Ollama is ever checked
// (the engine's /v1/ai/local); LiteZap never installs software by itself.
import * as api from './api.js';
import { h, mount, toast, errView, field, icon } from './ui.js';

const OLLAMA_DOWNLOAD = 'https://ollama.com/download';

// Fallbacks when /v1/ai/presets is unavailable (same text as the engine's table).
const FALLBACK = {
  ollama: { id: 'ollama', models: ['qwen2.5:7b', 'llama3.2:3b'], needs_key: false,
    quality: 'Boa para atendimento simples em português; erra mais que os modelos pagos em tarefas longas.',
    speed: 'Depende do computador: de 2 a 20 segundos por resposta.', privacy: 'As conversas não saem do seu computador.' },
  gemini_free: { id: 'gemini_free', models: ['gemini-2.5-flash', 'gemini-2.5-flash-lite'], needs_key: true, key_url: 'https://aistudio.google.com/apikey',
    quality: 'Muito boa, perto dos modelos pagos.', speed: 'Rápida, com limite de pedidos por minuto e por dia no plano grátis.',
    privacy: 'No plano grátis o Google pode usar as conversas para melhorar os modelos e pessoas podem revisá-las.' },
  groq_free: { id: 'groq_free', models: ['llama-3.3-70b-versatile', 'llama-3.1-8b-instant'], needs_key: true, key_url: 'https://console.groq.com/keys',
    quality: 'Boa; o modelo de 70B segue bem as instruções e usa ferramentas.', speed: 'Muito rápida, com limite de pedidos e de tokens por minuto no plano grátis.',
    privacy: 'As conversas passam pelos servidores da Groq (EUA).' },
};

const CHOICES = [
  { id: 'ollama', title: 'Grátis no meu computador', tag: 'recomendado', sub: 'Ollama: a IA roda aqui, sem conta, sem chave e sem cartão.' },
  { id: 'gemini_free', title: 'Grátis na nuvem (Google Gemini)', sub: 'Precisa só de uma conta Google. Sem cartão.' },
  { id: 'groq_free', title: 'Groq grátis', sub: 'Conta grátis na Groq. Sem cartão.' },
  { id: 'paid', title: 'Pago (Anthropic Claude)', sub: 'A melhor qualidade. Cobrado por uso, com cartão.' },
];

const gb = (bytes) => (bytes ? `${(bytes / 2 ** 30).toFixed(0)} GB` : 'desconhecida');
const ext = (href, text) => h('a', { href, target: '_blank', rel: 'noopener noreferrer' }, text);

function honest(p) {
  return h('ul', { class: 'ai-honest small' },
    h('li', null, h('strong', null, 'Qualidade: '), p.quality),
    h('li', null, h('strong', null, 'Velocidade: '), p.speed),
    h('li', null, h('strong', null, 'Privacidade: '), p.privacy));
}

// freeAIChooser renders the choice. base is the instance's /attendant URL; current is its
// settings view; onSaved(view) is called after a free option is saved; paid() returns the
// element of the paid option (the full provider form, or a link).
export function freeAIChooser({ base, current, onSaved, paid }) {
  const el = h('div', { class: 'ai-chooser' });
  const panel = h('div', { class: 'ai-panel' });
  let alive = true;
  let timer = null;
  let presets = { ...FALLBACK };
  const s = current || {};
  let selected = s.configured && (s.ready || s.preset) ? (s.preset || 'paid') : 'ollama';

  const stopPoll = () => { if (timer) { clearTimeout(timer); timer = null; } };

  const save = async (btn, patch) => {
    btn.disabled = true;
    try {
      const v = await api.put(base, patch);
      toast('IA configurada');
      if (onSaved) onSaved(v);
    } catch (e) { toast(e, 'error'); btn.disabled = false; }
  };

  const modelPick = (p, value) => {
    const sel = h('select', { class: 'mono' }, (p.models || []).map((m) => h('option', { value: m }, m)));
    if (value && (p.models || []).includes(value)) sel.value = value;
    return sel;
  };

  // ---------- Ollama ----------
  const renderLocal = async () => {
    const p = presets.ollama;
    mount(panel, h('p', { class: 'muted small' }, 'Verificando o Ollama neste computador…'));
    let st;
    try { st = await api.get('/v1/ai/local'); } catch (e) { mount(panel, h('p', { class: 'error' }, errView(e))); return; }
    if (!alive || selected !== 'ollama') return;
    const again = h('button', { class: 'btn', type: 'button', onclick: () => renderLocal() }, 'Verificar de novo');
    if (!st.reachable) {
      mount(panel,
        honest(p),
        h('div', { class: 'ag-notice' }, icon('alert', 16), h('span', null, 'O Ollama não está instalado ou não está aberto neste computador.')),
        h('ol', { class: 'ai-steps' },
          h('li', null, 'Baixe e instale o Ollama (grátis): ', ext(st.download_url || OLLAMA_DOWNLOAD, 'ollama.com/download'), '.'),
          h('li', null, 'Abra o Ollama. No Mac e no Windows ele fica perto do relógio; no Linux roda como serviço.'),
          h('li', null, 'Volte aqui e clique em "Verificar de novo".')),
        h('p', { class: 'small muted' }, 'O LiteZap não instala nada sozinho. Precisa de uns 5 GB livres em disco e, de preferência, 8 GB de RAM.'),
        h('div', { class: 'row end' }, again));
      return;
    }
    const opts = st.options || [];
    const pick = h('select', { class: 'mono' }, opts.map((o) => h('option', { value: o.model }, `${o.model} (${o.size_label})`)));
    const installed = (m) => (st.models || []).some((x) => x.name === m || x.name === `${m}:latest`);
    const cur = s.preset === 'ollama' && s.model ? s.model : (st.recommended && st.recommended.model);
    if (cur) pick.value = cur;
    const note = h('p', { class: 'small muted' });
    const action = h('div', { class: 'row end' });
    const progress = h('div');
    const refresh = () => {
      const o = opts.find((x) => x.model === pick.value) || {};
      note.textContent = `${o.note || ''}${st.recommended && o.model === st.recommended.model ? ' É o recomendado para este computador.' : ''}`;
      if (installed(pick.value)) {
        const use = h('button', { class: 'btn primary', type: 'button' }, 'Usar');
        use.onclick = () => save(use, { preset: 'ollama', model: pick.value, api_key: '' });
        mount(action, h('span', { class: 'small ag-ok' }, icon('check', 14), ' Modelo pronto'), use);
      } else {
        const dl = h('button', { class: 'btn primary', type: 'button' }, `Baixar modelo (${o.size_label || ''})`);
        dl.onclick = async () => {
          dl.disabled = true;
          try { await api.post('/v1/ai/local/pull', { model: pick.value }); poll(); } catch (e) { toast(e, 'error'); dl.disabled = false; }
        };
        mount(action, dl);
      }
    };
    const poll = async () => {
      stopPoll();
      let r;
      try { r = await api.get('/v1/ai/local/pull'); } catch (e) { mount(progress, h('p', { class: 'error' }, errView(e))); return; }
      if (!alive || selected !== 'ollama') return;
      const pl = r.pull;
      if (!pl) { mount(progress); return; }
      if (pl.done) {
        if (pl.success) { toast(`Modelo ${pl.model} baixado`); renderLocal(); return; }
        mount(progress, h('p', { class: 'error' }, `O download parou: ${pl.error || 'erro'}. Tente de novo (o Ollama continua de onde parou).`));
        refresh();
        return;
      }
      const bar = h('progress', { class: 'ai-progress', max: 100, value: pl.percent || 0 });
      const cancel = h('button', { class: 'btn small ghost', type: 'button', onclick: async () => { try { await api.del('/v1/ai/local/pull'); } catch (e) { toast(e, 'error'); } } }, 'Cancelar');
      const mb = (n) => `${(n / 1e6).toFixed(0)} MB`;
      mount(progress,
        h('div', { class: 'ai-progress-row' }, bar, h('span', { class: 'small mono' }, `${(pl.percent || 0).toFixed(1)}%`), cancel),
        h('p', { class: 'small muted' }, `Baixando ${pl.model}: ${pl.status || ''}${pl.total_bytes ? ` · ${mb(pl.completed_bytes)} de ${mb(pl.total_bytes)}` : ''}. Pode continuar usando o painel.`));
      mount(action);
      timer = setTimeout(poll, 1000);
    };
    pick.addEventListener('change', refresh);
    mount(panel,
      honest(p),
      h('p', { class: 'small' }, icon('check', 14), ` Ollama ${st.version || ''} encontrado. Memória deste computador: ${gb(st.ram_bytes)}.`),
      field('Modelo', pick),
      note, progress, action);
    refresh();
    if (st.pull && !st.pull.done) poll();
  };

  // ---------- cloud (Gemini, Groq) ----------
  const renderCloud = (id) => {
    const p = presets[id];
    const same = s.preset === id;
    const model = modelPick(p, same ? s.model : '');
    const key = h('input', { type: 'password', class: 'mono', autocomplete: 'off',
      placeholder: same && s.api_key_set ? 'definida (deixe vazio para manter)' : 'cole a chave aqui' });
    const use = h('button', { class: 'btn primary', type: 'submit' }, 'Usar');
    const steps = id === 'gemini_free'
      ? [h('li', null, 'Abra ', ext(p.key_url, 'aistudio.google.com/apikey'), ' e entre com sua conta Google.'),
        h('li', null, 'Clique em "Create API key" (Criar chave de API). Não é preciso cadastrar cartão.'),
        h('li', null, 'Copie a chave (começa com AIza) e cole abaixo.')]
      : [h('li', null, 'Crie uma conta grátis e abra ', ext(p.key_url, 'console.groq.com/keys'), '.'),
        h('li', null, 'Clique em "Create API Key". Não é preciso cadastrar cartão.'),
        h('li', null, 'Copie a chave (começa com gsk_) e cole abaixo.')];
    mount(panel, h('form', {
      onsubmit: (ev) => {
        ev.preventDefault();
        const k = key.value.trim();
        if (!k && !(same && s.api_key_set)) { toast('Cole a chave grátis primeiro', 'error'); return; }
        const patch = { preset: id, model: model.value };
        if (k) patch.api_key = k;
        save(use, patch);
      },
    },
    honest(p),
    id === 'gemini_free' ? h('div', { class: 'ag-notice' }, icon('alert', 16), h('span', null, 'Atenção: no plano grátis o Google pode usar as conversas para melhorar os modelos. Não use para dados sensíveis dos clientes.')) : null,
    h('ol', { class: 'ai-steps' }, steps),
    h('div', { class: 'grid3' }, field('Chave grátis', key, 'Guardada cifrada no servidor; nunca é exibida de volta.'), field('Modelo', model)),
    h('div', { class: 'row end' }, use)));
  };

  const renderPanel = () => {
    stopPoll();
    if (selected === 'ollama') renderLocal();
    else if (selected === 'paid') mount(panel, paid ? paid() : null);
    else renderCloud(selected);
  };

  const cards = CHOICES.map((c) => {
    const b = h('button', { type: 'button', class: 'ai-choice', 'aria-pressed': String(c.id === selected) },
      h('span', { class: 'ai-choice-title' }, c.title, c.tag ? h('span', { class: 'badge d-delivered' }, c.tag) : null),
      h('span', { class: 'small muted' }, c.sub),
      s.configured && s.ready && (s.preset || 'paid') === c.id ? h('span', { class: 'small ag-ok' }, icon('check', 14), ' em uso') : null);
    b.addEventListener('click', () => {
      selected = c.id;
      cards.forEach((x) => x.setAttribute('aria-pressed', String(x === b)));
      renderPanel();
    });
    return b;
  });

  mount(el, h('div', { class: 'ai-choices', role: 'group', 'aria-label': 'Qual IA usar' }, cards), panel);
  api.get('/v1/ai/presets').then((r) => {
    (r.presets || []).forEach((p) => { presets[p.id] = p; });
    if (alive && selected !== 'paid' && selected !== 'ollama') renderPanel();
  }).catch(() => {});
  renderPanel();
  return { el, cleanup() { alive = false; stopPoll(); } };
}

// costLabel is "Grátis" for a free configuration, else the estimated cost in USD.
export function costLabel(s, usd) {
  return s && s.free ? 'Grátis' : `US$ ${(usd || 0).toFixed(4)}`;
}
