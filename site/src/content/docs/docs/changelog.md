---
title: Changelog
description: O que mudou em cada marco do LiteZap.
---

As versões seguem os marcos do motor. Mudanças que quebram compatibilidade são marcadas com **Quebra**.

## IA grátis (A2)

**Novo**

- [IA grátis](/docs/guias/ia-gratis/): o Atendente IA e os Agentes funcionam sem pagar e sem cartão. Presets
  `ollama` (no seu computador, `qwen2.5:7b` ou `llama3.2:3b`), `gemini_free` (Google Gemini) e `groq_free` (Groq),
  com o campo `preset` em `PUT /v1/instances/{id}/attendant`; o custo dessas opções aparece como **Grátis**
  (`free: true`).
- `GET /v1/ai/local` verifica o Ollama local (versão, modelos, memória e modelo recomendado) e `POST /v1/ai/local/pull`
  baixa um modelo com progresso (`GET /v1/ai/local/pull`). Só o Ollama em `127.0.0.1:11434`; só o admin.
- Painel: **Qual IA usar?** no Atendente IA e na aba Agentes, com instalação guiada do Ollama, download do modelo com
  barra de progresso e expectativas honestas de qualidade, velocidade e privacidade.
- Modelos locais pequenos: instruções mais curtas em português com a lista de ferramentas, chamadas de ferramenta mal
  formatadas são corrigidas ou recusadas sem executar nada, com uma nova chance para o modelo; na segunda, a execução
  termina com `invalid_tool_call`.

## Agentes LiteZap (A1)

**Novo**

- [Agentes](/docs/guias/agentes/): o Claude operando o WhatsApp com as ferramentas do LiteZap, num loop de *tool use*
  executado dentro do motor (as mesmas ferramentas e validações do MCP, sem rede).
- Permissões em grupos (`ler`, `responder_no_chat`, `enviar_outros_chats`, `etiquetas`, `grupos`, `midia`), aplicadas
  pelo motor: envio para outro chat sem permissão é recusado e há no máximo 3 destinatários distintos por execução.
- Modos `aprovacao` (padrão: cada escrita vira um rascunho que você aprova), `autonomo` e `sugerir`; limite de 8 passos
  por execução; custo e limites do Atendente IA em cada chamada ao modelo.
- Gatilho "mensagem nova" pelo hook de agente com `target: "agents"` (filtros por chat, etiqueta, palavra-chave e
  grupos opcionais), "Executar agora" e o simulador (escritas sempre faz de conta).
- API `/v1/instances/{id}/agents` (agentes, execuções, rascunhos, simulador) e eventos `agent.run.completed`,
  `agent.draft.created` e `agent.draft.decided`. Aba **Agentes** no painel.
- O provedor OpenAI-compatível ganha *function calling* para os agentes.

## App local: download e Conectar a IA

**Novo**

- Downloads prontos em cada versão: `LiteZap-macos.dmg` (universal, com ícone), `LiteZap-windows.zip` e
  `LiteZap-linux-{amd64,arm64}.tar.gz`. Veja [Baixar o app](/docs/baixar/).
- **Conectar a IA** no menu da bandeja e no painel: um clique configura Claude Desktop, Claude Code ou Cursor
  (`mcpServers.litezap`), com cópia `.litezap.bak` do original, escrita atômica e **Desconectar**.
- `litezap-app mcp`: servidor MCP stdio que usa o motor do app aberto, sem copiar token; com o app fechado, as
  ferramentas respondem `app_not_running`.

**Ainda não**: assinatura de código e notarização, instaladores `.msi`/`.deb`, atualização automática.

## Distribuição

**Novo**

- CI no GitHub Actions: `gofmt`, `go vet`, `go test -race`, código gerado em dia e build do site.
- Releases pelo GoReleaser: binários para Linux e macOS (amd64 e arm64) e Windows (amd64), com checksums.
- Imagem Docker multi-arquitetura em `ghcr.io/douglasdf99/litezap` (distroless, usuário não root).
- `scripts/install.sh`: instalador POSIX com verificação de sha256.
- Arquivos da comunidade: CONTRIBUTING, SECURITY, código de conduta, modelos de issue e PR.

## Marco 5b: Atendente LiteZap embutido

**Novo**

- Atendente IA dentro do motor (`target: "builtin"` no hook de agente), sem nada para hospedar. Herda fila, pausa por
  humano, loop guard, um chat por vez e a suspensão do operador.
- Provedores: Anthropic (padrão `claude-sonnet-5-5`) e qualquer servidor OpenAI-compatível (OpenAI, Ollama, LM
  Studio). Chaves cifradas com AES-256-GCM (`data/attendant_key`), nunca devolvidas; provedor e chaves só com token
  admin.
- Agentes com modos prontos e editáveis: `vendedor`, `pedido` (facilitador de pedido), `sdr` (qualificação e
  agendamento), `suporte` e `custom`; base de conhecimento (produtos, preços, horários, regras); simulador de conversa
  sem envio.
- Multimodal: transcrição de mensagens de voz, imagens (e PDF no Claude) para o modelo, resposta em voz
  (`voice_reply`) e mensagem de follow-up quando uma ligação é recusada (`call_followup`).
- Eventos `agent.lead`, `agent.appointment_requested` e `agent.order_draft`; contexto com a origem de cada mensagem
  (`contact`, `agent`, `human`, `automatic`, `api`, `history`).
- Tetos de gasto: `daily_call_cap` (padrão 300/dia), `chat_daily_call_cap` (40 por conversa/dia) e
  `simulate_daily_cap` (50/dia); `GET /attendant/usage` com chamadas, tokens e custo estimado por dia.
- Rotas `/v1/instances/{id}/attendant` (provedor, modos, agentes, ativar, simular, uso), ferramentas MCP
  `get_attendant` e `get_attendant_agent`, aba **Atendente IA** no painel. Veja
  [Atendimento com IA](/docs/guias/atendimento-ia/).

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

## Marco 10: status, canais e etiquetas

**Novo**

- Status (stories): `POST /status` de texto, imagem ou vídeo pela fila (até 10 por dia), `GET /status` com os status
  recebidos e os nossos, `DELETE /status/{msg_id}`, `POST /status/{msg_id}/reply` (texto) e
  `GET /status/privacy`. Evento opt-in `status.received`.
- Canais: listar seguidos, criar, prévia por convite, metadados, seguir/deixar de seguir, silenciar, ler publicações,
  publicar (texto e mídia, só dono/admin, até 20 por dia) e reagir. Evento opt-in `channel.message`.
- Etiquetas do WhatsApp Business: listar, criar, renomear/recolorir, apagar, etiquetar conversas e mensagens, com
  evento `label.updated` (também para mudanças feitas no celular). `GET /business/profile` lê o perfil comercial.
- Cotas anti-ban para canais e etiquetas; ferramentas MCP de leitura mais `follow_channel` e `label_chat`; abas
  Status, Canais e Etiquetas no painel.

**Ainda não**: leitura automática de status, responder status com GIF ou figurinha, mídia de publicações de canal
recebidas, marcar publicações como vistas, eventos de agenda, produtos e catálogo.

## Marco 9: chats, contatos, perfil e privacidade

**Novo**

- Chats: `GET /chats/{jid}` (arquivado, fixado, silenciado, não lido), `PATCH /chats/{jid}` (arquivar, fixar,
  silenciar com `mute_seconds`, lido/não lido), `DELETE /chats/{jid}`, `PUT /messages/{msg_id}/star`. Evento
  `chat.updated`, inclusive para mudanças feitas no celular.
- Contatos: foto de perfil (`GET /contacts/{jid}/picture`), recado e perfil comercial
  (`GET /contacts/{jid}/profile`), bloquear, desbloquear e `GET /blocklist`, com cota própria de consultas (30/min,
  1000/dia).
- Perfil da conta: `GET`/`PATCH /profile` (nome, recado, plataforma, nome comercial) e foto (`GET`/`PUT`/`DELETE
  /profile/picture`, JPEG, PNG ou GIF reduzidos a 640x640).
- Privacidade: `GET`/`PATCH /privacy` (visto por último, online, foto, recado, quem adiciona em grupos, confirmação de
  leitura, chamadas, mensagens) e mensagens temporárias padrão.
- Leitura automática por instância (`auto_read`), em lote e com ritmo.
- Ferramentas MCP de contatos, bloqueio e `update_chat`; abas no painel.

**Ainda não**: limpar conversa, mensagens temporárias por chat, adicionar contato à agenda, listas de exceção da
privacidade. Pareamento por passkey não é suportado (use QR ou código).

## Marco 8: grupos e comunidades

**Novo**

- Grupos: listar, criar (até 10 participantes), metadados, alterar nome, descrição e configurações (`announce`,
  `locked`, `approval_required`, `admins_add_members`, `ephemeral_seconds`), trocar e remover foto, gerenciar
  participantes (`add`, `remove`, `promote`, `demote`) com resultado por pessoa, pedidos de entrada, link de convite
  e redefinição, sair, prévia e entrada por convite.
- Comunidades: listar, criar, metadados com os grupos, vincular e desvincular grupos.
- Evento `group.updated`, um por tipo de mudança (`kind`), inclusive as feitas no celular.
- Anti-ban: adições só de contatos conhecidos, 10 por lote, 10s entre lotes e cotas por hora e dia em
  `settings.groups` (persistidas); criação e entrada por link com cota diária.
- Nome dos chats de grupo no log preenchido e atualizado.
- Ferramentas MCP de grupos e aba **Grupos** no painel. Veja o [guia de grupos](/docs/guias/grupos/).

**Ainda não**: convite por mensagem para quem só aceita convite, desativar comunidade, proteção anti-golpe em grupos
de avisos.

## Marco 7: eventos e chamadas

**Novo**

- Eventos `chat.presence` (digitando, gravando, online/offline; opt-in com `presence.online`), `call.received`,
  `call.accepted`, `call.ended` (com `missed`) e `message.button_reply`.
- `POST /presence/subscribe` para acompanhar se um contato está online.
- Rejeição automática de chamadas individuais (`calls.auto_reject`, em menos de 3s) com mensagem pela fila
  (`calls.reject_message`, uma por contato a cada 10 min).
- Status `played` (áudio de voz, vídeo ou visualização única reproduzidos) em `message.status` e no log.
- Filtros de webhook: `filters.chat` (`direct`/`group`), `chats`, `exclude_chats`, `message_types` e `from_me`.

## Marco 5: hook de agente

**Novo**

- `PUT`/`GET`/`DELETE /v1/instances/{id}/agent-hook`: um `POST` assinado para o seu agente a cada mensagem elegível,
  com o contexto da conversa; a resposta (texto, mídia e ações `mark_read`, `react`, `pause`, `handoff`) passa pela
  fila normal.
- Pausa automática quando um humano responde pelo celular (`human_pause`), cancelando respostas ainda na fila; opção
  de pausar também em envios pela API.
- Uma chamada por vez por chat, loop guard (`max_replies_per_window`), liga/desliga por conversa
  (`PUT /chats/{jid}/agent`) e `GET /agent-hook/calls` com as últimas chamadas.
- Eventos `agent.failed` e `agent.handoff`; exemplo `examples/agent-claude`; aba **Atendente IA** no painel. Veja
  [hook de agente](/docs/mcp/hook-de-agente/).

## Nuvem C1: modo nuvem do motor

**Novo**

- Configuração inteira por ambiente (`LITEZAP_CONFIG_JSON` e variáveis das opções novas).
- Token de operador (`operator_token`) com settings travadas (`locked_settings`), limite de instâncias
  (`max_instances`), proxy obrigatório (`require_proxy`), webhooks do operador e envio suspenso global ou por
  instância (`PUT /v1/operator/sending`).
- Uso diário persistido, `/metrics` no formato Prometheus, endpoint de ops (`ops_listen_addr`), evento
  `instance.proxy_failed` e desligamento gracioso (`shutdown_timeout`).
- Login de um clique emitido pelo operador e exportação lógica (`POST /v1/export`).
- Logs em JSON (`LITEZAP_LOG_FORMAT=json`), watchdog de conexão (`zombie_timeout`) e retenção de mensagens e mídia
  (`message_retention_days`, `media_retention_days`).

O control plane da nuvem (cadastro, provisionamento por cliente e cobrança) ainda está em construção.

## Marco 6: mensagens completas

**Novo**

- Tipos `sticker`, `gif`, `ptv`, `location`, `contact` (1 a 10 cartões) e `poll`, mais as ações `edit` (até 15
  min), `revoke` (até 48h), `pin` e `forward`. Veja [tipos de mensagem](/docs/api/mensagens/tipos/).
- Campos transversais: `reply_to`, `mentions` (inclusive `["all"]` em grupos), `link_preview` (com filtro SSRF e
  limite de 5s), `delay_ms` e `typing_ms`.
- Recebimento de edições, apagadas e votos: eventos `message.edited`, `message.revoked` e `message.poll_vote`; o log
  guarda `edited_at` e `revoked_at`.
- Fila visível: `GET /queue`, `DELETE /queue/{msg_id}` e `DELETE /queue`.
- Ferramentas MCP `send_reaction`, `send_location`, `send_contact`, `edit_message` e `delete_message`;
  `send_message` e `send_media` com `reply_to`, menções e os tipos novos.

**Ainda não**: votar em enquete pela API; fixar não emite evento.

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
