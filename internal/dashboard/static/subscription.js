// "IA por assinatura" in the Agentes tab: who thinks for an agent. Besides the instance's
// AI provider (API key), an agent can run on the user's own Codex CLI (ChatGPT plan; opt-in
// runtime codex_cli, detected by GET /agents/runtimes). A Claude subscription is NOT a
// runtime: Anthropic does not let third-party apps use claude.ai logins, so the card only
// explains the Claude Code plantão (/litezap:whatsapp-plantao). Server text is rendered as
// text (h()), never as HTML.
import * as api from './api.js';
import { h, mount, toast, errView, icon, confirmDanger, skeleton } from './ui.js';

const GUIDE = 'https://litezap.com/docs/guias/ia-por-assinatura/';
const PLANTAO = '/loop 3m /litezap:whatsapp-plantao';

let cache = null; // {at, view}

async function runtimes(base, refresh) {
  if (!refresh && cache && Date.now() - cache.at < 20000) return cache.view;
  const view = await api.get(`${base}/runtimes${refresh ? '?refresh=1' : ''}`);
  cache = { at: Date.now(), view };
  return view;
}

function codexLine(st) {
  if (!st) return ['Indisponível nesta versão do LiteZap.', 'tone-danger'];
  if (!st.installed) return ['Codex CLI não encontrado neste computador.', 'tone-danger'];
  if (!st.logged_in) return ['Codex instalado, mas sem login.', 'tone-warn'];
  if (st.auth === 'api_key') return ['Codex logado com chave de API (cobrança por uso, não pela assinatura).', 'tone-warn'];
  return [`Pronto: ${st.version || 'Codex'} logado com a sua conta ChatGPT.`, 'tone-ok'];
}

function codexSteps(st) {
  const steps = [];
  if (!st || !st.installed) steps.push(h('li', null, 'Instale o Codex CLI: ', h('code', null, 'npm install -g @openai/codex'), ' (ou ', h('code', null, 'brew install codex'), ').'));
  if (!st || !st.logged_in || st.auth === 'api_key') steps.push(h('li', null, 'No Terminal, rode ', h('code', null, 'codex login'), ' e entre com a sua conta ChatGPT (Plus, Pro ou Business).'));
  steps.push(h('li', null, 'Num agente, em "Quem pensa", escolha "Minha assinatura ChatGPT (Codex)" e aceite o aviso.'));
  return h('ol', { class: 'small' }, steps);
}

// subscriptionCard is the "IA por assinatura" card of the agents list.
export function subscriptionCard(ctx, base) {
  const el = h('div', { class: 'card ag-sub' }, skeleton(1, 'skel-line'));
  const render = async (refresh) => {
    let v;
    try { v = await runtimes(base, refresh); } catch (e) { mount(el, h('p', { class: 'error' }, errView(e))); return; }
    const st = v.codex_cli;
    const [line, tone] = codexLine(st);
    const again = h('button', { class: 'btn small ghost', type: 'button', onclick: () => render(true) }, 'Verificar de novo');
    mount(el,
      h('h2', null, 'IA por assinatura'),
      h('p', { class: 'small muted' }, 'Em vez de pagar a API por chamada, use uma assinatura que você já tem. ',
        h('a', { href: GUIDE, target: '_blank', rel: 'noopener' }, 'Como funciona e limites')),
      h('div', { class: 'ag-sub-grid' },
        h('section', null,
          h('h3', null, 'Minha assinatura ChatGPT (Codex)'),
          h('p', { class: 'small' }, h('span', { class: `badge ${tone}` }, st && st.ready ? 'pronto' : 'configurar'), ' ', line),
          st && st.detail ? h('p', { class: 'small muted' }, st.detail) : null,
          st && st.ready ? null : codexSteps(st),
          h('p', { class: 'small muted' }, 'Opt-in por agente. O LiteZap roda o ', h('code', null, 'codex exec'), ' a cada gatilho, sem shell nem arquivos, só com as ferramentas do agente; as permissões e limites continuam valendo. Os termos da OpenAI sobre esse uso não são claros: leia o aviso antes.'),
          again),
        h('section', null,
          h('h3', null, 'Minha assinatura Claude (via Claude Code)'),
          h('p', { class: 'small' }, 'O LiteZap não usa a sua assinatura Claude sozinho: a Anthropic não permite que apps de terceiros usem o login do claude.ai. O caminho permitido é você deixar uma sessão do Claude Code de plantão, com o plugin LiteZap:'),
          h('p', null, h('code', null, PLANTAO)),
          h('p', { class: 'small muted' }, 'Começa em modo rascunho (você aprova cada envio). Só funciona com a sessão aberta e conta nos limites do seu plano. ',
            h('a', { href: '#/ia' }, 'Instalar o plugin')))));
  };
  render(false);
  return el;
}

// runtimeField is the "Quem pensa" choice of the agent editor: it reads and sets
// draft.runtime (and accept_runtime_notice when switching to codex_cli).
export function runtimeField(ctx, base, draft, current) {
  const radio = (value, title, hint, disabled = false) => {
    const r = h('input', { type: 'radio', name: 'ag-runtime', value, checked: (draft.runtime || 'provider') === value, disabled });
    return h('label', { class: 'ag-perm' }, r, h('span', null, h('strong', null, title), h('small', { class: 'hint' }, hint)));
  };
  const provider = radio('provider', 'Provedor de IA (chave de API)', 'O provedor configurado no Atendente IA: Anthropic ou OpenAI-compatível. Cobrança por chamada.');
  const codex = radio('codex_cli', 'Minha assinatura ChatGPT (Codex)', 'O Codex CLI logado neste computador. Verificando...');
  const claude = h('div', { class: 'ag-perm ag-perm-info' }, icon('sparkles', 16),
    h('span', null, h('strong', null, 'Minha assinatura Claude (via Claude Code)'),
      h('small', { class: 'hint' }, 'Não é uma opção daqui: use o plantão numa sessão sua do Claude Code (', h('code', null, PLANTAO), ').')));
  const codexInput = codex.querySelector('input');
  const codexHint = codex.querySelector('small');
  let notice = '';
  runtimes(base, false).then((v) => {
    const st = v.codex_cli;
    notice = (st && st.notice) || '';
    const [line] = codexLine(st);
    mount(codexHint, line, st && st.ready ? '' : ' Veja os passos no card "IA por assinatura".');
    if (!(st && st.ready) && current !== 'codex_cli') codexInput.disabled = true;
  }).catch((e) => mount(codexHint, errView(e)));
  let accepted = current === 'codex_cli';
  codexInput.addEventListener('change', async () => {
    if (!codexInput.checked || accepted) return;
    const ok = await confirmDanger(notice || 'Este agente vai usar o Codex CLI logado neste computador, com a sua conta.', { ok: 'Entendi, usar o Codex' });
    if (ok) { accepted = true; return; }
    codexInput.checked = false;
    provider.querySelector('input').checked = true;
  });
  const el = h('fieldset', null, h('legend', null, 'Quem pensa'), h('div', { class: 'ag-perms' }, provider, codex, claude));
  const collect = () => {
    const picked = codexInput.checked ? 'codex_cli' : 'provider';
    draft.runtime = picked;
    if (picked === 'codex_cli' && current !== 'codex_cli') draft.accept_runtime_notice = accepted;
    else delete draft.accept_runtime_notice;
    if (picked === 'codex_cli' && !accepted) { toast('Aceite o aviso para usar o Codex', 'error'); return false; }
    return true;
  };
  return { el, collect };
}

// runtimeBadge marks agents that run on the Codex CLI.
export function runtimeBadge(a) {
  return a && a.runtime === 'codex_cli' ? h('span', { class: 'badge ag-tone-info', title: 'Roda no Codex CLI (assinatura ChatGPT)' }, 'Codex') : null;
}
