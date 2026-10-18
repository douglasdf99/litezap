// Built-in attendant ("Atendente LiteZap", spec 4.7) inside the "Atendente IA" tab:
// provider (admin), agent profiles ("treinamento") and the conversation simulator.
import * as api from './api.js';
import { h, mount, toast, errMsg, field, confirmDanger } from './ui.js';

const MODE_LABEL = { vendedor: 'Vendedor', pedido: 'Facilitador de pedido', sdr: 'SDR / agendamento', suporte: 'Suporte', custom: 'Personalizado' };
const ACTION_LABEL = {
  handoff: 'Passar para humano (sempre ativo)', lead: 'Emitir agent.lead',
  appointment: 'Emitir agent.appointment_requested', order_draft: 'Emitir agent.order_draft',
};
const KIND_LABEL = { faq: 'Pergunta e resposta', product: 'Produto (catálogo)', hours: 'Horário', policy: 'Política', info: 'Informação' };
const lines = (s) => String(s || '').split('\n').map((x) => x.trim()).filter(Boolean);
const money = (n) => (typeof n === 'number' ? n.toFixed(2) : '');

export function attendantPanel(ctx) {
  const base = `${ctx.base}/attendant`;
  const providerBox = h('div', { class: 'card settings' }, h('p', { class: 'muted' }, 'Carregando…'));
  const agentsBox = h('div', { class: 'card' });
  const editorBox = h('div', { class: 'card', hidden: true });
  const simBox = h('div', { class: 'card' });
  let presets = {};
  let agents = [];
  let alive = true;

  // ---------- provider (admin) ----------
  const renderProvider = (s) => {
    const provider = h('select', null, h('option', { value: 'anthropic' }, 'Anthropic (Claude)'), h('option', { value: 'openai' }, 'OpenAI-compatível (OpenAI, Ollama, LM Studio…)'));
    provider.value = s.provider;
    const model = h('input', { class: 'mono', value: s.model, placeholder: 'claude-sonnet-5-5' });
    const baseURL = h('input', { type: 'url', class: 'mono', value: s.base_url || '', placeholder: 'https://api.openai.com/v1 ou http://127.0.0.1:11434/v1' });
    const key = h('input', { type: 'password', class: 'mono', autocomplete: 'off', placeholder: s.api_key_set ? 'definida (deixe vazio para manter)' : 'chave da API' });
    const cap = h('input', { type: 'number', min: 0, max: 1000000, value: s.daily_call_cap });
    const maxTok = h('input', { type: 'number', min: 64, max: 8192, value: s.max_tokens });
    const vision = h('input', { type: 'checkbox', checked: s.vision });
    const a = s.audio || {};
    const aBase = h('input', { type: 'url', class: 'mono', value: a.base_url || '', placeholder: 'vazio = mesmo servidor (OpenAI-compatível)' });
    const aTrans = h('input', { class: 'mono', value: a.transcription_model || '', placeholder: 'whisper-1 (vazio = sem transcrição)' });
    const aSpeech = h('input', { class: 'mono', value: a.speech_model || '', placeholder: 'gpt-4o-mini-tts (vazio = sem voz)' });
    const aVoice = h('input', { class: 'mono', value: a.voice || '', placeholder: 'alloy' });
    const aKey = h('input', { type: 'password', class: 'mono', autocomplete: 'off', placeholder: s.audio_api_key_set ? 'definida (deixe vazio para manter)' : 'chave do servidor de áudio (opcional)' });
    const save = h('button', { class: 'btn primary', type: 'submit' }, 'Salvar provedor');
    const status = s.ready
      ? h('span', { class: 'badge d-delivered' }, 'pronto')
      : h('span', { class: 'badge d-failed' }, s.not_ready || 'não configurado');
    const t = s.today || {};
    const form = h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        const p = {
          provider: provider.value, model: model.value.trim(), base_url: baseURL.value.trim(),
          daily_call_cap: Number(cap.value), max_tokens: Number(maxTok.value), vision: vision.checked,
          audio: { base_url: aBase.value.trim(), transcription_model: aTrans.value.trim(), speech_model: aSpeech.value.trim(), voice: aVoice.value.trim() },
        };
        if (key.value.trim()) p.api_key = key.value.trim();
        if (aKey.value.trim()) p.audio_api_key = aKey.value.trim();
        save.disabled = true;
        try { renderProvider(await api.put(base, p)); toast('Provedor salvo'); } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('div', { class: 'row' }, h('h2', null, 'Atendente LiteZap (embutido)'), status),
    h('p', { class: 'small muted' },
      'O atendente roda dentro do LiteZap: você não precisa hospedar um agente. Para ele responder, escolha ',
      '"Atendente LiteZap" como destino acima. As respostas passam pela mesma fila (ritmo, janela, rampa), pausa por humano e proteção contra loop. ',
      `Hoje: ${t.calls || 0} chamadas ao provedor de ${s.daily_call_cap || '∞'} · custo estimado US$ ${(t.cost_usd || 0).toFixed(4)}.`),
    h('fieldset', null, h('legend', null, 'Provedor de IA (só o admin altera)'),
      h('div', { class: 'grid3' }, field('Provedor', provider), field('Modelo', model, 'Padrão Anthropic: claude-sonnet-5-5.'),
        field('Base URL', baseURL, 'Só para OpenAI-compatível.')),
      field('Chave da API', key, 'Guardada cifrada no servidor; nunca é exibida de volta. Uso em produção exige chave de API (a assinatura do Claude só vale para testes locais com seu próprio agente).'),
      h('div', { class: 'grid3' },
        field('Limite diário de chamadas', cap, 'Inclui transcrição e voz. 0 = sem limite (não recomendado).'),
        field('Máx. tokens por resposta', maxTok)),
      h('label', { class: 'check' }, vision, ' Enviar imagens (e PDFs, no Claude) para o modelo')),
    h('fieldset', null, h('legend', null, 'Áudio (OpenAI-compatível: /audio/transcriptions e /audio/speech)'),
      h('div', { class: 'grid3' }, field('Base URL do áudio', aBase), field('Modelo de transcrição', aTrans), field('Modelo de voz', aSpeech), field('Voz', aVoice)),
      field('Chave do áudio', aKey)),
    h('div', { class: 'row end' }, save));
    mount(providerBox, form);
  };

  const loadProvider = async () => {
    try { const s = await api.get(base); if (alive) renderProvider(s); } catch (e) { mount(providerBox, h('p', { class: 'error' }, errMsg(e))); }
  };

  // ---------- agents ----------
  const loadAgents = async () => {
    try {
      const [list, pre] = await Promise.all([api.get(`${base}/agents`), Object.keys(presets).length ? { presets } : api.get(`${base}/presets`)]);
      agents = list.agents || [];
      presets = pre.presets || {};
    } catch (e) { mount(agentsBox, h('p', { class: 'error' }, errMsg(e))); return; }
    if (!alive) return;
    const rows = agents.map((a) => h('tr', null,
      h('td', null, a.name, a.active ? h('span', { class: 'badge d-delivered' }, ' responde') : null),
      h('td', null, MODE_LABEL[a.mode] || a.mode),
      h('td', { class: 'small' }, `${(a.knowledge || []).length} itens na base`, a.call_followup ? ' · segue ligações' : '', a.voice_reply !== 'off' ? ' · voz' : ''),
      h('td', null, h('div', { class: 'actions' },
        h('button', { class: 'btn small', type: 'button', onclick: () => openEditor(a) }, 'Editar'),
        a.active ? null : h('button', {
          class: 'btn small', type: 'button',
          onclick: async () => { try { await api.post(`${base}/agents/${a.id}/activate`); toast('Agente ativo'); loadAgents(); } catch (e) { toast(errMsg(e), 'error'); } },
        }, 'Ativar'),
        h('button', {
          class: 'btn small danger', type: 'button',
          onclick: async () => {
            if (!confirmDanger(`Excluir o agente "${a.name}"?`)) return;
            try { await api.del(`${base}/agents/${a.id}`); toast('Agente excluído'); loadAgents(); } catch (e) { toast(errMsg(e), 'error'); }
          },
        }, 'Excluir')))));
    const modePick = h('select', null, Object.keys(MODE_LABEL).map((m) => h('option', { value: m }, MODE_LABEL[m])));
    mount(agentsBox,
      h('h2', null, 'Agentes (treinamento)'),
      h('p', { class: 'small muted' }, 'Cada agente tem um modo (roteiro, tom, critérios de passagem para humano e ações permitidas) e o contexto do negócio. Um agente responde por vez.'),
      rows.length
        ? h('table', { class: 'table small' }, h('thead', null, h('tr', null, h('th', null, 'Nome'), h('th', null, 'Modo'), h('th', null, 'Base'), h('th', null, ''))), h('tbody', null, rows))
        : h('p', { class: 'muted small' }, 'Nenhum agente ainda.'),
      h('div', { class: 'row' }, modePick,
        h('button', { class: 'btn primary', type: 'button', onclick: () => openEditor({ ...(presets[modePick.value] || {}), name: '', mode: modePick.value }) }, 'Novo agente')));
    renderSim();
  };

  // ---------- editor ----------
  const openEditor = (a) => {
    editorBox.hidden = false;
    const name = h('input', { value: a.name || '', placeholder: 'Ex.: Bia da Pizzaria', maxlength: 80 });
    const mode = h('select', null, Object.keys(MODE_LABEL).map((m) => h('option', { value: m }, MODE_LABEL[m])));
    mode.value = a.mode || 'custom';
    const objective = h('textarea', { rows: 2 }); objective.value = a.objective || '';
    const steps = h('textarea', { rows: 5, placeholder: 'um passo por linha' }); steps.value = (a.steps || []).join('\n');
    const tone = h('input', { value: a.tone || '' });
    const criteria = h('textarea', { rows: 3, placeholder: 'um critério por linha' }); criteria.value = (a.handoff_criteria || []).join('\n');
    const actionBoxes = Object.keys(ACTION_LABEL).map((k) => {
      const cb = h('input', { type: 'checkbox', checked: k === 'handoff' || (a.actions || []).includes(k), disabled: k === 'handoff' });
      cb.dataset.action = k;
      return cb;
    });
    const business = h('textarea', { rows: 5, placeholder: 'Quem é a empresa, o que vende, onde fica, formas de pagamento e entrega…' }); business.value = a.business || '';
    const kbBody = h('tbody');
    const addKB = (k = { kind: 'faq', title: '', content: '' }) => {
      const kind = h('select', null, Object.keys(KIND_LABEL).map((x) => h('option', { value: x }, KIND_LABEL[x])));
      kind.value = k.kind || 'info';
      const title = h('input', { value: k.title || '', placeholder: 'pergunta / produto / rótulo' });
      const content = h('textarea', { rows: 2, placeholder: 'resposta / descrição' }); content.value = k.content || '';
      const price = h('input', { type: 'number', step: '0.01', min: 0, value: money(k.price), placeholder: 'preço' });
      const tr = h('tr', null, h('td', null, kind), h('td', null, title), h('td', null, content), h('td', null, price),
        h('td', null, h('button', { class: 'btn small ghost', type: 'button', onclick: () => tr.remove() }, 'Remover')));
      tr.read = () => {
        const out = { kind: kind.value, title: title.value.trim(), content: content.value.trim() };
        if (price.value !== '') out.price = Number(price.value);
        return out;
      };
      kbBody.appendChild(tr);
    };
    (a.knowledge || []).forEach(addKB);
    const voice = h('select', null, h('option', { value: 'off' }, 'Sempre texto'), h('option', { value: 'when_audio' }, 'Voz quando o cliente mandar áudio'), h('option', { value: 'always' }, 'Sempre voz'));
    voice.value = a.voice_reply || 'off';
    const callFollow = h('input', { type: 'checkbox', checked: !!a.call_followup });
    const callHint = h('input', { value: a.call_hint || '', placeholder: 'Ex.: ofereça o cardápio' });
    const maxChars = h('input', { type: 'number', min: 50, max: 4000, value: a.max_reply_chars || 1000 });
    const hideName = h('input', { type: 'checkbox', checked: !!a.hide_contact_name });
    mode.addEventListener('change', () => {
      const p = presets[mode.value];
      if (!p || !confirmDanger('Trocar o roteiro, tom, critérios e ações pelos do modo escolhido?')) return;
      objective.value = p.objective || ''; steps.value = (p.steps || []).join('\n'); tone.value = p.tone || '';
      criteria.value = (p.handoff_criteria || []).join('\n');
      actionBoxes.forEach((cb) => { cb.checked = cb.dataset.action === 'handoff' || (p.actions || []).includes(cb.dataset.action); });
    });
    const profile = () => ({
      name: name.value.trim(), mode: mode.value, objective: objective.value.trim(), steps: lines(steps.value), tone: tone.value.trim(),
      handoff_criteria: lines(criteria.value), actions: actionBoxes.filter((cb) => cb.checked).map((cb) => cb.dataset.action),
      business: business.value.trim(), knowledge: [...kbBody.children].map((tr) => tr.read()),
      voice_reply: voice.value, call_followup: callFollow.checked, call_hint: callHint.value.trim(),
      max_reply_chars: Number(maxChars.value), hide_contact_name: hideName.checked,
    });
    const save = h('button', { class: 'btn primary', type: 'submit' }, a.id ? 'Salvar agente' : 'Criar agente');
    mount(editorBox, h('form', {
      onsubmit: async (ev) => {
        ev.preventDefault();
        save.disabled = true;
        try {
          if (a.id) await api.put(`${base}/agents/${a.id}`, profile());
          else await api.post(`${base}/agents`, profile());
          toast('Agente salvo');
          editorBox.hidden = true;
          loadAgents();
        } catch (e) { toast(errMsg(e), 'error'); save.disabled = false; }
      },
    },
    h('h2', null, a.id ? `Editar ${a.name}` : 'Novo agente'),
    h('div', { class: 'grid3' }, field('Nome', name), field('Modo', mode), field('Tom', tone)),
    field('Objetivo', objective),
    field('Roteiro (passos)', steps),
    field('Quando passar para humano', criteria),
    h('fieldset', null, h('legend', null, 'Ações permitidas'), actionBoxes.map((cb) => h('label', { class: 'check' }, cb, ' ', ACTION_LABEL[cb.dataset.action]))),
    field('Contexto do negócio', business, 'Informação confiável que o agente usa. Nunca coloque senhas ou chaves aqui.'),
    h('fieldset', null, h('legend', null, 'Base de conhecimento (perguntas, catálogo com preço, horários, políticas)'),
      h('table', { class: 'table small' }, h('thead', null, h('tr', null, h('th', null, 'Tipo'), h('th', null, 'Título'), h('th', null, 'Conteúdo'), h('th', null, 'Preço'), h('th', null, ''))), kbBody),
      h('button', { class: 'btn small', type: 'button', onclick: () => addKB() }, 'Adicionar item')),
    h('fieldset', null, h('legend', null, 'Voz, ligações e privacidade'),
      h('div', { class: 'grid3' }, field('Resposta em voz', voice, 'Exige modelo de voz configurado.'), field('Máx. caracteres por resposta', maxChars)),
      h('label', { class: 'check' }, callFollow, ' Ligação recusada automaticamente → mandar mensagem ("vi que você ligou…"); exige calls.auto_reject'),
      field('Orientação para a mensagem pós-ligação', callHint),
      h('label', { class: 'check' }, hideName, ' Não enviar o nome do contato para a IA')),
    h('div', { class: 'row end' },
      h('button', { class: 'btn', type: 'button', onclick: () => { simProfile = profile(); renderSim(); simBox.scrollIntoView({ behavior: 'smooth' }); } }, 'Testar sem salvar'),
      h('button', { class: 'btn ghost', type: 'button', onclick: () => { editorBox.hidden = true; } }, 'Cancelar'),
      save)));
    editorBox.scrollIntoView({ behavior: 'smooth' });
  };

  // ---------- simulator ----------
  let simProfile = null;
  const renderSim = () => {
    const pick = h('select', null, agents.map((a) => h('option', { value: a.id }, a.name + (a.active ? ' (ativo)' : ''))));
    if (simProfile) pick.prepend(h('option', { value: '__unsaved', selected: true }, `${simProfile.name || 'sem nome'} (não salvo)`));
    const script = h('textarea', { rows: 6, class: 'mono', placeholder: 'cliente: oi, vocês entregam?\nauto: Não atendemos ligações, mande mensagem.\nvocê: Olá! Entregamos sim.\ncliente: quanto custa a pizza grande?' });
    const asCall = h('input', { type: 'checkbox' });
    const out = h('div');
    const run = h('button', { class: 'btn primary', type: 'button' }, 'Simular');
    run.onclick = async () => {
      const map = { cliente: 'customer', 'você': 'agent', voce: 'agent', atendente: 'agent', humano: 'human', auto: 'automatic' };
      const messages = lines(script.value).map((l) => {
        const m = l.match(/^(cliente|você|voce|atendente|humano|auto)\s*:\s*(.*)$/i);
        return m ? { from: map[m[1].toLowerCase()], text: m[2] } : { from: 'customer', text: l };
      });
      const body = { messages };
      if (asCall.checked) body.trigger = 'call';
      if (pick.value === '__unsaved') body.agent = simProfile; else if (pick.value) body.agent_id = pick.value;
      run.disabled = true;
      mount(out, h('p', { class: 'muted' }, 'Consultando o modelo…'));
      try {
        const r = await api.post(`${base}/simulate`, body);
        mount(out,
          h('p', null, h('strong', null, 'Resposta: '), r.reply ? h('span', { class: 'prewrap' }, r.reply) : h('em', null, '(ficaria em silêncio)'), r.voice ? ' · seria enviada como áudio' : ''),
          r.handoff ? h('p', { class: 'warn' }, `Passaria para humano: ${r.handoff_note || ''}`) : null,
          (r.events || []).length ? h('pre', { class: 'json' }, JSON.stringify(r.events, null, 2)) : null,
          h('p', { class: 'small muted' }, `${r.model} · ${r.usage.input_tokens} tokens de entrada, ${r.usage.output_tokens} de saída · US$ ${r.usage.cost_usd.toFixed(5)} · nada foi enviado no WhatsApp`));
      } catch (e) { mount(out, h('p', { class: 'error' }, errMsg(e))); } finally { run.disabled = false; }
    };
    mount(simBox,
      h('h2', null, 'Simular conversa'),
      h('p', { class: 'small muted' }, 'Roda o agente contra mensagens de exemplo sem enviar nada. Uma linha por mensagem, com o prefixo cliente:, você:, humano: ou auto: (mensagem automática). É uma chamada real ao provedor e conta no limite diário.'),
      h('div', { class: 'row' }, field('Agente', pick), h('label', { class: 'check' }, asCall, ' Simular mensagem pós-ligação')),
      script,
      h('div', { class: 'row end' }, run),
      out);
  };

  loadProvider();
  loadAgents();
  const el = h('div', { class: 'attendant' }, providerBox, agentsBox, editorBox, simBox);
  return { el, cleanup() { alive = false; } };
}
