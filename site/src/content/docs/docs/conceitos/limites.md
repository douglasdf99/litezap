---
title: Limites e cotas
description: Todos os limites do LiteZap num só lugar.
---

## Mensagens

| Limite | Valor |
|---|---|
| Texto | 64 KB |
| Imagem | 16 MB |
| Vídeo e áudio | 64 MB |
| Documento | 100 MB |
| Corpo de `POST /messages` | 140 MB (mídia em base64) |
| Corpo das demais rotas | 1 MB |
| `client_message_id` | até 128 caracteres |
| `file_name` | até 255 bytes |
| Emoji de reação | até 64 bytes |
| `expires_after` | de `1m` a `168h` (padrão `24h`) |
| `?wait=true` | `timeout` padrão `30s`, teto `2m` |
| Envios com corpo grande (> 1 MB) ao mesmo tempo | 3 (os demais esperam vaga) |
| Downloads/decodificações de mídia ao mesmo tempo | 3 |

## Ritmo da fila (por instância)

| Setting | Padrão | Limite |
|---|---|---|
| `min_interval_ms` | 3000 | mínimo 1000 (sem `unsafe_pacing`) |
| `jitter_ms` | 2000 | até 600000 |
| `max_per_minute` | 20 | máximo 60 (sem `unsafe_pacing`) |
| Rampa de destinatários novos | 50 → 150/dia em 14 dias | `start_per_day ≥ 1`, `days` de 1 a 365 |
| Consultas de número da fila | 30 novas/min, e o teto diário da rampa | — |
| Tentativas de envio | 5 | — |

## Verificação de números

| Limite | Valor |
|---|---|
| Números por chamada de `check-numbers` | 1 a 10 |
| Consultas novas por minuto | 10 por instância |
| Consultas novas por dia | 200 por instância (dia UTC) |
| Cache | 30 dias (tem WhatsApp) / 24h (não tem) |

## Webhooks e SSE

| Limite | Valor |
|---|---|
| Filtros de eventos por webhook | 32 |
| `secret` | 16 a 256 caracteres |
| URL | até 2048 caracteres, `http(s)` |
| Timeout por entrega | 10s |
| Prazo de retentativas | 24 horas |
| Entregas simultâneas | 8 no total, 2 por webhook |
| Retenção | entregas concluídas 7 dias, dead-letter 30 dias (configurável) |
| Atraso tolerado no SSE | 256 eventos (depois: `slow_consumer` e desconexão) |
| Heartbeat do SSE | 15s |

## Paginação

| Rota | `limit` padrão | Máximo |
|---|---|---|
| `GET /chats`, `GET /chats/{jid}/messages` | 50 | 200 |
| `GET /webhooks/{id}/deliveries` | 50 | 100 |

## Processos

| Limite | Valor |
|---|---|
| Memória por worker | `worker_memory_max_mb`, padrão 512 MB |
| Heartbeat do worker | 10s (3 falhas = restart) |
| Circuit breaker | 5 crashes em 10 min |
