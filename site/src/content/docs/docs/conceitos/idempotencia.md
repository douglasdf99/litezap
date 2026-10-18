---
title: Idempotência
description: Como repetir um envio com segurança usando client_message_id, e como deduplicar eventos.
---

Redes falham. Um `POST /messages` pode dar timeout depois de a mensagem já ter entrado na fila; se você simplesmente
repetir, o contato recebe duas vezes. O LiteZap resolve isso com `client_message_id`.

## No envio

```bash
curl -s -X POST "localhost:8787/v1/instances/$ID/messages" -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"text","text":"Pedido 123 confirmado","client_message_id":"pedido-123-confirmacao"}'
```

| Situação | Resultado |
|---|---|
| Primeira vez | Mensagem criada (`202`, ou `200` com `?wait=true`). |
| Mesmo `client_message_id`, mesmo conteúdo | Devolve **a mesma** mensagem, com o status atual. Nada é enviado de novo. |
| Mesmo `client_message_id`, conteúdo diferente | `409 conflict`. |

- Até 128 caracteres, único por instância. Use um id do seu domínio (pedido, ticket, agendamento) mais o propósito.
- A verificação acontece antes de baixar qualquer mídia: repetir um envio de vídeo não baixa o vídeo de novo.
- Duas requisições simultâneas com o mesmo id resultam numa única mensagem.

Combine com `?wait=true` para um envio "síncrono e seguro": se a conexão cair, repita a mesma requisição e receba
o resultado da original.

## Nas retentativas internas

O `wa_message_id` é escolhido quando a mensagem entra na fila e reusado em todas as tentativas. Se o worker cair no
meio de um envio, a retentativa usa o mesmo id e o WhatsApp não duplica a mensagem.

## Nos eventos

Webhooks são entregues **pelo menos uma vez**: depois de um crash, uma entrega interrompida é repetida. Deduplique
pelo cabeçalho `X-LiteZap-Event-Id` (o mesmo que `id` no envelope), por exemplo guardando os ids processados nas
últimas 24 horas. Veja [entrega e retentativas](/docs/eventos/entrega/).
