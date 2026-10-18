---
title: Primeiro envio
description: O que acontece entre o POST e a mensagem chegar no celular, e por que a primeira mensagem às vezes demora.
---

O [quickstart](/docs/quickstart/) leva você até a primeira mensagem. Esta página explica o que acontece no
caminho, para que a primeira resposta `202` não pareça um erro.

## O roteiro

```bash
# 1. instância conectada?
curl -s localhost:8787/v1/instances/$ID -H "Authorization: Bearer $TOK" | jq .state   # "connected"

# 2. acompanhe os eventos em outro terminal (opcional, mas ajuda a entender)
curl -N "localhost:8787/v1/instances/$ID/events" -H "Authorization: Bearer $TOK"

# 3. envie esperando o resultado por até 90s
curl -s -X POST "localhost:8787/v1/instances/$ID/messages?wait=true&timeout=90s" \
  -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511988887777","type":"text","text":"Olá do LiteZap!","client_message_id":"teste-1"}'
```

## Por que pode demorar

A mensagem passa por várias proteções antes de sair. Todas existem para o número parecer um humano:

1. **Carência pós-conexão.** Depois de cada `connected`, a fila espera **15 a 30 segundos** antes do primeiro envio.
2. **Verificação do número.** Antes da primeira mensagem para um número, o LiteZap pergunta ao WhatsApp se ele tem
   conta e qual JID usar (no Brasil, com ou sem o 9º dígito). Sem WhatsApp, a mensagem falha com `invalid_number`
   sem enviar nada.
3. **Janela de envio.** Um contato que não mandou nada nas últimas 24 horas é "frio": a mensagem só sai dentro da
   janela, por padrão **08:00–20:00** no fuso da instância. Fora dela, a resposta é `202` com `"status":"queued"` e
   a mensagem sai na próxima abertura. Respostas a conversas ativas saem a qualquer hora.
4. **Ritmo.** Pelo menos 3 segundos (mais 0–2s aleatórios) entre envios, e "digitando" antes de cada texto.
5. **Rampa.** Números recém-conectados têm limite diário de destinatários novos (50 no primeiro dia).

## Lendo a resposta

| Resposta | Significado |
|---|---|
| `200` com `"status":"sent"` | Enviada; `wa_message_id` é o id no WhatsApp. |
| `202` com `"status":"queued"` ou `"in_flight"` | Ainda na fila (janela, ritmo, carência ou o `timeout` acabou). Ela continua lá. |
| `400 invalid_number` | O número não tem WhatsApp. |
| `409 instance_not_connected` | A instância não está conectada (ou foi deslogada). |

Depois de um `202`, acompanhe por `GET /v1/instances/$ID/messages/<id>` ou pelos eventos `message.sent` e
`message.status`.

## Testar fora do horário

Para testar à noite, desligue a janela só na instância de teste:

```bash
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"send_window":{"start":"","end":""}}'
```

Em produção, mantenha a janela. Veja [janela de envio](/docs/conceitos/janela-de-envio/).

## O chat aparece com outro número?

O LiteZap usa o JID que o WhatsApp devolve na verificação. Um número brasileiro digitado com 9º dígito pode ficar
guardado sem ele (ou o contrário). Use o `jid` de `GET /chats` nas rotas `/chats/{jid}/...`. Veja
[identificadores](/docs/conceitos/identificadores/).
