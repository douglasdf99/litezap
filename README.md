# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

Licença: Apache-2.0. Roda no seu PC, no seu servidor ou na nuvem gerenciada do LiteZap.

> Status: marco 1 (conexão, pareamento e supervisão). Envio de mensagens, webhooks, MCP e hook de agente vêm nos próximos marcos.
> Design: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md`

## Rodar

```bash
go build -o litezap ./cmd/litezap
./litezap serve            # usa litezap.yaml se existir; veja litezap.example.yaml
```

O token admin fica em `data/admin_token` (ou defina `LITEZAP_ADMIN_TOKEN`).

Configuração (`litezap.yaml`; variáveis `LITEZAP_*` têm precedência):

| Chave | Variável | Padrão |
|---|---|---|
| `listen_addr` | `LITEZAP_LISTEN_ADDR` | `127.0.0.1:8787` |
| `public_url` | `LITEZAP_PUBLIC_URL` | vazio |
| `data_dir` | `LITEZAP_DATA_DIR` | `data` |
| `admin_token` | `LITEZAP_ADMIN_TOKEN` | gerado em `data/admin_token` |
| `worker_memory_max_mb` | `LITEZAP_WORKER_MEMORY_MAX_MB` | `512` |

Workers rodam em grupo de processos próprio. Se o supervisor morrer à força (SIGKILL), eles são encerrados no próximo `serve` (limpeza de órfãos via `data/run/*.pid`).

## API (marco 1)

Autenticação: `Authorization: Bearer <token>`.

| Rota | Token |
|---|---|
| `POST /v1/instances` `{"name"}` → `{instance, token}` | admin |
| `GET /v1/instances` | admin |
| `GET /v1/instances/{id}` | instância ou admin |
| `POST /v1/instances/{id}/start\|stop\|restart\|logout` | instância ou admin |
| `GET /v1/instances/{id}/qr[?format=png]` | instância ou admin |
| `POST /v1/instances/{id}/pair-phone` `{"phone"}` → `{code}` | instância ou admin |
| `POST /v1/instances/{id}/token/rotate` | admin |
| `DELETE /v1/instances/{id}` | admin |
| `GET /healthz`, `GET /readyz` | público (admin se o `listen_addr` não for loopback) |

Erros: `{"error":{"code","message","retryable"}}`. Códigos:

| Código | HTTP | Quando |
|---|---|---|
| `validation_failed` | 400 | corpo ou campo inválido |
| `unauthorized` | 401 | token ausente ou inválido |
| `forbidden` | 403 | token sem acesso ao recurso |
| `instance_not_found` | 404 | instância inexistente |
| `instance_not_connected` | 409 | worker da instância não está rodando |
| `instance_banned` | 409 | número banido; apague a instância |
| `instance_not_pairing` | 409 | instância não está aguardando QR/pareamento |
| `worker_error` | 502 | o worker da instância recusou ou falhou a operação (`retryable`) |
| `shutting_down` | 503 | servidor encerrando |
| `internal` | 500 | erro interno |

## Como testar com seu WhatsApp

Requer um número de teste no celular.

1. Suba o servidor: `go build -o litezap ./cmd/litezap && ./litezap serve`.
2. Em outro terminal, crie a instância com o token admin e guarde `ID` e `TOK` (o token da instância só aparece uma vez):
   ```bash
   ADMIN=$(cat data/admin_token)
   curl -s -X POST localhost:8787/v1/instances -H "Authorization: Bearer $ADMIN" -d '{"name":"teste"}'
   ```
3. Baixe o QR e escaneie em WhatsApp > Aparelhos conectados:
   ```bash
   curl -s "localhost:8787/v1/instances/$ID/qr?format=png" -H "Authorization: Bearer $TOK" -o qr.png && open qr.png
   ```
4. Confira o estado: `curl -s localhost:8787/v1/instances/$ID -H "Authorization: Bearer $TOK"` deve mostrar `"state":"connected"` e `phone`.
5. Crash do worker: `kill -9 $(cat data/run/$ID.pid)`; em alguns segundos o worker volta sozinho a `connected`, sem novo QR.
6. Pare o `serve` (Ctrl+C) e suba de novo: a instância reconecta sem QR.
7. Logout: `curl -s -X POST localhost:8787/v1/instances/$ID/logout -H "Authorization: Bearer $TOK"` leva o estado a `logged_out` e o aparelho some da lista no celular.

## Desenvolvimento

```bash
go test -race ./...
go tool buf generate   # após mudar proto/
```
