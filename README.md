# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

Licença: Apache-2.0. Roda no seu PC, no seu servidor ou na nuvem gerenciada do LiteZap.

> [!WARNING]
> **Projeto não oficial.** O LiteZap não tem vínculo, autorização ou endosso do WhatsApp nem da Meta; "WhatsApp" é marca registrada dos respectivos donos. Ele usa o protocolo do WhatsApp Web via [whatsmeow](https://github.com/tulir/whatsmeow), e o WhatsApp pode **banir ou restringir números** que usam clientes não oficiais, principalmente com envio em massa ou mensagens não solicitadas. Você é o único responsável pelo uso e por cumprir os [Termos de Serviço](https://www.whatsapp.com/legal/terms-of-service) e as políticas do WhatsApp e as leis aplicáveis (ex.: LGPD). Use por sua conta e risco; o software é fornecido "no estado em que se encontra", sem garantias (veja a [licença](LICENSE)).

> Status: marco 4 (envio com fila persistente, recebimento, chats, histórico, mídia, proxy por instância, eventos com webhooks assinados e SSE, servidor MCP em `/mcp` e `litezap mcp`) + app local v1 (painel web embutido e lançador de bandeja) + marco 5 (hook de agente para atendente de IA, com o exemplo `examples/agent-claude`).
> Status: marco 4 (envio com fila persistente, recebimento, chats, histórico, mídia, proxy por instância, eventos com webhooks assinados e SSE, servidor MCP em `/mcp` e `litezap mcp`) + app local v1 (painel web embutido e lançador de bandeja) + marcos 6 (mensagens completas) e 7 (eventos de chamada, presença, `played`, respostas de botão, filtros de webhook). O hook de agente vem no próximo marco.
> Design: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md`

## Instalar

Binários para Linux e macOS (amd64/arm64) e Windows (amd64). Não usa terminal? [Baixe o app](#baixar-o-app).

**Script** (Linux e macOS; detecta SO e arquitetura, confere o sha256 com `checksums.txt` e instala em `~/.local/bin` se estiver no `PATH`, senão em `/usr/local/bin`, pedindo `sudo` só se precisar):

```bash
curl -fsSL https://raw.githubusercontent.com/douglasdf99/litezap/main/scripts/install.sh | sh
# versão específica ou outro diretório:
curl -fsSL https://raw.githubusercontent.com/douglasdf99/litezap/main/scripts/install.sh | LITEZAP_VERSION=v0.1.0 LITEZAP_INSTALL_DIR=~/bin sh
```

**Docker** (imagem multi-arch `linux/amd64` e `linux/arm64`; veja [Docker](#docker)):

```bash
docker run -d --name litezap -p 127.0.0.1:8787:8787 -v litezap-data:/data ghcr.io/douglasdf99/litezap:latest
```

**go install** (Go da versão em `go.mod`; `litezap version` mostra `dev`):

```bash
go install github.com/douglasdf99/litezap/cmd/litezap@latest
```

**Releases**: baixe o `.tar.gz` (ou `.zip` no Windows) em [github.com/douglasdf99/litezap/releases](https://github.com/douglasdf99/litezap/releases) e confira com `sha256sum -c checksums.txt --ignore-missing`.

## Rodar

```bash
go build -o litezap ./cmd/litezap   # ou use um binário instalado acima
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

Autenticação: `Authorization: Bearer <token>`. O token de instância só acessa rotas da própria instância; uma chave de API (`lzk_`, marco 11) só as rotas dos seus escopos numa instância (seção "Plataforma e parceiros"). Tokens e chaves podem ter allowlist de IP.

| Rota | Token |
|---|---|
| `POST /v1/instances` `{"name"}` → `{instance, token}` | admin |
| `GET /v1/instances[?state=&tag=&q=&limit=&cursor=]` (sem `limit`: todas) | admin |
| `GET /v1/instances/{id}` | instância ou admin |
| `PATCH /v1/instances/{id}` `{"name"?, "tags"?, "ip_allowlist"?}` | instância ou admin (`tags` e `ip_allowlist` só admin) |
| `POST /v1/instances/bulk` `{"action": start\|stop\|restart\|delete, "ids"\|"tag", "confirm"?}` | admin |
| `POST\|DELETE /v1/instances/{id}/pairing-links` `{"ttl_seconds"?}` → `{id, url, token, expires_at}` | admin |
| `GET\|POST /v1/instances/{id}/api-keys` · `DELETE /v1/instances/{id}/api-keys/{key_id}` | instância ou admin |
| `GET /v1/instances/{id}/usage[?from=&to=]` | instância ou admin |
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
| `GET /pair/` (página de pareamento hospedada) · `GET /pair/api/state` · `POST /pair/api/phone\|start` · `GET /pair/embed.js` | nenhum (o link assinado vai no cabeçalho `X-Pairing-Token`) |
| `POST /v1/instances/{id}/presence/subscribe` `{"to"}` (presença online/offline de um contato; exige `presence.online`) | instância ou admin |
| `GET\|POST /v1/instances/{id}/groups` · `GET\|PATCH /groups/{jid}` · `PUT\|DELETE /groups/{jid}/photo` (seção "Grupos e comunidades") | instância ou admin |
| `POST /groups/{jid}/participants` `{"action","participants"}` · `GET\|POST /groups/{jid}/requests` · `GET /groups/{jid}/invite` · `POST /groups/{jid}/invite/reset` · `POST /groups/{jid}/leave` | instância ou admin |
| `POST /groups/preview` `{"code"}` · `POST /groups/join` `{"code"}` | instância ou admin |
| `GET\|POST /v1/instances/{id}/communities` · `GET /communities/{jid}` · `GET\|POST /communities/{jid}/groups` · `DELETE /communities/{jid}/groups/{group}` | instância ou admin |
| `GET\|PATCH\|DELETE /v1/instances/{id}/chats/{jid}` (estado do chat; arquivar, fixar, silenciar, não lida; apagar — seção "Chats, contatos, perfil e privacidade") | instância ou admin |
| `PUT /v1/instances/{id}/messages/{msg_id}/star` `{"starred"}` | instância ou admin |
| `GET /v1/instances/{id}/contacts/{jid}/picture[?preview=1&download=1]` · `/contacts/{jid}/profile` | instância ou admin (cota de consultas) |
| `POST /v1/instances/{id}/contacts/{jid}/block\|unblock` · `GET /v1/instances/{id}/blocklist` | instância ou admin |
| `GET\|PATCH /v1/instances/{id}/profile` · `GET\|PUT\|DELETE /v1/instances/{id}/profile/picture` | instância ou admin |
| `GET\|PATCH /v1/instances/{id}/privacy` | instância ou admin |
| `POST /v1/instances/{id}/status[?wait=true]` · `GET /status` · `GET /status/privacy` · `DELETE /status/{msg_id}` · `POST /status/{msg_id}/reply` (seção "Status, canais e etiquetas") | instância ou admin |
| `GET\|POST /v1/instances/{id}/channels` · `POST /channels/preview` `{"invite"}` · `GET /channels/{jid}` · `POST /channels/{jid}/follow\|unfollow\|mute\|unmute` · `GET\|POST /channels/{jid}/messages` · `POST /channels/{jid}/reactions` | instância ou admin |
| `GET\|POST /v1/instances/{id}/labels` · `PATCH\|DELETE /labels/{label_id}` · `PUT\|DELETE /labels/{label_id}/chats/{jid}` · `PUT\|DELETE /labels/{label_id}/messages/{msg_id}` · `GET /business/profile` | instância ou admin (conta Business) |
| `GET /v1/instances/{id}/events[?types=]` (SSE) | instância ou admin |
| `GET /v1/events[?types=]` (SSE, todas as instâncias) | admin |
| `POST /v1/webhooks` · `GET /v1/webhooks[?instance_id=]` | admin (global ou de uma instância) ou instância (só a própria) |
| `GET\|PATCH\|DELETE /v1/webhooks/{id}` | idem (webhook de outra instância responde 404) |
| `GET /v1/webhooks/{id}/deliveries[?status=pending\|delivered\|failed&limit=&cursor=]` | idem |
| `POST /v1/webhooks/{id}/deliveries/{delivery_id}/retry` | idem |
| `GET /v1/me` → `{admin, role, instance_id, key_id?, scopes?}` (`role`: `operator`, `admin` ou `instance`) | qualquer token |
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

A fila é persistente e por instância: ordem estrita por destinatário, pausa enquanto a instância não está `connected` (e por 15–30s depois de conectar), janela de envio para quem não mandou nada nas últimas 24h, ritmo e rampa conforme `settings`, até 5 tentativas em falhas temporárias. Antes do primeiro envio a um número, o LiteZap confere se ele tem WhatsApp (e usa o JID que o WhatsApp devolve, com ou sem o 9º dígito); número sem WhatsApp falha com `invalid_number` sem enviar nada. Envios de mídia grande esperam vaga (no máximo 3 ao mesmo tempo) em vez de falhar; só se o cliente desistir da requisição antes de abrir vaga a resposta é `503 rate_limited`. `client_message_id` repetido devolve a mesma mensagem (idempotência); repetido com outro conteúdo responde `409 conflict`. O status (`queued`, `in_flight`, `sent`, `delivered`, `read`, `played`, `failed`) fica em `GET /messages/{id}`. No log de mensagens (`GET /chats/{jid}/messages`) o `status` é `received` para mensagens recebidas e `pending` → `sent` → `delivered` → `read` → `played` para as enviadas (`played` = áudio de voz, vídeo ou visualização única reproduzidos) (`pending` = em envio, ainda sem confirmação do WhatsApp). Consultas de número também têm ritmo: cada uma que vai ao WhatsApp conta como um envio para `min_interval_ms`/`max_per_minute`, e por dia a instância faz no máximo o limite diário da rampa (quando ligada); acima disso o item espera a janela do dia seguinte. `check-numbers` tem limite próprio de 200 números não cacheados por dia (UTC), além de 10 por minuto (`429 rate_limited`).

### Settings

```bash
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"min_interval_ms":5000,"ramp":{"enabled":true},"send_window":{"start":"09:00","end":"18:00"},"timezone":"America/Sao_Paulo"}'
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $ADMIN" \
  -d '{"proxy_url":"socks5://user:senha@host:1080"}'
```

```bash
# chamadas: rejeitar sozinho e responder (marco 7); presença: ficar online (opt-in)
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"calls":{"auto_reject":true,"reject_message":"Não atendo ligações por aqui, me mande uma mensagem."},"presence":{"online":false}}'
```

Campos ausentes mantêm o valor atual. `GET /settings` esconde a senha do `proxy_url`. Pisos: `min_interval_ms` ≥ 1000 e `max_per_minute` ≤ 60, a menos que o admin ligue `unsafe_pacing`. `send_window` vazio (`{"start":"","end":""}`) libera qualquer horário; fora da janela, mensagens para quem não mandou nada nas últimas 24h esperam o início da próxima (com alguns minutos de variação), enquanto respostas a conversas ativas saem a qualquer hora; itens adiados pela rampa vão para a janela do dia seguinte (nunca à meia-noite). Falhas na verificação do número adiam só aquele item (1, 2, 5, 15 min; na 5ª falha, `failed`). `"proxy_url":""` volta ao `default_proxy_url`. Trocar o proxy reinicia o worker; se o proxy não responde, a instância fica `reconnecting` com `state_reason: proxy_unreachable` (nunca cai para conexão direta, e variáveis `HTTP(S)_PROXY` do ambiente não são herdadas pelos workers).

**Chamadas** (`calls`): com `auto_reject` toda chamada individual recebida é rejeitada assim que toca (em menos de 3s) e gera `call.received` com `rejected: true`. `reject_message` (até 1000 bytes) é mandada ao autor pela fila normal (ritmo, janela, rampa e suspensão do operador valem), no máximo uma vez por contato a cada 10 min; se não sair em 1h (janela fechada, fila longa) falha como `expired` em vez de chegar horas depois. Chamadas em grupo e ofertas com mais de 1 min (entregues depois de uma reconexão) só geram eventos: não são rejeitadas nem respondidas.

**Grupos** (`groups`): `max_adds_per_hour` (padrão 10, teto 30), `max_adds_per_day` (50, teto 200), `max_creates_per_day` (3, teto 10) e `max_joins_per_day` (10, teto 30) por instância; os tetos só sobem com `unsafe_pacing` (admin). Ex.: `{"groups":{"max_adds_per_hour":5}}`. Ver "Grupos e comunidades".

**Leitura automática** (`auto_read: {enabled, groups}`, desligada por padrão): mensagens recebidas são marcadas como lidas sozinhas, 2 a 5s depois de chegar, em lote por conversa (e por remetente, em grupos), no máximo uma confirmação por segundo por instância; grupos só com `groups: true`. O contato vê os tiques azuis, a menos que a confirmação de leitura esteja desligada na privacidade (aí o WhatsApp só limpa o "não lida" nos aparelhos da conta).

**Presença** (`presence.online`, desligado por padrão): marca a conta como "disponível" a cada conexão. Só assim o WhatsApp manda "digitando…/gravando" (`chat.presence`) e aceita `POST /presence/subscribe` (online/visto por último de um contato; até 20 assinaturas por minuto e 200 por dia por instância, recusadas enquanto o operador suspende envios; valem até a conexão cair). Presença que ainda chegue com a setting desligada é descartada. **Contrapartida**: enquanto a conta está disponível num aparelho conectado, o WhatsApp trata esse aparelho como ativo e o celular deixa de mostrar notificações. Ligue só em números atendidos pela API, não no celular de quem atende pessoalmente.

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
| `rate_limited` | 429 | muitas consultas de números por minuto em `check-numbers` (cota própria, separada da fila); cotas de grupos (adições, criações, entradas por link, 10s entre lotes de adição) (`retryable`) |
| `group_not_found` | 404 | grupo inexistente ou a instância não participa dele |
| `group_forbidden` | 403 | o WhatsApp recusou a operação no grupo (a instância não é admin ou não participa) |
| `invite_invalid` · `invite_revoked` | 400 · 410 | link de convite malformado · revogado |
| `sending_suspended` | 403 | envios suspensos pelo operador (também recusa criar grupo, adicionar participantes e entrar por link) |
| `not_implemented` | 501 | o worker não oferece grupos (versão antiga) |
| `rate_limited` | 503 | muitos envios com corpo/mídia grande ao mesmo tempo e a requisição desistiu de esperar vaga (`retryable`) |
| `worker_error` | 502 | o worker da instância recusou ou falhou a operação (`retryable`) |
| `worker_timeout` | 504 | o worker da instância não respondeu a tempo (`retryable`) |
| `shutting_down` | 503 | servidor encerrando |
| `internal` | 500 | erro interno |

### Chats, contatos, perfil e privacidade

```bash
# estado do chat (arquivado, fixado, silenciado, marcado como não lido), reconciliado com o celular
curl -s localhost:8787/v1/instances/$ID/chats/5511988887777@s.whatsapp.net -H "Authorization: Bearer $TOK"
# arquivar; fixar; silenciar por 8h (sem mute_seconds = até reativar); marcar como não lida / lida
curl -s -X PATCH localhost:8787/v1/instances/$ID/chats/5511988887777@s.whatsapp.net -H "Authorization: Bearer $TOK" -d '{"archived":true}'
curl -s -X PATCH .../chats/$JID -d '{"pinned":true}'
curl -s -X PATCH .../chats/$JID -d '{"muted":true,"mute_seconds":28800}'
curl -s -X PATCH .../chats/$JID -d '{"unread":true}'
# apagar a conversa no WhatsApp (o log do LiteZap continua); ?delete_media=1 apaga a mídia no celular
curl -s -X DELETE .../chats/$JID
# favoritar uma mensagem do log
curl -s -X PUT .../messages/$MSG_ID/star -d '{"starred":true}'
# foto (URL do CDN do WhatsApp, expira; ?download=1 baixa o JPEG pelo proxy da instância) e recado/empresa do contato
curl -s .../contacts/5511988887777/picture
curl -s .../contacts/5511988887777/profile
# bloquear, desbloquear, lista de bloqueados
curl -s -X POST .../contacts/5511988887777/block
curl -s .../blocklist
# perfil da própria conta: nome, recado e foto (JPEG, PNG ou GIF por URL ou base64, até 10MB; recortada e reduzida a 640x640 JPEG; WebP não)
curl -s -X PATCH .../profile -d '{"name":"Loja Exemplo","about":"Atendimento das 9h às 18h"}'
curl -s -X PUT .../profile/picture -d '{"url":"https://exemplo.com/logo.png"}'
curl -s -X DELETE .../profile/picture
# privacidade (nomes: last_seen, online, profile_photo, about, groups_add, read_receipts, calls, messages)
curl -s .../privacy
curl -s -X PATCH .../privacy -d '{"last_seen":"contacts","read_receipts":"none","default_disappearing_seconds":86400}'
```

- As ações de chat vão pelo *app state* do WhatsApp: o celular e os outros aparelhos acompanham, e mudanças feitas no celular chegam como `chat.updated` (`source: "device"`) e atualizam `GET /chats`. Logo depois de parear, até o celular mandar as chaves, as ações respondem `409 app_state_not_synced` (tente de novo em instantes). Só chats que estão no log (`404` nos demais). O WhatsApp permite 3 chats fixados (`409 pin_limit`); arquivar desafixa.
- Apagar só marca o chat no log: ele sai de `GET /chats` até chegar mensagem nova, e `GET /chats/{jid}/messages` continua funcionando. "Limpar conversa" não existe (o WhatsApp não documenta a mutação e o whatsmeow não a monta); limpezas feitas no celular só geram `chat.updated` com `cleared: true`.
- O estado que já existia no celular chega no sync inicial do app state (logo após parear) e é gravado no log sem um `chat.updated` por chat; `GET /chats/{jid}` ainda confere o chat com a cópia local do worker.
- Foto e perfil de contato consultam o WhatsApp: até 30 consultas por minuto e 1000 por dia por instância (`429 rate_limited`), recusadas enquanto o operador suspende envios; respostas ficam em cache (URL 10 min, arquivo 1h, "sem foto" 10 min). Foto inexistente ou escondida pela privacidade responde `404 not_found`, nunca `502`. A foto da própria conta (`GET /profile/picture`) não gasta a cota.
- Nome do perfil: 1 a 25 caracteres; recado: até 139. Privacidade: valores `all`, `contacts`, `contact_blacklist`, `none` (`last_seen`, `profile_photo`, `about`, `groups_add`), `all`/`none` (`read_receipts`), `all`/`match_last_seen` (`online`), `all`/`known` (`calls`), `all`/`contacts` (`messages`); as listas de exceção ("meus contatos, exceto…") só no celular.

### Fila de envio

`GET /queue` lista os itens ainda não enviados (`queued` e `in_flight`), do mais antigo ao mais novo; `?status=` filtra (`queued`, `in_flight`, `sent`, `failed`, `all`) e `next_cursor` pagina (`limit` padrão 50, máx. 200). `DELETE /queue/{msg_id}` remove um item `queued` sem enviar: ele termina como `failed` com `error: "canceled: ..."`, o arquivo de mídia preparado é apagado e sai `message.status` `failed`; um item `in_flight` (sendo enviado agora) ou já terminado responde `409 conflict`. `DELETE /queue` cancela todos os `queued` de uma vez (os `in_flight` terminam normalmente) e devolve `{"canceled": n}`. Útil antes de reconectar uma instância que ficou parada com mensagens velhas na fila.

### Grupos e comunidades

Gestão síncrona (timeout de 30s; a instância precisa estar conectada). `{jid}` é o JID do grupo (`120363…@g.us`); participantes aceitam número com DDI ou JID (`…@s.whatsapp.net` ou `…@lid`).

```bash
G=localhost:8787/v1/instances/$ID/groups
curl -s $G -H "Authorization: Bearer $TOK"                                  # grupos em que o número está (sem participantes)
curl -s -X POST $G -H "Authorization: Bearer $TOK" \
  -d '{"name":"Equipe","participants":["5511988887777"],"description":"…","settings":{"announce":false,"ephemeral_seconds":0}}'
curl -s $G/120363000000000001@g.us -H "Authorization: Bearer $TOK"          # metadados + participantes (admin/super_admin)
curl -s -X PATCH $G/120363000000000001@g.us -H "Authorization: Bearer $TOK" \
  -d '{"name":"Novo nome","description":"","settings":{"locked":true,"approval_required":true}}'
curl -s -X POST $G/120363000000000001@g.us/participants -H "Authorization: Bearer $TOK" \
  -d '{"action":"promote","participants":["5511988887777"]}'                 # add | remove | promote | demote
curl -s $G/120363000000000001@g.us/invite -H "Authorization: Bearer $TOK"   # {"link":"https://chat.whatsapp.com/…"}
curl -s -X POST $G/join -H "Authorization: Bearer $TOK" -d '{"code":"https://chat.whatsapp.com/AbC…"}'
```

- **Configurações** (`settings`): `announce` (só admins enviam), `locked` (só admins editam dados), `approval_required` (entrada com aprovação; pedidos em `GET /requests`, decididos com `POST /requests {"action":"approve|reject","participants":[…]}`), `admins_add_members` (só admins adicionam), `ephemeral_seconds` (`0`, `86400`, `604800` ou `7776000`). `PATCH` aceita os campos soltos ou dentro de `settings`; aplica na ordem nome, descrição, configurações e para na primeira falha (as anteriores ficam). Foto: `PUT /photo {"base64"|"url"}` (JPEG, PNG ou GIF até 10MB; o servidor recorta o centro e manda um JPEG 640x640, como na foto do perfil; WebP não), `DELETE /photo` remove.
- **Resultado por participante**: criar, `participants` e `requests` respondem `results: [{participant, jid, status, code?, invite_code?}]`, um por pessoa pedida; uma falha nunca derruba o lote. `status`: `ok`, `not_on_whatsapp`, `unknown_contact` (sem conversa e não verificável), `privacy_invite_required` (a pessoa só aceita convite; `invite_code` vem preenchido, mas o convite por mensagem ainda não é enviado: mande o link), `recently_left`, `already_participant`, `not_participant`, `group_full`, `error_<código>` ou `unknown` (o WhatsApp não respondeu por ela). Os códigos vêm do WhatsApp e o mapeamento é de melhor esforço.
- **Anti-ban** (adicionar desconhecidos em grupos é dos gatilhos de banimento mais rápidos): só são adicionadas pessoas que **já têm conversa no log** da instância ou que a **consulta de números** confirma no WhatsApp (gasta a cota do `check-numbers`); `@lid` sem conversa é recusado. A checagem não distingue um desconhecido que tem WhatsApp de um amigo: escolher quem adicionar continua sendo responsabilidade de quem chama. No máximo 10 por requisição (também ao criar), 10s entre lotes (criar com participantes também conta), e as cotas `groups.*` por hora/dia (persistidas: reiniciar não zera); as cotas são conferidas antes de gastar qualquer consulta. Criar grupo/comunidade e entrar por link têm cota diária. A cota só volta quando a requisição comprovadamente não chegou ao WhatsApp (worker parado ou desconectado); timeouts e recusas do WhatsApp contam, e um limite do próprio WhatsApp reinicia o intervalo de 10s. Com envios suspensos pelo operador, criar, adicionar, aprovar pedidos e entrar respondem `sending_suspended`; listar, ler e remover continuam.
- **Comunidades**: `POST /communities {"name", "description"?}` cria, sem participantes (o WhatsApp cria o grupo de avisos sozinho), com o formato de criação de comunidade do app (descrição obrigatória para o WhatsApp: sem `description` vai o nome); se o WhatsApp recusar (algumas contas não podem criar comunidades), responde `409 community_not_allowed`: tente criar uma pelo app com o mesmo número; `GET /communities/{jid}` traz a comunidade e seus grupos (`announcement_group: true` no de avisos); `POST /communities/{jid}/groups {"group"}` vincula um grupo existente, `DELETE …/groups/{group}` desvincula; para criar um grupo já dentro da comunidade use `POST /groups` com `"parent"`. Configurações, descrição, link e participantes de uma comunidade usam as rotas de `/groups/{jid}` (semântica do WhatsApp nesses casos não verificada em aparelho real). Desativar comunidade não existe no whatsmeow.
- O nome dos chats de grupo no log é preenchido ao chegar a primeira mensagem (o LiteZap consulta o grupo em segundo plano) e acompanha as mudanças de nome.

### Status, canais e etiquetas (Business)

```bash
S=localhost:8787/v1/instances/$ID
# status (story) de texto, imagem ou vídeo: vai para quem a privacidade do status do celular permite
curl -X POST "$S/status?wait=true" -H "Authorization: Bearer $TOK" -d '{"type":"text","text":"Bom dia!","background_color":"#1E6E4F","font":1}'
curl -X POST "$S/status" -H "Authorization: Bearer $TOK" -d '{"type":"image","caption":"Novidade","media":{"url":"https://exemplo.com/foto.jpg"}}'
curl "$S/status" -H "Authorization: Bearer $TOK"            # status recebidos dos contatos e os nossos (log)
curl "$S/status/privacy" -H "Authorization: Bearer $TOK"    # {type: contacts|blacklist|whitelist, list}
curl -X DELETE "$S/status/$MSG_ID" -H "Authorization: Bearer $TOK"   # apaga um status nosso para todos
curl -X POST "$S/status/$MSG_ID/reply" -H "Authorization: Bearer $TOK" -d '{"text":"Que lindo!"}'  # responde ao autor citando o status

# canais (newsletters)
curl "$S/channels" -H "Authorization: Bearer $TOK"                                   # seguidos e administrados
curl -X POST "$S/channels/preview" -H "Authorization: Bearer $TOK" -d '{"invite":"https://whatsapp.com/channel/0029Va..."}'
curl -X POST "$S/channels/120363...@newsletter/follow" -H "Authorization: Bearer $TOK"  # unfollow, mute, unmute
curl "$S/channels/120363...@newsletter/messages?count=20" -H "Authorization: Bearer $TOK"
curl -X POST "$S/channels/120363...@newsletter/reactions" -H "Authorization: Bearer $TOK" -d '{"server_id":123,"emoji":"👍"}'
curl -X POST "$S/channels" -H "Authorization: Bearer $TOK" -d '{"name":"Minha loja","description":"Ofertas"}'
curl -X POST "$S/channels/120363...@newsletter/messages?wait=true" -H "Authorization: Bearer $TOK" -d '{"type":"text","text":"Promoção!"}'

# etiquetas do WhatsApp Business (cor = índice 0-19 da paleta do WhatsApp) e perfil comercial
curl "$S/labels" -H "Authorization: Bearer $TOK"
curl -X POST "$S/labels" -H "Authorization: Bearer $TOK" -d '{"name":"Novo cliente","color":1}'
curl -X PUT "$S/labels/5/chats/5511988887777" -H "Authorization: Bearer $TOK"   # DELETE remove
curl "$S/business/profile" -H "Authorization: Bearer $TOK"
```

- **Status**: tipos `text` (até 700 caracteres, `background_color`/`text_color` `#RRGGBB`, `font` 0, 1, 2, 6, 7, 8, 9 ou 10), `image` e `video` (`caption`, mídia por `url` ou `base64`). Os destinatários são os da privacidade do status configurada no celular (todos os contatos, todos menos alguns, ou só alguns); o WhatsApp não permite escolher por envio. Publicar passa pela fila de envio (ritmo, janela de horário, rampa — conta como um destinatário novo por dia —, suspensão do operador; resposta igual à de `POST /messages`) e tem teto de **10 status por dia** por instância (`429 rate_limited`; apagar não conta). Os status dos contatos que chegam com a instância conectada ficam no log, no chat `status@broadcast` (fora de `GET /chats`; a mídia baixa pela rota `/media` de sempre), e saem no evento `status.received`.
- **Canais**: `GET /channels/{jid}` e a prévia por convite trazem nome, descrição, seguidores, link e o papel do número (`owner`, `admin`, `subscriber`). Seguir/deixar de seguir tem limite de 3 por minuto, 10 por hora e 30 por dia; silenciar, 10 por minuto; criar canal, 2 por dia; reagir, 30 por minuto (por instância, em memória). Publicar (`text`, `image`, `video`, `audio`, `document`) só como dono/admin, pela fila de envio, no máximo 20 publicações por dia por instância. Posts novos dos canais seguidos saem em `channel.message` (sem download da mídia).
- **Etiquetas**: só em conta WhatsApp Business (em conta pessoal, `409 business_account_required`). A lista vem do celular pelo app state (inclusive as que já existiam ao parear; numa sessão pareada antes do marco 10 o LiteZap pede a sincronização de novo e, até ela chegar, criar etiqueta responde `409 app_state_not_synced`, para um id novo nunca sobrescrever uma etiqueta do celular). Alterações de etiqueta têm limite de 30 por minuto. Cada mudança, pela API ou no celular, sai em `label.updated`. Logo após parear pode responder `409 app_state_not_synced` até o celular compartilhar as chaves.
- **Perfil comercial**: `GET /business/profile` lê endereço, e-mail, categorias, horário e opções (site) da própria conta Business. Editar perfil e catálogo de produtos não existem no whatsmeow.

### Verificar números

`POST /check-numbers` aceita de 1 a 10 números por chamada (DDI + DDD + número, 10–15 dígitos) e devolve `exists` e o `jid` que o WhatsApp usa. Resultados ficam em cache (30 dias para quem tem WhatsApp, 24h para quem não tem). Consultas novas têm cota de 10 por minuto por instância, separada da cota de 30 por minuto da fila de envio; acima disso, `429 rate_limited`.

### Plataforma e parceiros (marco 11)

Para quem oferece WhatsApp a clientes (uma instância por cliente):

```bash
B=localhost:8787; A="Authorization: Bearer $ADMIN"; J="Content-Type: application/json"
# renomear, etiquetar e restringir o token da instância a alguns IPs (tags e ip_allowlist: admin)
curl -X PATCH $B/v1/instances/$ID -H "$A" -H "$J" -d '{"name":"Loja Centro","tags":["cliente-acme","vip"],"ip_allowlist":["203.0.113.7","10.0.0.0/8"]}'
# listar com filtros e paginação (sem limit: todas, como antes)
curl "$B/v1/instances?tag=cliente-acme&state=connected,qr_pending&limit=50" -H "$A"   # → {instances, next_cursor?}
# lote: start|stop|restart|delete por ids ou por etiqueta (até 100; delete exige confirm)
curl -X POST $B/v1/instances/bulk -H "$A" -H "$J" -d '{"action":"stop","tag":"cliente-acme"}'
# link de pareamento hospedado: o cliente abre e escaneia o QR (ou pede o código por telefone)
curl -X POST $B/v1/instances/$ID/pairing-links -H "$A" -H "$J" -d '{"ttl_seconds":600}'
# → {"url":"https://zap.exemplo.com/pair/#t=pl1....","expires_at":...}; DELETE no mesmo caminho revoga
# chave só de leitura (ou "send") para entregar a terceiros
curl -X POST $B/v1/instances/$ID/api-keys -H "Authorization: Bearer $TOK" -H "$J" -d '{"name":"bi","scopes":["read"]}'
# uso diário da instância
curl "$B/v1/instances/$ID/usage?from=2026-10-01" -H "$A"
```

- **Allowlist de IP**: `ip_allowlist` (instância e chaves) e `admin_allowed_ips`/`operator_allowed_ips` (config) aceitam IPs e CIDRs; fora da lista, `403 forbidden` em todas as rotas, inclusive `/mcp`. O `X-Forwarded-For` só é lido quando a conexão vem de `trusted_proxies` (da direita para a esquerda, parando no primeiro endereço não confiável); sem `trusted_proxies`, o cliente é o par TCP.
- **Chaves de API** (`lzk_`, presas a uma instância, só o hash é guardado, até 50 por instância, `expires_in_seconds` e `ip_allowlist` opcionais): `read` = rotas `GET` de leitura (instância, mensagens, fila, chats, contatos, mídia, grupos, comunidades, perfil, privacidade, bloqueados, eventos SSE, uso, status, canais e suas publicações, etiquetas e perfil comercial — nunca QR, configurações, hook de agente ou convites; nenhuma escrita de status, canal ou etiqueta); `send` = `POST /messages` e `GET /messages/{msg_id}`. Todo o resto responde `403`, inclusive `/mcp`, webhooks, ciclo de vida e a gestão de chaves. Uma chave também respeita a `ip_allowlist` da instância; no stream SSE ela nunca recebe `instance.qr` nem `instance.pair_code`, e um stream aberto é encerrado em até um heartbeat (15s) quando a chave é revogada, expira ou sai da allowlist. Rotacionar o token da instância revoga as chaves criadas com ele (as criadas pelo admin continuam). Tokens existentes não mudam.
- **Pareamento hospedado**: o link é assinado (HMAC-SHA256, chave `data/pairing_key`), registrado no banco (revogável), vale até 10 min ou até o primeiro pareamento concluído e nunca expõe o token da instância (a página só vê estado, QR e código). O token do link vai no fragmento `#t=` (não chega a logs nem a `Referer`) e a página o tira da barra de endereço; as chamadas da página usam o cabeçalho `X-Pairing-Token`. CSP estrita (`script-src 'self'`, sem inline), `Referrer-Policy: no-referrer`; num iframe só nas origens de `pairing_frame_ancestors` (vazio = não pode ser embutida). Web component: `<script src="$B/pair/embed.js">` + `<litezap-pair url="...">` (eventos `litezap-state`, `litezap-paired`, `litezap-expired`). Até 5 códigos por telefone e 5 "iniciar" por link. A base do link é `public_url` (ou `listen_addr`).
- `instance.paired` sai uma vez por pareamento (qr_pending → connected), com `via_link: true` quando um link foi consumido.

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
| `message.status` | `sent`, `delivered`, `read`, `played` (áudio de voz, vídeo ou visualização única reproduzidos) ou `failed` de uma mensagem nossa | `{id, wa_message_id?, chat, status, error?}` |
| `message.reaction` | reação recebida | mensagem com `type: "reaction"`, `text` = emoji (vazio = reação removida), `reaction_to` |
| `message.edited` | mensagem editada (pela API, pelo celular ou pelo contato; edições recebidas só valem do próprio autor, as demais são ignoradas) | `{id?, wa_message_id, chat, sender?, from_me, source, text, timestamp}` (`text` = texto novo; `id` vazio se a mensagem não está no log) |
| `message.revoked` | mensagem apagada para todos (pelo autor, ou por admin do grupo) | `{id?, wa_message_id, chat, sender?, from_me, source, timestamp}` |
| `message.poll_vote` | voto em enquete (o voto atual da pessoa; lista vazia = voto retirado) | `{poll_id?, poll_wa_message_id, chat, voter, push_name?, from_me, selected_options, unknown_options?, timestamp}` |
| `message.button_reply` | resposta a botão, lista, template ou mensagem interativa (só recebimento; o LiteZap não envia botões) | mensagem com `type: "button_reply"`, `text` = texto escolhido, `reply_to` = mensagem dos botões e `button_reply {kind: button\|list\|template\|interactive, id, title, description?}` |
| `chat.presence` | contato digitando, gravando áudio ou parando; contato assinado ficando online/offline. **Opt-in**: só com `presence.online` ligado e só para quem pede o tipo explicitamente (`chat.presence` ou `chat.*`; `*` não inclui). Repetições do mesmo estado em menos de 5s são descartadas | `{chat, sender, is_group?, state: composing\|recording\|paused\|online\|offline, last_seen?}` |
| `call.received` | chamada tocando (individual ou em grupo) | `{call_id, chat, caller, is_group?, media?: audio\|video, rejected?, timestamp}` (`rejected: true` = rejeitada por `calls.auto_reject`; só chamadas individuais ao vivo) |
| `call.accepted` | chamada atendida num aparelho da conta | mesmos campos |
| `call.ended` | chamada encerrada (uma vez por chamada) | mesmos campos + `reason` e `missed`. `reason`: `caller_hangup` (tocou sem atender até quem ligou desistir; `missed: true`), `accepted_elsewhere` (atendida no celular ou outro aparelho da conta; vem logo depois de `call.accepted`, no momento do atendimento, porque o aparelho vinculado não é avisado do fim da conversa), `ended` (encerrada depois de atendida), `rejected` (por `calls.auto_reject`), `rejected_by_peer` (o outro lado rejeitou e nenhum fim chegou em 5s), `timeout` (nenhum sinal em 2 min; `missed` se ninguém atendeu) ou outro motivo enviado pelo WhatsApp. O "reject" que um aparelho hospedado do WhatsApp (`hosted.lid`) manda logo após toda oferta é ignorado |
| `group.updated` | mudança num grupo em que o número está (feita no celular, por outro membro ou pela API); um evento por tipo de mudança | `{chat, kind, actor?, participants?, reason?, name?, description?, removed?, settings?, group?, timestamp}`. `kind`: `participants_added` (`reason: "invite"` = entrou por link), `participants_removed`, `participants_promoted`, `participants_demoted`, `join_request` (pedido de entrada com aprovação), `join_request_revoked` (pedido cancelado ou recusado), `subject` (`name`), `description` (`""` = removida), `photo` (`removed`), `settings` (`settings {announce?, locked?, approval_required?, ephemeral_seconds?}`; uma mudança na aprovação de entrada sai depois, com o valor lido do grupo, ou `approval_mode_changed: true` se a leitura falhar), `invite_link_reset`, `joined` (o número foi adicionado ou criou o grupo; `name`), `deleted`, `suspended`/`unsuspended`, `community_linked`/`community_unlinked` (`group`). `filters.chat: "group"` casa com ele |
| `chat.updated` | chat arquivado/desarquivado, fixado, silenciado, marcado como (não) lido, apagado ou limpo, ou mensagem favoritada, pela API (`source: "api"`) ou em qualquer aparelho da conta (`source: "device"`); uma vez por mudança (o eco do WhatsApp para ações da API não duplica) | `{chat, is_group?, changed: [archived\|pinned\|muted\|unread\|deleted\|cleared\|starred], archived, pinned, muted, muted_until?, unread, deleted?, cleared?, message_id?, wa_message_id?, starred?, source, timestamp}` (`muted` sem `muted_until` = até reativar) |
| `status.received` | status (story) novo de um contato. **Opt-in**: só para quem pede o tipo explicitamente (`status.received` ou `status.*`; `*` não inclui) | mensagem com `chat: "status@broadcast"`, `sender` = autor, `media.url` para baixar |
| `channel.message` | post novo num canal seguido. **Opt-in** como `status.received` | `{channel, server_id, wa_message_id, type, text?, media?, views?, reactions?, timestamp}` (`media` só com metadados) |
| `label.updated` | etiqueta do WhatsApp Business criada, renomeada, recolorida ou apagada, ou aplicada/removida de um chat ou mensagem, pela API (`source: "api"`) ou no celular (`source: "device"`); o sync completo do pareamento não gera eventos | `{kind: edit\|chat\|message, label_id, name?, color?, deleted?, chat?, wa_message_id?, labeled?, source, timestamp}` |
| `history.synced` | fim da ingestão de um history sync (um evento por sync, 15s depois do último bloco) | `{chats, messages, new_messages}` |
| `agent.failed` | uma chamada do hook de agente falhou (nada foi enviado) ou o limite de respostas pausou o chat | `{chat, message_id, wa_message_id, reason, error?, http_status?}`; `reason`: `timeout`, `http_error`, `network_error`, `invalid_response`, `send_failed`, `loop_guard`, `internal`; do atendente embutido também `not_configured`, `daily_cap`, `llm_error` |
| `agent.handoff` | o agente passou o chat para um humano (ação `handoff`) | `{chat, message_id, wa_message_id, note?}` |
| `agent.lead` | o atendente embutido qualificou um lead (ação `lead`) | `{chat, message_id, agent_id, agent_name, mode, customer_name, need, notes}` |
| `agent.appointment_requested` | o atendente embutido coletou um pedido de agendamento (nenhuma agenda é tocada) | `{chat, message_id, agent_id, agent_name, mode, customer_name, need, preferred_time, notes}` |
| `agent.order_draft` | o atendente embutido montou um pedido e pediu confirmação | `{chat, message_id, agent_id, agent_name, mode, items[{name, quantity, unit_price, subtotal, in_catalog}], total, currency, all_items_in_catalog, address, notes, customer_name}` |
| `instance.proxy_failed` | a instância não alcança o WhatsApp pelo proxy (entra em `reconnecting`/`proxy_unreachable`), repetido a cada 5 min enquanto durar; a volta é o próximo `instance.status` | `{reason, since, duration_seconds, outages_last_hour, repeat, error?}` (`error` = último erro de conexão do worker, sem usuário/senha do proxy, telefones nem tokens) |
| `instance.paired` | pareamento concluído (a instância esperava QR/código e conectou); não sai em reconexões | `{phone, via_link}` (`via_link: true` = um link de pareamento hospedado foi consumido) |

Mensagem: `{id, wa_message_id, chat, sender, from_me, is_group, push_name, source, type, text, media?, reaction_to?, timestamp}` mais o conteúdo estruturado quando houver: `reply_to` (wa id citado), `mentions`, `forwarded`, `location {latitude, longitude, name, address, url}`, `contacts [{display_name, vcard}]`, `poll {name, options, selectable}` e `link_preview {url, title, description}`; os mesmos campos de `GET /messages/{id}`. Tipos recebidos: os de envio, mais `unknown`. Votos chegam cifrados e só são decifrados para enquetes cujo segredo a sessão conhece (criadas ou recebidas depois do pareamento); as opções são casadas pelo SHA-256 contra as opções guardadas no log (`unknown_options` conta as que não casaram). `media` traz `{id, kind, mime, size, file_name, seconds, ptt, url}`; `url` é a rota autenticada de download (`GET /v1/instances/{id}/media/{media_id}`). Mensagens de history sync não geram eventos individuais (só o `history.synced`); uma mensagem que chega duas vezes (reconexão do worker) gera um único evento.

`agent.failed` e `agent.handoff` chegam com o hook de agente (marco 5).

### SSE

```bash
curl -N "localhost:8787/v1/instances/$ID/events?types=message.*,instance.status" -H "Authorization: Bearer $TOK"
curl -N "localhost:8787/v1/events" -H "Authorization: Bearer $ADMIN"   # todas as instâncias
```

Cada evento sai como `id: <evt>`, `event: <type>` e `data: <envelope>`; a cada 15s vai um comentário `: ping` para manter a conexão. `types` aceita tipos exatos, famílias (`message.*`) e `*` (que não inclui `chat.presence`, `status.received` nem `channel.message`; sem `types` também não: use `types=*,chat.presence,status.received`). Não há replay: eventos de quando o cliente estava desconectado não são reenviados (use webhooks para isso). Um cliente que fica mais de 256 eventos atrasado recebe `event: error` (`slow_consumer`) e é desconectado; reconecte.

### Webhooks

```bash
curl -s -X POST localhost:8787/v1/webhooks -H "Authorization: Bearer $TOK" \
  -d '{"url":"https://exemplo.com/litezap","events":["message.*","instance.status"]}'
# 201 {"id":"wh_...","instance_id":"inst_...","url":...,"events":[...],"version":1,"enabled":true,"secret":"whsec_..."}
```

- Com token de instância o webhook é sempre daquela instância. Com token admin, `instance_id` escolhe a instância; sem ele o webhook é global (todas as instâncias).
- `secret` é opcional (16 a 256 caracteres); sem ele um é gerado. **Ele só aparece na resposta da criação** (e de um `PATCH` que o troque; `{"secret":""}` gera um novo). Guarde-o.
- `PATCH` aceita `url`, `events`, `enabled`, `secret` e `filters`; `instance_id` não muda.
- **Filtros** (`filters`, opcional; `{}` remove): `chat` (`direct` ou `group`), `chats` (só estes chats; número com DDI ou JID), `exclude_chats` (todos menos estes), `message_types` (`text`, `image`, `video`, `gif`, `ptv`, `audio`, `document`, `sticker`, `location`, `contact`, `poll`, `reaction`, `button_reply`, `unknown`) e `from_me` (`true` só as nossas, `false` exclui as nossas). Cada filtro só restringe eventos que têm aquele campo: `instance.*` sempre passam; `message_types` vale para `message.received/sent/reaction/button_reply`; `from_me` para eventos de mensagem (não se aplica a `message.status`, sempre sobre mensagens nossas). Ex.: `{"url":"...","events":["message.received"],"filters":{"chat":"direct","message_types":["text","audio"]}}` nunca recebe grupo.
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
| `target` | `url` | `url` (POST assinado para `url`) ou `builtin` ([Atendente LiteZap embutido](#atendente-litezap-embutido), sem URL) |
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

`context` termina com a própria mensagem. `origin` diz quem escreveu cada uma: `contact` (cliente), `agent` (respostas do próprio agente), `human` (alguém no celular), `automatic` (mensagens automáticas, como o aviso de ligação recusada), `api` (outros sistemas pela API) ou `history`. Trate `automatic`/`api` como contexto, nunca como falas do agente.

**Response** `200` `{"reply": "texto" | {MessagePayload sem "to"}, "actions": [...]}` ou `204` (nada a fazer). `reply` objeto aceita os tipos de conteúdo (`text`, `image`, `video`, `audio`, `document`, `sticker`, `gif`, `ptv`, `location`, `contact`, `poll`); sempre vai para o chat que disparou a chamada, uma vez por mensagem (`client_message_id = agent:<id>`), e expira em 1h se não sair. Ações: `{"type":"mark_read"}`, `{"type":"react","emoji":"👍"}`, `{"type":"pause","minutes":60}` (1–10080), `{"type":"handoff","note":"..."}` (desliga o hook no chat até reativação e emite `agent.handoff`). A resposta é enfileirada primeiro; só se ela for aceita saem `mark_read` e `react`. `pause` e `handoff` valem sempre, mesmo se a resposta for recusada. Ação desconhecida é ignorada; resposta inválida, erro HTTP, redirecionamento ou timeout → `agent.failed`, nada é enviado, sem retry.

Regras:

- Só `message.received` (nunca `status@broadcast`, canais, mensagens próprias); grupos só com `groups_enabled`.
- **Uma chamada por vez por chat**; mensagens que chegam durante a chamada são agrupadas: a próxima chamada leva a mais nova (as outras estão no `context`). No máximo 8 chamadas ao mesmo tempo no servidor.
- **Pausa por humano**: mensagem `from_me` cujo `wa_message_id` não foi enviado pelo LiteZap (você respondeu pelo celular) pausa o hook no chat por `human_pause`. Se isso acontece durante uma chamada, a resposta do agente é descartada.
- Com o envio suspenso pelo operador, o hook nem é chamado (aparece como `skipped` em `calls`). Mensagens antigas (mais de `max(2×timeout, 5min)`, como o acúmulo entregue depois de ficar offline) também são puladas (`stale`), assim como tipos `unknown`.
- Respostas do hook ainda na fila são canceladas quando um humano assume o chat, quando o limite de respostas estoura e não saem depois de 1h. `mentions: ["all"]` não é aceito numa resposta do agente.

**Segurança e risco de banimento**: trate o texto das mensagens como **não confiável** no seu agente (prompt injection: o cliente pode escrever "ignore as instruções e..."); nunca dê ao modelo segredos ou ferramentas que ele não deva usar a pedido de um cliente. Respostas automáticas só vão para quem escreveu primeiro e respeitam o ritmo da fila, mas um robô respondendo outro robô gera volume anormal: mantenha o `max_replies_per_window` baixo.

### Atendente LiteZap (embutido)

Com `target: "builtin"` o atendente roda **dentro do motor** — nada para hospedar — e herda todo o maquinário acima (fila, pausa por humano, loop guard, um chat por vez, grupos desligados). Você escolhe o provedor, cria agentes treinados com o contexto do negócio e liga.

```bash
# provedor (só admin): Claude (padrão claude-sonnet-5-5) ou qualquer servidor OpenAI-compatível
curl -s -X PUT $LZ/v1/instances/$ID/attendant -H "Authorization: Bearer $ADMIN" \
  -d '{"provider":"anthropic","api_key":"sk-ant-...","daily_call_cap":300}'
# -d '{"provider":"openai","base_url":"http://127.0.0.1:11434/v1","model":"llama3.1"}'   (Ollama, LM Studio, OpenAI...)
# áudio opcional (OpenAI-compatível): "audio":{"base_url":"https://api.openai.com/v1","transcription_model":"whisper-1","speech_model":"gpt-4o-mini-tts","voice":"alloy"},"audio_api_key":"sk-..."

# agente: modo vendedor | pedido | sdr | suporte | custom (o roteiro não enviado vem do modo)
curl -s -X POST $LZ/v1/instances/$ID/attendant/agents -H "Authorization: Bearer $TOK" -d '{"name":"Bia","mode":"pedido",
  "business":"Pizzaria do Zé, Rua A 10. Entrega em 40 min.",
  "knowledge":[{"kind":"product","title":"Pizza Margherita","price":45},{"kind":"hours","title":"Seg a Dom","content":"18h às 23h"}]}'

# testar sem enviar nada (chamada real ao provedor; conta no limite)
curl -s -X POST $LZ/v1/instances/$ID/attendant/simulate -H "Authorization: Bearer $TOK" \
  -d '{"messages":[{"from":"automatic","text":"Não atendemos ligações"},{"text":"quero 2 margheritas pra Rua B 5"}]}'

# ligar
curl -s -X PUT $LZ/v1/instances/$ID/agent-hook -H "Authorization: Bearer $TOK" -d '{"target":"builtin","enabled":true}'
```

Outras rotas: `GET /attendant` (sem chaves: `api_key_set`), `GET /attendant/presets`, `GET/PUT/DELETE /attendant/agents/{agent_id}`, `POST /attendant/agents/{agent_id}/activate` (qual agente responde; padrão: o primeiro), `GET /attendant/usage?days=7` (chamadas, tokens e custo estimado por dia). Painel: aba **Atendente IA** (provedor, agentes, base de conhecimento, simulador). MCP: `get_attendant`, `get_attendant_agent` (leitura).

- **Modos**: `vendedor` (catálogo, conduz para a compra, `agent.lead`), `pedido` (monta o pedido, confirma itens e endereço, `agent.order_draft` com itens, total recalculado pelos preços do catálogo e endereço), `sdr` (coleta nome, necessidade e horário: `agent.lead` / `agent.appointment_requested`; nenhuma agenda é tocada — use webhook/n8n), `suporte` (só a base de conhecimento). Todos podem passar para humano (`handoff`). Cada modo é editável: objetivo, roteiro, tom, critérios de passagem e ações permitidas.
- **Multimodal**: mensagem de voz → transcrição (`audio.transcription_model`; sem ele o agente só sabe que chegou um áudio); imagens (e PDFs no Claude) vão para o modelo com `vision`; resposta em voz (`voice_reply: when_audio | always` + `audio.speech_model`, nota de voz Ogg/Opus; falhou, vai texto); ligação recusada com `calls.auto_reject` + `call_followup` no agente (opt-in explícito) → mensagem da IA ("vi que você ligou…") pela fila, uma por chamador no cooldown (falhou, ficou em silêncio ou foi pulada: sai o `reject_message`, se houver).
- **Segurança**: chaves cifradas (AES-256-GCM, `data/attendant_key` 0600 — faça backup junto do banco) e nunca devolvidas; provedor e chaves só com token admin; prompt com as mensagens do cliente como dados não confiáveis e marcador canário (resposta que vaza o prompt ou uma chave é bloqueada); a resposta vai sempre para o chat que disparou; o telefone não vai para o modelo (pseudônimo); `daily_call_cap` (padrão 300/dia, inclui áudio) com custo logado, mais `chat_daily_call_cap` (padrão 40 por conversa/dia: uma conversa não esgota a instância) e `simulate_daily_cap` (padrão 50/dia para o simulador); falhas viram `agent.failed` (`not_configured`, `daily_cap`, `llm_error`, `invalid_response`).
- **Assinatura x chave**: a assinatura Claude (Pro/Max) só serve para testes locais com um agente seu, como o `examples/agent-claude` rodando na sua máquina. O atendente embutido e qualquer uso em produção exigem **chave de API** do provedor.

## MCP (Claude, ChatGPT e outros clientes de IA)

O LiteZap expõe as mesmas operações como ferramentas do [Model Context Protocol](https://modelcontextprotocol.io), para um assistente de IA ler conversas e enviar mensagens pelo seu número. Dois jeitos de conectar, com as mesmas ferramentas:

- **HTTP** (streamable HTTP) em `http://<listen_addr>/mcp`, no próprio `litezap serve`, com o mesmo `Authorization: Bearer <token>` da API.
- **stdio**: `litezap mcp`, uma ponte para clientes que iniciam um processo local (Claude Desktop, Claude Code). Ela não sobe supervisor: conversa via REST com um `litezap serve` já rodando, configurada por `LITEZAP_URL` (padrão `http://127.0.0.1:8787`) e `LITEZAP_TOKEN` (obrigatório).
- **App de bandeja**: `litezap-app mcp` faz o mesmo sem variáveis (lê endereço e token da pasta do app) e o menu **Conectar a IA** configura o cliente em um clique; veja [App de bandeja](#app-de-bandeja-litezap-app).

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
| `list_groups` · `get_group` | grupos (ou comunidades) do número; metadados com participantes e admins |
| `create_group` · `update_group` | cria um grupo (até 10 participantes) · muda nome, descrição e configurações; com aviso de risco de banimento |
| `manage_group_participants` | adiciona, remove, promove ou rebaixa (destrutiva; adições só de contatos conhecidos, com cotas e ritmo) |
| `get_group_invite_link` | link de convite (nunca redefine) |
| `get_profile_picture` · `get_contact_about` | foto (URL) e recado/empresa de um contato; gastam a cota de consultas (30/min) |
| `list_blocked` · `block_contact` · `unblock_contact` | bloqueados; bloquear (marcada como destrutiva, só a pedido do usuário) e desbloquear |
| `update_chat` | arquivar, fixar ou silenciar (`mute_hours`) um chat; nada é enviado ao contato |
| `list_status_updates` | status recebidos dos contatos e os nossos, do log (publicar status não é ferramenta: vai para todos os contatos) |
| `list_channels` · `get_channel` · `get_channel_messages` | canais seguidos; um canal por JID ou link de convite; posts recentes |
| `follow_channel` | segue ou deixa de seguir um canal (3/min, 10/h, 30/dia) |
| `list_labels` · `label_chat` · `get_business_profile` | etiquetas do WhatsApp Business e suas conversas; etiquetar/desetiquetar um chat; perfil comercial (conta Business) |

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
6. Chamadas e presença (marco 7), com o SSE aberto em `?types=*,chat.presence`:
   - Ligue do celular do contato para o número conectado: `call.received` e, ao desligar sem atender, `call.ended` com `missed: true`; atendendo no celular, `call.accepted` antes do `call.ended`. Ligue `{"calls":{"auto_reject":true,"reject_message":"..."}}`: a chamada cai sozinha em menos de 3s, o contato recebe a mensagem pela fila (uma vez a cada 10 min) e os eventos saem com `rejected: true` / `reason: "rejected"`.
   - Ligue `{"presence":{"online":true}}` (o celular conectado para de mostrar notificações enquanto estiver ligado) e comece a digitar ou gravar áudio no chat do contato: `chat.presence` com `composing`/`recording`/`paused`. `POST /presence/subscribe {"to":"<número>"}` passa a mandar `online`/`offline` (com `last_seen` quando o contato mostra).
   - Mande um áudio de voz pela API e ouça no celular do contato: `message.status` com `played`.

### Testar grupos

Com a instância conectada e o contato de teste já tendo conversado com o número (assim ele passa na checagem anti-ban sem gastar consulta), e o SSE aberto em `?types=group.*`:

1. Crie um grupo com o contato: `POST /groups {"name":"Teste LiteZap","participants":["<número do contato>"]}`. O celular do contato mostra o grupo; a resposta traz `results[0].status: "ok"` e sai `group.updated` `joined`.
2. Leia e mude: `GET /groups/<jid>` (participantes com o número como admin/`super_admin`), `PATCH` com `name`, `description` e `settings` (`announce`, `locked`, `ephemeral_seconds: 86400`) e confira no celular; mude o nome pelo celular: `group.updated` `subject` e o chat em `GET /chats` com o nome novo.
3. Participantes: promova e rebaixe o contato (`participants_promoted`/`_demoted` no SSE), remova e adicione de novo (um segundo lote em menos de 10s responde `429`). Peça para o contato sair pelo celular: `participants_removed` com o `actor` dele.
4. Link: `GET /invite`, `POST /invite/reset`, `POST /groups/preview {"code":"<link antigo>"}` → `410 invite_revoked`. Com `approval_required: true`, entre pelo link novo do celular do contato: `group.updated` `join_request` e `GET /requests`; aprove com `POST /requests`.
5. Foto: `PUT /photo {"url":"https://…/foto.jpg"}` e `DELETE /photo` (`group.updated` `photo`).
6. Comunidade (opcional): `POST /communities {"name":"Teste"}`, `POST /communities/<cjid>/groups {"group":"<jid>"}` e `GET /communities/<cjid>`.
7. Por último, `POST /groups/<jid>/leave`.

### Testar status, canais e etiquetas (marco 10)

Com o SSE aberto em `?types=status.received,channel.message,label.updated` e o celular de um contato de teste (que tenha o número salvo e esteja na privacidade do status):

1. `POST /status?wait=true {"type":"text","text":"Teste LiteZap","background_color":"#204060"}`: o contato vê o status (fundo azul) em até 1 min; o número também mostra o status no próprio celular. Repita com `{"type":"image","media":{"url":"..."}}` e apague um com `DELETE /status/<id>`.
2. O contato posta um status: chega `status.received` (com `media.url` para imagem/vídeo) e ele aparece em `GET /status`; responda com `POST /status/<id>/reply {"text":"..."}`: o contato recebe a resposta citando o status.
3. `GET /status/privacy` bate com Configurações → Privacidade → Status no celular.
4. Canais: abra um canal público por link (`POST /channels/preview`), siga (`/follow`), confira no celular, leia `GET /channels/<jid>/messages` e reaja a um post (`/reactions`). Crie um canal (`POST /channels`) e publique texto e imagem (`POST /channels/<jid>/messages`): os posts aparecem no canal no celular. Com o canal seguido, um post novo gera `channel.message`.
5. Etiquetas (conta Business): `GET /labels` lista as etiquetas do celular; `POST /labels {"name":"Teste","color":3}` aparece no celular; `PUT /labels/<id>/chats/<número>` etiqueta a conversa no celular; mude uma etiqueta no celular: `label.updated` com `source: "device"`. Em conta pessoal, `/labels` responde `409 business_account_required`.

### Testar chats, contatos, perfil e privacidade (marco 9)

Com o SSE aberto em `?types=chat.updated` e o celular à mão (`JID` = um chat de `GET /chats`):

1. `PATCH /chats/$JID {"archived":true}`: em até 10s a conversa aparece arquivada no celular; um único `chat.updated` com `source: "api"`. Repita com `{"pinned":true}`, `{"muted":true,"mute_seconds":28800}` e `{"unread":true}`. Se responder `app_state_not_synced`, espere o celular sincronizar (logo após parear) e tente de novo.
2. No celular, desarquive, fixe e silencie a mesma conversa: chega `chat.updated` com `source: "device"` e `GET /chats/$JID` mostra o novo estado.
3. `GET /contacts/<número>/picture` de um contato com foto devolve a URL; de um sem foto (ou que esconde a foto), `404 not_found`. `?download=1` baixa o JPEG. `GET /contacts/<número>/profile` mostra o recado.
4. `POST /contacts/<número>/block`: o contato aparece bloqueado no celular; `unblock` desfaz.
5. `PATCH /profile {"about":"teste"}` e `PUT /profile/picture {"url":"..."}`: o celular mostra o novo recado e a nova foto; `PATCH /profile {"name":"..."}` muda o nome que aparece para quem não salvou o número.
6. `PATCH /privacy {"last_seen":"contacts"}`: em Configurações > Privacidade do celular o visto por último muda.
7. Ligue `{"auto_read":{"enabled":true}}` e mande uma mensagem do celular do contato: 2–5s depois ela aparece lida (tiques azuis se a confirmação de leitura estiver ligada).

### Testar o pareamento hospedado (marco 11)

1. Com `public_url` apontando para o servidor, crie uma instância nova e gere o link: `POST /v1/instances/$ID/pairing-links`.
2. Abra a `url` num navegador (ou celular de outra pessoa): a página mostra o QR. Escaneie com o celular do número: a página mostra "WhatsApp conectado", sai `instance.paired` com `via_link: true` e `phone`.
3. Abra o mesmo link de novo depois de 2 minutos: "link expirou ou já foi usado".
4. Gere outro link para uma instância deslogada e use "Prefiro conectar com o número de telefone": o código aparece na página e funciona em Aparelhos conectados > Conectar com número de telefone.
5. Embutido: ponha a origem do seu app em `pairing_frame_ancestors`, use `<litezap-pair>` e confirme que de outra origem o navegador bloqueia o iframe.

### Testar o atendente IA

1. Libere destinos locais (`webhook_allow_private_urls: true`) e reinicie o `serve`.
2. No painel, aba **Atendente IA** da instância: URL `http://127.0.0.1:8790/`, marque "Responder automaticamente" e salve; copie o segredo que aparece.
3. Rode o exemplo: `cd examples/agent-claude && ANTHROPIC_API_KEY=... LITEZAP_AGENT_SECRET=<segredo> go run .`
4. De outro celular, mande "oi" para o número pareado: a resposta sai pela fila (com "digitando…") e aparece em "Chamadas recentes".
5. Responda você mesmo pelo celular pareado: o chat aparece como pausado por 30 min em "Por conversa"; mensagens novas do contato não chamam o agente até lá ("Reativar" encerra a pausa).
6. Peça "quero falar com um atendente": o agente responde e faz `handoff` (chat desligado, evento `agent.handoff` em "Eventos ao vivo").

### Testar o Atendente LiteZap (embutido)

1. Aba **Atendente IA**: no bloco "Atendente LiteZap", escolha Anthropic, cole a chave de API e salve (o status fica "pronto"). Para testar sem custo, use um servidor local OpenAI-compatível (Ollama: base URL `http://127.0.0.1:11434/v1`, modelo `llama3.1`).
2. "Novo agente" no modo **Facilitador de pedido**: coloque 2–3 produtos com preço e o endereço da loja; salve.
3. Simulador: `cliente: quero 2 margheritas pra Rua B 5` → resposta com resumo e total; o evento `agent.order_draft` aparece abaixo. Nada sai no WhatsApp.
4. Em cima, "Quem responde: Atendente LiteZap", marque "Responder automaticamente" e salve.
5. De outro celular: "oi, vocês entregam?" → resposta pela fila. Faça o pedido → `agent.order_draft` em "Eventos ao vivo".
6. Mande um **áudio** (com transcrição configurada) e uma **foto** de um produto; com `voice_reply: when_audio` e modelo de voz, a resposta ao áudio chega como nota de voz.
7. Ligue `calls.auto_reject` nas configurações e marque "Ligação recusada → mandar mensagem" no agente; ligue para o número: a chamada é recusada e chega "vi que você ligou…".
8. Responda pelo celular pareado: o atendente pausa 30 min naquela conversa. Peça "quero falar com uma pessoa": `handoff`.

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
- **Conversas — ações** (marco 9): arquivar, fixar, silenciar (8h, 1 semana, sempre), marcar como (não) lida, apagar; ver foto, recado e empresa do contato (gasta a cota de consultas) e bloquear/desbloquear. A lista marca chats fixados, arquivados, silenciados e não lidos e se atualiza com `chat.updated`.
- **Perfil**: nome, recado e foto da conta, leitura automática, privacidade (visto por último, online, foto, recado, grupos, confirmação de leitura, chamadas, mensagens, temporárias em conversas novas) e lista de bloqueados.
- **Configurações**: ritmo, rampa, janela, fuso, proxy e ritmo inseguro (só são enviados os campos alterados; a senha mascarada do proxy nunca é reenviada).
- **Atendente IA**: configurar o hook de agente (URL, segredo, timeout, contexto, pausa por humano, limite de respostas, ignorados), ligar/desligar por conversa, reativar chats pausados e ver as chamadas recentes com motivo das falhas.
- **Grupos**: grupos do número, metadados e configurações, participantes (promover, rebaixar, remover, adicionar com os limites anti-ban), link de convite (mostrar, copiar, redefinir), criar grupo e sair.
- **Status** (marco 10): publicar status de texto (com cor de fundo), imagem ou vídeo, ver quem recebe (privacidade do status), status recebidos dos contatos com mídia, responder e apagar os nossos.
- **Canais**: canais seguidos, abrir por link de convite, criar, seguir/deixar de seguir, silenciar, ver publicações, reagir e publicar (como dono/admin).
- **Etiquetas** (conta Business): perfil comercial, criar, renomear, apagar e aplicar etiquetas a conversas; atualiza com `label.updated`.
- **Webhooks**: criar (o segredo aparece uma vez), ligar/desligar, excluir, ver entregas por status e reenviar as da dead-letter. Há também a página de webhooks globais.
- **Eventos ao vivo**: os mesmos eventos dos webhooks, com o JSON de cada um.
- **Editar** (marco 11): renomear a instância, etiquetas e IPs permitidos para o token; as etiquetas aparecem na lista. Na aba **Parear**, "Link de pareamento para o cliente" gera (2, 5 ou 10 min), copia e revoga links da página hospedada.

**Login**: cole o token admin (`data/admin_token`). Ele fica só no navegador (`sessionStorage`, ou `localStorage` se marcar "lembrar"), vai apenas no header `Authorization` (nunca na URL) e "Sair" o apaga. "Lembrar" vale para o endereço (host e porta): não marque em computador compartilhado. Não há cookies, então não há superfície de CSRF; o painel tem CSP estrita (`script-src 'self'`, sem scripts inline) e monta a página só com texto (nada de HTML vindo de dados). Para desligar o painel num servidor: `disable_dashboard: true`.

Num servidor exposto, use HTTPS (proxy reverso) antes de abrir o painel pela internet: o token admin trafega em cada requisição.

### App de bandeja (`litezap-app`)

Para quem não quer terminal: um executável que sobe o motor no próprio processo, põe o ícone do LiteZap na bandeja/barra de menus e abre o painel já logado.

#### Baixar o app

| Sistema | Download | Primeira abertura |
|---|---|---|
| macOS 12+ (Apple Silicon e Intel) | [LiteZap-macos.dmg](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap-macos.dmg) | Arraste para Aplicativos. Sem assinatura da Apple: botão direito no app > **Abrir** > **Abrir** (ou Ajustes do Sistema > Privacidade e Segurança > **Abrir Mesmo Assim**) |
| Windows 10/11 (64 bits) | [LiteZap-windows.zip](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap-windows.zip) | Extraia e abra `LiteZap.exe`. No aviso do SmartScreen: **Mais informações** > **Executar assim mesmo** |
| Linux (amd64/arm64) | [LiteZap-linux-amd64.tar.gz](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap-linux-amd64.tar.gz), [arm64](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap-linux-arm64.tar.gz) | `tar xzf` e rode `LiteZap/litezap-app`; no GNOME, a extensão AppIndicator |

Cada arquivo tem um `.sha256` ao lado na release. Depois: escaneie o QR no painel e use **Conectar a IA** (abaixo).

- Menu: estado (`N de M conectada(s)`), **Abrir painel**, **Abrir pasta de dados**, **Ver log**, **Conectar a IA** (Claude Desktop, Claude Code, Cursor), **Sair** (encerra as instâncias com calma antes de sair).
- **Conectar a IA** (menu e aba do painel): um clique grava a entrada `mcpServers.litezap` na configuração do cliente apontando para o próprio executável do app com `["mcp"]`; não há token para copiar. Arquivos: Claude Desktop em `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS), `%APPDATA%\Claude\claude_desktop_config.json` (Windows) ou `~/.config/Claude/claude_desktop_config.json` (Linux); Claude Code via `claude mcp add --scope user` quando o `claude` é encontrado, senão `~/.claude.json`; Cursor em `~/.cursor/mcp.json`. Os outros servidores e chaves ficam intactos, a escrita é atômica, o original ganha uma cópia `<arquivo>.litezap.bak` antes da primeira escrita e um JSON inválido não é tocado (o app mostra o erro). Mostra "conectado", corrige uma entrada antiga (app movido) e oferece **Desconectar** (remove só a entrada `litezap`). No macOS, conecte com o app já na pasta Aplicativos (de dentro do `.dmg` o caminho deixa de existir). Depois, reinicie o cliente de IA.
- **`litezap-app mcp`**: servidor MCP stdio que conversa com o motor do app aberto, com o token admin lido da pasta de dados (arquivo só do usuário). Ele não sobe um motor próprio (brigaria pela porta e pelas sessões com a bandeja): com o app fechado, as ferramentas respondem `app_not_running` ("abra o app LiteZap"); se o app nunca foi aberto, sai com erro.
- Pasta de dados: `~/Library/Application Support/LiteZap` (macOS), `%APPDATA%\LiteZap` (Windows), `$XDG_DATA_HOME/litezap` ou `~/.local/share/litezap` (Linux). Dentro dela: `data/` (sessões, banco, `admin_token`), `litezap.log` e, se quiser, um `litezap.yaml` (mesmas chaves do servidor; `LITEZAP_*` continuam valendo). `LITEZAP_APP_DIR` troca a pasta (útil para testes).
- API e painel em `127.0.0.1:8787` (só a sua máquina). Se a porta estiver ocupada (outro LiteZap aberto?), o menu mostra o erro.
- **Login em um clique**: cada "Abrir painel" cria um código de uso único (vale 1 minuto; usar um descarta os outros pendentes) e abre `http://127.0.0.1:8787/app/#code=...`. O código vai no fragmento (não chega a logs nem a `Referer`), o painel o apaga da barra de endereço e o troca pelo token admin em `POST /app/api/session`. Esse endpoint só existe no app de bandeja com o servidor em loopback e só aceita conexões de loopback com `Host` de loopback (contra DNS rebinding), `Origin` igual ao host e `Content-Type: application/json`. `litezap serve` puro não aceita códigos (login colando o token). Limite conhecido: num computador compartilhado, outro usuário local que veja a linha de comando do navegador no instante da abertura poderia tentar usar o código antes; ele vale uma vez só e por 1 minuto (e o painel legítimo mostraria a falha).
- `litezap-app --no-browser` sobe sem abrir o navegador.

Compilar (o `litezap` principal continua sem CGO; só o app de bandeja usa [fyne.io/systray](https://github.com/fyne-io/systray)):

| SO | Comando | Observações |
|---|---|---|
| macOS | `go build -o litezap-app ./cmd/litezap-app` ou `cmd/litezap-app/package-macos.sh` (gera `dist/LiteZap.app` com ícone, só na barra de menus; `UNIVERSAL=1` junta arm64 e amd64, `DMG=1` gera `dist/LiteZap-macos.dmg`) | precisa de CGO e das Xcode Command Line Tools (`xcode-select --install`); sem outras dependências |
| Windows | `GOOS=windows go build -ldflags "-H windowsgui" -o litezap-app.exe ./cmd/litezap-app` | sem CGO; `-H windowsgui` evita a janela de console. Não testado no Windows ainda |
| Linux | `CGO_ENABLED=0 go build -o litezap-app ./cmd/litezap-app` | sem CGO; a bandeja usa DBus (StatusNotifierItem): GNOME precisa da extensão AppIndicator |

Ainda não (em breve): instaladores `.msi` e `.deb`, assinatura de código e notarização (no macOS, abra com botão direito > Abrir na primeira vez), auto-update, iniciar com o sistema, túnel opcional, envio de mídia pelo painel e login no painel com token de instância.

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

- SSE não tem replay (`Last-Event-ID` é ignorado).
- Grupos: convite por mensagem para quem só aceita convite (`privacy_invite_required`) não é enviado; desativar comunidade e listar participantes de todos os grupos da comunidade não existem; pedidos de entrada (`join_request`/`join_request_revoked`) dependem de notificações que o whatsmeow não interpreta (lidas pelos nomes que outros clientes documentam); o valor da aprovação de entrada num evento vem de uma consulta ao grupo (o whatsmeow não diz se ligou ou desligou); um `PATCH` que falha no meio não diz quais campos já foram aplicados (consulte o grupo); `pending` ao entrar por link é deduzido da prévia (o grupo exige aprovação); o código de erro por participante segue o que os clientes do WhatsApp documentam, sem garantia.
- A garantia "pelo menos uma vez" vale a partir do outbox. Antes dele o evento passa por um buffer em memória (até 10.000): um crash do supervisor entre gravar a mensagem no log e gravar a entrega, ou um buffer cheio (logado), perde o evento para os webhooks. Um shutdown normal grava o buffer antes de sair.
- `wait_for_events` (MCP) só vê eventos publicados enquanto espera; para o que chegou antes, use `get_messages`.
- Streams SSE checam o token só na conexão: um stream aberto continua depois de o token ser rotacionado (reconecte para aplicar).
- Se um recibo chegar antes da confirmação do envio, sai `message.status: delivered` (ou `read`) antes de `message.sent`, e o `message.status: sent` é omitido.
- Sem busca full-text (marco 2b).
- Marco 9: "limpar conversa" e mensagens temporárias por chat não existem; pareamento por passkey não é suportado (o WhatsApp exige uma asserção WebAuthn do site dele; use QR ou código); as listas de exceção da privacidade só no celular; o JID usado nas ações de chat em conversas diretas (LID quando a conta já migrou) ainda precisa de validação com celular real.
- Marco 10: os destinatários de um status são sempre os da privacidade do status do celular (não dá para escolher por envio) e, com agenda grande, publicar demora (cada contato recebe uma cópia cifrada); a lista de contatos usada vem da agenda sincronizada do celular. Status dos contatos só chegam com a instância conectada (não há histórico). Mídia de publicações de canais recebidas não é baixada; marcar publicações de canal como vistas, leitura automática de status, eventos de agenda e produtos/catálogo não existem. As cotas de canais e etiquetas (seguir, silenciar, criar, reagir, etiquetar) ficam em memória (reiniciar zera; os tetos diários de status e de publicações em canal são persistentes). Etiquetar uma mensagem nossa ou de outra pessoa em grupo usa o índice do app state deduzido do de favoritos (validar com celular real). Recolorir uma etiqueta embutida do WhatsApp exige informar o nome. A detecção de conta Business usa o nome comercial gravado no pareamento.
- Botões, listas e carrossel não são enviados (instáveis no WhatsApp); respostas a eles chegam como `unknown`.
- Votos em enquetes criadas antes do pareamento não são decifrados (a sessão não tem o segredo). Votos não ficam no log, só no evento `message.poll_vote`.
- Fixar mensagem não emite evento, e fixações feitas pelo celular não são registradas.
- A prévia de link `auto` lê só `og:*`/`twitter:*`/`<title>` da página (sem JavaScript); miniatura só de JPEG, PNG ou GIF.
- Mídia recebida baixada fica em `data/media/` para sempre, a menos que `media_retention_days` esteja definido.
- Marco 11: chaves de API só têm os escopos `read` e `send` e valem para uma instância; não há metadados livres além de `tags`; `litezap_worker_memory_rss_bytes` só existe no Linux; o rate limit da página de pareamento é em memória (reiniciar o servidor zera); a página de pareamento é só em português.
- A exportação (`POST /v1/export`) não inclui as sessões do WhatsApp: restaurar em outro servidor exige parear de novo. A importação ainda não existe.
- Mídia passa entre supervisor e worker por arquivos em `data/` (mesma máquina). Um spawner em container (nuvem) vai precisar de um volume compartilhado.
- Se o supervisor morrer entre o worker entregar um evento e o supervisor gravá-lo, esse evento se perde (sem confirmação de entrega worker→supervisor ainda).
- Suporte a Windows não testado (compila, mas não foi executado).

A lista completa do que foi adiado para o marco 2b (busca, retenção, confirmação de entrega, nome de grupos, métricas) está na seção "Marco 2b" de `docs/superpowers/plans/2026-10-17-litezap-m2-messaging.md`.

## Docker

```bash
docker compose up -d --build   # imagem distroless, binário estático, usuário não root, volume /data
```

Imagens prontas de cada release: `ghcr.io/douglasdf99/litezap:<versão>` e `:latest` (troque `build: .` por `image: ghcr.io/douglasdf99/litezap:latest` no `docker-compose.yml`). A imagem não tem `HEALTHCHECK` (sem shell nem cliente HTTP); aponte o probe do orquestrador para `/healthz` com o token admin ou para `LITEZAP_OPS_LISTEN_ADDR`.

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

Como contribuir: [CONTRIBUTING.md](CONTRIBUTING.md). Segurança: [SECURITY.md](SECURITY.md). Releases saem por tag `v*` (GitHub Actions + GoReleaser, `.goreleaser.yaml`).
