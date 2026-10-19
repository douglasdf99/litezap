// "Conectar a IA": writes the LiteZap MCP entry into Claude Desktop, Claude Code and
// Cursor on this computer, or hands the LiteZap extension (.mcpb) to Claude Desktop. Only
// the tray app enables it (GET api/info -> ai_clients).
import * as api from './api.js';
import { h, mount, toast, modal } from './ui.js';

const BASE = '/app/api/ai-clients';

function statusBadge(c) {
  if (c.error) return h('span', { class: 'badge st-failed', title: c.error }, 'configuração inválida');
  if (c.connected && c.restart_needed) return h('span', { class: 'badge st-qr_pending', title: c.hint }, 'conectado (reinicie o Claude)');
  if (c.connected) return h('span', { class: 'badge st-connected' }, 'conectado');
  if (c.extension_installed) return h('span', { class: 'badge st-connected' }, 'extensão instalada');
  if (c.present) return h('span', { class: 'badge st-qr_pending', title: 'A entrada do LiteZap aponta para outro programa' }, 'precisa atualizar');
  if (!c.installed) return h('span', { class: 'badge' }, 'não encontrado');
  return h('span', { class: 'badge' }, 'não conectado');
}

export function aiClientsView({ root, shell }) {
  const list = h('div', { class: 'ai-list' }, h('div', { class: 'skel skel-card' }), h('div', { class: 'skel skel-card' }));

  const extension = async (c, btn) => {
    btn.disabled = true;
    try {
      const res = await api.post(`${BASE}/${encodeURIComponent(c.id)}/extension`, {});
      const r = res.result || {};
      modal('Instalar a extensão no Claude Desktop', h('div', null,
        h('p', null, h('b', null, 'O Claude Desktop vai mostrar a instalação da extensão LiteZap: clique em Instalar.')),
        h('p', { class: 'muted' }, 'Se nada aparecer, abra o Claude Desktop, vá em Configurações > Extensões e arraste para lá o arquivo:'),
        h('p', { class: 'small' }, h('code', null, r.path || '')),
        c.present
          ? h('p', { class: 'small muted' }, 'Você também configurou o LiteZap no arquivo de configuração do Claude. Depois de instalar a extensão, ',
            'clique em Desconectar aqui para não ver as ferramentas em dobro.')
          : null,
        h('p', { class: 'muted' }, 'Depois, peça por exemplo: "liste minhas conversas do WhatsApp" ou "manda um oi pro Fulano".')));
    } catch (e) {
      toast(e, 'error');
    } finally {
      btn.disabled = false;
      load();
    }
  };

  const act = async (c, action, btn) => {
    btn.disabled = true;
    try {
      const res = await api.post(`${BASE}/${encodeURIComponent(c.id)}/${action}`, {});
      if (action === 'connect') {
        const r = res.result || {};
        modal(`Conectado ao ${c.name}`, h('div', null,
          h('p', null, h('b', null, res.message)),
          r.running && c.id === 'claude-desktop'
            ? h('p', { class: 'small muted' }, 'Se o Claude apagar a configuração ao fechar, o LiteZap a refaz sozinho.')
            : null,
          h('p', { class: 'muted' }, 'Depois, peça por exemplo: "liste minhas conversas do WhatsApp" ou "manda um oi pro Fulano".'),
          r.backup
            ? h('p', { class: 'small muted' }, 'Cópia do arquivo original: ', h('code', null, r.backup))
            : null));
      } else {
        toast(res.message);
      }
    } catch (e) {
      toast(e, 'error');
    } finally {
      btn.disabled = false;
      load();
    }
  };

  const card = (c) => {
    const actions = [];
    if (c.extension) {
      const b = h('button', { class: 'btn primary', onclick: () => extension(c, b) },
        c.extension_installed ? 'Reinstalar extensão' : 'Instalar extensão (recomendado)');
      actions.push(b);
    }
    if (!c.connected) {
      const label = c.present ? 'Atualizar' : c.extension ? 'Configurar manualmente (arquivo de config)' : 'Conectar';
      const b = h('button', { class: c.extension ? 'btn' : 'btn primary', onclick: () => act(c, 'connect', b) }, label);
      actions.push(b);
    }
    if (c.present) {
      const b = h('button', { class: 'btn danger', onclick: () => act(c, 'disconnect', b) }, 'Desconectar');
      actions.push(b);
    }
    const showHint = c.restart_needed || (c.connected && c.running);
    return h('div', { class: 'card ai-card' },
      h('div', { class: 'ai-head' }, h('h3', null, c.name), statusBadge(c)),
      c.extension
        ? h('p', { class: 'small muted' }, 'Recomendado: a extensão é instalada pelo próprio Claude Desktop e não depende do arquivo de configuração.')
        : null,
      h('p', { class: 'small muted' }, 'Arquivo: ', h('code', null, c.path)),
      showHint && c.hint ? h('p', { class: 'small' }, h('b', null, c.hint)) : null,
      c.error ? h('p', { class: 'error small' }, c.error) : null,
      h('div', { class: 'row' }, actions));
  };

  const load = async () => {
    try {
      const res = await api.get(BASE);
      mount(list, (res.clients || []).map(card));
    } catch (e) { toast(e, 'error'); }
  };

  load();
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, 'Conectar a IA'),
      h('p', { class: 'lead' }, 'Use o seu WhatsApp pelo Claude ou pelo Cursor (MCP). No Claude Desktop, instale a extensão do LiteZap; ',
        'nos outros, um clique adiciona o LiteZap à configuração do programa, sem copiar tokens. Os outros servidores configurados ',
        'não mudam e o arquivo original ganha uma cópia (.litezap.bak).'))),
    h('p', { class: 'secret-note' }, 'O LiteZap precisa estar aberto para a IA usar o WhatsApp. Depois de conectar, reinicie o programa de IA ',
      '(no Claude Desktop: Cmd+Q no Mac, ou Arquivo > Sair no Windows, e abra de novo).'),
    list), 'ai'));
  return () => {};
}
