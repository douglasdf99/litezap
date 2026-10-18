# LiteZap M7 — Eventos completos e chamadas (plano enxuto)

Data: 2026-10-18 · Branch: `feat/m7-events` (a partir de `main`) · Spec: `2026-10-17-zapi-parity.md`, seção 4 "M7" (WHK-3, WHK-5, WHK-6, WHK-8, MSG-29, CAL-3, INS-14, INS-15).

## APIs do whatsmeow conferidas (cache de módulos, `v0.0.0-20261016130820-0b0a1cdac3e2`)

- `types/events/call.go`: `CallOffer`, `CallOfferNotice` (grupo; `Media` audio/video), `CallAccept`, `CallPreAccept`, `CallTransport`, `CallRelayLatency`, `CallTerminate` (`Reason`), `CallReject`; todos com `types.BasicCallMeta{From, Timestamp, CallCreator, CallCreatorAlt, CallID, GroupJID}`.
- `call.go`: `RejectCall(ctx, callFrom, callID)` (manda `<call><reject call-id call-creator count=0>`; `callFrom.ToNonAD()`).
- `presence.go`: `SendPresence(ctx, types.PresenceAvailable|Unavailable)` (exige push name; `available` liga recibos "ativos" e faz o servidor tratar o aparelho como online — o celular deixa de notificar); `SubscribePresence(ctx, jid)` (usa tctoken; sem token tenta mesmo assim); `SendChatPresence`.
- `types/events/events.go`: `ChatPresence{MessageSource, State composing|paused, Media ""|audio}` — só chega com a conta marcada como disponível; `Presence{From, Unavailable, LastSeen}` — só para contatos assinados.
- `types/presence.go`: `ReceiptTypePlayed` ("played", áudio/vídeo/visualização única tocados) e `ReceiptTypePlayedSelf` (nossos outros aparelhos; ignorado, como já é `IsFromMe`).
- `proto/waE2E`: `ButtonsResponseMessage{SelectedButtonID, SelectedDisplayText}`, `ListResponseMessage{Title, SingleSelectReply.SelectedRowID, Description}`, `TemplateButtonReplyMessage{SelectedID, SelectedDisplayText, SelectedIndex}`, `InteractiveResponseMessage{Body.Text, NativeFlowResponseMessage{Name, ParamsJSON}}`.

## Decisões

1. `played` vira status próprio (`sent < delivered < read < played`), publicado em `message.status` (antes era rebaixado a `read`).
2. Proto: eventos `ChatPresence`, `Presence`, `Call` e RPCs `RejectCall{call_id}`, `SetPresence{available}`, `SubscribePresence{jid}` numa interface opcional `worker.Realtime` (não muda `Messenger`). O worker guarda as ofertas recentes (call_id → remetente, limitado) e só rejeita chamadas que ele viu: a API nunca passa JIDs crus.
3. Novo pacote `internal/realtime` (Observer do manager): publica `chat.presence`, `call.received`, `call.accepted`, `call.ended` (com `missed`), aplica `calls.auto_reject` (RPC em goroutine, < 3s) e manda `calls.reject_message` pela fila (`GuardedMessaging.Send`, ritmo/janela/rampa intactos; no máximo uma por contato a cada 10 min; só chamadas 1:1). Ao conectar com `presence.online=true` manda `SetPresence(available)`; mudar a setting aplica na hora.
4. Settings novas: `calls {auto_reject, reject_message}` e `presence {online}` (padrão desligado — ficar "disponível" silencia as notificações do celular).
5. `chat.presence` é opt-in também no filtro: `*` (ou SSE sem `types`) não o inclui; é preciso nomear `chat.presence` ou `chat.*`. Volume e privacidade (online/visto por último de contatos).
6. `POST /instances/{id}/presence/subscribe {"to"}`: exige `presence.online`, cota de 20 assinaturas/min e 200/dia por instância, recusada com envio suspenso pelo operador; as assinaturas valem até a conexão cair.
7. Respostas de botão/lista/template/interativa recebidas: tipo `button_reply` no log, `button_reply {kind, id, title, description?}` no meta, evento `message.button_reply` (só recebimento).
8. Filtros de webhook (`filters`): `chat` (`direct|group`), `chats` (permitidos), `exclude_chats`, `message_types`, `from_me` (true só nossas, false exclui nossas). Só restringem eventos que têm o campo (eventos `instance.*` sempre passam). O bus anexa atributos (chat, tipo, from_me) ao envelope; o dispatcher casa sem decodificar JSON. Migração `0006_m7_events.sql` (`webhooks.filters_json`).
9. Eventos de grupo (`group.updated`) ficam no M8.

## Tarefas (TDD, worker falso)

1. Proto + `go tool buf generate`.
2. Worker: handlers de presença/chamada, `button_reply`, RPCs; testes em `internal/worker`. Fake: `EmitCall`, `EmitChatPresence`, `EmitPresence`, `RejectedCalls`, `PresenceStates`, `Subscriptions`.
3. `events`: tipos, payloads, regra opt-in, atributos; `store`: rank `played`, migração, meta `button_reply`; `ingest`: `played`, `message.button_reply`.
4. `realtime`: testes com manager + fake (rejeição, mensagem pela fila, cooldown, missed, online ao conectar).
5. Filtros de webhook (service/store/dispatcher) + teste de contrato (`filters.chat=direct` nunca recebe grupo).
6. API (`presence/subscribe`, settings), MCP (`wait_for_events`), painel (resumo dos eventos novos, settings de chamadas/presença), README, OpenAPI, spec.
7. gofmt, vet, `go test -p 2 -race ./...`; revisão independente; smoke em 127.0.0.1:18796.

## Revisão independente (feita) e correções

- Presença aplicada em ordem: chamadas serializadas por instância, cada uma relê a setting (toggles rápidos terminam no último valor); `online` guardado por instância e eventos de presença descartados com a setting desligada; uma nova tentativa 20s depois quando o nome do perfil ainda não sincronizou.
- Ofertas velhas (> 1 min, entregues depois de reconectar) e chamadas que já terminaram não são rejeitadas nem respondidas; chamadas em grupo só são informadas.
- `RejectCall` usa o `CallCreator` da oferta (com `From` de reserva).
- Mensagem de rejeição enfileirada em segundo plano (o loop de eventos da instância só espera a rejeição); `Handler.Close` no shutdown.
- `SubscribePresence` passa pela suspensão de envio do operador; cota diária de 200.
- `message.status` não carrega `from_me` (o filtro `from_me:false` não esconde status); chats de filtro só aceitam `s.whatsapp.net`, `g.us` e `lid`; memória de chamadas descarta a mais antiga.

## Teste com celular real: "reject" do próprio aparelho

Duas ligações reais (contato desligou antes; atendida no celular) deram `call.received` seguido, no mesmo segundo, de um `events.CallReject` e nenhum `CallAccept`, com `auto_reject` desligado.

- whatsmeow (`call.go`, `handleCallEvent`): todo `<call>` com um filho `<reject>` vira `events.CallReject`; `From` é o atributo `from` do stanza, `CallCreator`/`CallID` vêm do filho. Não há campo de motivo nem distinção de origem: o próprio whatsmeow não sabe se o reject é do outro lado ou de um aparelho nosso. Também não há nada que mande `accept`/`terminate` aos companheiros: o que chega depende do servidor do WhatsApp.
- Hipótese (a confirmar com o log `call signal`): o celular (ou o servidor em nome dele) avisa os aparelhos vinculados que não atendem chamadas, como um companheiro "Chrome (Linux)", com um `reject` vindo do próprio usuário. Por isso chega no mesmo segundo da oferta, atendendo ou não.
- Correção: o worker marca `from_self` quando o `From` é o próprio usuário (JID de telefone ou LID, qualquer aparelho). O supervisor ignora esse `reject` (log "call reject from own device ignored") e espera o `terminate`. Todo sinal reinicia um prazo de 2 min; sem fim nesse prazo sai `call.ended` com `reason: timeout` (e `missed` se ninguém atendeu). Um `reject` do outro lado encerra com `reason: rejected_by_peer`. `terminate` com `accepted_elsewhere` conta como atendida.
- Não confirmado no código: se um companheiro recebe `CallAccept` quando o celular atende. Pelo teste real, não recebe; se nem o `terminate` chegar, a chamada atendida acaba como `timeout` com `missed: true` (limitação documentada).
- Log `call signal` no worker (Info): tipo, `from_self`, servidor e número do aparelho de origem, se o criador é a própria conta, motivo, tag do filho, tags dos netos e nomes dos atributos. Nenhum usuário/telefone.

### Segundo teste real (com o log `call signal`)

- Ligação 1 (contato desligou antes): `offer` (lid, aparelho 0) → `reject` de `hosted.lid` aparelho 99, ~0,2s depois, com atributos `call-creator call-id reason` → `terminate` sem motivo (lid, aparelho 0) 7s depois.
- Ligação 2 (atendida no celular): `offer` → `reject` de `hosted.lid`/99 → `terminate` com `reason=accepted_elsewhere` (atributos `call-creator call-id joinable reason`) 3s depois → `accept` com `from_self=true` (nosso celular) logo em seguida.
- Conclusão: o reject espúrio vem de um aparelho **hospedado pelo WhatsApp** (servidor `hosted`/`hosted.lid`, aparelho 99), não da nossa conta nem de quem liga. Os sinais com significado chegam: `terminate` sem motivo = não atendida; `terminate accepted_elsewhere` + `accept from_self` = atendida no celular. O fim da conversa atendida não chega ao aparelho vinculado.
- Regras atuais: reject de `hosted`/`hosted.lid` ou da própria conta é ignorado (log Info); reject do outro lado só encerra se nenhum `terminate` chegar em 5s (`rejected_by_peer`); `accept` (de qualquer origem) ou `terminate accepted_elsewhere` publicam `call.accepted` uma vez e o `call.ended` sai com `accepted_elsewhere`, `missed: false`; `terminate` sem motivo sem atendimento sai como `caller_hangup` com `missed: true` (`ended` se já atendida). O atributo `reason` do reject é lido e vai para o log.
