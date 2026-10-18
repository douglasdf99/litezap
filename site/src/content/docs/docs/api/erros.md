---
title: Erros
description: Todos os códigos de erro da API, com status HTTP e quando acontecem.
---

```json
{ "error": { "code": "rate_limited", "message": "too many number lookups; try again in a minute", "retryable": true } }
```

`code` é estável e é o que o seu código deve tratar. `message` é texto para humanos (em inglês) e nunca traz números
de telefone nem tokens. `retryable` diz se vale tentar de novo.

| Código | HTTP | Retentável | Quando |
|---|---|---|---|
| `validation_failed` | 400 | não | Corpo ou campo inválido, JSON malformado, cursor inválido, `media.url` que não é http(s) ou aponta para endereço privado/local. |
| `invalid_number` | 400 | não | O número não tem WhatsApp. |
| `unauthorized` | 401 | não | Token ausente ou inválido. |
| `forbidden` | 403 | não | Token sem acesso ao recurso (outra instância, rota de admin, `proxy_url`/`unsafe_pacing` sem admin); URL assinada inválida ou expirada. |
| `instance_not_found` | 404 | não | Instância inexistente. |
| `not_found` | 404 | não | Mensagem, chat, contato ou mídia inexistente. |
| `conflict` | 409 | não | `client_message_id` reusado com outro conteúdo; URLs assinadas desativadas (fora de loopback sem `public_url`). |
| `instance_not_connected` | 409 | depende | Worker da instância não está rodando (`retryable: true`) ou o número está deslogado (`false`). |
| `instance_banned` | 409 | não | Número banido. Apague a instância. |
| `instance_not_pairing` | 409 | depende | QR ou código pedidos fora de `qr_pending` (ex.: já conectada). |
| `media_unavailable` | 410 | não | A mídia recebida expirou no WhatsApp. |
| `message_expired` | 410 | não | A mensagem não foi enviada antes de `expires_after`. |
| `media_too_large` | 413 | não | Mídia acima do limite do tipo, ou corpo acima do limite da rota (1 MB; 140 MB em `POST /messages`). |
| `media_fetch_failed` | 422 | sim | `media.url` não pôde ser baixada. |
| `rate_limited` | 429 | sim | Cota de consultas novas de `check-numbers` esgotada (10/min ou 200/dia). |
| `rate_limited` | 503 | sim | Muitos envios com corpo grande ao mesmo tempo e a requisição desistiu de esperar vaga. |
| `worker_error` | 502 | sim | O worker da instância recusou ou falhou a operação. |
| `worker_timeout` | 504 | sim | O worker da instância não respondeu a tempo. |
| `shutting_down` | 503 | sim | O servidor está encerrando. |
| `internal` | 500 | sim | Erro interno inesperado (registrado no log do servidor). |

## Erros de envio

Com `?wait=true`, uma mensagem que falha na fila devolve o erro correspondente ao motivo (`invalid_number`,
`validation_failed`, `forbidden`, `instance_not_connected`, `worker_timeout`, `message_expired` ou `worker_error`).
Sem `wait`, o mesmo motivo aparece em `GET /messages/{msg_id}`, no campo `error`, no formato `codigo: detalhe`, com
`status: failed`.

## Sugestão de tratamento

- `retryable: true`: tente de novo com backoff exponencial (ex.: 1s, 2s, 4s... até um teto).
- `instance_not_connected`: confira o estado em `GET /v1/instances/{id}`; se `logged_out`, pareie de novo.
- `instance_banned`: não tente de novo; avise o responsável pelo número.
- `rate_limited` (429): espere pelo menos um minuto; se for o limite diário, só no dia seguinte (UTC).
