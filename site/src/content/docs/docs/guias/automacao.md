---
title: Integrar com n8n, Make e Zapier
description: Usar o LiteZap em ferramentas de automação com nós HTTP e webhooks.
---

O LiteZap não precisa de um conector dedicado: qualquer ferramenta que faça requisições HTTP e receba webhooks
consegue usá-lo. Conectores próprios estão no [roadmap](/docs/roadmap/).

## Enviar mensagens (nó HTTP)

Configure um nó de requisição HTTP:

| Campo | Valor |
|---|---|
| Método | `POST` |
| URL | `https://api.seu-dominio.com/v1/instances/<ID>/messages?wait=true` |
| Cabeçalho | `Authorization: Bearer <token da instância>` |
| Corpo (JSON) | `{"to": "{{telefone}}", "type": "text", "text": "{{mensagem}}", "client_message_id": "{{id_do_registro}}"}` |

- Guarde o token na área de credenciais da ferramenta, nunca no corpo do fluxo.
- `client_message_id` com o id do registro de origem evita mensagens duplicadas quando a ferramenta repete um passo.
- `?wait=true` faz o nó esperar o envio (até 30s, ou `timeout`); sem ele, o nó recebe `202` na hora e a mensagem
  segue na fila.

## Receber mensagens (gatilho de webhook)

1. Crie um gatilho do tipo webhook na ferramenta e copie a URL.
2. Cadastre essa URL no LiteZap:

   ```bash
   curl -s -X POST https://api.seu-dominio.com/v1/webhooks -H "Authorization: Bearer $TOK" \
     -d '{"url":"<URL do gatilho>","events":["message.received"]}'
   ```

3. Mande uma mensagem para o número: o gatilho recebe o envelope. Os campos úteis estão em `data`
   (`data.chat`, `data.text`, `data.push_name`, `data.media`).

Se a ferramenta permitir código num passo, [verifique a assinatura](/docs/eventos/assinatura/) com o segredo devolvido
na criação do webhook. Se não permitir, use uma URL de gatilho difícil de adivinhar e restrinja a origem no seu proxy.

## Fluxos comuns

- **Formulário → mensagem de boas-vindas:** gatilho do formulário → nó HTTP de envio (com consentimento do contato).
- **Mensagem recebida → planilha/CRM:** gatilho `message.received` → adicionar linha com `chat`, `push_name`, `text`.
- **Status de pedido → aviso:** gatilho do e-commerce → nó HTTP com `client_message_id` = `pedido-<id>-<status>`.

Lembre que a fila respeita ritmo, rampa e janela: automações que disparam para muitos contatos frios de uma vez vão
ver mensagens esperando. Isso é proposital; veja as [boas práticas](/docs/boas-praticas/).
