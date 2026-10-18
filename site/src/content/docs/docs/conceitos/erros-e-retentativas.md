---
title: Erros e retentativas
description: Quando tentar de novo, quando não, e o que o LiteZap já tenta sozinho.
---

## O envelope

```json
{ "error": { "code": "worker_timeout", "message": "instance worker did not answer in time", "retryable": true } }
```

Trate pelo `code`, que é estável. A `message` é para humanos e pode mudar. `retryable` resume se uma nova tentativa,
com espera, pode dar certo. A [tabela de erros](/docs/api/erros/) lista todos os códigos.

## O que o LiteZap já tenta por você

| Onde | Retentativas automáticas |
|---|---|
| Envio de mensagens | Até 5 tentativas em falhas temporárias (5s, 10s, 20s, 40s). Instância desconectada não gasta tentativa: a mensagem espera. |
| Verificação de número antes do envio | Adia só aquele item: 1, 2, 5 e 15 minutos; na 5ª falha, `failed`. |
| Worker que caiu | Restart automático com backoff (1s até 5 min) e circuit breaker. |
| Entregas de webhook | Backoff exponencial (1s, 2s, 4s... até 1h) por até 24 horas, depois dead-letter. |

Ou seja: **não reenvie uma mensagem só porque recebeu `202`**. Ela está na fila e vai sair.

## O que fazer do seu lado

| Situação | Ação |
|---|---|
| `retryable: true` (ex.: `worker_timeout`, `worker_error`, `rate_limited`, `shutting_down`) | Tente de novo com backoff exponencial e um teto (ex.: 1s, 2s, 4s, até 30s). Em envios, repita com o mesmo `client_message_id`. |
| `retryable: false` por dados (`validation_failed`, `invalid_number`, `media_too_large`) | Não repita; corrija a entrada. |
| `instance_not_connected` | Confira `GET /v1/instances/{id}`. Se `logged_out`, pareie de novo; se `reconnecting`, espere. |
| `instance_banned` | Pare. O número foi banido; apague a instância. |
| `401` / `403` | Problema de token; não adianta repetir. |

## Erro de rede sem resposta

Se a requisição caiu sem resposta, você não sabe se a mensagem entrou na fila. Repita **com o mesmo
`client_message_id`**: se entrou, você recebe a mesma mensagem; se não, ela é criada agora. Veja
[idempotência](/docs/conceitos/idempotencia/).
