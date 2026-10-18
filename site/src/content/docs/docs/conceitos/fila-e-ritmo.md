---
title: Fila e ritmo
description: Como o LiteZap enfileira, espaça e reenvia mensagens.
---

Todo envio passa por uma **fila persistente por instância**, gravada no SQLite. `POST /messages` só valida e
enfileira; um despachante por instância envia no ritmo configurado.

## O caminho de uma mensagem

1. `POST /v1/instances/{id}/messages` valida o corpo, baixa a mídia (se houver) e grava o item: `202` com
   `status: queued`.
2. O despachante pega o próximo item respeitando a **ordem estrita por destinatário**: um item em espera segura os
   seguintes daquele destinatário; outros destinatários seguem.
3. Antes do primeiro envio a um número sem chat, o número é [verificado](/docs/conceitos/verificacao-de-numeros/).
4. O item passa a `in_flight`, aparece no histórico como `pending` e é enviado pelo worker.
5. Sucesso: `sent`, com `wa_message_id`. Depois chegam `delivered` e `read` pelos recibos do WhatsApp.

## Quando a fila espera

- **Instância fora de `connected`**: a fila pausa sem gastar tentativas.
- **Carência pós-conexão**: depois de cada `connected`, 15 a 30 segundos antes do primeiro envio.
- **Janela de envio**: contatos sem mensagem recebida nas últimas 24h esperam a [janela](/docs/conceitos/janela-de-envio/).
- **Rampa**: acima do limite diário de destinatários novos, o item vai para o dia seguinte. Veja [rampa](/docs/conceitos/rampa/).
- **Mídia grande**: no máximo 3 corpos grandes e 3 processamentos de mídia ao mesmo tempo; os demais esperam vaga.

## Ritmo

| Setting | Padrão | Limite sem `unsafe_pacing` |
|---|---|---|
| `min_interval_ms` | 3000 | mínimo 1000 |
| `jitter_ms` | 2000 (soma 0–2s aleatórios) | até 600000 |
| `max_per_minute` | 20 | máximo 60 |
| `typing_simulation` | ligado: mostra "digitando" (~50ms por caractere, até 5s) | |

Mensagens idênticas para vários destinatários novos em sequência são espaçadas com o dobro de `min_interval_ms`.
Consultas de número que vão ao WhatsApp também contam como um envio para o ritmo. Só o token admin liga
`unsafe_pacing`, que remove os pisos.

## Tentativas e falhas

- Falha temporária (timeout ou erro de rede com a instância conectada): nova tentativa com backoff de 5s, 10s, 20s e
  40s, até **5 tentativas**; depois `failed`.
- Falha permanente (`invalid_number`, mídia inválida, sessão deslogada, banimento, sem permissão no grupo): `failed`
  na hora.
- Worker desconectado ou shutdown antes de o envio começar: o item volta à fila sem gastar tentativa. Um envio já
  iniciado ganha até 10s no shutdown.
- `expires_after` (padrão `24h`, de `1m` a `168h`): se não for enviada até lá, a mensagem falha com `message_expired`.

O status fica em `GET /v1/instances/{id}/messages/{msg_id}`: `queued`, `in_flight`, `sent`, `delivered`, `read` ou
`failed` (com `error` no formato `codigo: detalhe`).

## Modo síncrono e idempotência

- `?wait=true&timeout=30s` segura a requisição até o item terminar (máximo 2 minutos): `200` se enviado, erro se
  falhou, `202` com o status atual se o tempo acabar. A mensagem continua na fila.
- `client_message_id` (até 128 caracteres) torna o envio idempotente: repetir devolve a mesma mensagem; repetir com
  outro conteúdo responde `409 conflict`. Use um id do seu sistema, como o número do pedido.
