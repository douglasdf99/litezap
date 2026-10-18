---
title: Glossário
description: Termos usados na documentação do LiteZap.
---

| Termo | Significado |
|---|---|
| **Admin (token)** | Token que acessa todas as rotas: criar e apagar instâncias, webhooks globais, `proxy_url`. Prefixo `lza_`. Veja [tokens](/docs/conceitos/autenticacao/). |
| **Carência pós-conexão** | Espera de 15 a 30 segundos depois de cada `connected` antes do primeiro envio. |
| **Chat frio** | Chat sem mensagem recebida nas últimas 24 horas. Só recebe mensagens dentro da [janela de envio](/docs/conceitos/janela-de-envio/). |
| **`client_message_id`** | Id que você escolhe para um envio; repetir não duplica a mensagem. Veja [idempotência](/docs/conceitos/idempotencia/). |
| **Dead-letter** | Entregas de webhook que ficaram 24 horas sem sucesso. Ficam guardadas 30 dias e podem ser reenviadas. |
| **Envelope** | Formato comum de todos os eventos: `{id, type, version, instance_id, timestamp, data}`. |
| **Evento** | Algo que aconteceu numa instância (mensagem recebida, status mudou...). Entregue por webhook e SSE. |
| **Instância** | Um número de WhatsApp conectado ao LiteZap, com token, configurações e worker próprios. |
| **JID** | Identificador do WhatsApp para um destino: `5511999999999@s.whatsapp.net` (pessoa), `...@g.us` (grupo), `...@lid` (id oculto). Veja [identificadores](/docs/conceitos/identificadores/). |
| **LID** | "Linked ID": identificador que o WhatsApp usa em alguns contextos para esconder o telefone (`...@lid`). |
| **Outbox** | Tabela onde ficam as entregas de webhook até serem confirmadas. Também a pasta `data/outbox/` com anexos aguardando envio. |
| **Rampa** | Limite diário de destinatários novos que cresce com a idade da instância. Veja [rampa](/docs/conceitos/rampa/). |
| **SSE (Server-Sent Events)** | Stream HTTP de eventos ao vivo. Veja [tempo real com SSE](/docs/guias/sse/). |
| **Supervisor** | O processo `litezap serve`: API, banco, fila, eventos e controle dos workers. |
| **Webhook** | URL sua que recebe eventos por `POST`, assinados com HMAC-SHA256. Veja [webhooks](/docs/guias/webhooks/). |
| **Worker** | Processo `litezap worker` dedicado a um número. Se cair, só aquele número reconecta. |
| **`wa_message_id`** | Id de uma mensagem no WhatsApp (diferente do id `msg_...` do LiteZap). |
