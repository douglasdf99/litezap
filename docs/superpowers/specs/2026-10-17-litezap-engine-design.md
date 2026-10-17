# LiteZap Engine — Design (Peça #1)

Data: 2026-10-17
Status: aprovado em brainstorming, aguardando revisão final

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
| 3 | Nuvem: control plane, billing, ContainerSpawner | Não |
| 4 | Atendente IA completo: memória, base de conhecimento, painel | Não |
| 5 | Ferramentas por cima | Não |

## 2. Decisões tomadas

1. **Base de protocolo**: [whatsmeow](https://github.com/tulir/whatsmeow) (MPL-2.0) usado diretamente. GOWA/Wuzapi servem só de referência, sem fork.
2. **Exposição local**: API em `localhost` por padrão. Túnel seguro opcional fica na peça #2; o motor só precisa permitir configurar o endereço de bind e a URL pública.
3. **IA no motor**: MCP embutido + webhooks + **hook de agente** (contrato HTTP simples). O atendente completo é a peça #4.
4. **Modelo de processo**: **supervisor + um worker por instância, mesmo binário**. O `Spawner` é uma interface: `ProcessSpawner` (local, nesta peça) e `ContainerSpawner` (nuvem, peça #3).
5. **IA de referência**: para exemplos e testes manuais do hook de agente, usar o Claude padrão (modelo default configurável, chave via `ANTHROPIC_API_KEY`). Depois será migrado para uma conta/chave exclusiva do LiteZap, sem mudança de código, só de configuração.

## 3. Arquitetura

```
                ┌──────────────── litezap serve (supervisor) ────────────────┐
 REST /v1  ───▶ │ api ──────┐                                                │
 MCP (http/stdio)▶ mcp ─────┼─▶ manager ──▶ spawner (interface)             │
                │           │      │           ├─ ProcessSpawner (esta peça)│
                │ events ◀──┤      │ gRPC/unix └─ ContainerSpawner (peça #3)│
                │ queue  ◀──┤      ▼                                         │
                │ store  ◀──┘  ┌──────────┐ ┌──────────┐                     │
                └──────────────┤ worker A │ │ worker B │ ← 1 processo/número │
                               └──────────┘ └──────────┘
```

Binário único: `litezap serve | worker | version`. Sem dependências externas no modo local.

### 3.1 Pacotes do supervisor

Cada pacote tem uma responsabilidade e uma interface clara.

- **`service`**: camada de casos de uso (enviar mensagem, listar chats, criar instância...). REST e MCP são adaptadores finos sobre ela; nenhuma lógica duplicada.
- **`api`**: REST `/v1/instances/{id}/...` no estilo Z-API. Autenticação por token de conta (admin) e token de instância.
- **`mcp`**: servidor MCP (streamable HTTP e stdio). Ferramentas: `list_instances`, `get_instance_status`, `send_message`, `send_media`, `list_chats`, `get_messages`, `search_messages`, `get_contact`, `mark_read`.
- **`manager`**: ciclo de vida das instâncias, health check, política de restart e circuit breaker.
- **`spawner`**: interface `Start(ctx, instanceID) (WorkerHandle, error)`, `Stop`, `Status`. Esta peça implementa só o `ProcessSpawner`.
- **`queue`**: fila de envio persistente por instância, com controle de ritmo, retry e idempotência.
- **`events`**: barramento interno; consumidores são webhooks, hook de agente e streams SSE/WebSocket.
- **`webhook`**: entrega com assinatura HMAC, retry com backoff e dead-letter.
- **`agenthook`**: contrato de IA externa (seção 4.3).
- **`store`**: interface de persistência; implementação SQLite (local). Postgres fica para a peça #3, atrás da mesma interface.
- **`media`**: armazenamento de mídia em disco local com URLs temporárias assinadas. S3 fica para a peça #3.

### 3.2 Worker

`litezap worker --instance X --socket <path>`

- Envolve **um** cliente whatsmeow. O store de sessão do whatsmeow fica em `data/instances/X/session.db` (SQLite próprio por instância).
- Expõe gRPC via unix socket (named pipe no Windows): comandos `Send`, `DownloadMedia`, `GetQR`, `PairPhone`, `Logout`, `Ping`, `ListChats/Contacts` e um stream `Events`.
- Normaliza eventos whatsmeow para o **formato de evento LiteZap** (versionado, independente do whatsmeow).
- Sem lógica de negócio. Se morrer, o supervisor reinicia e a sessão volta do disco.

## 4. Fluxo de dados

### 4.1 Envio

1. `POST /v1/instances/X/messages/{text|image|audio|document|...}` → validação → grava na fila com `client_message_id` (idempotente) → `202 {id, status:"queued"}`.
2. O despachante da instância retira itens respeitando o ritmo: intervalo mínimo + jitter, limite por minuto, e "digitando…" opcional proporcional ao texto. Todos configuráveis por instância.
3. Chama o worker via `Send`, que devolve o `wa_message_id`.
4. Os status `sent → delivered → read | failed` chegam como eventos → atualizam o store → webhook `message-status`.
5. Modo síncrono `?wait=true` (timeout configurável, padrão 30s) para quem precisa do ID na hora (ex.: MCP).

### 4.2 Recebimento

1. O worker recebe o evento whatsmeow, normaliza e transmite via `Events`.
2. O supervisor grava no log de mensagens e publica no barramento.
3. Consumidores em paralelo:
   - **Webhooks**: POST com header `X-LiteZap-Signature` (HMAC-SHA256). Retry com backoff de 1s até 1h, por no máximo 24h. Falhas vão para a dead-letter, que é consultável e reenviável via API.
   - **Hook de agente** (seção 4.3).
   - **Streams** SSE/WebSocket em `/v1/instances/X/events`.

### 4.3 Hook de agente

Configuração por instância: `url`, `secret`, `timeout` (padrão 30s), `context_messages` (padrão 20), `enabled`.

- Request: `POST {url}` assinado, com `{instance, chat, message, context: [últimas N mensagens]}`.
- Response: `{reply?: string | MessagePayload, actions?: [...]}`. O `reply` entra na fila de envio normal (com ritmo).
- Regras embutidas: liga/desliga por chat; **pausa automática por chat quando um humano responde pelo celular** (duração configurável); lista de contatos e grupos ignorados; grupos desligados por padrão.
- Falha ou timeout: registra o erro, emite o evento `agent-hook-failed` e não responde nada (fail-closed).
- Exemplo de referência em `examples/agent-claude/`: um servidor mínimo que implementa o contrato usando o Claude padrão.

### 4.4 Conexão

- `POST /v1/instances` cria a instância e sobe o worker.
- `GET /v1/instances/X/qr` devolve o QR (PNG ou string); as atualizações chegam via stream de eventos.
- `POST /v1/instances/X/pair-phone` gera um código de pareamento por telefone.
- `POST /v1/instances/X/logout`, `.../restart` e `DELETE /v1/instances/X`.

## 5. Estabilidade e erros

### 5.1 Estados da instância

`created → starting → qr_pending → connecting → connected ⇄ reconnecting → logged_out | banned | failed | stopped`

As transições são explícitas, persistidas e emitidas como evento `instance-status`.

### 5.2 Supervisão

- **Heartbeat** `Ping` a cada 10s; 3 falhas seguidas → `SIGKILL` + restart.
- **Watchdog de conexão** no worker: se o estado é "conectado" mas não chega nenhum evento ou keepalive por um período configurável, força reconexão (sessão zumbi).
- **Restart com backoff exponencial** (1s → 5min). **Circuit breaker**: 5 crashes em 10 min → `failed` + evento; o worker não é reiniciado até ação manual.
- **Limite de memória** por worker: `GOMEMLIMIT` + kill acima de um teto configurável.
- **Erros terminais** (`logged_out`, `banned`, sessão inválida): sem retry; emite o evento e aguarda ação humana.
- **Shutdown gracioso**: drena os envios em andamento, desconecta os workers e encerra. No boot, itens `in_flight` voltam para `queued`, com checagem de idempotência contra o log.
- **Rolling restart**: o manager reinicia um worker por vez (para atualizações).

### 5.3 Erros na API

Formato único: `{"error": {"code": "...", "message": "...", "retryable": bool}}`.
Códigos estáveis, entre eles: `instance_not_found`, `instance_not_connected`, `rate_limited`, `invalid_number`, `media_too_large`, `unauthorized`, `validation_failed`.

### 5.4 Observabilidade

- Logs estruturados (`slog`) com `instance_id` em todas as linhas.
- `/metrics` (Prometheus): mensagens enviadas/recebidas, latência e profundidade da fila, restarts, estado por instância, entregas de webhook.
- `/healthz` (processo vivo) e `/readyz` (store ok e manager pronto).

## 6. Configuração

Arquivo `litezap.yaml` + variáveis de ambiente `LITEZAP_*` (env sobrescreve o arquivo). Itens principais: `listen_addr` (padrão `127.0.0.1:8787`), `public_url`, `data_dir`, `admin_token`, padrões de ritmo, limites de memória e de mídia.

## 7. Testes

- **Unitários** por pacote: fila, ritmo, máquina de estados, backoff e circuit breaker, usando relógio falso.
- **Worker falso**: implementação do mesmo contrato gRPC que simula conectar, cair, travar, banimento e lentidão. Valida o supervisor inteiro (restart, circuit breaker, fila sobrevivendo a crash) sem WhatsApp real.
- **Contrato** REST e MCP via `httptest`.
- **E2E** com número de teste real, atrás de `-tags=e2e`, fora do CI.

## 8. Stack

Go 1.23+, whatsmeow, `chi`, gRPC (supervisor↔worker), `modernc.org/sqlite` (sem CGO, binário multiplataforma), SDK Go oficial de MCP, `slog`, Prometheus client.

## 9. Fora do escopo (YAGNI)

Painel web, cobrança, organizações e multi-usuário, `ContainerSpawner`, Postgres, S3, túnel, atendente IA completo, envio para listas de transmissão e chamadas (não suportados pelo whatsmeow). As interfaces `Spawner`, `Store` e `media` ficam prontas para as próximas peças.

## 10. Riscos

- **Mudanças no protocolo do WhatsApp**: dependemos da velocidade de atualização do whatsmeow. Mitigação: o formato de evento LiteZap isola os consumidores; rolling restart para atualizar.
- **Banimento de números**: inerente ao uso não oficial. Mitigação: controle de ritmo por padrão conservador e documentação de boas práticas.
- **Licença MPL-2.0**: modificações em arquivos do whatsmeow precisam ser publicadas; usar como dependência não impõe licença ao restante do código.
