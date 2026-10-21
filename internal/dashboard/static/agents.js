// "Agentes" tab (spec 2026-10-20, slice A1): LiteZap agents that run a tool-use loop over
// the LiteZap tools. List with on/off, "Novo agente" stepper, pending drafts with
// approve/reject, and the agent page (runs with their steps, simulator, "Executar agora").
// Everything from the server is rendered as text (h()), never parsed as HTML.
import * as api from './api.js';
import {
  h, mount, toast, errView, field, fmtTime, phoneOf, confirmDanger, icon, dialog, emptyState, skeleton,
} from './ui.js';
import { freeAIChooser, costLabel } from './freeai.js';
import { subscriptionCard, runtimeField, runtimeBadge } from './subscription.js';

const MODE = {
  aprovacao: ['Aprovação', 'tone-warn', 'Cada envio vira um rascunho que você aprova.'],
  autonomo: ['Autônomo', 'tone-ok', 'Executa sozinho (os envios passam pela fila anti-ban).'],
  sugerir: ['Só sugerir', 'ag-tone-info', 'Nada é executado: as ações viram notas.'],
};
const GROUPS = {
  ler: ['Ler a conversa do gatilho', 'Mensagens, contato e grupo da conversa que disparou o agente, mais a lista de conversas (só nomes) e as etiquetas.'],
  ler_outros_chats: ['Ler outras conversas', 'Ler as mensagens de qualquer conversa e a agenda de contatos.'],
  responder_no_chat: ['Responder no chat do gatilho', 'Mensagens e reações só na conversa que disparou o agente.'],
  enviar_outros_chats: ['Enviar para outros chats', 'Mandar mensagem para qualquer conversa (no máximo o limite de destinatários por execução).'],
  etiquetas: ['Etiquetas', 'Aplicar e tirar etiquetas do WhatsApp Business, em qualquer conversa.'],
  grupos: ['Grupos (gerenciar)', 'Criar grupos, mudar nome e configurações, adicionar e remover pessoas e pegar o link de convite.'],
  midia: ['Mídia', 'Enviar imagem, áudio, vídeo e documento por URL.'],
};
const RISK = {
  ler_outros_chats: 'O agente poderá ler as mensagens de todas as suas conversas e a sua agenda de contatos, e esse conteúdo vai para o provedor de IA. Uma mensagem maliciosa pode tentar fazê-lo repassar o que leu: evite junto com "Enviar para outros chats".',
  enviar_outros_chats: 'O agente poderá mandar mensagens para outras pessoas além de quem escreveu. Uma mensagem maliciosa pode tentar convencê-lo a fazer isso: use só com instruções claras e, de preferência, no modo Aprovação.',
  grupos: 'Criar grupos e adicionar pessoas são ações com risco de banimento do número. Use só se o agente realmente precisa disso.',
};
const OUTCOME = {
  read: ['leitura', ''], executed: ['executado', 'tone-ok'], draft: ['rascunho', 'tone-warn'], note: ['nota', 'ag-tone-info'],
  dry_run: ['faz de conta', 'ag-tone-info'], refused: ['recusado', 'tone-danger'], error: ['erro', 'tone-danger'],
};
const STATUS = {
  running: ['rodando', 'tone-warn'], completed: ['concluída', 'tone-ok'], step_limit: ['limite de passos', 'tone-warn'],
  failed: ['falhou', 'tone-danger'], discarded: ['descartada (humano assumiu)', 'tone-warn'],
};
const TOOL_LABEL = {
  send_message: 'Mensagem', send_reaction: 'Reação', send_media: 'Mídia', label_chat: 'Etiqueta',
  create_group: 'Criar grupo', update_group: 'Alterar grupo', manage_group_participants: 'Participantes do grupo',
  get_group_invite_link: 'Link de convite do grupo',
};

const TEMPLATES = [
  {
    key: 'atendente', title: 'Atendente', icon: 'chat',
    text: 'Responde clientes na conversa em que eles escreveram.',
    agent: {
      name: 'Atendente', mode: 'aprovacao', tools: ['ler', 'responder_no_chat'], trigger: { type: 'message', groups: false },
      instructions: 'Você atende os clientes da minha empresa no WhatsApp. Responda dúvidas sobre horário, endereço, entrega e produtos usando só as informações abaixo. Seja breve e cordial, em português. Se o cliente pedir desconto, reclamar ou quiser falar com uma pessoa, diga que alguém da equipe vai responder em breve e não prometa nada.\n\nInformações da empresa:\n- Horário: \n- Endereço: \n- Entrega: ',
    },
  },
  {
    key: 'organizador', title: 'Organizador', icon: 'inbox',
    text: 'Etiqueta cada conversa nova (conta Business), sem responder.',
    agent: {
      name: 'Organizador', mode: 'aprovacao', tools: ['ler', 'etiquetas'], trigger: { type: 'message', groups: false },
      instructions: 'A cada mensagem nova, veja as etiquetas que existem (list_labels) e aplique na conversa a que melhor descreve o assunto (label_chat), por exemplo Novo cliente, Pedido, Suporte ou Financeiro. Não responda o cliente. Se nenhuma etiqueta servir, não faça nada.',
    },
  },
  {
    key: 'resumo', title: 'Resumo manual', icon: 'book',
    text: 'Quando você pedir, resume as conversas e diz o que precisa de resposta.',
    agent: {
      name: 'Resumo do dia', mode: 'sugerir', tools: ['ler'], trigger: { type: 'manual' },
      instructions: 'Quando eu executar, veja as conversas mais recentes (list_chats: nome, hora da última mensagem e não lidas) e escreva para mim um resumo curto de quem está esperando resposta, do mais antigo para o mais novo. Não envie mensagens. (Para resumir também o conteúdo das mensagens, ligue a permissão "Ler outras conversas".)',
    },
  },
  { key: 'branco', title: 'Em branco', icon: 'plus', text: 'Comece do zero.', agent: { name: '', mode: 'aprovacao', tools: ['ler', 'responder_no_chat'], trigger: { type: 'message' }, instructions: '' } },
];

const listOf = (s) => String(s || '').split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
const usd = (n) => `US$ ${(Number(n) || 0).toFixed(4)}`;

function modeBadge(mode) {
  const [label, tone] = MODE[mode] || [mode, ''];
  return h('span', { class: `badge ${tone}` }, label);
}

function triggerText(t) {
  if (!t || t.type === 'manual') return 'Só manual (Executar agora)';
  const parts = ['Mensagem nova', t.groups ? 'conversas e grupos' : 'só conversas 1:1'];
  if ((t.keywords || []).length) parts.push(`palavras: ${t.keywords.join(', ')}`);
  if ((t.chats || []).length) parts.push(`${t.chats.length} chat(s)`);
  if ((t.labels || []).length) parts.push(`etiquetas: ${t.labels.join(', ')}`);
  return parts.join(' · ');
}

// recipient renders a chat jid for people.
function recipient(jid) {
  if (!jid) return '';
  if (jid === '5500000000000@s.whatsapp.net') return 'conversa simulada';
  return jid.endsWith('@g.us') ? `grupo ${jid}` : phoneOf(jid);
}

export function agentsPanel(ctx, rest = []) {
  const base = `${ctx.base}/agents`;
  const el = h('div', { class: 'agents' });
  let alive = true;
  let view = null; // {cleanup, refresh}

  const show = (parts) => {
    if (view && view.cleanup) view.cleanup();
    view = parts[0] ? agentPage(ctx, base, parts[0]) : listPage(ctx, base);
    mount(el, view.el);
  };

  const off = ctx.on((type) => {
    if (!alive || !type.startsWith('agent.') || !view || !view.refresh) return;
    view.refresh(type);
  });
  show(rest);
  return {
    el,
    update(parts) { show(parts || []); },
    cleanup() { alive = false; off(); if (view && view.cleanup) view.cleanup(); },
  };
}

// ---------- list ----------

function listPage(ctx, base) {
  const setup = h('div', { class: 'card ag-setup' }, skeleton(1, 'skel-line'));
  let chooser = null;
  const grid = h('div', { class: 'ag-grid' }, skeleton(2));
  const drafts = draftsPanel(ctx, base);
  let agents = [];

  const loadSetup = async () => {
    let att = null;
    let hook = null;
    try {
      [att, hook] = await Promise.all([api.get(`${ctx.base}/attendant`).catch(() => null), api.get(`${ctx.base}/agent-hook`)]);
    } catch (e) { mount(setup, h('p', { class: 'error' }, errView(e))); return; }
    const ready = att && att.ready;
    const on = hook.target === 'agents' && hook.enabled;
    const toggle = h('input', { type: 'checkbox', class: 'ag-switch', checked: on, 'aria-label': 'Agentes respondem a mensagens novas' });
    toggle.addEventListener('change', async () => {
      const want = toggle.checked;
      if (want && hook.configured && hook.enabled && hook.target !== 'agents') {
        const who = hook.target === 'builtin' ? 'o Atendente LiteZap' : 'o seu agente externo (URL)';
        if (!(await confirmDanger(`Hoje quem responde às mensagens é ${who}. Ligar os Agentes coloca eles no lugar (um destino por instância). Continuar?`, { ok: 'Usar os Agentes', danger: false }))) {
          toggle.checked = false;
          return;
        }
      }
      toggle.disabled = true;
      try {
        hook = await api.put(`${ctx.base}/agent-hook`, want ? { target: 'agents', enabled: true } : { enabled: false });
        toast(want ? 'Agentes ligados para mensagens novas' : 'Agentes não respondem mais a mensagens novas');
      } catch (e) { toast(e, 'error'); toggle.checked = !want; } finally { toggle.disabled = false; }
    });
    if (chooser) { chooser.cleanup(); chooser = null; }
    if (!ready) {
      chooser = freeAIChooser({
        base: `${ctx.base}/attendant`, current: att, onSaved: () => loadSetup(),
        paid: () => h('p', { class: 'small' }, 'O Claude é pago por uso e exige chave de API com cartão. ',
          h('a', { href: ctx.href('agent') }, 'Configurar no Atendente IA'), '.'),
      });
    }
    mount(setup,
      h('div', { class: 'ag-setup-row' },
        h('div', { class: 'grow' },
          h('h2', null, 'Agentes LiteZap'),
          h('p', { class: 'small muted' }, 'Agentes de IA que leem, respondem e organizam o seu WhatsApp usando as ferramentas do LiteZap, dentro das permissões que você der. Funcionam de graça (no seu computador ou na nuvem) ou com o Claude. Todo envio passa pela fila anti-ban; quando você responde pelo celular, eles param naquela conversa.')),
        h('label', { class: 'check ag-switch-label' }, toggle, h('span', null, 'Responder a mensagens novas'))),
      ready
        ? h('p', { class: 'small muted ag-ok' }, icon('check', 14), h('span', null, `Provedor: ${att.preset || att.provider} · ${att.model}. Hoje: ${(att.today && att.today.calls) || 0} chamadas de ${att.daily_call_cap || '∞'} · ${att.free ? costLabel(att) : usd(att.today && att.today.cost_usd)}.`))
        : h('div', null,
          h('div', { class: 'ag-notice' }, icon('alert', 16),
            h('span', null, 'Falta escolher a IA que os agentes usam. Há opções grátis, sem cartão (só o token admin altera):')),
          chooser.el));
  };

  const card = (a) => {
    const href = ctx.href('agents', a.id);
    const toggle = h('input', { type: 'checkbox', class: 'ag-switch', checked: a.enabled, 'aria-label': `Ligar ${a.name}` });
    toggle.addEventListener('change', async () => {
      toggle.disabled = true;
      try {
        const v = await api.put(`${base}/${encodeURIComponent(a.id)}`, { enabled: toggle.checked });
        Object.assign(a, v);
        toast(v.enabled ? `${v.name} ligado` : `${v.name} pausado`);
      } catch (e) { toast(e, 'error'); toggle.checked = !toggle.checked; } finally { toggle.disabled = false; }
    });
    const runBtn = h('button', { class: 'btn small', type: 'button', onclick: () => runNowDialog(ctx, base, a, () => load()) }, icon('send', 14), 'Executar agora');
    const del = h('button', { class: 'icon-btn danger', type: 'button', title: 'Excluir', 'aria-label': `Excluir ${a.name}` }, icon('trash', 16));
    del.addEventListener('click', async () => {
      if (!(await confirmDanger(`Excluir o agente "${a.name}"? As execuções e os rascunhos dele também são apagados.`, { ok: 'Excluir' }))) return;
      try { await api.del(`${base}/${encodeURIComponent(a.id)}`); toast('Agente excluído'); load(); } catch (e) { toast(e, 'error'); }
    });
    return h('article', { class: 'card ag-card' },
      h('div', { class: 'ag-card-top' },
        h('a', { class: 'ag-name', href }, a.name),
        h('label', { class: 'check', title: a.enabled ? 'Ligado' : 'Pausado' }, toggle)),
      h('div', { class: 'row' }, modeBadge(a.mode), runtimeBadge(a), a.enabled ? null : h('span', { class: 'badge' }, 'pausado')),
      h('p', { class: 'small muted' }, triggerText(a.trigger)),
      h('p', { class: 'small' }, `${a.today.runs} execução(ões) hoje · ${usd(a.today.cost_usd)}`),
      h('div', { class: 'ag-card-foot' }, h('a', { class: 'btn small ghost', href }, 'Abrir'), runBtn, h('span', { class: 'grow' }), del));
  };

  const load = async () => {
    try {
      agents = (await api.get(base)).agents || [];
    } catch (e) { mount(grid, h('div', { class: 'card' }, h('p', { class: 'error' }, errView(e)))); return; }
    if (!agents.length) {
      mount(grid, h('div', { class: 'card ag-empty' }, emptyState({
        iconName: 'bot', title: 'Nenhum agente ainda',
        text: 'Crie um agente a partir de um modelo pronto. Ele começa no modo Aprovação: nada é enviado sem você aprovar.',
        action: h('button', { class: 'btn primary', type: 'button', onclick: () => openEditor(ctx, base, null, load) }, icon('plus', 16), 'Novo agente'),
      })));
      return;
    }
    mount(grid, agents.map(card));
  };

  loadSetup();
  load();
  const elx = h('div', { class: 'stack' },
    setup,
    subscriptionCard(ctx, base),
    h('div', { class: 'row' }, h('h2', { class: 'grow ag-h' }, 'Seus agentes'),
      h('button', { class: 'btn primary', type: 'button', onclick: () => openEditor(ctx, base, null, load) }, icon('plus', 16), 'Novo agente')),
    grid,
    drafts.el);
  let timer = null;
  return {
    el: elx,
    refresh(type) {
      if (type.startsWith('agent.draft.')) drafts.load();
      if (type === 'agent.run.completed') { clearTimeout(timer); timer = setTimeout(load, 600); }
    },
    cleanup() { clearTimeout(timer); if (chooser) chooser.cleanup(); },
  };
}

// ---------- drafts ----------

function draftsPanel(ctx, base) {
  const box = h('div', { class: 'card' });
  const load = async () => {
    let list = [];
    try { list = (await api.get(`${base}/drafts?status=pending`)).drafts || []; } catch (e) { mount(box, h('p', { class: 'error' }, errView(e))); return; }
    const decide = (d, approve, btns) => async () => {
      if (approve && d.tool !== 'send_message' && d.tool !== 'send_reaction' && d.tool !== 'label_chat' &&
        !(await confirmDanger(`Aprovar a ação "${TOOL_LABEL[d.tool] || d.tool}" do agente ${d.agent_name || ''}?`, { ok: 'Aprovar', danger: false }))) return;
      btns.forEach((b) => { b.disabled = true; });
      try {
        const v = await api.post(`${base}/drafts/${encodeURIComponent(d.id)}/${approve ? 'approve' : 'reject'}`);
        toast(v.status === 'approved' ? 'Aprovado: enviado para a fila' : v.status === 'rejected' ? 'Rascunho rejeitado' : `Falhou: ${v.result || ''}`, v.status === 'failed' ? 'error' : 'info');
        load();
      } catch (e) { toast(e, 'error'); btns.forEach((b) => { b.disabled = false; }); }
    };
    const item = (d) => {
      const a = d.args || {};
      const approveBtn = h('button', { class: 'btn small primary', type: 'button' }, icon('check', 14), 'Aprovar');
      const rejectBtn = h('button', { class: 'btn small', type: 'button' }, icon('x', 14), 'Rejeitar');
      approveBtn.addEventListener('click', decide(d, true, [approveBtn, rejectBtn]));
      rejectBtn.addEventListener('click', decide(d, false, [approveBtn, rejectBtn]));
      let what;
      switch (d.tool) {
        case 'send_message': what = h('div', { class: 'ag-bubble prewrap' }, a.text || ''); break;
        case 'send_reaction': what = h('div', { class: 'ag-bubble' }, `Reação ${a.emoji || ''} na mensagem ${a.message_id || ''}`); break;
        case 'send_media': what = h('div', { class: 'ag-bubble' }, `${a.type || 'arquivo'}: ${a.url || ''}`, a.caption ? h('div', { class: 'prewrap' }, a.caption) : null); break;
        case 'label_chat': what = h('div', { class: 'ag-bubble' }, `${a.labeled === false ? 'Tirar' : 'Aplicar'} a etiqueta ${a.label || ''}`); break;
        default: what = h('pre', { class: 'json' }, JSON.stringify(a, null, 2));
      }
      return h('li', { class: 'ag-draft' },
        h('div', { class: 'ag-draft-head' },
          h('strong', null, TOOL_LABEL[d.tool] || d.tool),
          h('span', null, ' para '), h('span', { class: 'mono' }, recipient(d.chat) || '—'),
          h('span', { class: 'grow' }),
          h('span', { class: 'small muted' }, `${d.agent_name || d.agent_id} · ${fmtTime(d.created_at)}`)),
        what,
        h('div', { class: 'row end' }, h('span', { class: 'small muted grow' }, `Vale até ${fmtTime(d.expires_at)}`), rejectBtn, approveBtn));
    };
    mount(box,
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Rascunhos pendentes'), list.length ? h('span', { class: 'badge tone-warn' }, String(list.length)) : null,
        h('button', { class: 'btn small ghost', type: 'button', onclick: load }, icon('refresh', 14), 'Atualizar')),
      list.length
        ? h('ul', { class: 'ag-drafts' }, list.map(item))
        : h('p', { class: 'muted small' }, 'Nada esperando aprovação. Agentes no modo Aprovação deixam aqui cada mensagem antes de enviar, com o destinatário e o texto exatos.'));
  };
  load();
  return { el: box, load };
}

// ---------- editor (modal stepper) ----------

const STEPS = ['Modelo', 'Instruções', 'Permissões', 'Modo e gatilho'];

function openEditor(ctx, base, agent, onSaved) {
  const editing = !!agent;
  const draft = editing
    ? JSON.parse(JSON.stringify({ name: agent.name, instructions: agent.instructions, mode: agent.mode, tools: agent.tools, trigger: agent.trigger, limits: agent.limits, runtime: agent.runtime || 'provider' }))
    : JSON.parse(JSON.stringify(TEMPLATES[0].agent));
  draft.trigger = Object.assign({ type: 'message', groups: false, keywords: [], chats: [], labels: [] }, draft.trigger || {});
  draft.limits = Object.assign({ max_steps: 8, max_recipients: 3, max_sends: 5, max_writes: 10, max_group_changes: 3, daily_runs: 200 }, draft.limits || {});
  let step = editing ? 1 : 0;
  const title = h('h2', null, editing ? `Editar ${agent.name}` : 'Novo agente');
  const stepper = h('ol', { class: 'stepper', 'aria-label': 'Etapas' });
  const body = h('div', { class: 'wiz-body' });
  const foot = h('div', { class: 'wiz-foot' });
  const shell = dialog({ label: editing ? 'Editar agente' : 'Novo agente', wide: true, onClose: () => shell.close() });
  shell.dlg.classList.add('wizard', 'ag-editor');
  mount(shell.dlg,
    h('div', { class: 'wiz-head' }, title, h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Fechar', onclick: () => shell.close() }, icon('x'))),
    stepper, body, foot);

  let collect = () => true; // reads the current step's inputs into draft; false keeps the step
  const go = (n) => {
    step = n;
    mount(stepper, STEPS.map((label, i) => h('li', { class: i < n ? 'done' : i === n ? 'current' : '', 'aria-current': i === n ? 'step' : null },
      h('span', { class: 'n' }, i < n ? icon('check', 14) : String(i + 1)), label)));
    collect = [templateStep, instructionsStep, permissionsStep, modeStep][n]();
    const back = h('button', { class: 'btn', type: 'button', onclick: () => { if (collect()) go(n - 1); } }, icon('back', 16), 'Voltar');
    const next = h('button', { class: 'btn primary', type: 'button', onclick: () => { if (collect()) go(n + 1); } }, 'Continuar', icon('next', 16));
    const save = h('button', { class: 'btn primary', type: 'button' }, editing ? 'Salvar' : 'Criar agente');
    save.addEventListener('click', async () => {
      if (!collect()) return;
      save.disabled = true;
      try {
        const v = editing
          ? await api.put(`${base}/${encodeURIComponent(agent.id)}`, draft)
          : await api.post(base, Object.assign({ enabled: false }, draft));
        toast(editing ? 'Agente salvo' : `Agente "${v.name}" criado (pausado: ligue quando quiser)`);
        shell.close();
        onSaved(v);
      } catch (e) { toast(e, 'error'); save.disabled = false; }
    });
    mount(foot, n > 0 ? back : null, h('span', { class: 'grow' }), n < STEPS.length - 1 ? next : null, n === STEPS.length - 1 || editing ? save : null);
  };

  function templateStep() {
    const pick = (t) => { Object.assign(draft, JSON.parse(JSON.stringify(t.agent))); draft.trigger = Object.assign({ type: 'message', groups: false, keywords: [], chats: [], labels: [] }, draft.trigger); go(1); };
    mount(body,
      h('p', { class: 'lead' }, 'Escolha um ponto de partida. Tudo pode ser editado depois.'),
      h('div', { class: 'ag-templates' }, TEMPLATES.map((t) => h('button', { class: 'card ag-template', type: 'button', onclick: () => pick(t) },
        h('span', { class: 'empty-icon' }, icon(t.icon, 22)), h('strong', null, t.title), h('span', { class: 'small muted' }, t.text)))));
    return () => true;
  }

  function instructionsStep() {
    const name = h('input', { value: draft.name, maxlength: 80, required: true, placeholder: 'ex.: Atendente da loja' });
    const instr = h('textarea', { rows: 12, maxlength: 20000, placeholder: 'Diga em português o que o agente faz, o que ele sabe sobre o seu negócio e quando deve parar e chamar uma pessoa.' });
    instr.value = draft.instructions || '';
    mount(body,
      field('Nome', name),
      field('Instruções', instr, 'As instruções são suas e confiáveis. As mensagens dos contatos nunca viram instruções: o agente as trata como dados. Não coloque senhas ou chaves aqui.'));
    name.focus();
    return () => {
      draft.name = name.value.trim();
      draft.instructions = instr.value.trim();
      if (!draft.name) { toast('Dê um nome ao agente', 'error'); return false; }
      return true;
    };
  }

  function permissionsStep() {
    const boxes = Object.keys(GROUPS).map((g) => {
      const cb = h('input', { type: 'checkbox', checked: (draft.tools || []).includes(g), 'data-group': g });
      if (RISK[g]) {
        cb.addEventListener('change', async () => {
          if (!cb.checked) return;
          if (!(await confirmDanger(RISK[g], { ok: 'Entendi, permitir' }))) cb.checked = false;
        });
      }
      const [label, hint] = GROUPS[g];
      return h('label', { class: `ag-perm${RISK[g] ? ' risky' : ''}` }, cb,
        h('span', null, h('span', { class: 'ag-perm-title' }, h('strong', null, label), RISK[g] ? h('span', { class: 'badge tone-danger' }, 'risco') : null), h('small', { class: 'hint' }, hint)));
    });
    const recips = h('input', { type: 'number', min: 1, max: 10, value: draft.limits.max_recipients });
    const steps = h('input', { type: 'number', min: 1, max: 20, value: draft.limits.max_steps });
    const daily = h('input', { type: 'number', min: 0, max: 100000, value: draft.limits.daily_runs });
    const sends = h('input', { type: 'number', min: 1, max: 50, value: draft.limits.max_sends });
    const writes = h('input', { type: 'number', min: 1, max: 100, value: draft.limits.max_writes });
    mount(body,
      h('p', { class: 'lead' }, 'O motor do LiteZap aplica estas permissões, não só o texto das instruções: um envio fora do permitido é recusado.'),
      h('div', { class: 'ag-perms' }, boxes),
      h('fieldset', null, h('legend', null, 'Limites'),
        h('div', { class: 'grid3' },
          field('Passos por execução', steps, 'Cada passo é uma chamada paga ao modelo.'),
          field('Destinatários por execução', recips, 'Sem envio em massa.'),
          field('Envios por execução', sends, 'Mensagens, reações e mídias, também na própria conversa.'),
          field('Ações por execução', writes, 'Todas as escritas: envios, etiquetas, grupos (rascunhos contam).'),
          field('Execuções por dia', daily, '0 = sem limite do agente (os limites do Atendente IA continuam valendo).'))));
    return () => {
      draft.tools = boxes.map((b) => b.querySelector('input')).filter((cb) => cb.checked).map((cb) => cb.dataset.group);
      draft.limits = Object.assign({}, draft.limits, {
        max_steps: Number(steps.value) || 8, max_recipients: Number(recips.value) || 3, max_sends: Number(sends.value) || 5,
        max_writes: Number(writes.value) || 10, daily_runs: Number(daily.value) || 0,
      });
      return true;
    };
  }

  function modeStep() {
    const radios = Object.entries(MODE).map(([k, [label, , hint]]) => {
      const r = h('input', { type: 'radio', name: 'ag-mode', value: k, checked: draft.mode === k });
      return h('label', { class: 'ag-perm' }, r, h('span', null, h('strong', null, label), h('small', { class: 'hint' }, hint)));
    });
    const type = h('select', null, h('option', { value: 'message' }, 'Mensagem nova'), h('option', { value: 'manual' }, 'Só manual (Executar agora)'));
    type.value = draft.trigger.type || 'message';
    const groups = h('input', { type: 'checkbox', checked: !!draft.trigger.groups });
    const keywords = h('input', { value: (draft.trigger.keywords || []).join(', '), placeholder: 'vazio = qualquer mensagem' });
    const chats = h('textarea', { rows: 2, class: 'mono', placeholder: 'vazio = todas as conversas; um número ou JID por linha' });
    chats.value = (draft.trigger.chats || []).join('\n');
    const labels = h('input', { value: (draft.trigger.labels || []).join(', '), placeholder: 'vazio = qualquer etiqueta' });
    const filters = h('div', null,
      h('label', { class: 'check' }, groups, ' Também em grupos (desligado: só conversas 1:1)'),
      field('Palavras-chave', keywords, 'Separadas por vírgula. O agente só roda se a mensagem tiver uma delas.'),
      field('Só nestas conversas', chats),
      field('Só em conversas com estas etiquetas', labels, 'Nome ou id da etiqueta (WhatsApp Business).'));
    const sync = () => { filters.hidden = type.value !== 'message'; };
    type.addEventListener('change', sync);
    sync();
    const runtime = runtimeField(ctx, base, draft, editing ? agent.runtime : 'provider');
    mount(body,
      runtime.el,
      h('fieldset', null, h('legend', null, 'Modo'), h('div', { class: 'ag-perms' }, radios)),
      h('fieldset', null, h('legend', null, 'Gatilho'), field('Quando roda', type), filters));
    return () => {
      const m = radios.map((l) => l.querySelector('input')).find((r) => r.checked);
      draft.mode = m ? m.value : 'aprovacao';
      draft.trigger = { type: type.value, groups: groups.checked, keywords: listOf(keywords.value), chats: listOf(chats.value), labels: listOf(labels.value) };
      return runtime.collect();
    };
  }

  go(step);
}

// ---------- run now ----------

function runNowDialog(ctx, base, a, onDone) {
  const { dlg, close } = dialog({ label: 'Executar agora' });
  const chat = h('input', { class: 'mono', placeholder: 'opcional: número ou JID da conversa' });
  const input = h('textarea', { rows: 3, maxlength: 4000, placeholder: 'opcional: um pedido para esta execução' });
  const out = h('div');
  const go = h('button', { class: 'btn primary', type: 'submit' }, icon('send', 14), 'Executar');
  const form = h('form', {
    onsubmit: async (ev) => {
      ev.preventDefault();
      go.disabled = true;
      mount(out, h('p', { class: 'muted' }, 'Executando… pode levar alguns segundos por passo.'));
      try {
        const body = {};
        if (chat.value.trim()) body.chat = chat.value.trim();
        if (input.value.trim()) body.input = input.value.trim();
        const r = await api.post(`${base}/${encodeURIComponent(a.id)}/run`, body);
        mount(out, runSummary(r));
        onDone(r);
      } catch (e) { mount(out, h('p', { class: 'error' }, errView(e))); } finally { go.disabled = false; }
    },
  },
  h('div', { class: 'modal-head' }, h('h2', null, `Executar ${a.name}`),
    h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Fechar', onclick: close }, icon('x'))),
  h('div', { class: 'modal-body' },
    h('p', { class: 'small muted' }, `Modo ${MODE[a.mode] ? MODE[a.mode][0] : a.mode}: ${MODE[a.mode] ? MODE[a.mode][2] : ''}`),
    field('Conversa', chat, 'Com uma conversa, o agente vê as últimas mensagens dela e responde nela.'),
    field('Pedido', input),
    out),
  h('div', { class: 'modal-actions' }, h('button', { class: 'btn', type: 'button', onclick: close }, 'Fechar'), go));
  mount(dlg, form);
  chat.focus();
}

// ---------- runs ----------

function statusBadge(s) {
  const [label, tone] = STATUS[s] || [s, ''];
  return h('span', { class: `badge ${tone}` }, label);
}

function stepsList(steps) {
  if (!steps || !steps.length) return h('p', { class: 'small muted' }, 'Nenhuma ferramenta chamada.');
  return h('ol', { class: 'ag-steps' }, steps.map((s) => {
    const [label, tone] = OUTCOME[s.outcome] || [s.outcome, ''];
    return h('li', null,
      h('div', { class: 'row' }, h('span', { class: 'mono' }, s.tool), h('span', { class: `badge ${tone}` }, label),
        s.chat ? h('span', { class: 'small muted' }, recipient(s.chat)) : null, h('span', { class: 'small muted' }, `passo ${s.n}`)),
      s.args && s.args !== '{}' ? h('pre', { class: 'json' }, s.args) : null,
      s.result ? h('details', null, h('summary', { class: 'small' }, 'Resultado'), h('pre', { class: 'json' }, s.result)) : null);
  }));
}

function runSummary(r) {
  return h('div', { class: 'ag-run-sum' },
    h('div', { class: 'row' }, statusBadge(r.status), r.dry_run ? h('span', { class: 'badge ag-tone-info' }, 'simulação: nada foi enviado') : null,
      h('span', { class: 'small muted' }, `${r.model_calls} chamada(s) ao modelo · ${usd(r.cost_usd)} · ${r.sent} executada(s) · ${r.drafts} rascunho(s)`)),
    r.error ? h('p', { class: 'small warn' }, r.error) : null,
    r.output ? h('div', { class: 'ag-output prewrap' }, r.output) : null,
    stepsList(r.steps));
}

// ---------- agent page ----------

function agentPage(ctx, base, id) {
  const head = h('div', { class: 'card' }, skeleton(1, 'skel-line'));
  const runsBox = h('div', { class: 'card' });
  const simBox = h('div', { class: 'card' });
  let agent = null;
  const url = `${base}/${encodeURIComponent(id)}`;

  const renderHead = () => {
    const a = agent;
    const toggle = h('input', { type: 'checkbox', class: 'ag-switch', checked: a.enabled, 'aria-label': 'Ligado' });
    toggle.addEventListener('change', async () => {
      toggle.disabled = true;
      try { agent = await api.put(url, { enabled: toggle.checked }); renderHead(); toast(agent.enabled ? 'Agente ligado' : 'Agente pausado'); } catch (e) { toast(e, 'error'); toggle.checked = !toggle.checked; toggle.disabled = false; }
    });
    mount(head,
      h('a', { href: ctx.href('agents'), class: 'back' }, icon('back', 16), 'Agentes'),
      h('div', { class: 'ag-setup-row' },
        h('div', { class: 'grow' },
          h('div', { class: 'row' }, h('h2', { class: 'ag-h' }, a.name), modeBadge(a.mode)),
          h('p', { class: 'small muted' }, triggerText(a.trigger), ' · ', (a.tools || []).map((g) => (GROUPS[g] ? GROUPS[g][0] : g)).join(', ') || 'sem permissões'),
          h('p', { class: 'small' }, `${a.today.runs} execução(ões) hoje · ${usd(a.today.cost_usd)} · até ${a.limits.max_steps} passos e ${a.limits.max_recipients} destinatários por execução`)),
        h('label', { class: 'check ag-switch-label' }, toggle, h('span', null, a.enabled ? 'Ligado' : 'Pausado'))),
      a.instructions ? h('details', null, h('summary', { class: 'small' }, 'Instruções'), h('div', { class: 'ag-output prewrap' }, a.instructions)) : null,
      h('div', { class: 'row end' },
        h('button', { class: 'btn', type: 'button', onclick: () => openEditor(ctx, base, agent, (v) => { agent = v; renderHead(); }) }, icon('edit', 14), 'Editar'),
        h('button', { class: 'btn primary', type: 'button', onclick: () => runNowDialog(ctx, base, agent, () => loadRuns()) }, icon('send', 14), 'Executar agora')));
  };

  const loadRuns = async () => {
    let runs = [];
    try { runs = (await api.get(`${url}/runs?limit=30`)).runs || []; } catch (e) { mount(runsBox, h('p', { class: 'error' }, errView(e))); return; }
    mount(runsBox,
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Execuções recentes'),
        h('button', { class: 'btn small ghost', type: 'button', onclick: loadRuns }, icon('refresh', 14), 'Atualizar')),
      runs.length
        ? h('ul', { class: 'ag-runs' }, runs.map((r) => h('li', null, h('details', null,
          h('summary', null,
            statusBadge(r.status),
            h('span', null, ` ${r.trigger === 'manual' ? 'manual' : 'mensagem'} `),
            r.chat ? h('span', { class: 'mono small' }, recipient(r.chat)) : null,
            h('span', { class: 'small muted' }, ` · ${fmtTime(r.started_at)} · ${(r.steps || []).length} passo(s) · ${usd(r.cost_usd)}`)),
          runSummary(r)))))
        : h('p', { class: 'muted small' }, 'Nenhuma execução ainda. Use "Executar agora" ou o simulador abaixo.'));
  };

  const renderSim = () => {
    const script = h('textarea', { rows: 5, class: 'mono', placeholder: 'cliente: oi, vocês abrem sábado?\nhumano: abrimos sim\ncliente: até que horas?' });
    const chat = h('input', { class: 'mono', placeholder: 'opcional: conversa real para as leituras' });
    const input = h('input', { placeholder: 'opcional: pedido para a execução' });
    const out = h('div');
    const run = h('button', { class: 'btn primary', type: 'button' }, 'Simular');
    run.addEventListener('click', async () => {
      const map = { cliente: 'customer', 'você': 'agent', voce: 'agent', agente: 'agent', humano: 'human', auto: 'automatic' };
      const messages = script.value.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => {
        const m = l.match(/^(cliente|você|voce|agente|humano|auto)\s*:\s*(.*)$/i);
        return m ? { from: map[m[1].toLowerCase()], text: m[2] } : { from: 'customer', text: l };
      });
      const body = { agent_id: id, messages };
      if (chat.value.trim()) body.chat = chat.value.trim();
      if (input.value.trim()) body.input = input.value.trim();
      run.disabled = true;
      mount(out, h('p', { class: 'muted' }, 'Consultando o modelo…'));
      try { mount(out, runSummary(await api.post(`${base}/simulate`, body))); } catch (e) { mount(out, h('p', { class: 'error' }, errView(e))); } finally { run.disabled = false; }
    });
    mount(simBox,
      h('h2', null, 'Simulador'),
      h('p', { class: 'small muted' }, 'Roda o agente de verdade, mas as ações de escrita são sempre faz de conta: você vê o que ele faria, sem enviar nada. Uma mensagem por linha, com cliente:, humano:, agente: ou auto:. Conta no limite diário do simulador.'),
      script,
      h('div', { class: 'grid2' }, field('Conversa', chat), field('Pedido', input)),
      h('div', { class: 'row end' }, run),
      out);
  };

  const load = async () => {
    try { agent = await api.get(url); } catch (e) {
      mount(head, h('a', { href: ctx.href('agents'), class: 'back' }, icon('back', 16), 'Agentes'), h('p', { class: 'error' }, errView(e)));
      return;
    }
    renderHead();
    loadRuns();
    renderSim();
  };
  load();
  let timer = null;
  return {
    el: h('div', { class: 'stack' }, head, runsBox, simBox),
    refresh(type) {
      if (type === 'agent.run.completed') { clearTimeout(timer); timer = setTimeout(loadRuns, 600); }
    },
    cleanup() { clearTimeout(timer); },
  };
}
