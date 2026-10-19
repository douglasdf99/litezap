---
title: Changelog
description: O que mudou em cada marco do LiteZap.
---

As versões seguem os marcos do motor. Mudanças que quebram compatibilidade são marcadas com **Quebra**.

## App local: download e Conectar a IA

**Novo**

- Downloads prontos em cada versão: `LiteZap-macos.dmg` (universal, com ícone), `LiteZap-windows.zip` e
  `LiteZap-linux-{amd64,arm64}.tar.gz`. Veja [Baixar o app](/docs/baixar/).
- **Conectar a IA** no menu da bandeja e no painel: um clique configura Claude Desktop, Claude Code ou Cursor
  (`mcpServers.litezap`), com cópia `.litezap.bak` do original, escrita atômica e **Desconectar**.
- `litezap-app mcp`: servidor MCP stdio que usa o motor do app aberto, sem copiar token; com o app fechado, as
  ferramentas respondem `app_not_running`.

**Ainda não**: assinatura de código e notarização, instaladores `.msi`/`.deb`, atualização automática.

## Marco 11: plataforma e parceiros

**Novo**

- Página de pareamento hospedada (`POST/DELETE /v1/instances/{id}/pairing-links`, página `/pair/`): link assinado,
  até 10 minutos ou até o primeiro pareamento, sem expor o token; web component `<litezap-pair>`;
  `pairing_frame_ancestors` para embutir.
- Evento `instance.paired`; `instance.proxy_failed` ganha `error` (sem credenciais).
- `PATCH /v1/instances/{id}` (nome, etiquetas, allowlist de IP); `GET /v1/instances` com `state`, `tag`, `q`,
  `limit` e `cursor`; `POST /v1/instances/bulk`; `GET /v1/instances/{id}/usage`.
- Chaves de API `lzk_` com escopo `read`/`send`; allowlist de IP por instância, por chave e para admin/operador
  (`admin_allowed_ips`, `operator_allowed_ips`), com `trusted_proxies`.
- Métricas `litezap_queue_send_latency_seconds` e `litezap_worker_memory_rss_bytes` (Linux).
- Painel: editar instância (nome, etiquetas, IPs) e gerar/revogar links de pareamento.

`GET /v1/instances` sem `limit` responde como antes; a resposta das instâncias ganha `tags` e `ip_allowlist`.

## App local v1

**Novo**

- Painel web embutido em `/app/` (`/` redireciona): instâncias com estado ao vivo, pareamento por QR e código,
  conversas e envio com explicação da fila, configurações, webhooks com entregas e reenvio, eventos ao vivo.
- App de bandeja `litezap-app`: motor no próprio processo, pasta de dados por usuário, login no painel em um clique
  (código de uso único, só em loopback), Abrir pasta de dados, Ver log e Sair gracioso.
- Configuração `disable_dashboard` (`LITEZAP_DISABLE_DASHBOARD`).

**Ainda não**: instaladores, assinatura de código, atualização automática, iniciar com o sistema, túnel.

## Marco 4: MCP (v0.4)

**Novo**

- Servidor MCP em `/mcp` (streamable HTTP, *stateless*, com os mesmos tokens da API) e ponte stdio `litezap mcp`
  (`LITEZAP_URL`, `LITEZAP_TOKEN`).
- 13 ferramentas: `list_instances`, `get_instance_status`, `get_qr`, `send_message`, `send_media`,
  `get_message_status`, `list_chats`, `get_messages`, `mark_read`, `list_contacts`, `get_contact`, `check_numbers`,
  `wait_for_events`.
- `GET /v1/me`: escopo do token (`admin`, `instance_id`).
- Proteção contra DNS rebinding no `/mcp` em loopback, com exceção para o host de `public_url`.

## Marco 3: eventos (v0.3)

**Novo**

- Barramento de eventos com envelope versionado (`version: 1`).
- Eventos `instance.status`, `instance.qr`, `instance.pair_code`, `message.received`, `message.sent` (com
  `source: api|device`), `message.status`, `message.reaction` e `history.synced`.
- Webhooks: `POST/GET /v1/webhooks`, `GET/PATCH/DELETE /v1/webhooks/{id}`, globais ou por instância, com filtros
  (`message.*`, `*`), assinatura `X-LiteZap-Signature` (HMAC-SHA256 com timestamp), outbox persistente, retry com
  backoff por até 24h, backoff por endpoint, dead-letter com `GET .../deliveries` e `POST .../retry`, retenção
  configurável.
- SSE: `GET /v1/instances/{id}/events` e `GET /v1/events` (admin), com filtro `?types=` e heartbeat.
- Configuração: `webhook_allow_private_urls`, `webhook_retention_days`, `webhook_failed_retention_days`.

**Limitações conhecidas**: `message.revoked`, `chat.presence` e `group.updated` ainda não são emitidos; SSE sem replay.

## Marco 2: mensagens (v0.2)

**Novo**

- Envio pela fila persistente: `text`, `image`, `video`, `audio` (com voz), `document`, `reaction`; mídia por URL
  (com proteção SSRF) ou base64.
- `?wait=true` (modo síncrono), idempotência por `client_message_id`, `expires_after`, até 5 tentativas.
- Ritmo (intervalo, variação, teto por minuto, "digitando"), rampa para números novos, janela de envio para contatos
  frios, carência pós-conexão, verificação do número antes do primeiro envio.
- Recebimento e log local: `GET /chats`, `GET /chats/{jid}/messages` com cursor, `POST /chats/{jid}/read`, history
  sync.
- Contatos e `POST /check-numbers` (cache e cotas).
- Mídia recebida sob demanda e URLs assinadas (`?signed=1`, 1 hora).
- Configurações por instância (`GET/PUT /settings`) e proxy HTTP/HTTPS/SOCKS5 por instância.

## Marco 1: esqueleto (v0.1)

**Novo**

- Binário único (`serve`, `worker`, `version`), configuração por YAML e `LITEZAP_*`, SQLite com migrações.
- Supervisor com um worker por número: heartbeat, restart com backoff, circuit breaker, workers que encerram com o
  supervisor, limpeza de órfãos.
- Instâncias: criar, listar, consultar, iniciar, parar, reiniciar, logout, apagar, rotacionar token.
- Pareamento por QR (JSON ou PNG) e por código no telefone.
- Tokens admin e de instância; `/healthz` e `/readyz`.
