---
title: Instâncias e estados
description: O que é uma instância, como ela é supervisionada e o que cada estado significa.
---

Uma **instância** é um número de WhatsApp conectado ao LiteZap. Cada instância tem um id (`inst_...`), um nome livre,
um token próprio, configurações de envio e um processo worker dedicado.

## Ciclo de vida

```
created → starting → qr_pending → connecting → connected
                         ↑             ↓            ↓ ↑
                         └──────── reconnecting ◀───┘
```

| Estado | O que significa |
|---|---|
| `created` | Acabou de ser criada; o worker sobe em seguida. |
| `starting` | Worker sendo iniciado (também depois de um crash). |
| `qr_pending` | Sem sessão salva: aguardando QR ou código de pareamento. |
| `connecting` | Sessão encontrada ou pareamento concluído; conectando. |
| `connected` | Pronta para enviar e receber. |
| `reconnecting` | Caiu a conexão e o worker está tentando voltar. `state_reason: proxy_unreachable` indica proxy fora do ar. |
| `logged_out` | A sessão foi desconectada (pelo celular ou pela API). `start` volta a `qr_pending` para parear de novo. |
| `banned` | O WhatsApp baniu o número. Permanente: a única ação é `DELETE`. |
| `failed` | Circuit breaker aberto (muitos crashes) ou banimento temporário (`state_reason: temp_banned:<duração>`, sessão preservada). Exige `start` manual. |
| `stopped` | Parada por `stop`. Não sobe sozinha no próximo boot. |

As ações (`start`, `stop`, `restart`, `logout`) respondem com a instância **logo após** a ação. `start` e `restart`
respondem assim que o worker é disparado, então a resposta pode ainda mostrar o estado anterior. Acompanhe com
`GET /v1/instances/{id}` até ver o estado esperado (eventos em tempo real chegam no marco 3).

## Supervisão

- **Heartbeat** a cada 10 segundos. Três falhas seguidas: o worker é morto e reiniciado.
- **Restart com backoff** exponencial, de 1 segundo até 5 minutos.
- **Circuit breaker**: 5 crashes em 10 minutos levam a instância a `failed`, sem novos restarts até ação manual.
- **Limite de memória** por worker (`worker_memory_max_mb`, padrão 512).
- **Sem órfãos**: cada worker vigia um pipe mantido aberto pelo supervisor e encerra quando ele fecha, mesmo que o
  supervisor tenha sido morto com `SIGKILL`. No boot, PIDs antigos em `data/run/*.pid` são limpos como garantia extra.
- **Boot**: o supervisor devolve à fila envios interrompidos e sobe todas as instâncias que não estavam `stopped`.

## Apagar

`DELETE /v1/instances/{id}` (só admin) tenta um logout (até 15s, para liberar o aparelho no celular), para o worker e
apaga sessão, mídia, fila e registros da instância. Pode levar uns 20 segundos.
