---
title: Entrega e retentativas
description: Outbox, backoff, dead-letter, idempotência e ordem das entregas de webhook.
---

## Como uma entrega acontece

1. O evento é gerado e passa por um buffer em memória.
2. Para cada webhook habilitado cujo filtro combina, uma **entrega** é gravada numa tabela (outbox) com prazo de
   24 horas.
3. O despachante faz o `POST`. Qualquer `2xx` em até 10 segundos é sucesso.
4. Falha (timeout, erro de rede, status fora de `2xx`, redirecionamento): nova tentativa com backoff.

## Backoff

- Por entrega: 1s, 2s, 4s, 8s... dobrando até no máximo 1 hora entre tentativas.
- Por endpoint: um webhook que falha segura **todas** as entregas dele pelo mesmo backoff, para não martelar um
  servidor fora do ar. Outros webhooks seguem normalmente.
- Concorrência: no máximo 8 entregas simultâneas no total e 2 por webhook.

## Dead-letter

Uma entrega que não teve sucesso em 24 horas passa a `failed` e fica guardada 30 dias (configurável em
`webhook_failed_retention_days`):

```bash
curl -s "localhost:8787/v1/webhooks/$WH/deliveries?status=failed" -H "Authorization: Bearer $TOK"
```

Cada item traz `attempts`, `last_status_code`, `last_error` (ex.: `http 503`, `timeout: ...`, `blocked: ...`) e o
`event` completo. Para reenviar, com mais 24 horas de prazo:

```bash
curl -s -X POST "localhost:8787/v1/webhooks/$WH/deliveries/$DLV/retry" -H "Authorization: Bearer $TOK"
```

Entregas concluídas ficam 7 dias (`webhook_retention_days`).

## Garantias

| Garantia | Detalhe |
|---|---|
| Pelo menos uma vez | A outbox sobrevive a restarts. Uma tentativa interrompida por um crash é repetida, então o mesmo evento pode chegar duas vezes: deduplique por `X-LiteZap-Event-Id`. |
| Sem ordem garantida | Retentativas reordenam. Use `timestamp`, ou consulte o estado atual pela API quando a ordem importa. |
| Shutdown limpo | Um shutdown normal grava os eventos em memória antes de sair; entregas pendentes continuam depois do restart. |

Limite conhecido: entre a geração do evento e a gravação na outbox há um buffer em memória (até 10.000 eventos). Um
crash do supervisor exatamente nesse intervalo, ou um buffer cheio (registrado no log), perde o evento para os
webhooks. A mensagem em si continua no log e em `GET /chats`.

## Webhook desligado

Com `"enabled": false`, o webhook não recebe entregas novas e as pendentes ficam pausadas até religar.

## Destinos bloqueados

Por padrão, entregas para endereços privados, loopback e link-local são recusadas (checagem depois do DNS, a cada
conexão) com `last_error` `blocked: ...`. Para um receptor no próprio PC, ligue `webhook_allow_private_urls`.
Redirecionamentos não são seguidos.
