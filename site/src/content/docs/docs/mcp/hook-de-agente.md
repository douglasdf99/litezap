---
title: Hook de agente
description: Contrato HTTP para um agente de IA responder mensagens recebidas. Em breve (marco 5).
---

<span class="lz-badge lz-badge--soon">Em breve · M5</span>

:::note[Ainda não disponível]
O contrato abaixo é o desenho aprovado para o marco 5 e pode ter ajustes até a entrega.
:::

O hook de agente faz o LiteZap chamar um serviço seu (ou um exemplo pronto com o Claude) a cada mensagem recebida, e
enviar a resposta que ele devolver.

## Configuração planejada (por instância)

| Campo | Padrão | Descrição |
|---|---|---|
| `url` | — | Endereço do agente |
| `secret` | — | Assina as chamadas como os webhooks |
| `timeout` | 30s | Tempo máximo de resposta |
| `context_messages` | 20 | Quantas mensagens anteriores do chat vão como contexto |
| `enabled` | — | Liga/desliga |
| `groups_enabled` | false | Responder também em grupos |
| `human_pause` | 30 min | Pausa no chat quando um humano responde pelo celular |
| `ignore` | — | JIDs que o agente nunca responde |

Também será possível ligar, desligar ou herdar o agente **por chat**.

## Contrato

O LiteZap faz `POST {url}` assinado:

```json
{
  "instance_id": "inst_7f3a9c2e5b1d4086",
  "chat": { "jid": "5511999999999@s.whatsapp.net", "name": "Maria", "is_group": false },
  "message": { "...": "mesmo objeto de message.received" },
  "context": [ { "...": "últimas N mensagens, mais antiga primeiro" } ]
}
```

O agente responde `200`:

```json
{ "reply": "Seu pedido sai hoje até as 18h.", "actions": [ { "type": "mark_read" } ] }
```

- `reply`: texto, ou um corpo de mensagem completo (o mesmo de `POST /messages`, sem `to`). Opcional.
- `actions`: `mark_read`, `react` (`emoji`), `pause` (`minutes`), `handoff` (`note`, desliga o agente no chat até
  reativação manual).
- `204` ou corpo vazio: não faz nada.
- Erro, resposta inválida ou timeout: nada é enviado (falha fechada) e sai o evento `agent.failed`.

## Garantias planejadas

- Uma mensagem por vez por chat (sem respostas concorrentes).
- **Pausa por humano**: uma mensagem enviada pelo celular (que não foi o LiteZap que enviou) pausa o agente naquele
  chat por `human_pause`.
- Respostas passam pela fila, com o mesmo ritmo e as mesmas proteções de qualquer envio.

Veja o guia [atendimento com IA](/docs/guias/atendimento-ia/) para montar algo parecido hoje com webhooks.
