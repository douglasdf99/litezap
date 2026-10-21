---
title: Agentes
description: Agentes LiteZap — uma IA (grátis no seu computador, grátis na nuvem ou o Claude) gerenciando o seu WhatsApp com as ferramentas do LiteZap, com permissões, modo de aprovação, gatilhos e limites de custo.
---

:::tip[Funciona de graça]
Não tem cartão de crédito? Os agentes funcionam com IA **grátis**: no seu computador com o Ollama (`qwen2.5:7b`) ou
na nuvem com o plano grátis do Google Gemini ou da Groq. Veja [IA grátis](/docs/guias/ia-gratis/).
:::

Um **agente LiteZap** é uma IA operando o seu WhatsApp sozinha, 24 horas, dentro das regras que você define. Ele
faz muito mais do que responder: lê conversas, responde, etiqueta, manda mídia e mexe em grupos, sempre só com as
permissões que você deu.

Por baixo, o agente roda um **loop de ferramentas** (*tool use*): o modelo chama as mesmas ferramentas do
[MCP do LiteZap](/docs/mcp/), executadas dentro do motor (sem rede) e com as mesmas validações da API, até concluir a
tarefa. Cada execução fica registrada, com os passos, o custo e o que foi enviado.

:::tip[Já paga uma assinatura?]
Em vez de uma chave de API, um agente pode rodar no **Codex CLI** logado com a sua conta ChatGPT, e uma sessão sua do
Claude Code pode ficar de **plantão** com a sua assinatura Claude. Veja [IA por assinatura](/docs/guias/ia-por-assinatura/).
:::

:::note[Agentes x Atendente IA]
O [Atendente IA](/docs/guias/atendimento-ia/) responde a um chat com um texto, seguindo um roteiro. O agente decide o
que fazer com ferramentas, e pode agir em mais de uma coisa por execução. Os dois usam o **mesmo provedor**, as
mesmas chaves cifradas e os mesmos limites de gasto. Por instância, quem responde às mensagens novas é um destino só:
o seu agente externo (URL), o Atendente LiteZap ou os Agentes.
:::

## Assinatura do Claude x chave de API

Há dois jeitos de usar o Claude com o seu WhatsApp, com as mesmas ferramentas:

| Onde | Como funciona | Quem paga o Claude |
|---|---|---|
| **Claude Code / Claude Desktop** | Você conversa com o Claude, que usa o MCP do LiteZap enquanto você acompanha | A **sua assinatura** do Claude, porque é você usando o seu Claude |
| **Agentes no app** (esta página) | O agente roda sozinho dentro do LiteZap, mesmo com você longe | A **chave de API** da instância (Anthropic ou servidor OpenAI-compatível) |

Os termos da Anthropic não permitem que um produto de terceiros use o login ou os limites da assinatura do Claude.ai
para rodar agentes por conta própria. Por isso os agentes autônomos usam uma **chave de API**, e o LiteZap nunca
chama o seu `claude` por conta própria. Quem quiser usar a assinatura usa o plugin ou o MCP no próprio Claude Code,
de forma interativa.

## Antes de começar

1. **Escolher a IA (admin).** Na aba **Agentes** (ou em **Atendente IA**), em **Qual IA usar?**, escolha uma opção
   grátis — **no meu computador** (Ollama, recomendado), **Google Gemini grátis** ou **Groq grátis** — ou o Claude
   pago. Pela API: `PUT /v1/instances/{id}/attendant` com `{"preset":"ollama"}` (veja [IA grátis](/docs/guias/ia-gratis/)).
   Qualquer servidor OpenAI-compatível funciona se o modelo suportar *function calling*. Com modelos locais pequenos,
   o LiteZap usa instruções mais curtas em português e dá **uma** nova chance quando o modelo manda uma chamada de
   ferramenta inválida; na segunda, a execução termina como `failed` (`invalid_tool_call`) sem executar nada.
2. **Ligar as mensagens novas.** Na aba **Agentes**, ative "Agentes respondem a mensagens novas". Isso coloca o hook
   de agente da instância com `target: "agents"`:

```bash
curl -s -X PUT $LZ/v1/instances/$ID/agent-hook -H "Authorization: Bearer $TOKEN" \
  -d '{"target":"agents","enabled":true}'
```

"Executar agora" e o simulador funcionam sem esse passo.

## Criar um agente

No painel, aba **Agentes** > **Novo agente**. Comece por um modelo pronto (Atendente, Organizador ou Resumo manual),
escreva as instruções em português e escolha as permissões, o modo e o gatilho. Pela API:

```bash
curl -s -X POST $LZ/v1/instances/$ID/agents -H "Authorization: Bearer $TOKEN" -d '{
  "name": "Atendente da loja",
  "enabled": true,
  "instructions": "Responda dúvidas de horário e entrega. Horário: seg a sáb, 9h às 18h. Se pedirem desconto, diga que uma pessoa vai responder.",
  "tools": ["ler", "responder_no_chat"],
  "mode": "aprovacao",
  "trigger": {"type": "message", "keywords": [], "groups": false}
}'
```

As instruções são suas e confiáveis. Tudo o que vem das conversas (mensagens, nomes de contato e de grupo,
resultados das ferramentas) é tratado como **dado não confiável**: vai para o modelo entre marcadores com sufixo
aleatório e com os colchetes e as quebras de linha neutralizados, como no Atendente IA.

## Modos

| Modo | O que acontece com as escritas (enviar, etiquetar, mexer em grupo) |
|---|---|
| `aprovacao` (padrão) | Cada escrita vira um **rascunho**. Você vê o destinatário e a mensagem exata e aprova ou rejeita. Aprovar executa exatamente a chamada guardada, pela fila. Um rascunho vale por 24 horas. |
| `autonomo` | O agente executa direto. Os envios passam pela fila anti-ban como qualquer envio. |
| `sugerir` | Nada é executado: cada escrita vira uma nota na execução, para você decidir. |

As leituras (conversas, mensagens, contatos, grupos, etiquetas) rodam em qualquer modo.

Os rascunhos ficam na aba Agentes ("Rascunhos pendentes") e em `GET /agents/drafts?status=pending`; decida com
`POST /agents/drafts/{draft_id}/approve` ou `/reject`. Os eventos `agent.draft.created` e `agent.draft.decided` avisam
o seu sistema (webhook, n8n).

## Permissões

| Grupo | Ferramentas | Observação |
|---|---|---|
| `ler` | `get_messages`, `get_message_status`, `get_contact` e `get_group` **só da conversa do gatilho**; `list_chats` (só nomes, horário e não lidas, sem texto), `list_groups`, `list_labels`, `get_instance_status` | Padrão |
| `ler_outros_chats` | As mesmas leituras em **qualquer** conversa, mais `list_contacts` (agenda) | **Risco**: o conteúdo de outras conversas vai para o provedor de IA; pede confirmação |
| `responder_no_chat` | `send_message`, `send_reaction` | Só no chat que disparou a execução. Padrão |
| `enviar_outros_chats` | `send_message`, `send_reaction` em qualquer chat | **Risco**: pede confirmação no painel |
| `etiquetas` | `label_chat` | Só contas Business. Pode etiquetar **qualquer** conversa |
| `grupos` | `create_group`, `update_group`, `manage_group_participants`, `get_group_invite_link` | **Risco de banimento**: pede confirmação. O link de convite conta como escrita (rascunho na aprovação) |
| `midia` | `send_media` | Segue a regra de chat de `responder_no_chat` |

O **motor** aplica as permissões, não só o prompt. Um envio para outro chat sem `enviar_outros_chats` é recusado,
mesmo que uma mensagem convença o modelo a tentar, e a recusa aparece como passo `refused` na execução. Ferramentas
que nenhum agente recebe: editar e apagar mensagens, bloquear contatos, consultar números, QR e eventos.

## Gatilhos

- **Mensagem nova** (`trigger.type: "message"`): roda quando chega uma mensagem de um contato. Filtros opcionais:
  `chats` (só estes chats), `labels` (só chats com uma destas etiquetas), `keywords` (só se a mensagem contém uma
  destas palavras) e `groups` (grupos ficam **desligados** por padrão). Mensagens suas e do próprio agente nunca
  disparam. Se mais de um agente ligado casar, roda o primeiro criado.
- **Manual** (`trigger.type: "manual"`): só roda com "Executar agora" ou `POST /agents/{agent_id}/run`, com `chat` e um
  pedido (`input`) opcionais. Qualquer agente pode ser executado agora, ligado ou não.

Horário, eventos e vários agentes roteados por chat chegam nas próximas fatias.

## Segurança

Valem as mesmas regras de qualquer envio do LiteZap, e mais algumas:

- **Fila anti-ban**: ritmo, janela de envio, rampa e validade. Nenhum agente contorna.
- **Uma execução por chat de cada vez**: mensagens que chegam durante uma execução esperam; depois, só a mais nova
  dispara a próxima (as outras estão no contexto).
- **Pausa quando um humano responde**: se você responde pelo celular, os agentes param naquele chat pelo tempo de
  `human_pause` (padrão 30 min). Antes de cada escrita no modo autônomo, o motor confere de novo; se você assumiu no
  meio da execução, ela é **descartada** (`discarded`) e nada mais sai.
- **Proteção contra loop**: dois robôs conversando ou um agente descontrolado pausam o chat (`loop_guard`).
- **Sem envio em massa**: por execução, no máximo `max_recipients` destinatários distintos (padrão 3),
  `max_sends` envios (padrão 5, contando a própria conversa), `max_writes` escritas no total (padrão 10, contando
  rascunhos e sugestões) e `max_group_changes` mudanças em grupos (padrão 3).
- **Aprovação revalida**: aprovar um rascunho só funciona com o agente ligado e ainda com a permissão, e repete as
  checagens de destinatário, menções e limites antes de executar. Um rascunho que ficou "aprovando" numa queda do
  servidor é concluído na volta (o envio é idempotente).
- **Registro enxuto**: a execução guarda os argumentos resumidos, mas o resultado das ferramentas (mensagens,
  contatos) só quando a chamada foi recusada ou falhou, com os números mascarados. Os eventos de rascunho levam só
  ids, ferramenta, chat e status, nunca o texto.
- **Chaves**: continuam cifradas e só o admin configura o provedor. Uma resposta que repete as instruções do agente
  ou uma chave é bloqueada.
- **Dados**: com ferramentas, o agente vê números e nomes dos seus chats (como você no celular). Ao provedor vai um
  pseudônimo do chat como identificador do usuário.

## Custos e limites

Cada **passo** de uma execução é uma chamada paga ao provedor. Os limites:

- `limits.max_steps` (padrão 8): chamadas ao modelo por execução. Estourou: a execução para com `step_limit`.
- `limits.daily_runs` (padrão 200): execuções do agente por dia.
- Os limites do Atendente IA valem para cada chamada: `chat_daily_call_cap` (por conversa, padrão 40),
  `daily_call_cap` (da instância, padrão 300) e `simulate_daily_cap` (simulador, padrão 50). Atingido o limite, a
  execução para e sai `agent.failed` com `reason: daily_cap`.

O custo estimado de cada execução aparece nela (`cost_usd`) e na lista de agentes (`today`); o total do dia está em
`GET /attendant/usage`.

## Simulador

Teste um agente antes de ligar: no painel, na página do agente, ou com `POST /agents/simulate`. O simulador roda o
loop de verdade, mas as **ferramentas de escrita são sempre faz de conta** (passo `dry_run`, com o que o agente
faria). As leituras leem os dados reais da instância. Nada é guardado nem enviado.

```bash
curl -s -X POST $LZ/v1/instances/$ID/agents/simulate -H "Authorization: Bearer $TOKEN" -d '{
  "agent": {"name": "Teste", "instructions": "Responda dúvidas de horário: seg a sáb, 9h às 18h."},
  "messages": [{"text": "vocês abrem sábado?"}]
}'
```

## Registro

Cada execução guarda o gatilho, os passos (ferramenta, argumentos resumidos e o resultado só das chamadas recusadas
ou com erro), o resumo final do agente,
o que foi enviado ou virou rascunho, o custo e a duração, por 30 dias: `GET /agents/{agent_id}/runs` e
`GET /agents/runs/{run_id}`. Ao terminar, sai o evento `agent.run.completed`.

A referência completa está em [API › Agentes](/docs/api/agentes/).
