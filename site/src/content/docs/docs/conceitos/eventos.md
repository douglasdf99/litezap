---
title: Eventos
description: Envelope, versionamento, webhooks × SSE e as garantias de entrega.
---

Tudo o que acontece numa instância (mensagem recebida, status de envio, conexão caiu, QR novo) vira um **evento**
com o mesmo envelope. Eventos chegam por dois canais:

| | Webhooks | SSE |
|---|---|---|
| Como | `POST` assinado para uma URL sua | Conexão HTTP aberta (`text/event-stream`) |
| Precisa de servidor público | Sim (ou rede local com `webhook_allow_private_urls`) | Não |
| Persistência | Outbox no banco; sobrevive a restarts | Nenhuma; só ao vivo |
| Se o receptor cair | Retry por até 24h, depois dead-letter | Eventos perdidos (sem replay) |
| Bom para | Integrações de produção | Painéis, depuração, apps locais |

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

`data` varia por tipo; o [catálogo](/docs/eventos/) tem uma página por evento, com exemplos.

## Versionamento

- **Adicionar** campos em `data` não muda a versão: seu código deve ignorar campos desconhecidos.
- **Remover** ou **mudar o sentido** de um campo criaria `version: 2`. Cada webhook escolhe a versão na criação
  (hoje só existe a 1).

## Garantias

- **Pelo menos uma vez** nos webhooks: deduplique por `X-LiteZap-Event-Id`.
- **Sem ordem garantida**: retentativas reordenam; use `timestamp`. Um recibo que chega antes da confirmação do envio
  pode produzir `message.status: delivered` antes de `message.sent`.
- **Sem eventos individuais do histórico**: as mensagens sincronizadas no pareamento são gravadas no log e geram um
  único [`history.synced`](/docs/eventos/history-synced/).
- **Um evento por mensagem**, mesmo que o worker a receba duas vezes numa reconexão.

## Limites conhecidos

- Antes de chegar ao outbox, o evento passa por um buffer em memória (até 10.000). Um crash do supervisor exatamente
  entre gravar a mensagem no log e gravar a entrega, ou um buffer cheio (registrado no log), perde aquele evento para
  os webhooks. Um shutdown normal grava o buffer antes de sair. A mensagem continua no log e em `GET /chats`.
