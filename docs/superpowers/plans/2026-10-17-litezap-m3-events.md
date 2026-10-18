# LiteZap Marco 3 — Eventos, webhooks e SSE (plano curto)

Spec: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md` seções 4.2, 5.1–5.3, 6, 8, 10, 13 item 3.

## Decisões

- **Envelope v1** (5.1): `{id:"evt_…", type, version:1, instance_id, timestamp, data}`. O JSON é serializado uma vez no `Publish` e reusado por SSE e webhooks.
- **Produtores**: `events.Observer` (manager.Observer) emite `instance.status` (StateChanged ganha `reason`) e `instance.qr` (o manager passa a repassar eventos QR ao observer); `ingest` emite `message.received`/`message.sent` (`source: device`)/`message.reaction` só para mensagens novas (dedupe pelo INSERT) e `message.status` (`delivered`/`read`) só para linhas cujo status subiu (`ApplyReceipt … RETURNING`); `queue` emite `message.sent` (`source: api`) + `message.status: sent` no `MarkSent` e `message.status: failed` no `MarkFailed`; `service.Instances.PairPhone` emite `instance.pair_code`.
- **history.synced**: o worker não marca o fim de um sync, então `events.HistoryTracker` agrega os blocos por instância e publica um evento após 15s sem blocos novos (`chats`, `messages`, `new_messages`); `Close` publica o que estiver pendente.
- **Adiados** (o worker não reporta): `message.revoked`, `chat.presence`, `group.updated`; `agent.*` é do marco 5.
- **Barramento** (`events.Bus`): `Publish` nunca bloqueia (RLock + envio não bloqueante). Assinaturas dinâmicas (SSE) com buffer limitado; buffer cheio → assinatura encerrada (cliente lento desconectado). Sinks fixos (webhook) recebem a chamada síncrona e devem ser não bloqueantes. Nenhum consumidor chama o Manager.
- **Webhooks** (`internal/webhook`): `Enqueue` põe o envelope num canal grande (10.000); um goroutine grava `webhook_deliveries` em lote (INSERT guardado por `EXISTS` do webhook). Canal cheio → descarte logado/contado (produtores não bloqueiam). Shutdown drena o canal antes de fechar o store. Entrega: no máximo N simultâneas (8) e 2 por webhook; 10s timeout; 2xx = sucesso; sem seguir redirecionamento; backoff exponencial 1s→1h por entrega e por endpoint (falha de um endpoint segura as outras entregas dele); `expires_at` = criação + 24h → `failed` (dead-letter). Pendentes vencidos também são varridos para `failed`. Entrega em voo fica `pending` no banco: um crash reentrega (at-least-once; o receptor deduplica por `X-LiteZap-Event-Id`).
- **Assinatura**: `X-LiteZap-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<corpo>")>`, `X-LiteZap-Event-Id`, `X-LiteZap-Event-Type`. Segredo `whsec_…` gerado se ausente, devolvido só na criação (e na rotação via PATCH `secret`); nunca logado.
- **SSRF**: o transporte do webhook usa o dialer do `media` (`media.SafeDialer`); `webhook_allow_private_urls` libera privados/loopback para uso local.
- **Retenção**: `webhook_retention_days` (entregues, padrão 7) e `webhook_failed_retention_days` (dead-letter, padrão 30); limpeza a cada hora.
- **API**: `POST/GET /v1/webhooks`, `GET/PATCH/DELETE /v1/webhooks/{id}`, `GET /v1/webhooks/{id}/deliveries?status=`, `POST /v1/webhooks/{id}/deliveries/{delivery_id}/retry`. Admin: global ou por instância; token de instância: só a própria (webhook de outra instância = 404). SSE: `GET /v1/instances/{id}/events` (inst) e `GET /v1/events` (admin), `?types=message.*,instance.status`, heartbeat `: ping` a cada 15s, `http.ResponseController` com deadline de escrita por evento; o shutdown do servidor encerra os streams.

## Tarefas (TDD em cada uma)

1. `events`: Envelope, `Match` (curinga `message.*`, `*`), Bus (publish/subscribe/slow-drop/sinks), HistoryTracker (relógio falso). Testes de unidade.
2. Produtores: assinatura de `StateChanged` com reason; manager repassa QR; `events.Observer`; ingest (dedupe, receipts com RETURNING, history tracker); queue (sent/failed); service pair_code. Testes com `workertest`.
3. Store: migração `0003_webhooks.sql` + CRUD de webhooks e operações de entregas. Testes de store.
4. `webhook`: assinatura/verificação, backoff, Dispatcher (writer em lote, seleção de vencidas com exclusão de endpoints em backoff/cheios, pool, dead-letter, retry manual, retenção, drenagem no Close). Testes com `httptest` + relógio falso.
5. `service.Webhooks` + rotas REST + SSE na `api`. Testes de contrato com `httptest` (escopo admin/instância, segredo uma vez, SSE com filtro e heartbeat).
6. Config (`webhook_allow_private_urls`, retenções), wiring no `serve` (ordem de shutdown: HTTP → fila → manager → tracker → webhooks → store) e README (envelope, catálogo, verificação em Go/Node, retry, teste manual).
7. Revisão independente (concorrência, garantias de entrega, segurança) e correções.

Cada commit: `gofmt`, `go vet ./...`, `go test -p 2 -race ./...`.
