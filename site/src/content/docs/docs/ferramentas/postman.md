---
title: Postman e outros clientes
description: Importar a API do LiteZap no Postman, Insomnia ou Bruno a partir do OpenAPI.
---

A especificação completa da API está em [`/openapi.yaml`](/openapi.yaml) (OpenAPI 3.1). Clientes HTTP importam esse
arquivo e geram uma coleção com todas as rotas, parâmetros e exemplos.

## Postman

1. **Import** > **Link** > cole a URL do `openapi.yaml` deste site (ou baixe o arquivo e use **File**).
2. Em **Variables** da coleção, defina `baseUrl` (ex.: `http://localhost:8787`).
3. Em **Authorization** da coleção, escolha **Bearer Token** e use uma variável, como `{{token}}`. Guarde o token em
   um ambiente, não na coleção, para não compartilhá-lo sem querer.

## Insomnia

**Create** > **Import** > **From URL** com o endereço do `openapi.yaml`. Configure o Bearer token no ambiente.

## Bruno

**Import Collection** > **OpenAPI V3 Spec** e selecione o arquivo baixado.

## Dicas

- Use um token de instância de teste; o token admin dá acesso a tudo.
- Para SSE, o Postman não é o melhor cliente: prefira `curl -N` (veja [tempo real com SSE](/docs/guias/sse/)).
- A seção `webhooks` do OpenAPI descreve os payloads dos eventos; nem todo cliente a exibe, mas o
  [catálogo de eventos](/docs/eventos/) tem todos os exemplos.

Coleções prontas publicadas (sem precisar importar) estão no [roadmap](/docs/roadmap/).
