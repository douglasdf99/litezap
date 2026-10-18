---
title: Hook de agente
description: Contrato HTTP para um agente de IA seu responder mensagens recebidas — ou o Atendente LiteZap embutido.
---

O hook de agente entrega cada mensagem recebida a um atendente de IA e envia a resposta pela fila normal. O destino
(`target`) é:

- `builtin`: o [Atendente LiteZap](/docs/guias/atendimento-ia/) embutido — nada para hospedar;
- `url` (padrão): um serviço seu, chamado com um `POST` assinado como os webhooks. O exemplo
  [`examples/agent-claude`](https://github.com/douglasdf99/litezap/tree/main/examples/agent-claude) implementa o
  contrato com o Claude.

## Configuração (por instância)

`PUT /v1/instances/{id}/agent-hook` (atualização parcial):

| Campo | Padrão | Descrição |
|---|---|---|
| `target` | `url` | `url` ou `builtin` |
| `url` | — | Endereço do agente (só `target: url`) |
| `secret` | gerado | Assina as chamadas como os webhooks; aparece só quando é gerado |
| `enabled` | `false` | Padrão para as conversas sem regra própria |
| `timeout` | 30s | Tempo máximo de resposta (1s–2m) |
| `context_messages` | 20 | Mensagens anteriores do chat no contexto |
| `groups_enabled` | `false` | Responder também em grupos |
| `human_pause` | 30 min | Pausa no chat quando um humano responde pelo celular |
| `pause_on_api_send` | `false` | Pausar também quando alguém envia pela API (CRM, painel) |
| `ignore` | `[]` | Números/JIDs que o agente nunca responde |
| `max_replies_per_window` / `reply_window` | 10 / 10 min | Proteção contra loop |

Por conversa: `PUT /v1/instances/{id}/chats/{jid}/agent` com `{"enabled": true | false | null}`.

## Contrato (`target: url`)

```json
{
  "instance_id": "inst_7f3a9c2e5b1d4086",
  "chat": { "jid": "5511999999999@s.whatsapp.net", "name": "Maria", "is_group": false },
  "message": { "...": "mesmo objeto de message.received" },
  "context": [
    { "id": "msg_1", "from_me": false, "source": "contact", "origin": "contact", "type": "text", "text": "oi" },
    { "id": "q_2", "from_me": true, "source": "api", "origin": "automatic", "type": "text", "text": "Não atendemos ligações." },
    { "id": "msg_3", "from_me": false, "source": "contact", "origin": "contact", "type": "text", "text": "qual o horário?" }
  ]
}
```

`origin` diz **quem escreveu** cada mensagem do contexto:

| `origin` | Quem |
|---|---|
| `contact` | o cliente (ou um participante, em grupos) |
| `agent` | o próprio agente (respostas anteriores) |
| `human` | alguém da equipe, pelo celular |
| `automatic` | mensagem automática do sistema (ex.: aviso de ligação recusada) |
| `api` | outro sistema pela API (CRM, campanhas) |
| `history` | histórico sincronizado do celular (autor desconhecido) |

Trate `automatic` e `api` como contexto, nunca como falas do agente — sem isso, o modelo tende a repetir a última
fala automática como se fosse dele.

O agente responde `200`:

```json
{ "reply": "Seu pedido sai hoje até as 18h.", "actions": [ { "type": "mark_read" } ] }
```

- `reply`: texto, ou um corpo de mensagem (o mesmo de `POST /messages`, sem `to`). Opcional.
- `actions`: `mark_read`, `react` (`emoji`), `pause` (`minutes`), `handoff` (`note`).
- `204` ou corpo vazio: não faz nada.
- Erro, resposta inválida, redirecionamento ou timeout: nada é enviado (falha fechada) e sai `agent.failed`.

## Garantias

- Uma mensagem por vez por chat; as que chegam durante uma chamada são agrupadas.
- **Pausa por humano**: uma mensagem enviada pelo celular pausa o agente naquele chat por `human_pause` e cancela as
  respostas dele ainda na fila.
- Respostas passam pela fila, com o mesmo ritmo, janela, rampa e suspensão de qualquer envio, e expiram em 1h.
- Proteção contra loop: acima de `max_replies_per_window` respostas em `reply_window`, o chat pausa; de novo em 24h,
  o agente é desligado no chat (`agent.handoff`).
