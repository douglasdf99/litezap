---
title: Catálogo de eventos
description: Envelope, cabeçalhos dos webhooks e todos os tipos de evento do LiteZap.
---

Eventos chegam por [webhook](/docs/guias/webhooks/) (persistente, assinado, com retry) e por
[SSE](/docs/guias/sse/) (ao vivo). O formato é o mesmo nos dois.

## Envelope (v1)

```json
{
  "id": "evt_5f2c9a1b7d3e4f60",
  "type": "message.received",
  "version": 1,
  "instance_id": "inst_7f3a9c2e5b1d4086",
  "timestamp": "2026-10-17T13:45:00.123Z",
  "data": { }
}
```

| Campo | Descrição |
|---|---|
| `id` | Id único do evento. Use para deduplicar. |
| `type` | Tipo do evento (tabela abaixo). |
| `version` | Versão do envelope. Campos novos em `data` não mudam a versão. |
| `instance_id` | Instância de origem. |
| `timestamp` | Quando o evento foi gerado (UTC, com milissegundos). |
| `data` | Conteúdo, que varia por tipo. |

## Cabeçalhos do webhook

Cada entrega é um `POST` com o envelope como corpo (`Content-Type: application/json`) e:

| Cabeçalho | Valor |
|---|---|
| `X-LiteZap-Signature` | `t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<corpo>")>`. Veja [Verificar assinatura](/docs/eventos/assinatura/). |
| `X-LiteZap-Event-Id` | Id do evento (`evt_...`), para deduplicar. |
| `X-LiteZap-Event-Type` | Tipo do evento. |

## Tipos disponíveis

| Tipo | Quando | Desde |
|---|---|---|
| [`instance.status`](/docs/eventos/instance-status/) | Toda transição de estado da instância | M3 |
| [`instance.qr`](/docs/eventos/instance-qr/) | Novo QR de pareamento | M3 |
| [`instance.pair_code`](/docs/eventos/instance-pair-code/) | Código de pareamento gerado | M3 |
| [`message.received`](/docs/eventos/message-received/) | Mensagem nova de outra pessoa | M3 |
| [`message.sent`](/docs/eventos/message-sent/) | Envio pela API confirmado, ou mensagem enviada pelo celular | M3 |
| [`message.status`](/docs/eventos/message-status/) | `sent`, `delivered`, `read` ou `failed` de uma mensagem enviada | M3 |
| [`message.reaction`](/docs/eventos/message-reaction/) | Reação recebida | M3 |
| [`history.synced`](/docs/eventos/history-synced/) | Fim da sincronização de histórico | M3 |

Tipos planejados (`message.revoked`, `chat.presence`, `call.received`, `group.updated`...) estão em
[Eventos em breve](/docs/eventos/em-breve/).

## Filtros

Webhooks (`events`) e SSE (`?types=`) aceitam:

- um tipo exato: `message.status`;
- uma família: `message.*`, `instance.*`;
- tudo: `*`.

Um filtro com tipo desconhecido responde `400 validation_failed`, com a lista de tipos válidos na mensagem.
