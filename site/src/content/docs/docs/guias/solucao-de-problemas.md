---
title: Solução de problemas
description: QR que não aparece, desconexões, mensagens presas na fila, webhooks que não chegam e número banido.
---

## O QR não aparece

- **`409 instance_not_pairing`**: a instância não está em `qr_pending`. Veja o estado com `GET /v1/instances/{id}`.
  Se `connected`, já está pareada; se `logged_out`, faça `POST /start` para voltar a `qr_pending`; se `stopped` ou
  `failed`, `POST /start`.
- **O QR expira rápido**: ele muda a cada poucos segundos. Busque de novo, ou assine `instance.qr` para receber cada
  código novo.
- **Fica em `reconnecting` com `proxy_unreachable`**: o proxy da instância não responde. Corrija ou remova o
  `proxy_url` (admin).

## A instância desconecta

| Estado | O que fazer |
|---|---|
| `reconnecting` | Rede ou proxy instável; o worker tenta sozinho. Se persistir, confira a conexão e o proxy. |
| `starting` repetidas vezes | O worker está caindo. Veja o log do `serve`. |
| `failed` | Circuit breaker (5 crashes em 10 min) ou banimento temporário (`state_reason: temp_banned:...`). Corrija a causa e faça `POST /start`. |
| `logged_out` | O aparelho foi removido no celular ou houve logout. `POST /start` e pareie de novo. |
| `banned` | O WhatsApp baniu o número. Só resta `DELETE`. |

Telefone desligado por muitos dias também derruba sessões de aparelhos conectados.

## A mensagem não sai (fica `queued`)

Em ordem de probabilidade:

1. **Janela de envio**: o contato não falou com você nas últimas 24h e está fora de 08:00–20:00 (no fuso da
   instância). Confira `GET /settings` e o `timezone`; servidores em UTC são a causa mais comum.
2. **Carência**: a instância conectou há menos de 30 segundos.
3. **Rampa**: o limite diário de destinatários novos foi atingido; a mensagem sai na janela do dia seguinte.
4. **Ritmo**: muitas mensagens na frente. Com 3s + jitter e 20/min, 100 mensagens levam uns 5 minutos.
5. **Instância fora de `connected`**: a fila pausa até reconectar.
6. **Mensagem anterior ao mesmo destinatário em espera**: a ordem é estrita por destinatário.

`GET /messages/{id}` mostra `status`, `attempts` e `error`. Mensagens que não saem até `expires_after` (padrão 24h)
falham com `message_expired`.

## `invalid_number`

O número não tem WhatsApp, ou foi digitado sem DDI. Use 10 a 15 dígitos com DDI (`55` para o Brasil). Confira com
`POST /check-numbers`.

## O chat não aparece em `/chats/{jid}`

Use o `jid` que vem de `GET /chats` ou `check-numbers`. No Brasil, o WhatsApp pode guardar o número sem o 9º dígito.

## Webhooks não chegam

1. `GET /v1/webhooks/{id}/deliveries?status=pending` mostra `attempts` e `last_error`.
2. `blocked: webhook URL points to a private or local address`: a URL resolve para IP privado. Em teste local, ligue
   `webhook_allow_private_urls`.
3. `http 3xx`: redirecionamentos não são seguidos; use a URL final.
4. `timeout`: o receptor demorou mais de 10s. Responda `2xx` antes de processar.
5. O webhook está `enabled: false`, ou os filtros de `events` não incluem o tipo esperado.
6. Assinatura inválida no receptor: você está usando o corpo já parseado. Use o corpo bruto.

## SSE desconecta

- `event: error` com `slow_consumer`: o cliente ficou mais de 256 eventos atrasado. Processe mais rápido e reconecte.
- Atrás de proxy reverso: desligue buffering e aumente o timeout de leitura.

## Número banido ou restrito

Pare os envios para contatos frios, revise o público e o texto, e leia as [boas práticas](/docs/boas-praticas/). Não
há como desbanir um número pela API. Se o WhatsApp oferecer revisão no aplicativo, faça pelo celular.

## Onde olhar

- Log do `serve` (stderr): todas as linhas têm `instance_id`.
- `GET /readyz`: banco acessível.
- `kill -9 $(cat data/run/$ID.pid)`: força um restart do worker para testar a recuperação.
