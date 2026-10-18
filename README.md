# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

Licença: Apache-2.0. Roda no seu PC, no seu servidor ou na nuvem gerenciada do LiteZap.

> Status: marco 4 (envio com fila persistente, recebimento, chats, histórico, mídia, proxy por instância, eventos com webhooks assinados e SSE, servidor MCP em `/mcp` e `litezap mcp`). O hook de agente vem no próximo marco.
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
| `webhook_allow_private_urls` | `LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS` | `false` (`true` libera webhooks para `127.0.0.1`, rede local etc.) |
| `webhook_retention_days` | só no arquivo | `7` (entregas concluídas) |
| `webhook_failed_retention_days` | só no arquivo | `30` (dead-letter) |
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
| `GET /v1/instances/{id}/events[?types=]` (SSE) | instância ou admin |
| `GET /v1/events[?types=]` (SSE, todas as instâncias) | admin |
| `POST /v1/webhooks` · `GET /v1/webhooks[?instance_id=]` | admin (global ou de uma instância) ou instância (só a própria) |
| `GET\|PATCH\|DELETE /v1/webhooks/{id}` | idem (webhook de outra instância responde 404) |
| `GET /v1/webhooks/{id}/deliveries[?status=pending\|delivered\|failed&limit=&cursor=]` | idem |
| `POST /v1/webhooks/{id}/deliveries/{delivery_id}/retry` | idem |
| `GET /v1/me` → `{admin, instance_id}` | qualquer token |
| `POST /mcp` (MCP streamable HTTP, seção "MCP") | instância ou admin |
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
| `history.synced` | fim da ingestão de um history sync (um evento por sync, 15s depois do último bloco) | `{chats, messages, new_messages}` |

Mensagem: `{id, wa_message_id, chat, sender, from_me, is_group, push_name, source, type, text, media?, reaction_to?, timestamp}`, os mesmos campos de `GET /messages/{id}`. `media` traz `{id, kind, mime, size, file_name, seconds, ptt, url}`; `url` é a rota autenticada de download (`GET /v1/instances/{id}/media/{media_id}`). Mensagens de history sync não geram eventos individuais (só o `history.synced`); uma mensagem que chega duas vezes (reconexão do worker) gera um único evento.

Ainda não emitidos (o worker não reporta esses fatos): `message.revoked`, `chat.presence`, `group.updated`. `agent.failed` e `agent.handoff` chegam com o hook de agente (marco 5).

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
| `send_message` | envia texto; espera o resultado por padrão (`timeout_seconds` 30, máx. 120) |
| `send_media` | envia imagem, vídeo, áudio (ou nota de voz) ou documento a partir de uma URL pública |
| `get_message_status` | `queued`, `in_flight`, `sent`, `delivered`, `read` ou `failed` |
| `list_chats` | chats do log, mais recentes primeiro, com não lidas (paginado) |
| `get_messages` | mensagens de um chat, mais novas primeiro (paginado por `cursor`) |
| `mark_read` | marca o chat como lido (o contato vê os tiques azuis) |
| `list_contacts` / `get_contact` | contatos da sessão (filtro `query`, `limit`) |
| `check_numbers` | até 10 números por chamada; respeita o cache e a cota de consultas da instância |
| `wait_for_events` | espera até 60s por eventos novos da instância (ex.: `message.received`), sem replay |

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
- **O assistente não fura o anti-ban**: todo envio passa pela mesma fila da API (intervalo mínimo, rampa diária de números novos, janela de envio, espera após conectar). Não há ferramenta para mudar settings, proxy ou `unsafe_pacing`. Uma mensagem que fica na fila volta com `sent: false` e uma nota dizendo que será enviada sozinha e não deve ser reenviada; `client_message_id` torna novas tentativas seguras.
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

### Testar o MCP com um cliente de IA

1. Com o `serve` rodando e uma instância criada (`ID`, `TOK` acima), configure o Claude Desktop ou o Claude Code com `TOK` (seção "MCP").
2. Peça "liste minhas instâncias do WhatsApp": o assistente chama `list_instances` e vê só a instância do token.
3. Antes de parear, peça "mostre o QR para parear": `get_qr` devolve a imagem; escaneie no celular e peça o status até `connected`.
4. Peça "quais são minhas conversas recentes?" e "o que Fulano disse por último?" (`list_chats`, `get_messages`).
5. Peça para enviar uma mensagem ao contato de teste. Fora da janela de envio a resposta diz que ficou na fila (`sent: false`) e que sai sozinha depois; confirme com `get_message_status` ou no celular.
6. Peça "espere a resposta dele" e responda pelo celular do contato em até 60s: `wait_for_events` devolve o `message.received`.

## Limitações conhecidas

- Eventos ainda não emitidos: `message.revoked`, `chat.presence` e `group.updated` (o worker não os reporta). SSE não tem replay (`Last-Event-ID` é ignorado).
- A garantia "pelo menos uma vez" vale a partir do outbox. Antes dele o evento passa por um buffer em memória (até 10.000): um crash do supervisor entre gravar a mensagem no log e gravar a entrega, ou um buffer cheio (logado), perde o evento para os webhooks. Um shutdown normal grava o buffer antes de sair.
- `wait_for_events` (MCP) só vê eventos publicados enquanto espera; para o que chegou antes, use `get_messages`.
- Streams SSE checam o token só na conexão: um stream aberto continua depois de o token ser rotacionado (reconecte para aplicar).
- Se um recibo chegar antes da confirmação do envio, sai `message.status: delivered` (ou `read`) antes de `message.sent`, e o `message.status: sent` é omitido.
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
