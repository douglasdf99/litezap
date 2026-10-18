---
title: Rampa para números novos
description: Limite diário de destinatários novos que cresce com a idade da instância.
---

Um número recém-conectado que sai falando com centenas de desconhecidos chama atenção. A **rampa** limita quantos
destinatários **novos** a instância pode alcançar por dia, e esse limite cresce com o tempo.

## Como é calculado

- Começa em `start_per_day` (padrão **50**) no dia do primeiro `connected`.
- Sobe em linha reta até `end_per_day` (padrão **150**) ao longo de `days` (padrão **14**) dias.
- "Dia" é o dia no `timezone` da instância.

| Dia | Limite (padrão) |
|---|---|
| 0 | 50 |
| 7 | 100 |
| 14 em diante | 150 |

## O que conta como destinatário novo

- Chats (diretos ou de grupo) que **nunca** mandaram mensagem para a instância, inclusive considerando o histórico
  sincronizado no pareamento.
- Cada destinatário conta uma vez por dia.
- Respostas a quem já falou com a instância **não contam**.

Acima do limite, os envios para destinatários novos ficam `queued` até o **início da janela de envio do dia
seguinte** (com alguns minutos de variação, nunca à meia-noite), sem gastar tentativa. As consultas de número da
fila também respeitam esse teto diário quando a rampa está ligada.

## Configurar

```bash
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"ramp":{"enabled":true,"start_per_day":30,"end_per_day":120,"days":21}}'
```

Valores aceitos: `1 ≤ start_per_day ≤ end_per_day` e `days` de 1 a 365. Desligar (`"enabled": false`) é possível,
mas só faz sentido para números maduros com conversas que os destinatários esperam.
