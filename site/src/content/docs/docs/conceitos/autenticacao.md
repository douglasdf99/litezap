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

## Chave de API com escopo

Desde o M11, uma instância pode ter chaves `lzk_` para entregar acesso limitado a terceiros
([criar chave](/docs/api/plataforma/criar-chave-de-api/)). Cada chave é presa a uma instância e tem escopo `read`
(rotas de leitura, sem QR, configurações nem hook de agente) e/ou `send` (`POST /messages` e o status do envio).
Qualquer outra rota responde `403 forbidden`, inclusive `/mcp`. Só o hash fica guardado; a chave pode expirar e é
revogada na hora com `DELETE`.

## Allowlist de IP

O token de instância e as chaves aceitam uma lista de IPs/CIDRs (`ip_allowlist`); os tokens admin e operador usam
`admin_allowed_ips` e `operator_allowed_ips` da configuração. Fora da lista: `403 forbidden`. Atrás de proxy
reverso, configure `trusted_proxies` — o `X-Forwarded-For` só é lido quando a conexão vem de um deles. Veja
[Multi-instância e parceiros](/docs/guias/multi-instancia/#allowlist-de-ip).

## Escopo por rota

| Rotas | Token |
|---|---|
| `POST /v1/instances`, `GET /v1/instances`, `POST /v1/instances/bulk` | admin |
| `DELETE /v1/instances/{id}`, `POST .../token/rotate`, `POST`/`DELETE .../pairing-links` | admin |
| `PATCH /v1/instances/{id}` com `tags` ou `ip_allowlist` | admin (só `name`: instância ou admin) |
| Demais rotas de `/v1/instances/{id}/...` | instância ou admin |
| `PUT .../settings` com `proxy_url` ou `unsafe_pacing` | admin |
| `GET /media/{id}/{media_id}?exp=&sig=` | nenhum (a assinatura é a credencial) |
| `/pair/` (página de pareamento) | nenhum (o link assinado é a credencial, no cabeçalho `X-Pairing-Token`) |
| `GET /healthz`, `GET /readyz` | público em loopback; admin fora dele |

O esquema `Bearer` não diferencia maiúsculas de minúsculas. Respostas que trazem segredos (token, QR, código de
pareamento, URL assinada) levam `Cache-Control: no-store`.
