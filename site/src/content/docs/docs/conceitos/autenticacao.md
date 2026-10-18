---
title: Tokens admin e de instância
description: Os dois tipos de token, o que cada um acessa e como guardá-los.
---

Toda rota em `/v1` exige `Authorization: Bearer <token>`. Existem dois tipos de token.

## Token admin

- Acessa tudo: criar, listar e apagar instâncias, rotacionar tokens e alterar `proxy_url` e `unsafe_pacing`.
- Vem de `admin_token` na config ou de `LITEZAP_ADMIN_TOKEN`. Se nenhum for definido, é gerado na primeira execução
  em `data/admin_token` (prefixo `lza_`, permissão `0600`).
- Use-o só no seu backend ou em tarefas de operação. Nunca o coloque num front-end ou num app distribuído.

## Token de instância

- Gerado em `POST /v1/instances` (prefixo `lzi_`, 32 bytes aleatórios) e mostrado **uma única vez**. O servidor
  guarda só o hash SHA-256.
- Só acessa as rotas da própria instância (`/v1/instances/{id}/...`). Com outro `id`, a resposta é `403 forbidden`.
- Pode enviar, ler chats, alterar ritmo, janela, fuso e rampa, mas não `proxy_url` nem `unsafe_pacing`.
- Rotacione com `POST /v1/instances/{id}/token/rotate` (só admin): o token antigo deixa de valer na hora.

## Escopo por rota

| Rotas | Token |
|---|---|
| `POST /v1/instances`, `GET /v1/instances` | admin |
| `DELETE /v1/instances/{id}`, `POST .../token/rotate` | admin |
| Demais rotas de `/v1/instances/{id}/...` | instância ou admin |
| `PUT .../settings` com `proxy_url` ou `unsafe_pacing` | admin |
| `GET /media/{id}/{media_id}?exp=&sig=` | nenhum (a assinatura é a credencial) |
| `GET /healthz`, `GET /readyz` | público em loopback; admin fora dele |

O esquema `Bearer` não diferencia maiúsculas de minúsculas. Respostas que trazem segredos (token, QR, código de
pareamento, URL assinada) levam `Cache-Control: no-store`.
