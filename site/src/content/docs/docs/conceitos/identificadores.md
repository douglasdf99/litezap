---
title: Identificadores
description: Telefone, JID, LID, o 9º dígito no Brasil e os ids do LiteZap.
---

## Telefone e JID

O WhatsApp endereça tudo por **JID** (Jabber ID). Onde a API pede um destino (`to`, `{jid}`), você pode passar:

| Forma | Exemplo | Quando |
|---|---|---|
| Telefone com DDI | `5511999999999` (10 a 15 dígitos; separadores são ignorados) | O jeito mais simples para pessoas. |
| JID de pessoa | `5511999999999@s.whatsapp.net` | O que a API devolve em `chat` e `sender`. |
| JID de grupo | `120363000000000001@g.us` | Grupos (envio para grupos em que o número já está). |
| LID | `100000000000001@lid` | Id oculto que o WhatsApp usa em alguns contextos no lugar do telefone. |

A parte de aparelho de um JID (`5511999999999:12@s.whatsapp.net`) é descartada.

## O 9º dígito no Brasil

Celulares brasileiros ganharam o 9º dígito, mas contas antigas do WhatsApp podem estar registradas sem ele. Antes do
primeiro envio a um número, o LiteZap [pergunta ao WhatsApp](/docs/conceitos/verificacao-de-numeros/) qual JID usar
e passa a enviar para ele. Consequência prática:

- você envia para `5511999999999`, mas o chat pode aparecer como `551199999999@s.whatsapp.net` (sem o 9);
- nas rotas `/chats/{jid}/...`, use o `jid` devolvido por `GET /chats` ou `check-numbers`, não o número digitado.

Uma resposta vinda de um `@lid` nunca vira o chat principal de um telefone.

## Ids do LiteZap

| Prefixo | O quê |
|---|---|
| `inst_` | Instância |
| `msg_` | Mensagem (no envio, é o id do item da fila; também o id no log) |
| `med_` | Mídia recebida |
| `wh_` | Webhook |
| `dlv_` | Entrega de webhook |
| `evt_` | Evento (use para deduplicar) |
| `lzi_` / `lza_` | Token de instância / token admin |
| `whsec_` | Segredo de webhook gerado |

`wa_message_id` é o id da mensagem **no WhatsApp** (ex.: `3EB0A1B2C3D4E5F6A7B8C9`). Ele é escolhido quando a
mensagem entra na fila e reaproveitado em todas as tentativas, então uma retentativa nunca gera duplicata no
celular do contato. Reações aceitam tanto o id `msg_...` quanto o `wa_message_id`.
