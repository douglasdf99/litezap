---
title: SDKs
description: Bibliotecas oficiais para Node.js/TypeScript, Python, Go e PHP. Em breve.
---

<span class="lz-badge lz-badge--soon">Em breve</span>

Ainda **não existem** SDKs oficiais. A API é HTTP + JSON simples, e toda página de operação traz exemplos prontos em
cURL, Node.js, Python, Go e PHP, gerados do mesmo [openapi.yaml](/openapi.yaml).

## Planejado

| Linguagem | Conteúdo |
|---|---|
| Node.js / TypeScript | Cliente tipado, verificação de webhook, consumidor de SSE |
| Python | Cliente síncrono e assíncrono, verificação de webhook, consumidor de SSE |
| Go | Cliente, verificação de webhook (a mesma lógica do motor), consumidor de SSE |
| PHP | Cliente e verificação de webhook |

Cada SDK terá instalação, autenticação, um exemplo por recurso e referência por método gerada.

## Enquanto isso

- **Gere um cliente** a partir do [openapi.yaml](/openapi.yaml) com o gerador da sua preferência (OpenAPI Generator,
  `openapi-typescript`, `oapi-codegen`...).
- **Verificação de webhook** pronta em [Verificar assinatura](/docs/eventos/assinatura/), nas quatro linguagens.
- **Coleções** para Postman, Insomnia e Bruno: veja [Postman e outros clientes](/docs/ferramentas/postman/).
