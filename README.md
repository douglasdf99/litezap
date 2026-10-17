# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

Licença: Apache-2.0. Roda no seu PC, no seu servidor ou na nuvem gerenciada do LiteZap.

> Status: marco 2 (envio com fila persistente, recebimento, chats, histórico, mídia e proxy por instância). Webhooks/SSE, MCP e hook de agente vêm nos próximos marcos.
> Design: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md`

## Rodar

```bash
go build -o litezap ./cmd/litezap
./litezap serve            # usa litezap.yaml se existir; veja litezap.example.yaml
```

O token admin fica em `data/admin_token` (ou defina `LITEZAP_ADMIN_TOKEN`). O segredo das URLs assinadas de mídia fica em `data/media_key` (apagá-lo invalida as URLs já emitidas).

Configuração (`litezap.yaml`; variáveis `LITEZAP_*` têm precedência):

| Chave | Variável | Padrão |
|---|---|---|
| `listen_addr` | `LITEZAP_LISTEN_ADDR` | `127.0.0.1:8787` |
| `public_url` | `LITEZAP_PUBLIC_URL` | vazio (URLs assinadas usam `http://<listen_addr>`) |
| `data_dir` | `LITEZAP_DATA_DIR` | `data` |
| `admin_token` | `LITEZAP_ADMIN_TOKEN` | gerado em `data/admin_token` |
| `worker_memory_max_mb` | `LITEZAP_WORKER_MEMORY_MAX_MB` | `512` |
| `default_proxy_url` | `LITEZAP_DEFAULT_PROXY_URL` | vazio (conexão direta) |
| `media_allow_private_urls` | `LITEZAP_MEDIA_ALLOW_PRIVATE_URLS` | `false` |
| `instance_defaults` (bloco) | só no arquivo | ritmo 3s + 0–2s, 20/min, digitando ligado, rampa 50→150 em 14 dias, janela 08:00–20:00 no fuso do servidor |

Workers rodam em grupo de processos próprio e encerram sozinhos assim que o supervisor morre, mesmo à força (SIGKILL): cada worker vigia um pipe no stdin mantido aberto pelo supervisor e sai ao receber EOF. A limpeza de órfãos no próximo `serve` (via `data/run/*.pid`) é só um fallback.

## API

Autenticação: `Authorization: Bearer <token>`. O token de instância só acessa rotas da própria instância.

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
| `GET\|PUT /v1/instances/{id}/settings` | instância ou admin (`proxy_url` e `unsafe_pacing` só admin) |
| `POST /v1/instances/{id}/messages[?wait=true&timeout=30s]` | instância ou admin |
| `GET /v1/instances/{id}/messages/{msg_id}` | instância ou admin |
| `GET /v1/instances/{id}/chats[?limit=&cursor=]` | instância ou admin |
| `GET /v1/instances/{id}/chats/{jid}/messages[?limit=&cursor=]` | instância ou admin |
| `POST /v1/instances/{id}/chats/{jid}/read` | instância ou admin |
| `GET /v1/instances/{id}/contacts` · `/contacts/{jid}` | instância ou admin |
| `POST /v1/instances/{id}/check-numbers` `{"phones":[...]}` | instância ou admin |
| `GET /v1/instances/{id}/media/{media_id}[?signed=1]` | instância ou admin (`?signed=1` exige `public_url` se o `listen_addr` não for loopback) |
| `GET /media/{id}/{media_id}?exp=&sig=` (URL assinada, vale 1h) | nenhum |
| `GET /healthz`, `GET /readyz` | público (admin se o `listen_addr` não for loopback) |

As ações respondem com a instância. `start` e `restart` respondem assim que o worker é disparado, então a resposta pode ainda mostrar o estado anterior: consulte `GET /v1/instances/{id}` até ver o novo estado.

### Enviar mensagens

```bash
# texto; 202 {"id","status":"queued"} — ou 200 com wa_message_id se usar ?wait=true
curl -s -X POST "localhost:8787/v1/instances/$ID/messages?wait=true" -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"text","text":"Olá!","client_message_id":"pedido-123"}'

# mídia por URL (o servidor baixa; IPs privados bloqueados) ou base64
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"image","caption":"foto","media":{"url":"https://exemplo.com/a.jpg"}}'

# reação a uma mensagem do log (id do LiteZap ou wa_message_id)
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"reaction","reaction":{"message_id":"msg_...","emoji":"👍"}}'
```

Tipos: `text`, `image`, `video`, `audio` (`media.ptt: true` envia como mensagem de voz), `document`, `reaction`. `to` aceita número com DDI ou JID (`...@s.whatsapp.net`, `...@g.us`, `...@lid`). `?wait=true` espera o resultado por `timeout` (padrão `30s`, no máximo 2 minutos); se o tempo acabar a resposta é `202` com o status atual (`queued`/`in_flight`) e o envio continua na fila. Limites: imagem 16MB, vídeo/áudio 64MB, documento 100MB. `expires_after` (padrão `24h`, máximo `168h`): não enviada até lá, a mensagem falha com `message_expired`.

A fila é persistente e por instância: ordem estrita por destinatário, pausa enquanto a instância não está `connected` (e por 15–30s depois de conectar), janela de envio para quem não mandou nada nas últimas 24h, ritmo e rampa conforme `settings`, até 5 tentativas em falhas temporárias. Antes do primeiro envio a um número, o LiteZap confere se ele tem WhatsApp (e usa o JID que o WhatsApp devolve, com ou sem o 9º dígito); número sem WhatsApp falha com `invalid_number` sem enviar nada. Envios de mídia grande esperam vaga (no máximo 3 ao mesmo tempo) em vez de falhar; só se o cliente desistir da requisição antes de abrir vaga a resposta é `503 rate_limited`. `client_message_id` repetido devolve a mesma mensagem (idempotência); repetido com outro conteúdo responde `409 conflict`. O status (`queued`, `in_flight`, `sent`, `delivered`, `read`, `failed`) fica em `GET /messages/{id}`. No log de mensagens (`GET /chats/{jid}/messages`) o `status` é `received` para mensagens recebidas e `pending` → `sent` → `delivered` → `read` para as enviadas (`pending` = em envio, ainda sem confirmação do WhatsApp). Consultas de número também têm ritmo: cada uma que vai ao WhatsApp conta como um envio para `min_interval_ms`/`max_per_minute`, e por dia a instância faz no máximo o limite diário da rampa (quando ligada); acima disso o item espera a janela do dia seguinte. `check-numbers` tem limite próprio de 200 números não cacheados por dia (UTC), além de 10 por minuto (`429 rate_limited`).

### Settings

```bash
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"min_interval_ms":5000,"ramp":{"enabled":true},"send_window":{"start":"09:00","end":"18:00"},"timezone":"America/Sao_Paulo"}'
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $ADMIN" \
  -d '{"proxy_url":"socks5://user:senha@host:1080"}'
```

Campos ausentes mantêm o valor atual. `GET /settings` esconde a senha do `proxy_url`. Pisos: `min_interval_ms` ≥ 1000 e `max_per_minute` ≤ 60, a menos que o admin ligue `unsafe_pacing`. `send_window` vazio (`{"start":"","end":""}`) libera qualquer horário; fora da janela, mensagens para quem não mandou nada nas últimas 24h esperam o início da próxima (com alguns minutos de variação), enquanto respostas a conversas ativas saem a qualquer hora; itens adiados pela rampa vão para a janela do dia seguinte (nunca à meia-noite). Falhas na verificação do número adiam só aquele item (1, 2, 5, 15 min; na 5ª falha, `failed`). `"proxy_url":""` volta ao `default_proxy_url`. Trocar o proxy reinicia o worker; se o proxy não responde, a instância fica `reconnecting` com `state_reason: proxy_unreachable` (nunca cai para conexão direta, e variáveis `HTTP(S)_PROXY` do ambiente não são herdadas pelos workers).

Erros: `{"error":{"code","message","retryable"}}`. Códigos:

| Código | HTTP | Quando |
|---|---|---|
| `validation_failed` | 400 | corpo ou campo inválido, incluindo `media.url` que não é http(s) ou aponta para endereço privado/local |
| `invalid_number` | 400 | o número não tem WhatsApp |
| `unauthorized` | 401 | token ausente ou inválido |
| `forbidden` | 403 | token sem acesso ao recurso; URL assinada inválida ou expirada |
| `instance_not_found` | 404 | instância inexistente |
| `not_found` | 404 | mensagem, chat, contato ou mídia inexistente |
| `conflict` | 409 | `client_message_id` reusado com outro conteúdo; URLs assinadas desativadas |
| `instance_not_connected` | 409 | worker da instância não está rodando ou número deslogado |
| `instance_banned` | 409 | número banido; apague a instância |
| `instance_not_pairing` | 409 | instância não está aguardando QR/pareamento |
| `media_unavailable` | 410 | mídia recebida expirou no WhatsApp |
| `message_expired` | 410 | mensagem não enviada antes de `expires_after` |
| `media_too_large` | 413 | mídia acima do limite do tipo, ou corpo do envio acima de 140MB |
| `media_fetch_failed` | 422 | `media.url` não pôde ser baixada (`retryable`) |
| `rate_limited` | 429 | muitas consultas de números por minuto em `check-numbers` (cota própria, separada da fila) (`retryable`) |
| `rate_limited` | 503 | muitos envios com corpo/mídia grande ao mesmo tempo e a requisição desistiu de esperar vaga (`retryable`) |
| `worker_error` | 502 | o worker da instância recusou ou falhou a operação (`retryable`) |
| `worker_timeout` | 504 | o worker da instância não respondeu a tempo (`retryable`) |
| `shutting_down` | 503 | servidor encerrando |
| `internal` | 500 | erro interno |

### Verificar números

`POST /check-numbers` aceita de 1 a 10 números por chamada (DDI + DDD + número, 10–15 dígitos) e devolve `exists` e o `jid` que o WhatsApp usa. Resultados ficam em cache (30 dias para quem tem WhatsApp, 24h para quem não tem). Consultas novas têm cota de 10 por minuto por instância, separada da cota de 30 por minuto da fila de envio; acima disso, `429 rate_limited`.

## Como testar com seu WhatsApp

Requer um número de teste no celular e um segundo número (contato) para conversar.

1. Suba o servidor: `go build -o litezap ./cmd/litezap && ./litezap serve`.
2. Em outro terminal, crie a instância com o token admin e guarde `ID` e `TOK` (o token da instância só aparece uma vez):
   ```bash
   ADMIN=$(cat data/admin_token)
   RESP=$(curl -s -X POST localhost:8787/v1/instances -H "Authorization: Bearer $ADMIN" -d '{"name":"teste"}')
   ID=$(echo "$RESP" | jq -r .instance.id)
   TOK=$(echo "$RESP" | jq -r .token)
   ```
3. Baixe o QR e escaneie em WhatsApp > Aparelhos conectados:
   ```bash
   curl -s "localhost:8787/v1/instances/$ID/qr?format=png" -H "Authorization: Bearer $TOK" -o qr.png && open qr.png
   ```
4. Confira o estado: `curl -s localhost:8787/v1/instances/$ID -H "Authorization: Bearer $TOK"` deve mostrar `"state":"connected"` e `phone`.
5. Envie um texto ao contato com `?wait=true&timeout=90s` (seção "Enviar mensagens") e responda pelo celular do contato. Depois de conectar, a fila espera 15–30s antes do primeiro envio; e a primeira mensagem para um contato novo é "fria" (ele não mandou nada nas últimas 24h): só sai dentro da janela de envio, 08:00–20:00 no fuso da instância (`timezone`, padrão o do servidor). Fora desse horário a resposta é `202` com `"status":"queued"` e a mensagem sai na abertura da próxima janela. Para testar a qualquer hora, desligue a janela antes: `curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" -d '{"send_window":{"start":"","end":""}}'` (`start` e `end` vazios = sem janela). A resposta do contato aparece em `GET /v1/instances/$ID/chats` e `.../chats/<jid>/messages`. Os chats são endereçados pelo JID que o WhatsApp devolve: um número brasileiro digitado com ou sem o 9º dígito pode ficar guardado sob o outro formato, então use o `jid` de `GET /chats` (ou de `check-numbers`) nas rotas `/chats/{jid}/...`, não o número digitado.
6. Crash do worker: `kill -9 $(cat data/run/$ID.pid)`; em alguns segundos o worker volta sozinho a `connected`, sem novo QR.
7. Pare o `serve` (Ctrl+C) e suba de novo: a instância reconecta sem QR e mensagens que estavam na fila são enviadas.
8. Logout: `curl -s -X POST localhost:8787/v1/instances/$ID/logout -H "Authorization: Bearer $TOK"` leva o estado a `logged_out` e o aparelho some da lista no celular.

Os estados mudam de forma assíncrona: depois de cada ação, repita o `GET` do passo 4 até ver o estado esperado.

## Limitações conhecidas (marco 2)

- Sem eventos, SSE ou webhooks ainda (marco 3): acompanhe por polling em `GET /chats` e `GET /messages/{id}`.
- Sem busca full-text e sem envio de `sticker`, `location` e `contact` (marco 2b); essas mensagens são recebidas e registradas.
- Mídia recebida baixada fica em `data/media/` sem limpeza automática ainda (o prazo de 7 dias é gravado, a limpeza vem no marco 2b).
- Mídia passa entre supervisor e worker por arquivos em `data/` (mesma máquina). Um spawner em container (nuvem) vai precisar de um volume compartilhado.
- Se o supervisor morrer entre o worker entregar um evento e o supervisor gravá-lo, esse evento se perde (sem confirmação de entrega worker→supervisor ainda).
- Mensagens editadas e apagadas ainda não atualizam o log.
- Suporte a Windows não testado (compila, mas não foi executado).

A lista completa do que foi adiado para o marco 2b (busca, envio de sticker/location/contact, retenção, confirmação de entrega, edições/revogações, nome de grupos, métricas) está na seção "Marco 2b" de `docs/superpowers/plans/2026-10-17-litezap-m2-messaging.md`.

## Desenvolvimento

```bash
go test -race ./...
go tool buf generate   # após mudar proto/
```
