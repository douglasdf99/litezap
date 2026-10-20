// "Conectar a IA": hands the LiteZap extension (.mcpb) to Claude Desktop (the one primary
// action there: Claude enables it with no restart), connects Claude Code (claude mcp add,
// or the LiteZap plugin with ready-made commands and agents) and Cursor. The Claude
// Desktop config file option stays under "Avançado". Only the tray app enables it
// (GET api/info -> ai_clients).
import * as api from './api.js';
import { h, mount, toast, modal, copyButton } from './ui.js';

const BASE = '/app/api/ai-clients';
// safeHref (ui.js) only lets the public docs through: the download link lives there.
const EXT_DOWNLOAD = 'https://litezap.com/docs/baixar/#extensão-do-claude-desktop';

function statusBadge(c) {
  if (c.error) return h('span', { class: 'badge st-failed', title: c.error }, 'configuração inválida');
  if (c.extension_installed && c.extension_disabled) {
    return h('span', { class: 'badge st-qr_pending', title: 'Ative em Configurações > Extensões, no Claude Desktop' }, 'extensão desativada');
  }
  if (c.extension_installed) return h('span', { class: 'badge st-connected' }, 'Conectado via extensão');
  if (c.connected && c.restart_needed) return h('span', { class: 'badge st-qr_pending', title: c.hint }, 'conectado (reinicie o Claude)');
  if (c.connected) return h('span', { class: 'badge st-connected' }, 'conectado');
  if (c.present) return h('span', { class: 'badge st-qr_pending', title: 'A entrada do LiteZap aponta para outro programa' }, 'precisa atualizar');
  if (!c.installed) return h('span', { class: 'badge' }, 'não encontrado');
  return h('span', { class: 'badge' }, 'não conectado');
}

// commandRow is one command to paste, with its copy button.
function commandRow(cmd) {
  return h('div', { class: 'row ai-cmd' }, h('code', null, cmd), copyButton(cmd, 'Copiar', 'btn small'));
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
        r.hint ? h('p', null, r.hint) : null,
        h('p', { class: 'muted' }, 'Se nada aparecer, abra o Claude Desktop, vá em Configurações > Extensões e arraste para lá o arquivo:'),
        h('p', { class: 'small' }, h('code', null, r.path || '')),
        r.manual_removed
          ? h('p', { class: 'small' }, 'A configuração manual do LiteZap (arquivo de configuração do Claude) foi removida, para você não ver as ferramentas em dobro.',
            r.manual_hint ? ' ' + r.manual_hint : '')
          : null,
        r.manual_error
          ? h('p', { class: 'small error' }, 'Não foi possível remover a configuração manual do LiteZap (', r.manual_error,
            '): use Avançado > Desconectar aqui para não ver as ferramentas em dobro.')
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

  // configButtons are the config file actions (connect, repair, disconnect) of c.
  const configButtons = (c, connectLabel, primary) => {
    const out = [];
    if (!c.connected) {
      const b = h('button', { class: primary ? 'btn primary' : 'btn', onclick: () => act(c, 'connect', b) }, c.present ? 'Atualizar' : connectLabel);
      out.push(b);
    }
    if (c.present) {
      const b = h('button', { class: 'btn danger', onclick: () => act(c, 'disconnect', b) }, 'Desconectar');
      out.push(b);
    }
    return out;
  };

  const head = (c) => h('div', { class: 'ai-head' }, h('h3', null, c.name), statusBadge(c));

  const desktopCard = (c) => {
    const main = [];
    if (c.extension) {
      const b = h('button', { class: 'btn primary', onclick: () => extension(c, b) },
        c.extension_installed ? 'Reinstalar extensão' : 'Instalar extensão');
      main.push(b);
    } else {
      main.push(h('a', { class: 'btn primary', href: EXT_DOWNLOAD, target: '_blank', rel: 'noopener' }, 'Baixar a extensão (LiteZap.mcpb)'));
    }
    const duplicate = c.extension_installed && c.present;
    return h('div', { class: 'card ai-card' },
      head(c),
      h('p', { class: 'small muted' }, c.extension
        ? 'O próprio Claude Desktop instala e liga a extensão: clique em Instalar na tela dele e as ferramentas aparecem sem reiniciar (pode levar alguns segundos).'
        : 'Esta versão do app não traz a extensão: baixe o LiteZap.mcpb e dê dois cliques nele, com o Claude Desktop aberto.'),
      c.extension_installed && !c.extension_disabled ? h('p', { class: 'small' }, h('b', null, c.hint)) : null,
      c.extension_disabled ? h('p', { class: 'small' }, 'A extensão está desligada: no Claude Desktop, ative-a em Configurações > Extensões.') : null,
      duplicate ? h('p', { class: 'small error' }, 'A configuração manual também está ativa: use Avançado > Desconectar para não ver as ferramentas em dobro.') : null,
      c.error ? h('p', { class: 'error small' }, c.error) : null,
      h('div', { class: 'row' }, main),
      h('details', { class: 'adv', open: duplicate || (c.connected && !c.extension_installed) ? '' : null },
        h('summary', null, 'Avançado'),
        h('p', { class: 'small muted' }, 'Configuração manual pelo arquivo de config do Claude. Só use se a extensão não funcionar: ',
          'ela exige fechar o Claude por completo (Cmd+Q no Mac, Arquivo > Sair no Windows) e abrir de novo.'),
        h('p', { class: 'small muted' }, 'Arquivo: ', h('code', null, c.path)),
        c.restart_needed && c.hint ? h('p', { class: 'small' }, h('b', null, c.hint)) : null,
        h('div', { class: 'row' }, configButtons(c, 'Configurar manualmente', false))));
  };

  const codeCard = (c) => {
    const cmds = c.plugin_commands || [];
    const plugin = h('div', { class: 'ai-plugin', hidden: true },
      h('p', { class: 'small' }, 'Cole os dois comandos, um de cada vez, numa sessão do Claude Code:'),
      cmds.map(commandRow),
      h('p', { class: 'small muted' }, 'O plugin traz as ferramentas do WhatsApp, os comandos /litezap:whatsapp-resumo, ',
        '/litezap:whatsapp-responder e /litezap:whatsapp-agente e os agentes atendente, SDR e follow-up. ',
        'Com o plugin, você não precisa do "Conectar (só ferramentas)": use um dos dois para não ver as ferramentas em dobro.'));
    const pluginBtn = h('button', { class: 'btn', onclick: () => { plugin.hidden = !plugin.hidden; } },
      'Instalar plugin LiteZap (comandos e agentes prontos)');
    return h('div', { class: 'card ai-card' },
      head(c),
      h('p', { class: 'small muted' }, 'Não precisa reiniciar nada: as sessões novas do Claude Code já veem o LiteZap.'),
      (c.connected || c.restart_needed) && c.hint ? h('p', { class: 'small' }, h('b', null, c.hint)) : null,
      c.error ? h('p', { class: 'error small' }, c.error) : null,
      h('div', { class: 'row' }, configButtons(c, 'Conectar (só ferramentas)', true), cmds.length ? pluginBtn : null),
      plugin,
      h('p', { class: 'small muted' }, 'Arquivo: ', h('code', null, c.path)));
  };

  const otherCard = (c) => {
    const showHint = c.restart_needed || (c.connected && c.running);
    return h('div', { class: 'card ai-card' },
      head(c),
      h('p', { class: 'small muted' }, 'Arquivo: ', h('code', null, c.path)),
      showHint && c.hint ? h('p', { class: 'small' }, h('b', null, c.hint)) : null,
      c.error ? h('p', { class: 'error small' }, c.error) : null,
      h('div', { class: 'row' }, configButtons(c, 'Conectar', true)));
  };

  const card = (c) => (c.id === 'claude-desktop' ? desktopCard(c) : c.id === 'claude-code' ? codeCard(c) : otherCard(c));

  const load = async () => {
    try {
      const res = await api.get(BASE);
      mount(list, (res.clients || []).map(card));
    } catch (e) { toast(e, 'error'); }
  };

  load();
  mount(root, shell(h('section', null,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, 'Conectar a IA'),
      h('p', { class: 'lead' }, 'Use o seu WhatsApp pelo Claude ou pelo Cursor (MCP), sem copiar tokens. No Claude Desktop, instale a ',
        'extensão do LiteZap; no Claude Code, conecte as ferramentas ou instale o plugin com comandos e agentes prontos. ',
        'Quando o LiteZap mexe num arquivo de configuração, os outros servidores não mudam e o original ganha uma cópia (.litezap.bak).'))),
    h('p', { class: 'secret-note' }, 'O LiteZap precisa estar aberto para a IA usar o WhatsApp.'),
    list), 'ai'));
  return () => {};
}
