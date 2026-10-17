# LiteZap Engine — Design (Peça #1)

Data: 2026-10-17
Status: aprovado em brainstorming, revisado, aguardando revisão final

## 1. Contexto e visão

LiteZap é uma API de WhatsApp não oficial com dois modos de uso, sobre o mesmo código:

- **Nuvem**: serviço gerenciado, cobrança mensal por instância (modelo Z-API).
- **Local**: app instalável na máquina do usuário, sem depender da nuvem.

Objetivos de produto: estabilidade equivalente à Z-API e alta integrabilidade com IA (Claude, GPT) via MCP e via atendimento automatizado.

O produto é decomposto em peças, cada uma com seu próprio spec → plano → implementação:

| # | Peça | Este spec? |
|---|------|-----------|
| 1 | **Motor (Go)**: conexão WhatsApp, REST, webhooks, MCP, hook de agente | **Sim** |
| 2 | App local (desktop): bandeja, QR, auto-update, túnel opcional | Não |
| 3 | Nuvem: control plane fechado, um projeto LiteZap isolado por cliente, billing, pool gerenciado de IPs de saída | Não |
| 4 | Atendente IA completo: memória, base de conhecimento, painel | Não |
| 5 | Ferramentas por cima | Não |

## 2. Decisões tomadas

1. **Base de protocolo**: [whatsmeow](https://github.com/tulir/whatsmeow) (MPL-2.0) usado diretamente. GOWA/Wuzapi servem só de referência, sem fork.
2. **Exposição local**: API em `127.0.0.1` por padrão. Túnel seguro opcional fica na peça #2; o motor só permite configurar endereço de bind e `public_url`.
3. **IA no motor**: MCP embutido + webhooks + **hook de agente** (contrato HTTP). O atendente completo é a peça #4.
4. **Modelo de processo**: **supervisor + um worker por instância, mesmo binário**. `Spawner` é uma interface: `ProcessSpawner` (esta peça) e `ContainerSpawner` (peça #3).
5. **IA de referência**: exemplos e testes manuais do hook de agente usam o Claude padrão (modelo default configurável, chave via `ANTHROPIC_API_KEY`). Depois migra para uma conta/chave exclusiva do LiteZap, só por configuração.
6. **Streams em tempo real**: só SSE (WebSocket fora do escopo).
7. **Modelo de distribuição (estilo Supabase)**: o motor é open source e idêntico em três formas: app local (PC), self-host (servidor/VPS próprio, Docker) e nuvem gerenciada. A nuvem vende um **projeto pré-instalado por cliente**: um container/VM isolado rodando o mesmo binário, com upgrades, backup, monitoramento e IPs gerenciados. O que fica fechado é só o control plane da nuvem (provisionamento, billing, pool de IPs). **Licença do motor: Apache-2.0** (a mesma do Supabase), compatível com a dependência whatsmeow (MPL-2.0).
8. **IP de saída por instância**: o motor suporta proxy HTTP/SOCKS5 por instância (seção 4.6). Isso é o que permite, na nuvem, não colocar vários números de clientes diferentes atrás do mesmo IP de datacenter.
9. **Rampa de envio para números novos**: limites diários crescem com a idade da instância (seção 4.1). Vem ligada por padrão e é configurável.

## 3. Arquitetura

```
                ┌──────────────── litezap serve (supervisor) ────────────────┐
 REST /v1  ───▶ │ api ──────┐                                                │
 MCP HTTP  ───▶ │ mcp ──────┼─▶ service ─▶ manager ──▶ spawner (interface)   │
                │           │                 │          └─ ProcessSpawner   │
                │ events ◀──┤                 │ gRPC (unix socket)           │
                │ queue  ◀──┤                 ▼                              │
                │ store  ◀──┘  ┌──────────┐ ┌──────────┐                     │
                └──────────────┤ worker A │ │ worker B │ ← 1 processo/número │
                               └──────────┘ └──────────┘
 litezap mcp (stdio) ──REST──▶ supervisor já rodando
```

Binário único, subcomandos:

- `litezap serve`: supervisor (API, MCP HTTP, fila, eventos, store).
- `litezap worker --instance X --socket <path>`: worker de uma instância (iniciado pelo spawner, nunca pelo usuário).
- `litezap mcp`: ponte MCP via stdio para clientes como Claude Desktop. **Não** sobe supervisor; conecta a um `serve` já rodando via REST (`LITEZAP_URL`, `LITEZAP_TOKEN`) e expõe as mesmas ferramentas.
- `litezap version`.

Sem dependências externas no modo local.

### 3.1 Pacotes do supervisor

- **`service`**: casos de uso (criar instância, enviar mensagem, listar chats...). REST e MCP são adaptadores finos; nenhuma lógica duplicada. As ferramentas MCP são definidas uma única vez sobre uma interface `Backend` com duas implementações: `service` local (MCP HTTP) e cliente REST (`litezap mcp`).
- **`api`**: REST (seção 6). Autenticação e tokens (seção 6.1).
- **`mcp`**: servidor MCP streamable HTTP em `/mcp` + implementação stdio usada por `litezap mcp`. Ferramentas na seção 7.
- **`manager`**: ciclo de vida das instâncias, máquina de estados (9.1), heartbeat, restart, circuit breaker.
- **`spawner`**: interface `Start(ctx, instanceID) (WorkerHandle, error)`, `Stop(ctx, instanceID) error`, `Status(instanceID) WorkerStatus`. `WorkerHandle` expõe o cliente gRPC e o PID/ID.
- **`queue`**: fila de envio persistente por instância (4.1).
- **`events`**: barramento interno com o envelope da seção 5; consumidores: `webhook`, `agenthook`, SSE.
- **`webhook`**: entrega assinada, retry e dead-letter (5.3).
- **`agenthook`**: contrato de IA externa (4.3).
- **`store`**: interface de persistência (seção 8); implementação SQLite.
- **`media`**: armazenamento em disco local com URLs temporárias assinadas (4.5).

### 3.2 Worker

- Envolve **um** cliente whatsmeow. Store de sessão do whatsmeow em `data/instances/X/session.db`.
- gRPC sobre unix socket (AF_UNIX funciona no Windows 10+; não usamos named pipes). Caminho curto para respeitar o limite de ~104 bytes do macOS: `<tmpdir>/lz-<hash8>/<instance_hash8>.sock`, onde `hash8` deriva do `data_dir`.
- RPCs: `Ping`, `GetQR`, `PairPhone(phone)`, `Send(payload) → wa_message_id`, `MarkRead`, `SendPresence`, `DownloadMedia(ref)`, `GetContacts`, `GetContact(jid)`, `CheckNumbers([]phone)`, `Logout`, e o stream `Events`.
- **Não existe `ListChats` no worker**: o whatsmeow não lista chats. Chats e histórico vêm do log de mensagens do supervisor (8), alimentado pelos eventos de mensagem e de *history sync*.
- Normaliza eventos whatsmeow para o envelope LiteZap (seção 5).
- Sem lógica de negócio. Se morrer, o supervisor reinicia e a sessão volta do disco.

## 4. Fluxo de dados

### 4.1 Envio

1. `POST /v1/instances/{id}/messages` com `{to, type, ..., client_message_id?}` → validação → grava em `outbound_queue` → `202 {id, status:"queued"}`. `client_message_id` repetido devolve o item existente (idempotência).
2. Status do item: `queued → in_flight → sent | failed`. Despachante por instância (uma goroutine) retira itens em **ordem estrita** (um item em backoff segura os seguintes daquele destinatário; outros destinatários seguem). O despachante **fica pausado enquanto a instância não está `connected`**, sem consumir tentativas. Ritmo: respeitando o ritmo configurado: `min_interval` (padrão 3s) + `jitter` (padrão 0–2s), `max_per_minute` (padrão 20), `typing_simulation` (padrão ligado, ~50ms por caractere, máx. 5s).
3. Chama `Send` no worker. Sucesso → `sent` + `wa_message_id` gravado (também usado na detecção de humano, 4.3).
4. Falha retentável (timeout ou erro de rede do worker com a instância `connected`) → volta para a fila com backoff, até **5 tentativas**; depois `failed`. Falha não retentável (`invalid_number`, mídia inválida, `logged_out`, `banned`) → `failed` imediato.
5. Status `delivered`/`read` chegam como eventos (receipts) → atualizam o store → evento `message.status`.
6. **Modo síncrono** `?wait=true&timeout=30s`: a requisição aguarda o item sair de `queued`. Se ficar `sent` dentro do prazo → `200` com `wa_message_id`; se `failed` → erro; se o prazo estourar, a mensagem **continua na fila** e a resposta é `202 {id, status:"queued"}`.

**Rampa por idade da instância** (padrão ligado, configurável por instância em `settings`): o limite diário de destinatários *novos* (chats sem mensagem recebida anteriormente) começa em 50/dia e sobe linearmente até 150/dia ao longo de 14 dias desde o primeiro `connected`. Acima do limite, os envios para destinatários novos ficam `queued` até o dia seguinte; respostas a chats que já falaram com a instância não contam. Mensagens idênticas para muitos destinatários novos em sequência são espaçadas com o dobro do `min_interval`.

Tipos de mensagem suportados (lista fechada nesta peça): `text`, `image`, `video`, `audio` (inclui voz/ptt), `document`, `sticker`, `location`, `contact`, `reaction`. Mídia entra por `media.url` (o supervisor baixa) ou `media.base64`; multipart fica fora. O download por URL tem proteção contra SSRF: timeout 30s, limite de tamanho do tipo, e bloqueio de IPs privados/loopback/link-local (desligável por config para uso local).

O `id` devolvido no envio é o id do item da fila, e é ele que se consulta em `GET /messages/{msg_id}` (que também mostra o `wa_message_id` quando houver).

### 4.2 Recebimento

1. O worker recebe o evento whatsmeow, normaliza e transmite via `Events`.
2. O supervisor grava no log (`messages`, `chats`) e publica no barramento.
3. Consumidores em paralelo: webhooks (5.3), hook de agente (4.3), SSE `GET /v1/instances/{id}/events` e `GET /v1/events` (todas as instâncias, só token admin).
4. *History sync* do pareamento é ingerido no log (sem gerar webhooks por mensagem; emite um único `history.synced` com contagem).

### 4.3 Hook de agente

Configuração por instância (`PUT /v1/instances/{id}/agent-hook`): `url`, `secret`, `timeout` (padrão 30s), `context_messages` (padrão 20), `enabled`, `groups_enabled` (padrão false), `human_pause` (padrão 30min), `ignore` (lista de JIDs).
Override por chat (`PUT /v1/instances/{id}/chats/{jid}/agent`): `{enabled: true | false | null}` (`null` herda o padrão da instância). As ações `handoff` e o desligamento manual usam esse mesmo campo.

Disparo: só para `message.received` de chats não ignorados, não pausados e não desligados. Mensagens são processadas uma por vez por chat (sem respostas concorrentes no mesmo chat).

**Request** `POST {url}`, assinado como webhook (5.3):

```json
{
  "instance_id": "inst_123",
  "chat": {"jid": "5511999999999@s.whatsapp.net", "name": "João", "is_group": false},
  "message": { "...": "mesmo objeto data de message.received" },
  "context": [ { "...": "últimas N mensagens do chat, mais antiga primeiro" } ]
}
```

**Response** `200`:

```json
{
  "reply": "texto",
  "actions": [ {"type": "mark_read"} ]
}
```

- `reply`: string (vira mensagem `text`) **ou** um `MessagePayload` (o mesmo corpo de `POST /messages`, sem `to`). Opcional.
- `actions` (opcional), conjunto fechado:
  - `{"type":"mark_read"}`: marca a mensagem como lida.
  - `{"type":"react","emoji":"👍"}`: reage à mensagem recebida.
  - `{"type":"pause","minutes":60}`: desliga o hook neste chat por N minutos.
  - `{"type":"handoff","note":"..."}`: desliga o hook neste chat até reativação manual e emite evento `agent.handoff`.
- Ação desconhecida: ignorada e registrada em log; não invalida o resto da resposta.
- `204` ou corpo sem `reply` e sem `actions`: não faz nada.
- Erro, resposta inválida ou timeout: evento `agent.failed` com o motivo; nada é enviado (fail-closed); sem retry.

**Pausa por humano**: quando chega mensagem `from_me` cujo `wa_message_id` **não** está entre os enviados pelo LiteZap, um humano respondeu pelo celular → o hook pausa naquele chat por `human_pause`.

Exemplo de referência em `examples/agent-claude/`: servidor mínimo em Go que implementa este contrato com o Claude padrão.

### 4.4 Conexão

- `POST /v1/instances` cria a instância (devolve o token da instância) e sobe o worker.
- `GET /v1/instances/{id}/qr` devolve o QR atual (`?format=png|text`); atualizações via evento `instance.qr`.
- `POST /v1/instances/{id}/pair-phone {phone}` devolve código de pareamento; o evento `instance.pair_code` também é emitido.

### 4.5 Mídia

- Recebida: **download sob demanda**. O evento traz `media.id`; `GET /v1/instances/{id}/media/{media_id}` baixa via worker na primeira vez e guarda em `data/media/`. `GET .../media/{media_id}?signed=1` devolve `{url}` assinada (TTL 1h) que funciona sem token, para repassar a terceiros (ex.: IA). Arquivos expiram após 7 dias (configurável).
- Enviada: limites de tamanho por tipo (padrão: imagem 16MB, vídeo/áudio 64MB, documento 100MB).

### 4.6 Rede: proxy por instância

- Config por instância em `settings.proxy_url` (`http://`, `https://` ou `socks5://`, com usuário/senha opcionais), mais um `default_proxy_url` global na config.
- O worker aplica o proxy antes de conectar (`whatsmeow.Client.SetProxyAddress`), tanto no websocket quanto em upload/download de mídia. Trocar o proxy reinicia o worker.
- **Fixo por sessão**: o motor nunca troca o IP de saída sozinho durante uma sessão. Mudar de IP com frequência é um sinal suspeito.
- Falha de conexão pelo proxy entra como `reconnecting` com `reason: proxy_unreachable`; o motor não cai silenciosamente para conexão direta.
- O motor não fornece IPs. Ele só aceita proxies. No modo local, o padrão é a conexão da própria máquina (IP residencial do usuário). Na nuvem, o control plane atribui proxies do pool gerenciado (peça #3).

## 5. Eventos

### 5.1 Envelope (versionado)

```json
{
  "id": "evt_01J...",
  "type": "message.received",
  "version": 1,
  "instance_id": "inst_123",
  "timestamp": "2026-10-17T13:45:00Z",
  "data": { }
}
```

Regra de versionamento: adicionar campos em `data` não muda a versão; remover ou alterar o sentido de um campo cria `version: 2`, e webhooks escolhem a versão na assinatura (padrão: a mais recente na criação do webhook).

### 5.2 Catálogo (v1)

| type | quando |
|------|--------|
| `instance.status` | qualquer transição de estado (9.1) |
| `instance.qr` | novo QR disponível |
| `instance.pair_code` | código de pareamento gerado |
| `message.received` | mensagem recebida de outra pessoa |
| `message.sent` | mensagem enviada (pela API ou `from_me` pelo celular; campo `source: api\|device`) |
| `message.status` | `sent`, `delivered`, `read`, `failed` de mensagem enviada |
| `message.reaction` | reação recebida |
| `message.revoked` | mensagem apagada para todos |
| `chat.presence` | digitando/gravando/online |
| `group.updated` | entrada/saída de participantes, mudança de nome |
| `history.synced` | ingestão de history sync concluída |
| `agent.failed` | falha no hook de agente |
| `agent.handoff` | ação `handoff` do agente |

### 5.3 Webhooks

- Cadastro: `POST /v1/webhooks {url, secret?, events: ["message.*", ...], instance_id?, version?}`. Sem `instance_id` = global (todas as instâncias). Também `GET/PATCH/DELETE /v1/webhooks/{id}`.
- Assinatura: header `X-LiteZap-Signature: t=<unix>,v1=<hex>` onde `v1 = HMAC-SHA256(secret, "<t>.<corpo>")`. O receptor deve rejeitar `t` com mais de 5 minutos (anti-replay). Header `X-LiteZap-Event-Id` para deduplicação.
- Entrega: timeout 10s; 2xx = sucesso. Retry com backoff exponencial (1s → 1h), por no máximo 24h; depois vai para a dead-letter.
- Dead-letter: `GET /v1/webhooks/{id}/deliveries?status=failed` e `POST /v1/webhooks/{id}/deliveries/{delivery_id}/retry`.

## 6. Superfície REST (v1)

Prefixo `/v1`. Erros no formato `{"error":{"code","message","retryable"}}` (9.3).

| Método e rota | Escopo | Descrição |
|---|---|---|
| `POST /instances` | admin | cria instância; devolve `{instance, token}` |
| `GET /instances` | admin | lista |
| `GET /instances/{id}` | inst | estado e dados do número |
| `DELETE /instances/{id}` | admin | logout + para worker + apaga dados |
| `POST /instances/{id}/start` · `/stop` · `/restart` | inst | controle do worker |
| `POST /instances/{id}/logout` | inst | desconecta o número |
| `POST /instances/{id}/token/rotate` | admin | gera novo token de instância |
| `GET /instances/{id}/qr` · `POST .../pair-phone` | inst | pareamento |
| `GET/PUT /instances/{id}/settings` | inst | ritmo de envio e limites |
| `POST /instances/{id}/messages` | inst | enviar (4.1) |
| `GET /instances/{id}/messages/{msg_id}` | inst | status de uma mensagem |
| `GET /instances/{id}/chats` | inst | chats do log, ordenados por última mensagem |
| `GET /instances/{id}/chats/{jid}/messages` | inst | histórico paginado (cursor) |
| `GET /instances/{id}/messages/search?q=` | inst | busca full-text (FTS5) |
| `POST /instances/{id}/chats/{jid}/read` | inst | marcar como lido |
| `GET /instances/{id}/contacts` · `/contacts/{jid}` | inst | contatos |
| `POST /instances/{id}/check-numbers` | inst | verifica se números têm WhatsApp |
| `GET /instances/{id}/media/{media_id}` | inst | mídia recebida |
| `PUT /instances/{id}/agent-hook` · `PUT .../chats/{jid}/agent` | inst | hook de agente (4.3) |
| `GET /instances/{id}/events` · `GET /events` | inst · admin | SSE |
| `/webhooks...` | admin ou inst (só os da própria instância) | 5.3 |
| `GET /healthz` · `/readyz` · `/metrics` | sem auth em bind loopback; admin caso contrário | observabilidade |

### 6.1 Autenticação e tokens

- **Token admin**: definido em `admin_token` na config; se ausente na primeira execução, é gerado e gravado em `data/admin_token` (permissão 0600).
- **Token de instância**: gerado em `POST /instances` (32 bytes aleatórios, prefixo `lzi_`), mostrado uma única vez; o store guarda só o hash SHA-256. Rotação via `/token/rotate`.
- Envio: `Authorization: Bearer <token>`. Token de instância só acessa rotas da própria instância.
- MCP HTTP usa os mesmos tokens; com token de instância, as ferramentas operam só naquela instância e o parâmetro `instance_id` é opcional.

## 7. Ferramentas MCP

Todas mapeiam casos de uso do `service`:

`list_instances` (com token de instância devolve só a própria), `get_instance_status`, `send_message` (texto, com `wait` por padrão), `send_media` (por URL), `list_chats`, `get_messages` (por chat, paginado), `search_messages`, `get_contact`, `check_numbers`, `mark_read`.

## 8. Store (SQLite)

Arquivo `data/litezap.db`, modo WAL, migrações versionadas embutidas no binário. Entidades:

- `instances` (id, name, state, phone, jid, token_hash, settings_json, created_at, updated_at)
- `chats` (instance_id, jid, name, is_group, last_message_at, unread_count, agent_enabled NULL|bool, agent_paused_until)
- `messages` (instance_id, id, wa_message_id, chat_jid, from_me, source, type, text, media_json, status, timestamp) + tabela FTS5 sobre `text`
- `outbound_queue` (id, instance_id, client_message_id, payload_json, status, attempts, next_attempt_at, last_error, created_at)
- `webhooks` (id, instance_id?, url, secret, events_json, version, enabled)
- `webhook_deliveries` (id, webhook_id, event_id, status, attempts, next_attempt_at, last_status_code, last_error)
- `agent_hooks` (instance_id, config_json)
- `media` (id, instance_id, message_id, mime, size, path, expires_at)

Retenção do log de mensagens: ilimitada por padrão, com `message_retention_days` configurável. A interface `Store` expõe operações por agregado (Instances, Chats, Messages, Queue, Webhooks, Media) para permitir implementação Postgres na peça #3.

## 9. Estabilidade e erros

### 9.1 Estados da instância

| De | Para | Gatilho |
|---|---|---|
| `created` | `starting` | `start` (automático na criação) |
| `starting` | `qr_pending` | worker ativo, sem sessão salva |
| `starting` | `connecting` | worker ativo, com sessão salva |
| `starting` | `connected` · `reconnecting` | o snapshot do worker traz só o estado mais recente |
| `qr_pending` | `connecting` | QR escaneado / código pareado |
| `connecting` | `connected` | conexão estabelecida |
| `connected` | `reconnecting` | queda de conexão ou watchdog |
| `reconnecting` | `connected` | reconectou |
| qualquer ativo | `logged_out` | logout pelo celular ou pela API |
| qualquer ativo | `banned` | erro de conta banida/bloqueada (permanente) |
| qualquer ativo | `failed` | circuit breaker aberto; banimento temporário (`state_reason` `temp_banned:<duração>`, sessão preservada) |
| `connecting` | `reconnecting` | falha ao conectar com sessão válida |
| qualquer ativo | `starting` | crash do worker (restart automático com backoff) |
| qualquer, exceto `banned` | `stopped` | `stop` |
| `logged_out` · `failed` · `stopped` | `starting` | `start` ou `restart` (em `logged_out` leva a `qr_pending`) |
| `banned` | — | só `DELETE` |

Toda transição é persistida e emitida como `instance.status`.

### 9.2 Supervisão

- **Heartbeat** `Ping` a cada 10s; 3 falhas seguidas → kill do processo + restart.
- **Watchdog de conexão** no worker: em `connected` sem nenhum evento/keepalive por `zombie_timeout` (padrão 5min) → força reconexão.
- **Restart com backoff exponencial** (1s → 5min). **Circuit breaker**: 5 crashes em 10min → `failed` + evento; sem restart até ação manual.
- **Limite de memória** por worker: `GOMEMLIMIT` + kill acima de `worker_memory_max` (padrão 512MB).
- **Erros terminais** (`logged_out`, `banned`): sem retry.
- **Shutdown gracioso**: para despachantes, aguarda envios em voo (até 10s), desconecta workers e encerra.
- **Boot**: cada worker grava `data/run/X.pid`. No boot, o supervisor mata PIDs órfãos que ainda sejam `litezap worker`, remove sockets antigos, devolve itens `in_flight` para `queued` (checando idempotência contra `messages`) e sobe as instâncias que não estavam `stopped`.

### 9.3 Erros na API

Códigos estáveis: `unauthorized`, `forbidden`, `instance_not_found`, `instance_not_connected`, `instance_banned`, `instance_not_pairing`, `worker_error`, `shutting_down`, `rate_limited`, `invalid_number`, `media_too_large`, `media_unavailable`, `validation_failed`, `internal`. `retryable` indica se o cliente pode tentar de novo. Erros gRPC do worker: `Unavailable`/`DeadlineExceeded`/`Canceled` viram `instance_not_connected`; qualquer outro vira `worker_error` (HTTP 502, `retryable`, mensagem do worker sem números de telefone nem tokens).

### 9.4 Observabilidade

- Logs estruturados (`slog`) com `instance_id` em toda linha.
- `/metrics` (Prometheus): mensagens enviadas/recebidas, latência e profundidade da fila, restarts, estado por instância, entregas de webhook por status.
- `/healthz` (processo vivo) e `/readyz` (store ok e manager pronto).

## 10. Configuração

`litezap.yaml` + variáveis `LITEZAP_*` (env sobrescreve arquivo): `listen_addr` (padrão `127.0.0.1:8787`), `public_url`, `data_dir`, `admin_token`, padrões de ritmo (4.1), `worker_memory_max`, `zombie_timeout`, limites de mídia, `media_retention_days`, `message_retention_days`.

## 11. Testes

- **Unitários** por pacote: fila e ritmo, máquina de estados (tabela 9.1), backoff e circuit breaker, assinatura de webhook, regras do hook de agente; com relógio falso.
- **Worker falso**: implementação do mesmo contrato gRPC que simula conectar, cair, travar, banimento, lentidão e mensagens recebidas. Valida o supervisor inteiro sem WhatsApp real.
- **Contrato** REST e MCP via `httptest`.
- **E2E** com número de teste real, atrás de `-tags=e2e`, fora do CI.

## 12. Stack

Go na versão exigida pela versão mais recente do whatsmeow (fixada no `go.mod`), whatsmeow, `chi`, gRPC (supervisor↔worker), `modernc.org/sqlite` (sem CGO, com FTS5; o `sqlstore` do whatsmeow exige `_pragma=foreign_keys(1)` no DSN), SDK Go oficial de MCP, `slog`, Prometheus client.

## 13. Marcos de implementação

Cada marco é testável sozinho:

1. Esqueleto: binário, config, store e migrações, `spawner` + worker com whatsmeow, máquina de estados, pareamento (QR e código).
2. Envio (fila, ritmo, rampa, idempotência, `wait`), recebimento (log, chats, history sync) e proxy por instância.
3. Eventos: barramento, SSE, webhooks com assinatura, retry e dead-letter.
4. MCP: HTTP em `/mcp` e `litezap mcp` (stdio).
5. Hook de agente + `examples/agent-claude/`.

## 14. Fora do escopo (YAGNI)

Painel web, cobrança, organizações e multi-usuário, `ContainerSpawner`, Postgres, S3, túnel, atendente IA completo, WebSocket, upload multipart, rolling restart, listas de transmissão e chamadas (não suportados pelo whatsmeow).

## 15. Riscos

- **Mudanças no protocolo do WhatsApp**: dependemos do ritmo de atualização do whatsmeow. Mitigação: envelope de eventos LiteZap isola consumidores; atualizar = trocar binário e reiniciar.
- **Banimento de números**: inerente ao uso não oficial. Os principais gatilhos são mensagens idênticas em massa, velocidade anormal, muitas denúncias ou bloqueios e muitos números no mesmo IP. Mitigação: ritmo e rampa conservadores por padrão, proxy por instância, e na nuvem um pool de IPs com no máximo um cliente por IP. O LiteZap é voltado a conversas que o destinatário espera (atendimento, clientes, opt-in). Não terá recursos para fabricar perfis falsos ou conversas artificiais de "maturação".
- **Contaminação entre clientes na nuvem**: um cliente que faz spam marca o IP de saída, e os outros no mesmo IP pagam o preço. Mitigação: na nuvem, isolamento de IP por cliente (peça #3).
- **Licença MPL-2.0**: modificações em arquivos do whatsmeow precisam ser publicadas; usar como dependência não impõe licença ao restante do código.
