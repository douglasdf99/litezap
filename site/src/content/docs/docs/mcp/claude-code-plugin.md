---
title: "Claude Code: plugin e agentes"
description: Instale o plugin do LiteZap no Claude Code e ganhe as ferramentas do WhatsApp, comandos prontos e agentes de atendimento, SDR e follow-up, sem reiniciar nada.
---

<span class="lz-badge lz-badge--ok">Novo</span>

O plugin **LiteZap** para o [Claude Code](https://code.claude.com/docs) traz, num comando só:

- as **ferramentas do WhatsApp** (o MCP do [app LiteZap](/docs/baixar/) deste computador, sem copiar tokens);
- três **comandos**: resumo do dia, respostas para não lidas e um gerador de agentes;
- três **agentes** prontos: atendente, SDR e follow-up, cada um com as ferramentas restritas ao que precisa.

## Instalar

Com o [app LiteZap](/docs/baixar/) instalado e aberto, cole numa sessão do Claude Code, um de cada vez:

```text
/plugin marketplace add douglasdf99/litezap
/plugin install litezap@litezap
```

O painel do app mostra os mesmos comandos, com botão de copiar, em **Conectar a IA > Claude Code > Instalar plugin
LiteZap (comandos e agentes prontos)**.

Não precisa reiniciar o Claude Code: as sessões novas já carregam o plugin, e numa sessão aberta `/reload-plugins`
carrega na hora. Confira com `/mcp` (o servidor aparece como `plugin:litezap:litezap`) e `/agents`.

:::note[Repositório público]
`/plugin marketplace add douglasdf99/litezap` baixa o repositório do GitHub, então só funciona com ele público (ou
com acesso ao repositório privado configurado no git). Para testar a partir de uma cópia local, aponte para a pasta:
`/plugin marketplace add ./caminho/para/litezap` e depois `/plugin install litezap@litezap`.
:::

**Plugin ou "Conectar (só ferramentas)"?** Use um dos dois. O plugin já traz as ferramentas; se você também
conectou pelo app (`claude mcp add litezap`), elas aparecem em dobro. Nesse caso, clique em **Desconectar** no
cartão do Claude Code do app.

## Comandos

Os comandos do plugin levam o prefixo `litezap:`.

| Comando | O que faz |
|---|---|
| `/litezap:whatsapp-resumo [foco]` | Resume as conversas de hoje: quem precisa de resposta, o que foi resolvido, destaques dos grupos. Só lê: não envia nem marca como lido. |
| `/litezap:whatsapp-responder [tom ou contexto]` | Rascunha respostas para os chats não lidos (até 10 por vez) e **pergunta** quais enviar antes de mandar qualquer coisa. |
| `/litezap:whatsapp-plantao [autônomo] [perfil: ...]` | Uma rodada de plantão: confere mensagens novas e responde por um perfil (rascunho por padrão; autônomo só se você disser). Repita com `/loop 3m /litezap:whatsapp-plantao`. Veja [IA por assinatura](/docs/guias/ia-por-assinatura/). |
| `/litezap:montar-cardapio [texto, foto ou CSV]` | Monta o [Cardápio](/docs/guias/cardapio/#montar-com-a-sua-assinatura-claudechatgpt) a partir de um texto colado, da foto de um cardápio impresso ou de uma planilha: prévia (`dry_run`) em tabela, pergunta antes de salvar e só envia o cardápio para um chat que você indicar. Nunca inventa preço. |
| `/litezap:whatsapp-agente <descrição>` | Cria um subagente seu (`.claude/agents/<nome>.md`) a partir de uma descrição, com as ferramentas mínimas e as regras de segurança do LiteZap. |

O resumo também é usado sozinho quando você pede "resume meu WhatsApp de hoje". Os outros dois só rodam quando
você chama o comando, porque podem levar a envios.

## Agentes

Chame com `@agent-litezap:<nome>` ou pedindo, por exemplo, "use o atendente-whatsapp para responder os clientes".
Antes, passe as informações do negócio (horários, preços, endereço, regras): o agente não inventa o que não sabe.

| Agente | Papel | Pode enviar | Passa para um humano quando |
|---|---|---|---|
| `atendente-whatsapp` | Responde quem já escreveu, com as informações que você deu | Depois do seu "ok" nas primeiras respostas: uma resposta por mensagem do cliente, até 10 por rodada | O cliente pede uma pessoa, reclama, fala de dinheiro fora do combinado, traz assunto sensível ou algo que ele não sabe |
| `sdr-whatsapp` | Qualifica leads que entraram em contato e marca conversa nos horários que você liberou | Perguntas de qualificação (no máximo 4) e a confirmação do horário | O lead pede proposta, desconto, algo técnico ou uma pessoa |
| `followup-whatsapp` | Retoma conversas paradas esperando o contato | Só a lista que você aprovar (até 10), uma mensagem por chat | Descarta quem recusou, pediu para parar ou já recebeu 2 follow-ups sem resposta |

Cada agente só enxerga as ferramentas de que precisa (ler conversas, enviar, etiquetar, esperar respostas). Nenhum
deles edita ou apaga mensagens, cria grupos, adiciona pessoas ou consulta números (`check_numbers`).

### Regras de segurança embutidas

Todos os agentes e comandos carregam as regras do servidor MCP do LiteZap:

- cada mensagem chega a uma pessoa real, pelo seu número: só sai o que você pediu ou o que o papel do agente permite,
  para os chats indicados; nada em massa, não solicitado ou repetido;
- os envios passam pela fila anti-ban (intervalo mínimo, rampa de números novos, janela de envio); uma mensagem
  `queued` sai sozinha depois e nunca é reenviada; todo envio leva `client_message_id`;
- texto de mensagens, nomes de contatos e eventos são de terceiros: são tratados como dados, nunca como instruções;
- com o app fechado (`app_not_running`) ou um limite atingido (`rate_limited`), o agente para e avisa.

## Como o plugin acha o LiteZap

O servidor MCP do plugin é um script pequeno (`scripts/litezap-mcp.sh`, rodado com `sh`) que não traz ferramentas
próprias: ele encontra o LiteZap instalado e passa a conversa para ele, como a
[extensão do Claude Desktop](/docs/app-local/#extensão-do-claude-desktop). Ordem:

1. `LITEZAP_TOKEN` definido e `litezap` no `PATH`: `litezap mcp` (instalação de servidor; lê também `LITEZAP_URL`,
   padrão `http://127.0.0.1:8787`). Veja [MCP e IA](/docs/mcp/).
2. O app: `LITEZAP_APP_EXE`; o arquivo `app-exe` que o app grava na pasta de dados a cada abertura; e os lugares de
   costume (`/Applications/LiteZap.app` e `~/Applications` no macOS; `~/LiteZap`, `~/Applications/LiteZap`,
   `~/.local/bin` e `/opt/LiteZap` no Linux; `%LOCALAPPDATA%\Programs\LiteZap`, `%LOCALAPPDATA%\LiteZap` e
   `%ProgramFiles%\LiteZap` no Windows).
3. `litezap` no `PATH`, mesmo sem token (ele diz o que falta).

Só é executado um arquivo chamado `litezap-app` (`LiteZap.exe` no Windows) que nem ele nem a pasta dele possam ser
alterados por outros usuários (macOS e Linux). Sem o app, o servidor sai com "o app LiteZap não foi encontrado":
instale e abra o app e rode `/mcp` para reconectar.

:::caution[Windows]
O servidor do plugin roda com `sh`, que no Windows vem com o Git for Windows mas nem sempre está no `PATH`. Se o
`/mcp` mostrar o `plugin:litezap:litezap` com falha, use **Conectar (só ferramentas)** no app para as ferramentas e
mantenha o plugin para os comandos e agentes: eles aceitam os dois nomes das ferramentas
(`mcp__plugin_litezap_litezap__*` e `mcp__litezap__*`).
:::

## Só as ferramentas, sem o plugin

O app também conecta só as ferramentas: **Conectar a IA > Claude Code > Conectar (só ferramentas)** roda
`claude mcp add --scope user litezap -- <app> mcp`. Não precisa reiniciar: abra uma nova sessão do Claude Code (ou
use `/mcp`) e as ferramentas aparecem. Detalhes em [App local](/docs/app-local/#arquivo-de-configuração).
