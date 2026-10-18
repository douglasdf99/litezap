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
| 6 | Site e documentação: página do produto, docs com guias e referência completa da API (estilo Supabase/Z-API, identidade própria) | Não |

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
- **`lookup`**: resolução número → JID (`IsOnWhatsApp`) com cache e limite por instância, usada pela fila e por `check-numbers`.
- **`ingest`**: grava os eventos de dados do worker no log (`messages`, `chats`, `media`).
- **`events`**: barramento interno com o envelope da seção 5; consumidores: `webhook`, `agenthook`, SSE.
- **`webhook`**: entrega assinada, retry e dead-letter (5.3).
- **`agenthook`**: contrato de IA externa (4.3).
- **`store`**: interface de persistência (seção 8); implementação SQLite.
- **`media`**: armazenamento em disco local com URLs temporárias assinadas (4.5).

### 3.2 Worker

- Envolve **um** cliente whatsmeow. Store de sessão do whatsmeow em `data/instances/X/session.db`.
- gRPC sobre unix socket (AF_UNIX funciona no Windows 10+; não usamos named pipes). Caminho curto para respeitar o limite de ~104 bytes do macOS: `<tmpdir>/lz-<hash8>/<instance_hash8>.sock`, onde `hash8` deriva do `data_dir`.
- RPCs: `Ping`, `PairPhone(phone)`, `Send(payload) → wa_message_id`, `MarkRead`, `SendPresence`, `DownloadMedia(ref, dest_path)`, `GetContacts`, `GetContact(jid)`, `CheckNumbers([]phone)`, `Logout`, e o stream `Events` (o QR vem pelo stream).
- **Mídia por arquivo**: `Send` lê `media_path` só de `data_dir/outbox/<instância>/` e `DownloadMedia` escreve `dest_path` só em `data_dir/media/<instância>/` (o worker recusa qualquer outro caminho: outras instâncias, `litezap.db`, `media_key`, a própria sessão); o worker usa upload/download em streaming do whatsmeow. Nada de mídia trafega pelo gRPC (limite de mensagem: 32MB, para blocos de history sync). Um spawner em container precisará de volume compartilhado.
- **Entrega de eventos** (`Events`): primeiro o snapshot (estado, QR), depois o backlog e os eventos ao vivo. Duas filas por worker: **ao vivo** (estado, QR, mensagens, recibos; até 10.000 por inscrito, e um backlog do mesmo tamanho enquanto não há inscrito) e **histórico** (até 8 blocos de *history sync*). Eventos ao vivo sempre saem antes de blocos de histórico. Quando a fila de histórico enche, o handler de history sync **bloqueia** (contrapressão) em vez de descartar. Quando a fila ao vivo enche, cai o evento mais antigo que não é estado de conexão (um `ConnState` nunca é descartado); todo descarte é logado e contado. Eventos não enviados por um stream que caiu voltam ao backlog, respeitando o mesmo limite. No supervisor, blocos de histórico são gravados numa goroutine própria, sem atrasar estados e heartbeats.
- Códigos de erro dos RPCs de mensagens: `InvalidArgument`/`PermissionDenied` = permanente, `FailedPrecondition` = sessão deslogada, `Unavailable` = sem conexão agora, `DeadlineExceeded` = timeout, outros = retentável; a mensagem do status começa com um código estável (`invalid_number: ...`).
- **Não existe `ListChats` no worker**: o whatsmeow não lista chats. Chats e histórico vêm do log de mensagens do supervisor (8), alimentado pelos eventos de mensagem e de *history sync*.
- Normaliza eventos whatsmeow para o envelope LiteZap (seção 5).
- Sem lógica de negócio. Se morrer, o supervisor reinicia e a sessão volta do disco.

## 4. Fluxo de dados

### 4.1 Envio

1. `POST /v1/instances/{id}/messages` com `{to, type, text?, caption?, media?{url|base64, mime?, file_name?, ptt?}, reaction?{message_id, emoji}, client_message_id?, expires_after?}` → validação → grava em `outbound_queue` → `202 {id, status:"queued"}`. `client_message_id` repetido devolve o item existente (idempotência); repetido com outro conteúdo responde `409 conflict`. O `wa_message_id` é escolhido no enfileiramento e reusado em todas as tentativas. `expires_after` (padrão 24h, de 1min a 7 dias): não enviada até lá, a mensagem falha com `expired`.
2. Status do item: `queued → in_flight → sent | failed`. Despachante por instância (uma goroutine) retira itens em **ordem estrita** (um item em backoff segura os seguintes daquele destinatário; outros destinatários seguem). O despachante **fica pausado enquanto a instância não está `connected`**, sem consumir tentativas, e espera uma carência de 15–30s depois de cada `connected`. Ritmo: `min_interval` (padrão 3s, **mínimo 1s**) + `jitter` (padrão 0–2s), `max_per_minute` (padrão 20, **máximo 60**), `typing_simulation` (padrão ligado, ~50ms por caractere, máx. 5s). Os pisos só caem com `unsafe_pacing`, que só o admin liga.
   **Janela de envio**: `send_window` (padrão 08:00–20:00) no `timezone` da instância (IANA; padrão: fuso do servidor; o binário embute a base de fusos). A janela vale para destinatários **sem mensagem recebida nas últimas 24h** (`chats.last_inbound_at`): fora dela esses itens são adiados até a abertura; respostas a conversas ativas saem a qualquer hora. O início de cada janela tem uma variação aleatória de alguns minutos. Janela vazia libera qualquer horário.
   **Verificação de número**: antes do primeiro envio a um número sem chat, o despachante consulta `IsOnWhatsApp` (com cache por instância; cota de 30 consultas novas/min para a fila e 10/min, separada, para `check-numbers`). Número sem WhatsApp falha como `invalid_number` sem enviar nada; número que o WhatsApp conhece com outro JID de telefone (9º dígito no Brasil) tem os itens daquele destinatário redirecionados (uma resposta `@lid` nunca vira o chat). Falha na consulta adia só aquele item, com backoff de 1, 2, 5 e 15 min; na 5ª falha seguida o item vira `failed` (`worker_error`); o limite de cota adia o item 1 min sem contar como falha.
3. Ao entrar em voo, o envio é gravado no log como `pending` (um recibo que chegue antes da confirmação não se perde). Chama `Send` no worker. Sucesso → `sent` + `wa_message_id` gravado (também usado na detecção de humano, 4.3).
4. Falha retentável (timeout ou erro de rede do worker com a instância `connected`) → volta para a fila com backoff (5s, 10s, 20s, 40s), até **5 tentativas**; depois `failed`. Falha não retentável (`invalid_number`, mídia inválida, `logged_out`, `banned`, sem permissão no grupo) → `failed` imediato. O worker dizendo que não está conectado, ou o shutdown chegando antes de o envio começar, devolvem o item à fila sem gastar tentativa; um envio já iniciado ganha até 10s no shutdown. Se um recibo (`delivered`/`read`) já chegou para o item, uma resposta de erro do worker não o marca como falho: ele conta como `sent`.
5. Status `delivered`/`read` chegam como eventos (receipts) → atualizam o store → evento `message.status`.
6. **Modo síncrono** `?wait=true&timeout=30s` (máximo 2min): a requisição aguarda o item terminar. Se ficar `sent` dentro do prazo → `200` com `wa_message_id`; se `failed` → erro; se o prazo estourar, a mensagem **continua na fila** e a resposta é `202 {id, status:"queued"}`.

**Rampa por idade da instância** (padrão ligado, configurável por instância em `settings`): o limite diário de destinatários *novos* (chats — diretos ou grupos — sem nenhuma mensagem recebida, inclusive via history sync) começa em 50/dia e sobe linearmente até 150/dia ao longo de 14 dias desde o primeiro `connected`. Cada destinatário conta uma vez por dia; o "dia" é o do `timezone` da instância. Acima do limite, os envios para destinatários novos ficam `queued` até o **início da janela de envio do dia seguinte, mais a variação** (nunca à meia-noite), sem gastar tentativa; respostas a chats que já falaram com a instância não contam. Mensagens idênticas para muitos destinatários novos em sequência são espaçadas com o dobro do `min_interval`.

Tipos de mensagem suportados: `text`, `image`, `video`, `audio` (inclui voz/ptt), `document`, `reaction` (marco 2) e, desde o marco 6, `sticker` (WebP até 1MB), `gif` (MP4 com `GifPlayback`), `ptv` (vídeo redondo), `location`, `contact` (1 a 10 vCards), `poll`, `edit` (texto próprio, até 15 min), `revoke` (próprias até 48h; de outros só em grupo onde a instância é admin), `pin` (24h/7d/30d) e `forward` (texto, localização, contato e mídia recebida, reenviada pelo `ref` sem novo upload). Campos transversais do marco 6: `reply_to` (mensagem do log; fora dele, `validation_failed`), `mentions` (lista ou `["all"]` em grupo de até 256), `link_preview` (`auto`/`off`/custom; página buscada no enfileiramento com o filtro SSRF da mídia, sempre sem endereços privados (mesmo com `media_allow_private_urls`), só HTML até 512KB, orçamento total de 5s, sem prévia em falha), `delay_ms`/`typing_ms` (0–15s, só somam espera depois da verificação de ritmo). Edições, revogações e fixações passam pela fila (ritmo, janela, rampa) e não viram linha no log; as janelas de edição/revogação são conferidas no enfileiramento e no despacho (`edit_window_expired`, `revoke_window_expired`). A fila é visível: `GET /queue`, `DELETE /queue/{msg_id}` (só `queued`; `in_flight` → `conflict`) e `DELETE /queue` (cancelado = `failed` com `canceled:`). Botões, listas e carrossel ficam fora (instáveis). Mídia entra por `media.url` (o supervisor baixa) ou `media.base64`; multipart fica fora; no máximo 3 corpos grandes e 3 downloads/decodificações de mídia ao mesmo tempo (os demais esperam vaga; o corpo é liberado assim que a mensagem entra na fila, antes da espera de `?wait=true`). O download por URL tem proteção contra SSRF: timeout 30s, limite de tamanho do tipo, sem proxy de ambiente, e só endereços unicast globais (bloqueia privados, loopback, link-local, `0.0.0.0/8`, `100.64.0.0/10`, `198.18.0.0/15`, `240.0.0.0/4`, `64:ff9b::/96`, `2002::/16`), checados depois da resolução DNS em toda conexão, inclusive redirecionamentos (desligável por config para uso local: `media_allow_private_urls`). Um `Content-Type` remoto que contradiz o tipo da mensagem é ignorado; conteúdo que não é imagem/vídeo/áudio de verdade é recusado (SVG nunca vale como imagem).

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

Implementado no marco 5 (pacote `agenthook`, consumidor do barramento como os webhooks), com estas definições:

- **Envio**: a resposta segue o mesmo caminho de `POST /messages` (fila, ritmo, janela, rampa e suspensão do operador), com `client_message_id = agent:<id da mensagem>` (uma resposta por mensagem) e expiração de 1h. `reply` objeto aceita só tipos de conteúdo (`text`, mídias, `location`, `contact`, `poll`); `to` e `client_message_id` do agente são ignorados (sempre o chat que disparou). `react` também passa pela fila. Com envio suspenso pelo operador, o hook nem é chamado.
- **Elegibilidade**: override `true` liga o hook no chat mesmo com `enabled: false` na instância; nunca `status@broadcast` nem canais; `ignore` vale para chat ou remetente.
- **Um por vez com coalescência**: mensagens que chegam durante uma chamada esperam; ao fim, só a mais nova gera a próxima chamada (as demais estão no `context`, que termina com a própria mensagem). Até 8 chamadas simultâneas no servidor.
- **Revalidação**: depois da resposta o estado do chat é relido; se um humano respondeu (ou o chat foi pausado/desligado) durante a chamada, a resposta é descartada. A pausa por humano também descarta a mensagem que esperava a vez.
- **Fail-closed**: além de erro/timeout/JSON inválido, redirecionamento (nunca seguido) e ação conhecida malformada contam como falha; `agent.failed.reason` ∈ `timeout`, `http_error`, `network_error`, `invalid_response`, `send_failed`, `loop_guard`, `internal`. A resposta é enfileirada antes de `mark_read`/`react` (recusada, eles não saem); `pause`/`handoff` valem sempre.
- **Proteção contra loop** (dois robôs conversando, agente descontrolado): no máximo `max_replies_per_window` (padrão 10) respostas por chat em `reply_window` (padrão 10min); acima disso o chat pausa por `human_pause` (ou pela janela, se a pausa for 0) com `agent.failed` (`loop_guard`); um segundo estouro em 24h desliga o hook no chat (override `false` + `agent.handoff`). O log de chamadas guarda sempre as últimas 24h (além das 500 mais recentes), para a contagem nunca se perder.
- **Humano assumiu**: além da pausa, as respostas do hook ainda na fila daquele chat (`client_message_id` `agent:`/`agent-react:`) são canceladas, também no estouro do limite. Opcional `pause_on_api_send`: envios pela API que não são do hook (CRM, painel, MCP) também contam como humano.
- **Mensagens antigas**: mais velhas que `max(2×timeout, 5min)` quando a chamada começaria (acúmulo offline entregue após reconexão, ou espera longa por vaga) são puladas (`skipped`/`stale`), assim como o tipo `unknown`. `mentions: ["all"]` é recusado numa resposta.
- **Rede**: mesmo filtro SSRF dos webhooks (`webhook_allow_private_urls` libera endereços locais), sem proxy de ambiente; o segredo (gerado se ausente, mostrado uma vez) nunca volta na API.
- **Rotas extras**: `GET`/`DELETE /instances/{id}/agent-hook`, `GET .../agent-hook/chats` (overrides e pausas) e `GET .../agent-hook/calls` (log das últimas 500 chamadas: `ok`, `failed`, `skipped`, `discarded`, `paused`). `PUT .../chats/{jid}/agent` com `true` ou `null` também encerra uma pausa. Store: tabelas `agent_hooks` e `agent_calls` (migração 0006). Painel: aba "Atendente IA".

### 4.4 Conexão

- `POST /v1/instances` cria a instância (devolve o token da instância) e sobe o worker.
- `GET /v1/instances/{id}/qr` devolve o QR atual (`?format=png|text`); atualizações via evento `instance.qr`.
- `POST /v1/instances/{id}/pair-phone {phone}` devolve código de pareamento; o evento `instance.pair_code` também é emitido.

### 4.5 Mídia

- Recebida: **download sob demanda**. O evento traz `media.id`; `GET /v1/instances/{id}/media/{media_id}` baixa via worker na primeira vez (o worker escreve num arquivo temporário único, renomeado ao final; leituras simultâneas da primeira vez compartilham um único download) e guarda em `data/media/<instância>/`. `GET .../media/{media_id}?signed=1` devolve `{url, expires_at}` assinada (TTL 1h) que funciona sem token, para repassar a terceiros (ex.: IA): `GET /media/{instance_id}/{media_id}?exp=&sig=` (fora de `/v1`), HMAC-SHA256 com um segredo próprio em `data/media_key` (0600). Sem `public_url` e com bind fora de loopback, `?signed=1` é recusado (`409 conflict`). Toda mídia servida leva `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'` e `Content-Disposition: attachment`, salvo JPEG/PNG/WebP/GIF, áudio e vídeo (`inline`). Arquivos expiram após 7 dias (configurável; limpeza no marco 2b).
- Enviada: limites de tamanho por tipo (padrão: imagem 16MB, vídeo/áudio 64MB, documento 100MB).

### 4.6 Rede: proxy por instância

- Config por instância em `settings.proxy_url` (`http://`, `https://` ou `socks5://`, com usuário/senha opcionais; **só o token admin altera**), mais um `default_proxy_url` global na config. `proxy_url` vazio herda o default.
- O supervisor passa o proxy ao worker por variável de ambiente (não por flag, visível no `ps`) e remove `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY`/`NO_PROXY` herdados. O worker sempre chama `whatsmeow.Client.SetProxyAddress` antes de conectar (vazio = transporte sem proxy de ambiente), valendo para websocket e upload/download de mídia. Trocar o proxy reinicia o worker.
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
| `message.status` | `sent`, `delivered`, `read`, `played` (marco 7), `failed` de mensagem enviada |
| `message.reaction` | reação recebida |
| `message.edited` | mensagem editada (API, celular ou contato) — marco 6 |
| `message.revoked` | mensagem apagada para todos — marco 6 |
| `message.poll_vote` | voto em enquete, com as opções decifradas (casadas por SHA-256 com as opções guardadas) — marco 6 |
| `message.button_reply` | resposta a botão/lista/template/interativa recebida (só recebimento) — marco 7 |
| `chat.presence` | digitando/gravando/parou, online/offline de contato assinado — marco 7; opt-in: exige `presence.online` e não entra em `*` |
| `call.received` · `call.accepted` · `call.ended` | chamada tocando (com `rejected` se `calls.auto_reject`), atendida num aparelho da conta, encerrada (`reason`, `missed`) — marco 7 |
| `group.updated` | mudança num grupo, um evento por `kind` (participantes adicionados/removidos/promovidos/rebaixados, pedido de entrada, nome, descrição, foto, configurações, link redefinido, `joined`, `deleted`, vínculo de comunidade) — marco 8 |
| `status.received` | status (story) de um contato, gravado no log em `status@broadcast` — marco 10; opt-in: não entra em `*` |
| `channel.message` | publicação num canal seguido (não gravada) — marco 10; opt-in |
| `label.updated` | etiqueta do WhatsApp Business editada ou aplicada a chat/mensagem, pela API ou no celular (sync completo sem evento) — marco 10 |
| `history.synced` | ingestão de history sync concluída |
| `agent.failed` | falha no hook de agente |
| `agent.handoff` | ação `handoff` do agente |

### 5.3 Webhooks

- Cadastro: `POST /v1/webhooks {url, secret?, events: ["message.*", ...], instance_id?, version?, filters?}`. `filters` (marco 7): `chat` (`direct|group`), `chats`, `exclude_chats`, `message_types`, `from_me`; cada filtro só restringe eventos que têm o campo (o bus anexa chat/tipo/from_me ao envelope, sem decodificar o JSON). Sem `instance_id` = global (todas as instâncias). Também `GET/PATCH/DELETE /v1/webhooks/{id}`.
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
| `GET/PUT /instances/{id}/settings` | inst (`proxy_url` e `unsafe_pacing`: admin) | ritmo, janela, fuso, rampa, proxy, `calls {auto_reject, reject_message}`, `presence {online}` (marco 7) |
| `POST /instances/{id}/presence/subscribe` | inst | presença de um contato (exige `presence.online`; 20/min e 200/dia; bloqueada por suspensão de envio) — marco 7 |
| `POST /instances/{id}/messages` | inst | enviar (4.1) |
| `GET /instances/{id}/messages/{msg_id}` | inst | status de uma mensagem |
| `GET /instances/{id}/queue` · `DELETE .../queue/{msg_id}` · `DELETE .../queue` | inst | fila visível: listar, cancelar item `queued`, esvaziar (marco 6) |
| `GET /instances/{id}/chats` | inst | chats do log, ordenados por última mensagem |
| `GET /instances/{id}/chats/{jid}/messages` | inst | histórico paginado (cursor) |
| `GET /instances/{id}/messages/search?q=` | inst | busca full-text (FTS5) |
| `POST /instances/{id}/chats/{jid}/read` | inst | marcar como lido |
| `GET /instances/{id}/contacts` · `/contacts/{jid}` | inst | contatos |
| `POST /instances/{id}/check-numbers` | inst | verifica se números têm WhatsApp |
| `GET /instances/{id}/media/{media_id}` | inst | mídia recebida (`?signed=1` → URL assinada) |
| `GET /media/{id}/{media_id}?exp=&sig=` | assinatura | mídia recebida via URL assinada (4.5) |
| `GET/PUT/DELETE /instances/{id}/agent-hook` · `PUT .../chats/{jid}/agent` · `GET .../agent-hook/chats` · `GET .../agent-hook/calls` | inst | hook de agente (4.3) |
| `GET/POST /instances/{id}/groups` · `GET/PATCH .../groups/{jid}` · `PUT/DELETE .../photo` · `POST .../participants` · `GET/POST .../requests` · `GET .../invite` · `POST .../invite/reset` · `POST .../leave` · `POST .../groups/preview` · `POST .../groups/join` | inst | grupos (marco 8): síncrono, resultado por participante; criar/adicionar/entrar passam por cotas `settings.groups` (tetos só com `unsafe_pacing`), adições só de contatos conhecidos, bloqueados por suspensão de envio |
| `GET/POST /instances/{id}/communities` · `GET .../communities/{jid}` · `GET/POST .../communities/{jid}/groups` · `DELETE .../groups/{group}` | inst | comunidades (marco 8) |
| `POST/GET /instances/{id}/status` · `GET .../status/privacy` · `DELETE .../status/{msg_id}` · `POST .../status/{msg_id}/reply` | inst | status (marco 10): publicar pela fila (destino `status@broadcast`), 30/dia; listar do log; responder citando |
| `GET/POST /instances/{id}/channels` · `POST .../channels/preview` · `GET .../channels/{jid}` · `POST .../{jid}/follow\|unfollow\|mute\|unmute` · `GET/POST .../{jid}/messages` · `POST .../{jid}/reactions` | inst | canais (marco 10): publicar pela fila só como dono/admin; seguir 3/min e 30/dia, criar 2/dia, reagir 30/min |
| `GET/POST /instances/{id}/labels` · `PATCH/DELETE .../labels/{label_id}` · `PUT/DELETE .../{label_id}/chats/{jid}` · `PUT/DELETE .../{label_id}/messages/{msg_id}` · `GET .../business/profile` | inst | etiquetas e perfil comercial (marco 10): só conta Business (`409 business_account_required`) |
| `GET /instances/{id}/events` · `GET /events` | inst · admin | SSE |
| `/webhooks...` | admin ou inst (só os da própria instância) | 5.3 |
| `GET /me` | qualquer | quem é o token (`{admin, instance_id}`), usado por `litezap mcp` |
| `POST /mcp` (fora do `/v1`) | admin ou inst | MCP streamable HTTP (seção 7) |
| `GET /healthz` · `/readyz` · `/metrics` | sem auth em bind loopback; admin caso contrário; sem auth no `ops_listen_addr` | observabilidade |

### 6.1 Autenticação e tokens

- **Token admin**: definido em `admin_token` na config; se ausente na primeira execução, é gerado e gravado em `data/admin_token` (permissão 0600).
- **Token de instância**: gerado em `POST /instances` (32 bytes aleatórios, prefixo `lzi_`), mostrado uma única vez; o store guarda só o hash SHA-256. Rotação via `/token/rotate`.
- Envio: `Authorization: Bearer <token>`. Token de instância só acessa rotas da própria instância.
- MCP HTTP usa os mesmos tokens; com token de instância, as ferramentas operam só naquela instância e o parâmetro `instance_id` é opcional.

## 7. Ferramentas MCP

Todas mapeiam casos de uso do `service`:

`list_instances` (com token de instância devolve só a própria), `get_instance_status`, `send_message` (texto, com `wait` por padrão), `send_media` (por URL), `list_chats`, `get_messages` (por chat, paginado), `search_messages`, `get_contact`, `check_numbers`, `mark_read`.

Marco 6: `send_message` ganha `reply_to` e `mentions`, `send_media` aceita `sticker`, `gif` e `ptv`, e entram `send_reaction`, `send_location`, `send_contact`, `edit_message` e `delete_message` (as duas últimas marcadas como destrutivas, com aviso para só agir a pedido explícito).

Implementado no marco 4 (além das acima, exceto `search_messages`, que depende da busca do marco 2b): `get_qr` (imagem PNG), `get_message_status`, `list_contacts` e `wait_for_events` (espera curta por eventos novos, sem replay). O endpoint HTTP é *stateless* (cada POST autenticado ganha um servidor ligado ao escopo do token) e `litezap mcp` descobre o escopo do token por `GET /v1/me`. Envios sempre passam pela fila; mensagem que fica na fila volta como sucesso com `sent: false` e uma nota, não como erro.

Marco 8: `list_groups`, `get_group` (leitura), `create_group`, `update_group`, `manage_group_participants` (destrutiva) e `get_group_invite_link` (nunca redefine), com aviso de risco de banimento nas escritas; as cotas e a checagem de participantes valem como na REST.

Marco 10: `list_status_updates`, `list_channels`, `get_channel`, `get_channel_messages`, `list_labels`, `get_business_profile` (leitura), `follow_channel` e `label_chat` (escritas seguras, com as cotas da REST). Publicar status não é ferramenta (vai para todos os contatos).

## 8. Store (SQLite)

Arquivo `data/litezap.db`, modo WAL, migrações versionadas embutidas no binário. Entidades:

- `instances` (id, name, state, state_reason, jid, token_hash, settings_json, first_connected_at, created_at, updated_at) — `settings_json` guarda só os overrides da instância; o efetivo soma os padrões da config
- `chats` (instance_id, jid, name, is_group, last_message_at, unread_count, has_inbound, last_inbound_at, agent_enabled NULL|bool, agent_paused_until) — `is_group` e `has_inbound` nunca voltam a falso; `last_inbound_at` define conversa ativa para a janela de envio
- `messages` (seq, instance_id, id, wa_message_id, chat_jid, sender_jid, from_me, source `contact|api|device|history`, type, text, media_json, reaction_to, status `received|pending|sent|delivered|read`, timestamp, meta_json, edited_at, revoked_at), única por (instance_id, chat_jid, wa_message_id) + tabela FTS5 sobre `text` (marco 2b). `meta_json` (marco 6) guarda citação, menções, encaminhado, localização, contatos, enquete e prévia de link; edição troca `text` e marca `edited_at`, revogação limpa `text` e marca `revoked_at`
- `outbound_queue` (seq, id, instance_id, client_message_id, chat_jid, wa_message_id, payload_json, status, attempts, next_attempt_at, last_error, new_recipient, request_hash, expires_at, lookup_failures, created_at, updated_at, sent_at), com índice parcial dos itens ativos
- `number_cache` (instance_id, phone, found, jid, checked_at) — resultado de `IsOnWhatsApp` (válido 30 dias se encontrado, 24h se não)
- `webhooks` (id, instance_id?, url, secret, events_json, version, enabled)
- `webhook_deliveries` (id, webhook_id, event_id, status, attempts, next_attempt_at, last_status_code, last_error)
- `agent_hooks` (instance_id, config_json)
- `media` (id, instance_id, message_id, kind, mime, size, file_name, ref, path, expires_at, created_at) — `ref` é a referência opaca de download vinda do worker

Marco 8: as cotas de grupos (adições, criações, entradas por link) ficam em `engine_kv` (`groups.quota:<instância>`, timestamps das últimas 24h), sem migração nova.

Marco 10 (migração `0010_m10_social.sql`): `labels` (instance_id, label_id, name, color, predefined_id, deleted, updated_at), `label_chats` (instance_id, label_id, chat_jid) e `label_messages` (instance_id, label_id, chat_jid, wa_message_id), alimentadas pelo app state; status recebidos ficam em `messages` no chat `status@broadcast` (fora de `GET /chats`) e o teto diário de status conta `outbound_queue`.

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
| `qr_pending` | `reconnecting` | worker pareando perdeu o acesso ao WhatsApp (ex.: proxy fora) |
| `reconnecting` | `qr_pending` | worker pareando voltou a mostrar códigos |
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
- **Watchdog de conexão** no worker (`zombie_timeout`, padrão `0` = desligado para não mudar o self-host; a nuvem usa `5m`): worker logado cujo estado reportado diverge do socket por mais que o limite — `connected` sem socket, ou `reconnecting`/`keepalive_timeout` com socket ainda "aberto" — é forçado a reconectar (`state_reason: zombie_watchdog`). O whatsmeow já força reconexão após 3 min de keepalive falhando; o watchdog é a rede de segurança para evento perdido ou socket travado. Ficar sem eventos não conta (número ocioso é normal).
- **Restart com backoff exponencial** (1s → 5min). **Circuit breaker**: 5 crashes em 10min → `failed` + evento; sem restart até ação manual.
- **Limite de memória** por worker: `GOMEMLIMIT` + kill acima de `worker_memory_max_mb` (padrão 512).
- **Erros terminais** (`logged_out`, `banned`): sem retry.
- **Shutdown gracioso**: para despachantes, aguarda envios em voo (até 10s), desconecta workers e encerra.
- **Boot**: cada worker grava `data/run/X.pid`. No boot, o supervisor mata PIDs órfãos que ainda sejam `litezap worker`, remove sockets antigos, devolve itens `in_flight` para `queued` (exceto os cujo registro no log já foi promovido por um recibo; o reenvio usa o mesmo `wa_message_id`) e sobe as instâncias que não estavam `stopped`.

### 9.3 Erros na API

Códigos estáveis: `unauthorized`, `forbidden`, `instance_not_found`, `not_found` (404: mensagem, chat, contato ou mídia), `conflict` (409: `client_message_id` reusado com outro conteúdo; URLs assinadas desativadas), `instance_not_connected`, `instance_banned`, `instance_not_pairing`, `worker_error`, `worker_timeout`, `shutting_down`, `rate_limited` (429: cota de consultas de `check-numbers` esgotada; envios de mídia grandes esperam vaga em vez de falhar), `invalid_number` (400), `media_too_large` (413), `media_unavailable` (410: mídia recebida expirou no WhatsApp), `media_fetch_failed` (422, `retryable`: `media.url` não baixou), `message_expired` (410: não enviada antes de `expires_after`), `validation_failed`, `internal`. `retryable` indica se o cliente pode tentar de novo. Erros gRPC do worker: `InvalidArgument` vira HTTP 400 (`invalid_number` quando a mensagem começa com esse código, senão `validation_failed`); `FailedPrecondition` (sessão deslogada) vira `instance_not_connected` (HTTP 409); `NotFound` com `media_unavailable` vira `media_unavailable` (HTTP 410); `DeadlineExceeded` vira `worker_timeout` (HTTP 504, `retryable`); `Unavailable`/`Canceled` viram `instance_not_connected` (HTTP 409, `retryable`); qualquer outro vira `worker_error` (HTTP 502, `retryable`). As mensagens nunca levam números de telefone nem tokens. Um corpo acima do limite responde `media_too_large` (413) e `media.url` que não é URL http(s) responde `validation_failed`.

### 9.4 Observabilidade

- Logs estruturados (`slog`) com `instance_id` em toda linha.
- `/metrics` (Prometheus, texto 0.0.4): instâncias por estado, profundidade da fila por status, entregas de webhook por status, mensagens enviadas/recebidas, bytes de mídia, mudanças de estado (inclui starts de worker), falhas de proxy, envio suspenso, pronto/encerrando e runtime Go do supervisor. Latência da fila em histograma e memória por worker ficam para depois.
- `/healthz` (processo vivo) e `/readyz` (store ok; `503 draining` assim que o shutdown começa).
- `ops_listen_addr` serve os três sem token num listener separado (rede privada da plataforma).
- `LITEZAP_LOG_FORMAT=json` troca os logs para JSON.
- Uso persistido por instância e dia (`usage_daily`, `GET /v1/usage`, só operador) para cobrança; independe das métricas.

## 10. Configuração

`litezap.yaml` + variáveis `LITEZAP_*` (env sobrescreve arquivo): `listen_addr` (padrão `127.0.0.1:8787`), `public_url`, `data_dir`, `admin_token`, `default_proxy_url`, `media_allow_private_urls` (padrão `false`), `instance_defaults` (ritmo, janela, fuso, rampa e `unsafe_pacing`; `LITEZAP_INSTANCE_DEFAULTS_JSON`), `worker_memory_max_mb`, `zombie_timeout` (padrão `0`), `shutdown_timeout`, `media_retention_days` e `message_retention_days` (padrão `0` = guardar para sempre), `webhook_retention_days`, `webhook_failed_retention_days`. O documento inteiro também pode vir em `LITEZAP_CONFIG_JSON` (aplicado depois do arquivo; variáveis individuais vencem).

Hospedagem gerenciada (todas desligadas por padrão; detalhes no README e no spec da nuvem): `operator_token` (até dois, rotação), `locked_settings` (`proxy_url`, `unsafe_pacing`), `max_instances` (ajustável em tempo de execução pelo operador), `require_proxy`, `ops_listen_addr`.

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
6. Mensagens completas (matriz de paridade com a Z-API, M6): tipos novos, citação, menções, prévia de link, editar/apagar/fixar/encaminhar, enquetes e votos, `delay_ms`/`typing_ms`, fila visível.
7. Eventos e chamadas (M7): chamadas recebida/atendida/perdida, rejeição automática com mensagem, presença opt-in, recibo `played`, respostas de botão/lista, filtros de webhook.

### 13.1 Backlog anotado (pedidos do usuário, 2026-10-18)

- **M5b — atendente multimodal:** áudio recebido → transcrição → resposta em texto ou em áudio (voz sintetizada); imagens e documentos analisados pela IA; ligação recebida → mensagem automática da IA ("vi que você ligou, como posso ajudar?").
- **Peça #4 — treinamento de agentes:** criar e treinar agentes com contexto do negócio (base de conhecimento, catálogo, horários, regras) e **modos operantes** prontos e editáveis: vendedor, facilitador de pedido (monta o pedido, confirma itens e endereço), SDR para agendamento (qualifica e marca consulta/reunião), suporte; cada modo com objetivo, roteiro, critérios de passagem para humano e ferramentas permitidas; testes de conversa antes de publicar. Lição do teste real: sem contexto, o agente repetiu a última fala automática da conversa — o contexto precisa separar mensagens automáticas do sistema das falas do atendente.
- **Pesquisa — atender ligação de voz:** verificar como produtos de terceiros oferecem atendimento de chamadas (WhatsApp Business Calling API oficial da Meta ou VoIP reverso experimental) e se cabe no LiteZap sem risco de banimento.

## 14. Fora do escopo (YAGNI)

Painel web, cobrança, organizações e multi-usuário, `ContainerSpawner`, Postgres, S3, túnel, atendente IA completo, WebSocket, upload multipart, rolling restart, listas de transmissão e chamadas (não suportados pelo whatsmeow).

## 15. Riscos

- **Mudanças no protocolo do WhatsApp**: dependemos do ritmo de atualização do whatsmeow. Mitigação: envelope de eventos LiteZap isola consumidores; atualizar = trocar binário e reiniciar.
- **Banimento de números**: inerente ao uso não oficial. Os principais gatilhos são mensagens idênticas em massa, velocidade anormal, muitas denúncias ou bloqueios e muitos números no mesmo IP. Mitigação: ritmo e rampa conservadores por padrão, proxy por instância, e na nuvem um pool de IPs com no máximo um cliente por IP. O LiteZap é voltado a conversas que o destinatário espera (atendimento, clientes, opt-in). Não terá recursos para fabricar perfis falsos ou conversas artificiais de "maturação".
- **Contaminação entre clientes na nuvem**: um cliente que faz spam marca o IP de saída, e os outros no mesmo IP pagam o preço. Mitigação: na nuvem, isolamento de IP por cliente (peça #3).
- **Licença MPL-2.0**: modificações em arquivos do whatsmeow precisam ser publicadas; usar como dependência não impõe licença ao restante do código.
