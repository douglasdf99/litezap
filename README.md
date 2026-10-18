# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

Licença: Apache-2.0. Roda no seu PC, no seu servidor ou na nuvem gerenciada do LiteZap.

> Status: marco 4 (envio com fila persistente, recebimento, chats, histórico, mídia, proxy por instância, eventos com webhooks assinados e SSE, servidor MCP em `/mcp` e `litezap mcp`) + app local v1 (painel web embutido e lançador de bandeja) + marco 5 (hook de agente para atendente de IA, com o exemplo `examples/agent-claude`).
> Design: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md`

## Rodar

```bash
go build -o litezap ./cmd/litezap
./litezap serve            # usa litezap.yaml se existir; veja litezap.example.yaml
```

O token admin fica em `data/admin_token` (ou defina `LITEZAP_ADMIN_TOKEN`). O segredo das URLs assinadas de mídia fica em `data/media_key` (apagá-lo invalida as URLs já emitidas).

O painel web abre em `http://127.0.0.1:8787/` (redireciona para `/app/`). Veja [Painel web e app de bandeja](#painel-web-e-app-de-bandeja).

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
| `webhook_allow_private_urls` | `LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS` | `false` (`true` libera webhooks para `127.0.0.1`, rede local etc.) |
| `webhook_retention_days` | `LITEZAP_WEBHOOK_RETENTION_DAYS` | `7` (entregas concluídas) |
| `webhook_failed_retention_days` | `LITEZAP_WEBHOOK_FAILED_RETENTION_DAYS` | `30` (dead-letter) |
| `disable_dashboard` | `LITEZAP_DISABLE_DASHBOARD` | `false` (`true` desliga o painel em `/app/`; a API continua) |
| `instance_defaults` (bloco) | `LITEZAP_INSTANCE_DEFAULTS_JSON` | ritmo 3s + 0–2s, 20/min, digitando ligado, rampa 50→150 em 14 dias, janela 08:00–20:00 no fuso do servidor |
| `message_retention_days` | `LITEZAP_MESSAGE_RETENTION_DAYS` | `0` (guarda o log para sempre) |
| `media_retention_days` | `LITEZAP_MEDIA_RETENTION_DAYS` | `0` (cópias locais de mídia nunca são apagadas) |
| `zombie_timeout` | `LITEZAP_ZOMBIE_TIMEOUT` | `0` (watchdog de conexão desligado; ex.: `5m`) |
| `shutdown_timeout` | `LITEZAP_SHUTDOWN_TIMEOUT` | `0` (fases padrão, até ~40s; ex.: `25s` para caber num prazo de parada de 30s, ex.: `TimeoutStopSec=30` do systemd ou `docker stop -t 30`) |
| `ops_listen_addr` | `LITEZAP_OPS_LISTEN_ADDR` | vazio (veja [Hospedagem gerenciada](#hospedagem-gerenciada-operador)) |
| `operator_token`, `locked_settings`, `max_instances`, `require_proxy` | `LITEZAP_OPERATOR_TOKEN`, `LITEZAP_LOCKED_SETTINGS`, `LITEZAP_MAX_INSTANCES`, `LITEZAP_REQUIRE_PROXY` | desligados (veja [Hospedagem gerenciada](#hospedagem-gerenciada-operador)) |

Plataformas configuradas só por ambiente podem passar o documento inteiro em `LITEZAP_CONFIG_JSON` (as mesmas chaves do YAML, em JSON); ele é aplicado depois do arquivo, e as variáveis individuais acima ainda vencem. `LITEZAP_LOG_FORMAT=json` troca os logs (supervisor e workers) para JSON por linha.

Com `message_retention_days`, uma limpeza de hora em hora apaga mensagens mais velhas que o prazo, a mídia delas e os envios já concluídos. Com `media_retention_days`, a cópia local de um anexo é apagada N dias depois de baixada; o próximo `GET .../media/{id}` baixa de novo enquanto o WhatsApp ainda tiver o arquivo (depois disso, `410 media_unavailable`).

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
| `GET\|PUT /v1/instances/{id}/settings` | instância ou admin (`proxy_url` e `unsafe_pacing` só admin; só operador quando travados em `locked_settings`) |
| `POST /v1/instances/{id}/messages[?wait=true&timeout=30s]` | instância ou admin |
| `GET /v1/instances/{id}/messages/{msg_id}` | instância ou admin |
| `GET /v1/instances/{id}/queue[?status=queued\|in_flight\|sent\|failed\|all&limit=&cursor=]` | instância ou admin |
| `DELETE /v1/instances/{id}/queue/{msg_id}` (cancela um item `queued`) · `DELETE /v1/instances/{id}/queue` (esvazia) | instância ou admin |
| `GET /v1/instances/{id}/chats[?limit=&cursor=]` | instância ou admin |
| `GET /v1/instances/{id}/chats/{jid}/messages[?limit=&cursor=]` | instância ou admin |
| `POST /v1/instances/{id}/chats/{jid}/read` | instância ou admin |
| `GET /v1/instances/{id}/contacts` · `/contacts/{jid}` | instância ou admin |
| `POST /v1/instances/{id}/check-numbers` `{"phones":[...]}` | instância ou admin |
| `GET /v1/instances/{id}/media/{media_id}[?signed=1]` | instância ou admin (`?signed=1` exige `public_url` se o `listen_addr` não for loopback) |
| `GET /media/{id}/{media_id}?exp=&sig=` (URL assinada, vale 1h) | nenhum |
| `GET /v1/instances/{id}/events[?types=]` (SSE) | instância ou admin |
| `GET /v1/events[?types=]` (SSE, todas as instâncias) | admin |
| `POST /v1/webhooks` · `GET /v1/webhooks[?instance_id=]` | admin (global ou de uma instância) ou instância (só a própria) |
| `GET\|PATCH\|DELETE /v1/webhooks/{id}` | idem (webhook de outra instância responde 404) |
| `GET /v1/webhooks/{id}/deliveries[?status=pending\|delivered\|failed&limit=&cursor=]` | idem |
| `POST /v1/webhooks/{id}/deliveries/{delivery_id}/retry` | idem |
| `GET /v1/me` → `{admin, role, instance_id}` (`role`: `operator`, `admin` ou `instance`) | qualquer token |
| `POST /v1/export` `{"include_media"?, "upload_url"?}` → `tar.gz` | admin (`upload_url` só operador) |
| `GET /v1/usage[?from=&to=&instance_id=]` | operador |
| `GET\|PUT /v1/operator/limits` · `GET\|PUT /v1/operator/sending` · `PUT /v1/operator/instances/{id}/sending` · `POST /v1/operator/dashboard-login` | operador |
| `POST /mcp` (MCP streamable HTTP, seção "MCP") | instância ou admin |
| `GET /healthz`, `GET /readyz` | público (admin se o `listen_addr` não for loopback); sem token no `ops_listen_addr` |
| `GET /metrics` | admin (sempre); sem token no `ops_listen_addr` |
| `GET /app/` (painel) · `GET /app/api/info` | público (arquivos estáticos; o painel pede o token admin) |
| `POST /app/api/session` `{"code"}` → `{token}` | código de uso único do app de bandeja (só em loopback) ou emitido pelo operador |

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

# resposta citando uma mensagem, com menção e prévia do link
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"1203630000@g.us","type":"text","text":"@5511988887777 veja https://exemplo.com","reply_to":"msg_...","mentions":["5511988887777"],"link_preview":"auto"}'

# localização, contatos e enquete
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"location","location":{"latitude":-23.5614,"longitude":-46.6559,"name":"MASP"}}'
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"contact","contacts":[{"name":"Ana","phone":"5511977776666"}]}'
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"1203630000@g.us","type":"poll","poll":{"name":"Almoço?","options":["12h","13h"],"max_selections":1}}'

# editar (até 15 min), apagar para todos (até 48h), fixar e encaminhar
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"type":"edit","message_id":"msg_...","text":"texto corrigido"}'
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"type":"revoke","message_id":"msg_..."}'
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"type":"pin","message_id":"msg_...","pin":{"duration":"7d"}}'
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511966665555","type":"forward","forward":{"message_id":"msg_..."}}'
```

Tipos: `text`, `image`, `video`, `gif` (MP4 tocado em loop), `ptv` (vídeo redondo, sem legenda), `audio` (`media.ptt: true` envia como mensagem de voz), `document`, `sticker` (WebP até 1MB; animado detectado sozinho), `location`, `contact` (1 a 10 cartões: `name` + `phone`, ou `vcard` pronto), `poll` (2 a 12 opções distintas; `max_selections` padrão 1, `0` = qualquer número), `reaction`, `edit`, `revoke`, `pin`, `forward`.

Campos que valem para mensagens novas (não para `reaction`, `edit`, `revoke`, `pin`):

- `reply_to`: id do LiteZap ou `wa_message_id` de uma mensagem do chat; fora do log responde `validation_failed`.
- `mentions`: números ou JIDs (escreva `@<número>` no texto onde a menção aparece); em grupo, `["all"]` menciona todos (grupos de até 256 participantes).
- `link_preview` (só `text`): `"off"` (padrão), `"auto"` (lê o primeiro link do texto: título, descrição e miniatura de `og:*`) ou um objeto `{"url","title","description","image_url"}` com o cartão pronto. A página é buscada no enfileiramento com o mesmo filtro de endereço da mídia (IPs privados e locais sempre recusados, mesmo com `media_allow_private_urls`, e sem proxy do ambiente), só `text/html`, até 512KB, e todo o trabalho tem limite de 5s: se falhar ou demorar, a mensagem sai sem prévia. No objeto, `url` precisa aparecer no texto.
- `delay_ms` e `typing_ms` (0 a 15000): espera extra antes do envio e "digitando..." por esse tempo (mesmo com `typing_simulation` desligado). Só somam tempo depois da verificação de ritmo: `min_interval_ms`, `max_per_minute`, rampa e janela continuam valendo.

`edit`, `revoke` e `pin` usam `message_id` (id do LiteZap ou `wa_message_id`); com id do LiteZap, `to` é opcional. Editar: só textos enviados por este número, até 15 minutos depois (`409 edit_window_expired`). Apagar para todos: as próprias mensagens até 48h (`409 revoke_window_expired`); de outra pessoa, só em grupo onde o número é admin (o worker confere; senão `403 forbidden`). Fixar: `pin.duration` `24h`, `7d` (padrão) ou `30d`; `pin.unpin: true` desafixa. As janelas são conferidas no envio e de novo quando o item sai da fila. Editar e apagar atualizam o log (`edited_at`, `revoked_at` e texto) e emitem `message.edited`/`message.revoked`; esses itens passam pela fila como qualquer envio, mas não viram mensagem no log. Encaminhar (`forward.message_id`, ou `wa_message_id` com `forward.chat`): textos, localizações, contatos e mídia **recebida** (reenviada a partir dos servidores do WhatsApp, sem novo upload); mídia enviada pela API não pode ser encaminhada porque o LiteZap não guarda o arquivo, e mídia de visualização única nunca é encaminhada. Em grupos que endereçam membros por LID, o `@<número>` de uma menção é trocado no texto pelo `@<lid>` que o WhatsApp espera.

`to` aceita número com DDI ou JID (`...@s.whatsapp.net`, `...@g.us`, `...@lid`). `?wait=true` espera o resultado por `timeout` (padrão `30s`, no máximo 2 minutos); se o tempo acabar a resposta é `202` com o status atual (`queued`/`in_flight`) e o envio continua na fila. Limites: imagem 16MB, vídeo/áudio 64MB, documento 100MB. `expires_after` (padrão `24h`, máximo `168h`): não enviada até lá, a mensagem falha com `message_expired`.

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
| `conflict` | 409 | `client_message_id` reusado com outro conteúdo; URLs assinadas desativadas; cancelar item da fila que já está em envio ou terminou |
| `instance_not_connected` | 409 | worker da instância não está rodando ou número deslogado |
| `instance_banned` | 409 | número banido; apague a instância |
| `instance_not_pairing` | 409 | instância não está aguardando QR/pareamento |
| `media_unavailable` | 410 | mídia recebida expirou no WhatsApp |
| `message_expired` | 410 | mensagem não enviada antes de `expires_after` |
| `message_canceled` | 409 | mensagem removida da fila antes do envio (`?wait=true` ou reenvio com o mesmo `client_message_id`) |
| `edit_window_expired` · `revoke_window_expired` | 409 | edição depois de 15 minutos · apagar para todos depois de 48h |
| `media_too_large` | 413 | mídia acima do limite do tipo, ou corpo do envio acima de 140MB |
| `media_fetch_failed` | 422 | `media.url` não pôde ser baixada (`retryable`) |
| `rate_limited` | 429 | muitas consultas de números por minuto em `check-numbers` (cota própria, separada da fila) (`retryable`) |
| `rate_limited` | 503 | muitos envios com corpo/mídia grande ao mesmo tempo e a requisição desistiu de esperar vaga (`retryable`) |
| `worker_error` | 502 | o worker da instância recusou ou falhou a operação (`retryable`) |
| `worker_timeout` | 504 | o worker da instância não respondeu a tempo (`retryable`) |
| `shutting_down` | 503 | servidor encerrando |
| `internal` | 500 | erro interno |

### Fila de envio

`GET /queue` lista os itens ainda não enviados (`queued` e `in_flight`), do mais antigo ao mais novo; `?status=` filtra (`queued`, `in_flight`, `sent`, `failed`, `all`) e `next_cursor` pagina (`limit` padrão 50, máx. 200). `DELETE /queue/{msg_id}` remove um item `queued` sem enviar: ele termina como `failed` com `error: "canceled: ..."`, o arquivo de mídia preparado é apagado e sai `message.status` `failed`; um item `in_flight` (sendo enviado agora) ou já terminado responde `409 conflict`. `DELETE /queue` cancela todos os `queued` de uma vez (os `in_flight` terminam normalmente) e devolve `{"canceled": n}`. Útil antes de reconectar uma instância que ficou parada com mensagens velhas na fila.

### Verificar números

`POST /check-numbers` aceita de 1 a 10 números por chamada (DDI + DDD + número, 10–15 dígitos) e devolve `exists` e o `jid` que o WhatsApp usa. Resultados ficam em cache (30 dias para quem tem WhatsApp, 24h para quem não tem). Consultas novas têm cota de 10 por minuto por instância, separada da cota de 30 por minuto da fila de envio; acima disso, `429 rate_limited`.

## Eventos, webhooks e SSE

Tudo o que acontece numa instância vira um evento com o mesmo envelope, entregue por webhook (persistente, assinado, com retry) e por SSE (ao vivo, sem persistência).

### Envelope (v1)

```json
{
  "id": "evt_5f2c9a1b7d3e4f60",
  "type": "message.received",
  "version": 1,
  "instance_id": "inst_123",
  "timestamp": "2026-10-17T13:45:00.123Z",
  "data": { }
}
```

Adicionar campos em `data` não muda a versão; remover ou mudar o sentido de um campo criaria `version: 2`.

### Catálogo

| type | quando | `data` |
|---|---|---|
| `instance.status` | toda transição de estado | `{state, reason?}` |
| `instance.qr` | novo QR de pareamento | `{code, timeout_ms}` |
| `instance.pair_code` | código gerado por `POST /pair-phone` | `{code}` |
| `message.received` | mensagem nova de outra pessoa | mensagem (abaixo), `source: "contact"` |
| `message.sent` | envio pela API concluído (`source: "api"`) ou mensagem enviada pelo celular (`source: "device"`) | mensagem |
| `message.status` | `sent`, `delivered`, `read` ou `failed` de uma mensagem nossa | `{id, wa_message_id?, chat, status, error?}` |
| `message.reaction` | reação recebida | mensagem com `type: "reaction"`, `text` = emoji (vazio = reação removida), `reaction_to` |
| `message.edited` | mensagem editada (pela API, pelo celular ou pelo contato; edições recebidas só valem do próprio autor, as demais são ignoradas) | `{id?, wa_message_id, chat, sender?, from_me, source, text, timestamp}` (`text` = texto novo; `id` vazio se a mensagem não está no log) |
| `message.revoked` | mensagem apagada para todos (pelo autor, ou por admin do grupo) | `{id?, wa_message_id, chat, sender?, from_me, source, timestamp}` |
| `message.poll_vote` | voto em enquete (o voto atual da pessoa; lista vazia = voto retirado) | `{poll_id?, poll_wa_message_id, chat, voter, push_name?, from_me, selected_options, unknown_options?, timestamp}` |
| `history.synced` | fim da ingestão de um history sync (um evento por sync, 15s depois do último bloco) | `{chats, messages, new_messages}` |
| `agent.failed` | uma chamada do hook de agente falhou (nada foi enviado) ou o limite de respostas pausou o chat | `{chat, message_id, wa_message_id, reason, error?, http_status?}`; `reason`: `timeout`, `http_error`, `network_error`, `invalid_response`, `send_failed`, `loop_guard`, `internal` |
| `agent.handoff` | o agente passou o chat para um humano (ação `handoff`) | `{chat, message_id, wa_message_id, note?}` |
| `instance.proxy_failed` | a instância não alcança o WhatsApp pelo proxy (entra em `reconnecting`/`proxy_unreachable`), repetido a cada 5 min enquanto durar; a volta é o próximo `instance.status` | `{reason, since, duration_seconds, outages_last_hour, repeat}` |

Mensagem: `{id, wa_message_id, chat, sender, from_me, is_group, push_name, source, type, text, media?, reaction_to?, timestamp}` mais o conteúdo estruturado quando houver: `reply_to` (wa id citado), `mentions`, `forwarded`, `location {latitude, longitude, name, address, url}`, `contacts [{display_name, vcard}]`, `poll {name, options, selectable}` e `link_preview {url, title, description}`; os mesmos campos de `GET /messages/{id}`. Tipos recebidos: os de envio, mais `unknown`. Votos chegam cifrados e só são decifrados para enquetes cujo segredo a sessão conhece (criadas ou recebidas depois do pareamento); as opções são casadas pelo SHA-256 contra as opções guardadas no log (`unknown_options` conta as que não casaram). `media` traz `{id, kind, mime, size, file_name, seconds, ptt, url}`; `url` é a rota autenticada de download (`GET /v1/instances/{id}/media/{media_id}`). Mensagens de history sync não geram eventos individuais (só o `history.synced`); uma mensagem que chega duas vezes (reconexão do worker) gera um único evento.

Ainda não emitidos (o worker não reporta esses fatos): `chat.presence`, `group.updated`.

### SSE

```bash
curl -N "localhost:8787/v1/instances/$ID/events?types=message.*,instance.status" -H "Authorization: Bearer $TOK"
curl -N "localhost:8787/v1/events" -H "Authorization: Bearer $ADMIN"   # todas as instâncias
```

Cada evento sai como `id: <evt>`, `event: <type>` e `data: <envelope>`; a cada 15s vai um comentário `: ping` para manter a conexão. `types` aceita tipos exatos, famílias (`message.*`) e `*`. Não há replay: eventos de quando o cliente estava desconectado não são reenviados (use webhooks para isso). Um cliente que fica mais de 256 eventos atrasado recebe `event: error` (`slow_consumer`) e é desconectado; reconecte.

### Webhooks

```bash
curl -s -X POST localhost:8787/v1/webhooks -H "Authorization: Bearer $TOK" \
  -d '{"url":"https://exemplo.com/litezap","events":["message.*","instance.status"]}'
# 201 {"id":"wh_...","instance_id":"inst_...","url":...,"events":[...],"version":1,"enabled":true,"secret":"whsec_..."}
```

- Com token de instância o webhook é sempre daquela instância. Com token admin, `instance_id` escolhe a instância; sem ele o webhook é global (todas as instâncias).
- `secret` é opcional (16 a 256 caracteres); sem ele um é gerado. **Ele só aparece na resposta da criação** (e de um `PATCH` que o troque; `{"secret":""}` gera um novo). Guarde-o.
- `PATCH` aceita `url`, `events`, `enabled` e `secret`; `instance_id` não muda.
- Só URLs `http(s)`. Por padrão endereços privados, loopback e link-local são bloqueados (checados depois do DNS, a cada conexão); para um receptor no próprio PC use `webhook_allow_private_urls: true`. Redirecionamentos não são seguidos (um `3xx` conta como falha).

Cada entrega é um `POST` com o envelope como corpo e os headers:

| Header | Valor |
|---|---|
| `X-LiteZap-Signature` | `t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<corpo>")>` |
| `X-LiteZap-Event-Id` | id do evento (`evt_...`), para deduplicar |
| `X-LiteZap-Event-Type` | tipo do evento |

**Retry**: timeout de 10s por tentativa; qualquer `2xx` é sucesso. Falhas são repetidas com backoff exponencial (1s, 2s, 4s... até 1h entre tentativas) por até 24h; depois a entrega vai para a dead-letter (`status: failed`). Um endpoint que falha também segura as outras entregas dele pelo mesmo backoff (não martelamos um servidor fora do ar); outros endpoints seguem normalmente. No máximo 8 entregas simultâneas no total e 2 por webhook.

**Garantia**: pelo menos uma vez. As entregas ficam numa tabela (outbox) e sobrevivem a restarts; uma tentativa interrompida por um crash é repetida, então o mesmo evento pode chegar duas vezes: deduplique por `X-LiteZap-Event-Id`. A ordem entre eventos não é garantida (retries reordenam); use `timestamp`.

**Dead-letter**: `GET /v1/webhooks/{id}/deliveries?status=failed` lista as entregas que desistimos de fazer, com `attempts`, `last_status_code`, `last_error` e o `event` completo; `POST /v1/webhooks/{id}/deliveries/{delivery_id}/retry` recoloca uma na fila com mais 24h de prazo. Entregas concluídas ficam 7 dias e a dead-letter 30 dias (configurável).

#### Verificar a assinatura

Rejeite assinaturas inválidas e `t` com mais de 5 minutos de diferença (anti-replay). Use o corpo **bruto**, antes de qualquer parse de JSON.

Go:

```go
func verify(secret, header string, body []byte) bool {
	var ts string
	var sigs []string
	for _, p := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(p, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-t)) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	want := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return true
		}
	}
	return false
}
```

(Dentro deste repositório, `webhook.Verify` em `internal/webhook` faz o mesmo.)

Node:

```js
const crypto = require("node:crypto");

function verify(secret, header, rawBody) {
  const parts = Object.fromEntries(header.split(",").map((p) => p.split("=", 2)));
  const t = Number(parts.t);
  if (!parts.v1 || !Number.isFinite(t) || Math.abs(Date.now() / 1000 - t) > 300) return false;
  const want = crypto.createHmac("sha256", secret).update(`${parts.t}.${rawBody}`).digest("hex");
  const a = Buffer.from(parts.v1, "hex"), b = Buffer.from(want, "hex");
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
```

## Atendente IA (hook de agente)

Um atendente de IA externo (o seu servidor, ou o exemplo [`examples/agent-claude`](examples/agent-claude)) recebe cada mensagem elegível como um `POST` assinado e responde com o texto e ações. A resposta **passa pela fila normal** (ritmo, janela, rampa, suspensão do operador): o hook nunca envia direto.

```bash
# configurar (cria; o segredo é gerado e aparece só nesta resposta)
curl -s -X PUT $LZ/v1/instances/$ID/agent-hook -H "Authorization: Bearer $TOK" \
  -d '{"url":"https://meu-agente.exemplo.com/litezap","enabled":true}'
curl -s $LZ/v1/instances/$ID/agent-hook -H "Authorization: Bearer $TOK"          # ler (sem o segredo)
curl -s -X DELETE $LZ/v1/instances/$ID/agent-hook -H "Authorization: Bearer $TOK" # remover
# por conversa: true | false | null (null = padrão da instância; true/null também encerram a pausa)
curl -s -X PUT $LZ/v1/instances/$ID/chats/5511999999999/agent -H "Authorization: Bearer $TOK" -d '{"enabled":false}'
curl -s $LZ/v1/instances/$ID/agent-hook/chats -H "Authorization: Bearer $TOK"   # chats com regra própria ou pausados
curl -s $LZ/v1/instances/$ID/agent-hook/calls -H "Authorization: Bearer $TOK"   # últimas chamadas (status, motivo, duração)
```

| Campo | Padrão | |
|---|---|---|
| `url` | — | `http(s)`; endereços privados/loopback só com `webhook_allow_private_urls` |
| `secret` | gerado | 16–256 caracteres; assina como os webhooks (`X-LiteZap-Signature`) |
| `enabled` | `false` | padrão para conversas sem regra própria |
| `timeout` | `30s` | 1s–2m |
| `context_messages` | `20` | 0–100 mensagens do chat, mais antiga primeiro |
| `groups_enabled` | `false` | também responder em grupos |
| `human_pause` | `30m` | um humano respondeu pelo celular: o hook pausa no chat e cancela respostas dele ainda na fila (`0` = não pausa) |
| `pause_on_api_send` | `false` | também pausa quando alguém envia pela API (CRM, painel, MCP); as respostas do próprio hook não contam |
| `ignore` | `[]` | números/JIDs que nunca são atendidos (chat ou remetente) |
| `max_replies_per_window` / `reply_window` | `10` / `10m` | proteção contra loop entre robôs: estourou, o chat pausa por `human_pause` e sai `agent.failed` (`loop_guard`); estourou de novo em 24h, o hook é desligado no chat (`agent.handoff`) |

**Request** (`POST {url}`, `Content-Type: application/json`, headers `X-LiteZap-Signature`, `X-LiteZap-Event-Id` = id do `message.received`, `X-LiteZap-Event-Type: agent.request`):

```json
{"instance_id":"inst_...","chat":{"jid":"5511999999999@s.whatsapp.net","name":"Ana","is_group":false},
 "message":{ "...": "o data do message.received" },
 "context":[{"id":"msg_...","wa_message_id":"...","sender":"...","from_me":false,"source":"contact","type":"text","text":"oi","timestamp":"..."}]}
```

`context` termina com a própria mensagem; `from_me` com `source: api` são respostas do robô, `source: device` são de um humano no celular.

**Response** `200` `{"reply": "texto" | {MessagePayload sem "to"}, "actions": [...]}` ou `204` (nada a fazer). `reply` objeto aceita os tipos de conteúdo (`text`, `image`, `video`, `audio`, `document`, `sticker`, `gif`, `ptv`, `location`, `contact`, `poll`); sempre vai para o chat que disparou a chamada, uma vez por mensagem (`client_message_id = agent:<id>`), e expira em 1h se não sair. Ações: `{"type":"mark_read"}`, `{"type":"react","emoji":"👍"}`, `{"type":"pause","minutes":60}` (1–10080), `{"type":"handoff","note":"..."}` (desliga o hook no chat até reativação e emite `agent.handoff`). A resposta é enfileirada primeiro; só se ela for aceita saem `mark_read` e `react`. `pause` e `handoff` valem sempre, mesmo se a resposta for recusada. Ação desconhecida é ignorada; resposta inválida, erro HTTP, redirecionamento ou timeout → `agent.failed`, nada é enviado, sem retry.

Regras:

- Só `message.received` (nunca `status@broadcast`, canais, mensagens próprias); grupos só com `groups_enabled`.
- **Uma chamada por vez por chat**; mensagens que chegam durante a chamada são agrupadas: a próxima chamada leva a mais nova (as outras estão no `context`). No máximo 8 chamadas ao mesmo tempo no servidor.
- **Pausa por humano**: mensagem `from_me` cujo `wa_message_id` não foi enviado pelo LiteZap (você respondeu pelo celular) pausa o hook no chat por `human_pause`. Se isso acontece durante uma chamada, a resposta do agente é descartada.
- Com o envio suspenso pelo operador, o hook nem é chamado (aparece como `skipped` em `calls`). Mensagens antigas (mais de `max(2×timeout, 5min)`, como o acúmulo entregue depois de ficar offline) também são puladas (`stale`), assim como tipos `unknown`.
- Respostas do hook ainda na fila são canceladas quando um humano assume o chat, quando o limite de respostas estoura e não saem depois de 1h. `mentions: ["all"]` não é aceito numa resposta do agente.

**Segurança e risco de banimento**: trate o texto das mensagens como **não confiável** no seu agente (prompt injection: o cliente pode escrever "ignore as instruções e..."); nunca dê ao modelo segredos ou ferramentas que ele não deva usar a pedido de um cliente. Respostas automáticas só vão para quem escreveu primeiro e respeitam o ritmo da fila, mas um robô respondendo outro robô gera volume anormal: mantenha o `max_replies_per_window` baixo.

## MCP (Claude, ChatGPT e outros clientes de IA)

O LiteZap expõe as mesmas operações como ferramentas do [Model Context Protocol](https://modelcontextprotocol.io), para um assistente de IA ler conversas e enviar mensagens pelo seu número. Dois jeitos de conectar, com as mesmas ferramentas:

- **HTTP** (streamable HTTP) em `http://<listen_addr>/mcp`, no próprio `litezap serve`, com o mesmo `Authorization: Bearer <token>` da API.
- **stdio**: `litezap mcp`, uma ponte para clientes que iniciam um processo local (Claude Desktop, Claude Code). Ela não sobe supervisor: conversa via REST com um `litezap serve` já rodando, configurada por `LITEZAP_URL` (padrão `http://127.0.0.1:8787`) e `LITEZAP_TOKEN` (obrigatório).

### Ferramentas

| Ferramenta | O que faz |
|---|---|
| `list_instances` | instâncias visíveis para o token e seus estados (com token de instância, só a própria) |
| `get_instance_status` | estado, número conectado e dica do próximo passo |
| `get_qr` | QR de pareamento como imagem PNG (estado `qr_pending`) |
| `send_message` | envia texto, com `reply_to` e `mentions` opcionais; espera o resultado por padrão (`timeout_seconds` 30, máx. 55: muitos clientes MCP desistem de uma chamada em 60s) |
| `send_media` | envia imagem, vídeo, áudio (ou nota de voz), documento, sticker, gif ou vídeo redondo a partir de uma URL pública |
| `send_reaction` | reage com um emoji a uma mensagem (vazio remove) |
| `send_location` · `send_contact` | localização; 1 a 10 cartões de contato (nome + telefone) |
| `edit_message` · `delete_message` | edita um texto enviado (até 15 min) · apaga para todos (até 48h; marcadas como destrutivas, com aviso para só agir a pedido do usuário) |
| `get_message_status` | `queued`, `in_flight`, `sent`, `delivered`, `read` ou `failed` |
| `list_chats` | chats do log, mais recentes primeiro, com não lidas (paginado) |
| `get_messages` | mensagens de um chat, mais novas primeiro (paginado por `cursor`) |
| `mark_read` | marca o chat como lido (o contato vê os tiques azuis) |
| `list_contacts` / `get_contact` | contatos da sessão (filtro `query`, `limit`) |
| `check_numbers` | até 10 números por chamada; respeita o cache e a cota de consultas da instância |
| `wait_for_events` | espera até 55s por eventos novos da instância (ex.: `message.received`), sem replay |

`instance_id` é opcional com token de instância (é sempre a dela) e com token admin quando só existe uma instância. A busca de mensagens fica para o marco 2b.

### Claude Desktop

Em `claude_desktop_config.json` (Settings > Developer > Edit Config), com o caminho absoluto do binário:

```json
{
  "mcpServers": {
    "litezap": {
      "command": "/caminho/absoluto/litezap",
      "args": ["mcp"],
      "env": {
        "LITEZAP_URL": "http://127.0.0.1:8787",
        "LITEZAP_TOKEN": "lzi_..."
      }
    }
  }
}
```

Reinicie o Claude Desktop com o `litezap serve` rodando.

### Claude Code

```bash
# stdio (ponte local)
claude mcp add litezap -e LITEZAP_URL=http://127.0.0.1:8787 -e LITEZAP_TOKEN=lzi_... -- /caminho/absoluto/litezap mcp
# ou HTTP direto no serve
claude mcp add --transport http litezap http://127.0.0.1:8787/mcp --header "Authorization: Bearer lzi_..."
```

### Clientes HTTP (OpenAI Agents SDK, Responses API e outros)

Qualquer cliente MCP que aceite URL + cabeçalho funciona: URL `https://<seu-host>/mcp`, cabeçalho `Authorization: Bearer <token>`. O endpoint é *stateless* (não usa `Mcp-Session-Id`; cada requisição é autenticada de novo). Exemplo na Responses API da OpenAI:

```json
{
  "type": "mcp",
  "server_label": "litezap",
  "server_url": "https://zap.exemplo.com/mcp",
  "headers": { "Authorization": "Bearer lzi_..." },
  "require_approval": "always"
}
```

Serviços na nuvem (ChatGPT, OpenAI) precisam alcançar o servidor: publique-o atrás de um proxy reverso com TLS e defina `public_url`. Pedidos que chegam num `listen_addr` de loopback com um `Host` que não é loopback são recusados (proteção contra DNS rebinding); o host de `public_url` é a exceção, para o proxy na mesma máquina.

### Segurança

- **Use token de instância** (`lzi_...`) para dar ao assistente só um número: ele não lista nem opera outras instâncias, não cria, apaga nem troca token. O token admin dá acesso a todas.
- **O assistente não fura o anti-ban**: todo envio passa pela mesma fila da API (intervalo mínimo, rampa diária de números novos, janela de envio, espera após conectar). Não há ferramenta para mudar settings, proxy ou `unsafe_pacing`. Uma mensagem que fica na fila volta com `sent: false` e uma nota dizendo que será enviada sozinha e não deve ser reenviada. Cada envio usa um `client_message_id` (o do modelo ou um gerado, `mcp_…`); num erro ambíguo (timeout, conexão perdida) a mensagem de erro traz esse id e pede para conferir `get_message_status` antes de reenviar.
- **`check_numbers`** gasta a cota diária de consultas da instância e responde `rate_limited` quando ela acaba.
- **Texto de terceiros**: mensagens, nomes e eventos vêm de outras pessoas. As instruções do servidor e as descrições das ferramentas pedem ao modelo para tratá-los como dados, mas prompt injection não é um problema resolvido: prefira clientes que pedem aprovação antes de `send_message`/`send_media` (por exemplo `require_approval`).
- Erros chegam ao modelo como erro de ferramenta, com código, dica e se dá para tentar de novo (ex.: `instance_not_connected` sugere `get_instance_status`/`get_qr`). Erros internos aparecem só como `internal error` (o detalhe vai para o log do servidor).

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

Os estados mudam de forma assíncrona: depois de cada ação, repita o `GET` do passo 4 até ver o estado esperado (ou acompanhe `instance.status` pelo SSE).

### Testar eventos e webhooks

Com a instância conectada (passos acima):

1. SSE: em outro terminal, `curl -N "localhost:8787/v1/instances/$ID/events" -H "Authorization: Bearer $TOK"`. Mande uma mensagem do celular do contato: aparece `event: message.received`. Envie pela API: aparecem `message.sent` e `message.status` (`sent`, depois `delivered` e `read` quando o contato recebe e abre). Reaja a uma mensagem no celular do contato: `message.reaction`. Responda pelo celular do número conectado: `message.sent` com `"source":"device"`.
2. Receptor local: salve como `receiver.js` e rode `LITEZAP_SECRET=... node receiver.js` (o segredo vem do passo 3; rode de novo depois de criar o webhook):
   ```js
   const http = require("node:http"), crypto = require("node:crypto");
   http.createServer((req, res) => {
     let body = "";
     req.setEncoding("utf8"); // não quebra caracteres multibyte divididos entre pedaços
     req.on("data", (c) => (body += c)).on("end", () => {
       const sig = Object.fromEntries((req.headers["x-litezap-signature"] || "").split(",").map((p) => p.split("=", 2)));
       const want = crypto.createHmac("sha256", process.env.LITEZAP_SECRET || "").update(`${sig.t}.${body}`).digest("hex");
       console.log(req.headers["x-litezap-event-type"], sig.v1 === want ? "assinatura ok" : "ASSINATURA INVÁLIDA", body);
       res.writeHead(204).end();
     });
   }).listen(9000);
   ```
3. Libere destinos locais (`webhook_allow_private_urls: true` no `litezap.yaml`, ou `LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS=true`), reinicie o `serve` e cadastre o webhook guardando o segredo:
   ```bash
   SECRET=$(curl -s -X POST localhost:8787/v1/webhooks -H "Authorization: Bearer $TOK" \
     -d '{"url":"http://127.0.0.1:9000/","events":["*"]}' | jq -r .secret)
   echo "$SECRET"
   ```
   Reinicie o receptor com `LITEZAP_SECRET=$SECRET` e troque mensagens: cada evento aparece com "assinatura ok".
4. Retry e dead-letter: pare o receptor, mande uma mensagem e liste `GET /v1/webhooks/<wh_id>/deliveries?status=pending` (`attempts` subindo, `last_error` com o motivo). Suba o receptor de novo: a entrega chega no próximo retry (o intervalo dobra a cada falha, até 1h). Uma entrega que fica 24h sem sucesso aparece em `?status=failed` e pode ser reenviada com `POST .../deliveries/<delivery_id>/retry`.
5. Restart: com o receptor parado, mande mensagens, pare e suba o `serve` e depois o receptor: as entregas pendentes são feitas depois do restart.

### Testar o atendente IA

1. Libere destinos locais (`webhook_allow_private_urls: true`) e reinicie o `serve`.
2. No painel, aba **Atendente IA** da instância: URL `http://127.0.0.1:8790/`, marque "Responder automaticamente" e salve; copie o segredo que aparece.
3. Rode o exemplo: `cd examples/agent-claude && ANTHROPIC_API_KEY=... LITEZAP_AGENT_SECRET=<segredo> go run .`
4. De outro celular, mande "oi" para o número pareado: a resposta sai pela fila (com "digitando…") e aparece em "Chamadas recentes".
5. Responda você mesmo pelo celular pareado: o chat aparece como pausado por 30 min em "Por conversa"; mensagens novas do contato não chamam o agente até lá ("Reativar" encerra a pausa).
6. Peça "quero falar com um atendente": o agente responde e faz `handoff` (chat desligado, evento `agent.handoff` em "Eventos ao vivo").

### Testar o MCP com um cliente de IA

1. Com o `serve` rodando e uma instância criada (`ID`, `TOK` acima), configure o Claude Desktop ou o Claude Code com `TOK` (seção "MCP").
2. Peça "liste minhas instâncias do WhatsApp": o assistente chama `list_instances` e vê só a instância do token.
3. Antes de parear, peça "mostre o QR para parear": `get_qr` devolve a imagem; escaneie no celular e peça o status até `connected`.
4. Peça "quais são minhas conversas recentes?" e "o que Fulano disse por último?" (`list_chats`, `get_messages`).
5. Peça para enviar uma mensagem ao contato de teste. Fora da janela de envio a resposta diz que ficou na fila (`sent: false`) e que sai sozinha depois; confirme com `get_message_status` ou no celular.
6. Peça "espere a resposta dele" e responda pelo celular do contato em até 55s: `wait_for_events` devolve o `message.received`.

## Painel web e app de bandeja

### Painel (`/app/`)

`litezap serve` serve um painel web embutido no binário (sem Node, sem arquivos extras) em `/app/`; `/` redireciona para lá. O mesmo painel funciona no PC e num servidor próprio. Ele usa a própria API REST com o **token admin**:

- **Instâncias**: lista com estado ao vivo (SSE), criar (o token da instância aparece uma vez), iniciar/parar/reiniciar/deslogar, excluir.
- **Parear**: QR que se atualiza sozinho (evento `instance.qr`) e pareamento por código (número de telefone).
- **Conversas**: lista de chats, histórico, envio de texto com status ao vivo (`na fila` → `enviada` → `entregue` → `lida`). Quando o envio fica na fila, o painel explica por quê: carência de 15–30s após conectar, ritmo, rampa e janela de envio (com o horário atual no fuso da instância).
- **Configurações**: ritmo, rampa, janela, fuso, proxy e ritmo inseguro (só são enviados os campos alterados; a senha mascarada do proxy nunca é reenviada).
- **Atendente IA**: configurar o hook de agente (URL, segredo, timeout, contexto, pausa por humano, limite de respostas, ignorados), ligar/desligar por conversa, reativar chats pausados e ver as chamadas recentes com motivo das falhas.
- **Webhooks**: criar (o segredo aparece uma vez), ligar/desligar, excluir, ver entregas por status e reenviar as da dead-letter. Há também a página de webhooks globais.
- **Eventos ao vivo**: os mesmos eventos dos webhooks, com o JSON de cada um.

**Login**: cole o token admin (`data/admin_token`). Ele fica só no navegador (`sessionStorage`, ou `localStorage` se marcar "lembrar"), vai apenas no header `Authorization` (nunca na URL) e "Sair" o apaga. "Lembrar" vale para o endereço (host e porta): não marque em computador compartilhado. Não há cookies, então não há superfície de CSRF; o painel tem CSP estrita (`script-src 'self'`, sem scripts inline) e monta a página só com texto (nada de HTML vindo de dados). Para desligar o painel num servidor: `disable_dashboard: true`.

Num servidor exposto, use HTTPS (proxy reverso) antes de abrir o painel pela internet: o token admin trafega em cada requisição.

### App de bandeja (`litezap-app`)

Para quem não quer terminal: um executável que sobe o motor no próprio processo, põe o ícone do LiteZap na bandeja/barra de menus e abre o painel já logado.

- Menu: estado (`N de M conectada(s)`), **Abrir painel**, **Abrir pasta de dados**, **Ver log**, **Sair** (encerra as instâncias com calma antes de sair).
- Pasta de dados: `~/Library/Application Support/LiteZap` (macOS), `%APPDATA%\LiteZap` (Windows), `$XDG_DATA_HOME/litezap` ou `~/.local/share/litezap` (Linux). Dentro dela: `data/` (sessões, banco, `admin_token`), `litezap.log` e, se quiser, um `litezap.yaml` (mesmas chaves do servidor; `LITEZAP_*` continuam valendo). `LITEZAP_APP_DIR` troca a pasta (útil para testes).
- API e painel em `127.0.0.1:8787` (só a sua máquina). Se a porta estiver ocupada (outro LiteZap aberto?), o menu mostra o erro.
- **Login em um clique**: cada "Abrir painel" cria um código de uso único (vale 1 minuto; usar um descarta os outros pendentes) e abre `http://127.0.0.1:8787/app/#code=...`. O código vai no fragmento (não chega a logs nem a `Referer`), o painel o apaga da barra de endereço e o troca pelo token admin em `POST /app/api/session`. Esse endpoint só existe no app de bandeja com o servidor em loopback e só aceita conexões de loopback com `Host` de loopback (contra DNS rebinding), `Origin` igual ao host e `Content-Type: application/json`. `litezap serve` puro não aceita códigos (login colando o token). Limite conhecido: num computador compartilhado, outro usuário local que veja a linha de comando do navegador no instante da abertura poderia tentar usar o código antes; ele vale uma vez só e por 1 minuto (e o painel legítimo mostraria a falha).
- `litezap-app --no-browser` sobe sem abrir o navegador.

Compilar (o `litezap` principal continua sem CGO; só o app de bandeja usa [fyne.io/systray](https://github.com/fyne-io/systray)):

| SO | Comando | Observações |
|---|---|---|
| macOS | `go build -o litezap-app ./cmd/litezap-app` ou `cmd/litezap-app/package-macos.sh` (gera `dist/LiteZap.app`, só na barra de menus) | precisa de CGO e das Xcode Command Line Tools (`xcode-select --install`); sem outras dependências |
| Windows | `GOOS=windows go build -ldflags "-H windowsgui" -o litezap-app.exe ./cmd/litezap-app` | sem CGO; `-H windowsgui` evita a janela de console. Não testado no Windows ainda |
| Linux | `CGO_ENABLED=0 go build -o litezap-app ./cmd/litezap-app` | sem CGO; a bandeja usa DBus (StatusNotifierItem): GNOME precisa da extensão AppIndicator |

Ainda não (em breve): instaladores (`.dmg`, `.msi`, `.deb`), assinatura de código e notarização (no macOS, abra com botão direito > Abrir na primeira vez), ícone do `.app`, auto-update, iniciar com o sistema, túnel opcional, envio de mídia pelo painel e login no painel com token de instância.

## Hospedagem gerenciada (operador)

Opções para rodar o LiteZap como serviço para terceiros (a nuvem gerenciada usa o mesmo binário). Todas vêm desligadas: sem elas o servidor se comporta como no self-host.

- **Token de operador** (`operator_token`, até dois valores separados por vírgula para rotação, mínimo 20 caracteres, diferente do admin). Fica só com quem opera a plataforma. Tem tudo o que o admin tem e mais: muda as settings travadas, cria instâncias além do limite e chama `/v1/operator/*` e `/v1/usage`. Webhooks criados com ele ficam invisíveis para o admin e para tokens de instância (ex.: o `instance.status` que a plataforma monitora).
- **Admin "de cliente"**: com `locked_settings: [proxy_url, unsafe_pacing]`, o token admin recebe `403 forbidden` ao mudar esses campos e o `GET /settings` mostra só esquema e host do proxy (sem usuário e senha), com `"locked": [...]`. O painel desabilita os campos travados.
- **Limite de instâncias** (`max_instances`): o admin acima do limite recebe `403 plan_limit`. O operador ajusta sem reiniciar: `PUT /v1/operator/limits {"max_instances": 5}` (persistido; `null` volta ao valor configurado).
- **Proxy obrigatório** (`require_proxy: true`): nenhum worker sobe sem proxy (nunca conexão direta). Instância criada sem proxy fica `stopped` com `state_reason: proxy_required`; `start`/`restart` respondem `409 proxy_required` até o operador definir `proxy_url`. Limpar o proxy é recusado. Fluxo do control plane: `POST /v1/instances` → `PUT .../settings {"proxy_url": ...}` → `POST .../start`. No boot, instâncias sem proxy também ficam `stopped/proxy_required` (sem `instance.status`; consulte `GET /v1/instances`).
- **Envio suspenso**: `PUT /v1/operator/sending {"suspended": true, "reason": "..."}` (todas as instâncias) ou `PUT /v1/operator/instances/{id}/sending`. Novos envios de qualquer tipo (texto, mídia, reação, localização, contato, enquete, edição, apagar, fixar) e `check-numbers`, pela REST e pelas ferramentas MCP, respondem `403 sending_suspended` (inclusive a repetição idempotente de um `client_message_id`); o que já estava na fila fica retido até liberar, e itens cujo `expires_after` vencer nesse meio-tempo falham como `expired`. Ver e cancelar a fila (`GET`/`DELETE .../queue`) continua permitido. Recepção, webhooks e leitura continuam.
- **Uso**: `GET /v1/usage?from=2026-10-01&to=2026-10-31` devolve, por instância e dia (UTC), envios pela API que chegaram ao WhatsApp, mensagens recebidas, bytes de mídia, segundos conectada e entregas a webhooks do cliente. Gravado a cada 30s e no shutdown; sobrevive à exclusão da instância.
- **Login de um clique**: `POST /v1/operator/dashboard-login` → `{code, url, expires_at}`. A `url` (`<public_url>/app/#code=...`) abre o painel já logado com o token admin (nunca o de operador). O código vale uma vez, por 2 minutos.
- **Endpoint de ops** (`ops_listen_addr`, ex.: `[::]:9090` numa rede privada): `/healthz`, `/readyz` e `/metrics` sem token, para health checks da plataforma e o coletor Prometheus. Não exponha esse endereço à internet.
- **Upgrade gracioso**: ao receber SIGTERM o `/readyz` passa a `503 {"status":"draining"}` na hora; `shutdown_timeout` encolhe todas as fases do encerramento para caber no prazo da plataforma.
- **Exportação**: `POST /v1/export` (admin) devolve um `tar.gz` com `manifest.json`, `litezap.db` (cópia online, sem webhooks e dados do operador, sem `proxy_url` e sem os hashes dos tokens de instância) e `media/`. Com `upload_url` (só operador; HTTPS, ex.: URL pré-assinada de S3) o arquivo é enviado por `PUT` e a resposta traz `bytes` e `sha256`. Uma exportação por vez.

Métricas em `/metrics`: `litezap_instances{state}`, `litezap_queue_messages{status}`, `litezap_webhook_deliveries{status}`, `litezap_messages_sent_total`, `litezap_messages_received_total`, `litezap_media_bytes_total`, `litezap_instance_state_changes_total{state}`, `litezap_proxy_failures_total`, `litezap_proxy_unreachable_instances`, `litezap_sending_suspended`, `litezap_sending_suspended_instances`, `litezap_ready`, `litezap_draining`, `litezap_build_info{version}` e métricas básicas do runtime Go do supervisor.

Toda a configuração cabe em variáveis de ambiente, então serve para qualquer provedor (VM, container, systemd). Regras gerais: segredos (`LITEZAP_ADMIN_TOKEN`, `LITEZAP_OPERATOR_TOKEN`) vêm do cofre do provedor, nunca de arquivo no volume de dados; o `ops_listen_addr` fica numa interface privada (health check e Prometheus); `shutdown_timeout` fica alguns segundos abaixo do prazo que a plataforma dá entre SIGTERM e SIGKILL.

Exemplo (AWS: uma EC2 por projeto em `sa-east-1`, segredos no SSM Parameter Store, Caddy com Let's Encrypt na frente). Um script de boot (ou `ExecStartPre`) gera o arquivo de ambiente a partir do SSM, com permissão `0600`:

```bash
# /usr/local/bin/litezap-env: monta /etc/litezap/env a partir de /litezap/<ref>/* no SSM
install -m 600 /dev/null /etc/litezap/env
aws ssm get-parameters-by-path --region sa-east-1 --path "/litezap/$REF/" --with-decryption \
  --query 'Parameters[].[Name,Value]' --output text |
  while read -r name value; do echo "${name##*/}=$value"; done >> /etc/litezap/env
```

Parâmetros (`SecureString` para os tokens): `LITEZAP_ADMIN_TOKEN`, `LITEZAP_OPERATOR_TOKEN`, `LITEZAP_PUBLIC_URL=https://<ref>.<ip>.sslip.io`, `LITEZAP_LISTEN_ADDR=127.0.0.1:8787` (só o Caddy fala com o motor), `LITEZAP_OPS_LISTEN_ADDR=<ip privado>:9090` (security group liberando só a rede de monitoramento), `LITEZAP_DATA_DIR=/var/lib/litezap`, `LITEZAP_LOCKED_SETTINGS=proxy_url,unsafe_pacing`, `LITEZAP_REQUIRE_PROXY=true`, `LITEZAP_MAX_INSTANCES`, `LITEZAP_ZOMBIE_TIMEOUT=5m`, `LITEZAP_SHUTDOWN_TIMEOUT=25s`, `LITEZAP_MEDIA_RETENTION_DAYS=7`, `LITEZAP_MESSAGE_RETENTION_DAYS=90`, `LITEZAP_LOG_FORMAT=json`.

```ini
# /etc/systemd/system/litezap.service
[Service]
ExecStartPre=/usr/local/bin/litezap-env
EnvironmentFile=/etc/litezap/env
ExecStart=/usr/local/bin/litezap serve -config ""
Restart=always
KillSignal=SIGTERM
TimeoutStopSec=30
```

Com Docker, o mesmo arquivo vai em `docker run --env-file /etc/litezap/env --stop-timeout 30 -v /var/lib/litezap:/var/lib/litezap ...`. O Caddy faz `reverse_proxy 127.0.0.1:8787` no hostname do `public_url` (sem buffer de resposta, por causa do SSE: `flush_interval -1`). O health check e o Prometheus usam `http://<ip privado>:9090/readyz` e `/metrics`. Com o motor em loopback, `/healthz` e `/readyz` também respondem sem token pelo Caddy (só dizem "ok"/"ready"); `/metrics` na porta pública sempre exige o token admin.

## Limitações conhecidas

- Eventos ainda não emitidos: `chat.presence` e `group.updated` (o worker não os reporta). SSE não tem replay (`Last-Event-ID` é ignorado).
- A garantia "pelo menos uma vez" vale a partir do outbox. Antes dele o evento passa por um buffer em memória (até 10.000): um crash do supervisor entre gravar a mensagem no log e gravar a entrega, ou um buffer cheio (logado), perde o evento para os webhooks. Um shutdown normal grava o buffer antes de sair.
- `wait_for_events` (MCP) só vê eventos publicados enquanto espera; para o que chegou antes, use `get_messages`.
- Streams SSE checam o token só na conexão: um stream aberto continua depois de o token ser rotacionado (reconecte para aplicar).
- Se um recibo chegar antes da confirmação do envio, sai `message.status: delivered` (ou `read`) antes de `message.sent`, e o `message.status: sent` é omitido.
- Sem busca full-text (marco 2b).
- Botões, listas e carrossel não são enviados (instáveis no WhatsApp); respostas a eles chegam como `unknown`.
- Votos em enquetes criadas antes do pareamento não são decifrados (a sessão não tem o segredo). Votos não ficam no log, só no evento `message.poll_vote`.
- Fixar mensagem não emite evento, e fixações feitas pelo celular não são registradas.
- A prévia de link `auto` lê só `og:*`/`twitter:*`/`<title>` da página (sem JavaScript); miniatura só de JPEG, PNG ou GIF.
- Mídia recebida baixada fica em `data/media/` para sempre, a menos que `media_retention_days` esteja definido.
- `instance.proxy_failed` não traz a mensagem de erro do worker, só o motivo `proxy_unreachable`.
- A exportação (`POST /v1/export`) não inclui as sessões do WhatsApp: restaurar em outro servidor exige parear de novo. A importação ainda não existe.
- Mídia passa entre supervisor e worker por arquivos em `data/` (mesma máquina). Um spawner em container (nuvem) vai precisar de um volume compartilhado.
- Se o supervisor morrer entre o worker entregar um evento e o supervisor gravá-lo, esse evento se perde (sem confirmação de entrega worker→supervisor ainda).
- Suporte a Windows não testado (compila, mas não foi executado).

A lista completa do que foi adiado para o marco 2b (busca, retenção, confirmação de entrega, nome de grupos, métricas) está na seção "Marco 2b" de `docs/superpowers/plans/2026-10-17-litezap-m2-messaging.md`.

## Docker

```bash
docker compose up -d --build   # imagem distroless, binário estático, usuário não root, volume /data
```

Dentro do container o servidor escuta em `0.0.0.0:8787` (a porta é publicada só em `127.0.0.1` do host): `/healthz` e `/readyz` exigem o token admin e URLs assinadas exigem `LITEZAP_PUBLIC_URL`. Detalhes em `site/src/content/docs/docs/self-hosted/docker.md`.

## Site e docs

O site do produto e a documentação (Astro + Starlight, busca com Pagefind) ficam em `site/`. A referência da API é gerada de `site/public/openapi.yaml`.

```bash
cd site
pnpm install
pnpm dev     # http://localhost:4321
pnpm build   # gera site/dist (estático)
```

## Desenvolvimento

```bash
go test -race ./...
go tool buf generate   # após mudar proto/
```
