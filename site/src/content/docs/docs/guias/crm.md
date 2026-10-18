---
title: Construir um CRM simples
description: Caixa de entrada com lista de chats, histórico, não lidas, resposta e confirmação de leitura, usando só a API atual.
---

Com o log local do LiteZap você monta uma caixa de entrada de atendimento sem banco próprio de mensagens.

## 1. Lista de conversas

```bash
curl -s "localhost:8787/v1/instances/$ID/chats?limit=30" -H "Authorization: Bearer $TOK"
```

Cada chat traz `jid`, `name`, `is_group`, `last_message_at` e `unread_count`, já ordenados pela última mensagem.
Pagine com `next_cursor`.

## 2. Abrir uma conversa

```bash
curl -s "localhost:8787/v1/instances/$ID/chats/$JID/messages?limit=50" -H "Authorization: Bearer $TOK"
```

Mensagens vêm da mais recente para a mais antiga. `from_me` separa os lados; `status` mostra `received` (recebida)
ou `pending` → `sent` → `delivered` → `read` (enviadas). Mídia traz `media.url` para baixar com o token.

## 3. Marcar como lida

Quando o atendente abre a conversa:

```bash
curl -s -X POST "localhost:8787/v1/instances/$ID/chats/$JID/read" -H "Authorization: Bearer $TOK"
```

Envia a confirmação de leitura (os "tiques azuis") e zera `unread_count`.

## 4. Responder

```bash
curl -s -X POST "localhost:8787/v1/instances/$ID/messages" -H "Authorization: Bearer $TOK" \
  -d "{\"to\":\"$JID\",\"type\":\"text\",\"text\":\"Oi! Já verifico para você.\",\"client_message_id\":\"ticket-881-r1\"}"
```

Respostas a quem falou com você nas últimas 24 horas saem a qualquer hora (a janela de envio só vale para contatos
frios) e não contam na rampa.

## 5. Atualizar em tempo real

- **Backend com URL pública:** um [webhook](/docs/guias/webhooks/) com `message.*` atualiza a lista quando chega
  `message.received` e os tiques quando chega `message.status`.
- **Painel interno:** consuma o [SSE](/docs/guias/sse/) no backend e repasse ao navegador (WebSocket ou SSE seu).
- Depois de um `history.synced` (logo após parear), recarregue a lista de chats.

## 6. Detectar resposta pelo celular

`message.sent` com `"source":"device"` significa que alguém respondeu pelo celular do número. Use isso para marcar
a conversa como "em atendimento humano" e pausar automações.

## Boas práticas

- Use sempre o `jid` vindo da API nas rotas de chat (o 9º dígito pode diferir do número digitado).
- Guarde o `client_message_id` de cada resposta: reenviar após um erro de rede não duplica.
- Atendentes múltiplos no mesmo número: o LiteZap não tem distribuição de atendentes; faça no seu CRM, usando o log
  e os eventos.
