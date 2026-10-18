---
title: Atendimento com IA
description: Hook de agente para respostas automáticas com IA. Em breve (marco 5).
---

<span class="lz-badge lz-badge--soon">Em breve · M5</span>

Este guia vai mostrar como ligar um agente de IA (Claude, GPT ou o seu) para responder mensagens recebidas, usando o
**hook de agente** do LiteZap. Nada aqui está disponível ainda; o contrato está em
[Hook de agente](/docs/mcp/hook-de-agente/).

## O que será possível

- Configurar por instância uma URL de agente que recebe cada mensagem nova com o contexto das últimas mensagens do
  chat e devolve a resposta.
- Ações além do texto: marcar como lida, reagir, pausar o agente naquele chat, transferir para humano.
- Uma mensagem por vez por chat, sem respostas concorrentes.
- **Pausa automática quando um humano responde pelo celular**.
- Falha fechada: erro ou timeout do agente não envia nada e gera um evento `agent.failed`.
- Exemplo de referência com o Claude.

## Enquanto isso

Você já consegue montar um atendente com o que existe hoje:

1. Um [webhook](/docs/guias/webhooks/) com `message.received` chama o seu serviço.
2. O serviço busca o contexto em `GET /chats/{jid}/messages` e chama o modelo.
3. A resposta sai por `POST /messages` com `client_message_id` = id do evento (evita resposta dupla em retentativas).
4. Ao ver `message.sent` com `source: device` no mesmo chat, pause o bot: um humano assumiu.
