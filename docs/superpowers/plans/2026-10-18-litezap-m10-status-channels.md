# LiteZap M10 — Status, canais e Business (plano enxuto)

Data: 2026-10-18 · Branch: `feat/m10-status-channels` (a partir de `main`) · Spec: `2026-10-17-zapi-parity.md`, seção 4 "M10" (STA-1…6, CHN-1, 3, 4, 6, 8, 12, 13, BUS-4, 5, 8).

Em paralelo, outro agente faz o M11 (plataforma/parceiros) a partir de `main`: este marco fica em arquivos novos (`social.proto`, `worker/social.go`, `workertest/social.go`, `store/social.go`, migração `0010_m10_social.sql`, `service/social*.go`, `api/social.go`, `events/social.go`, `mcp/social.go`, `dashboard/static/social.js`) e só toca de leve `worker.proto` (eventos 30–32 no oneof, `StatusStyle` e `Quote.remote_jid`), `whatsmeow.go` (`handle`), `compose.go` (status e canal no envio), `run.go`/`spawner`/`manager` (cliente Social), `api.go`, `serve.go`, MCP e painel.

## APIs do whatsmeow conferidas (cache de módulos, `v0.0.0-20261016130820-0b0a1cdac3e2`)

- Status: `SendMessage` para `types.StatusBroadcastJID` cai em `sendGroup` com os destinatários de `getStatusBroadcastRecipients` (broadcast.go): lista da privacidade do status (`GetStatusPrivacy`; whitelist usa a lista, senão todos os contatos com `FullName` menos a blacklist) + o próprio número. Não há como escolher destinatários por envio: a privacidade é a do celular. Texto de status = `ExtendedTextMessage` com `BackgroundArgb`, `TextArgb`, `Font`. Revogar = `BuildRevoke(status@broadcast, own, id)`.
- Status recebido: `events.Message` com `Info.Chat == status@broadcast` (`IsGroup` verdadeiro, `Sender`/`SenderAlt`); hoje `outOfScope` descarta.
- Canais (newsletter.go): `GetSubscribedNewsletters`, `GetNewsletterInfo(jid)`, `GetNewsletterInfoWithInvite(key)` (sem `ViewerMeta`), `CreateNewsletter(CreateNewsletterParams{Name, Description, Picture})`, `FollowNewsletter`, `UnfollowNewsletter`, `NewsletterToggleMute`, `GetNewsletterMessages(jid, {Count, Before})` → `[]*types.NewsletterMessage{MessageServerID, MessageID, Timestamp, ViewsCount, ReactionCounts, Message}`, `NewsletterSendReaction(jid, serverID, emoji, "")` ("" remove), `NewsletterSubscribeLiveUpdates`. `types.NewsletterMetadata{ID, State, ThreadMeta{Name.Text, Description.Text, InviteCode, SubscriberCount, VerificationState, CreationTime}, ViewerMeta{Mute, Role}}`.
- Publicar em canal: `SendMessage` para `@newsletter` vai por `sendNewsletter` (plaintext, sem cifra); mídia precisa de `UploadNewsletterReader` (sem `MediaKey`/`FileEncSHA256`) e `SendRequestExtra.MediaHandle = resp.Handle`.
- Mensagens de canal recebidas: `events.Message` com `Info.Chat.Server == newsletter`, `Info.ServerID`; sem cifra, `IsFromMe` não é preenchido.
- Etiquetas (appstate/encode.go): `BuildLabelEdit(id, name, color, deleted)`, `BuildLabelChat(target, id, labeled)`, `BuildLabelMessage(target, id, msgID, labeled)`; eventos `LabelEdit{LabelID, Action{Name, Color, Deleted, PredefinedID}}`, `LabelAssociationChat{JID, LabelID, Action.Labeled}`, `LabelAssociationMessage{JID, LabelID, MessageID}` (com `FromFullSync`; o M9 já liga `EmitAppStateEventsOnFullSync`).
- Conta Business: `Store.BusinessName` (gravado no pareamento a partir de `<biz name>`); `GetBusinessProfile(jid)` → `types.BusinessProfile{Address, Email, Categories, ProfileOptions, BusinessHoursTimeZone, BusinessHours}`. Catálogo: não há API (só `GetOrderDetails`) → adiado.

## Decisões

1. Serviço gRPC novo `Social` (social.proto, mesmo socket, interface opcional `worker.SocialClient`): privacidade do status, canais (listar, obter, criar, ação follow/unfollow/mute/unmute, mensagens, reagir), etiquetas (editar, aplicar a chat/mensagem) e perfil comercial.
2. Publicar status e publicar em canal passam pela fila de envio existente (destino `status@broadcast` ou `<id>@newsletter`): ritmo, janela, rampa (conta como um destinatário novo por dia), suspensão do operador, idempotência, cancelamento. Cota extra: 30 status/dia por instância (contados na fila, persistente). Canal: só publica se a conta for dona/admin (consulta ao WhatsApp antes de enfileirar).
3. Status recebidos entram no log de mensagens no chat `status@broadcast` (mídia baixável pelo `/media`), escondido de `GET /chats`; evento `status.received` é opt-in (nunca casa com `*`). Mensagens de canal saem em `channel.message` (opt-in, não gravadas).
4. Etiquetas: tabelas `labels`, `label_chats`, `label_messages` alimentadas pelos eventos do app state (inclusive sync completo, sem evento); evento `label.updated`. Rotas exigem conta Business (`409 business_account_required`).
5. Cotas de canal: seguir/deixar 10/h e 30/dia; criar 2/dia; reagir 30/min.
6. Responder status (STA-4): `POST /status/{msg_id}/reply` manda texto ao autor citando o status (`ContextInfo.RemoteJID = status@broadcast`).
7. Adiados: leitura automática de status (INS-13), eventos (MSG-24), produto/catálogo (MSG-30, BUS-1), mídia de mensagens de canal recebidas.

## Tarefas (TDD, worker falso)

1. Proto + `go tool buf generate`; `worker/social.go` (+ envio de status/canal em `compose.go`, eventos em `handle`) e testes com stubs; fake em `workertest/social.go`.
2. Store: migração e etiquetas; contagem de status do dia.
3. Service `Social` + observador `SocialEvents`; testes com manager + fake.
4. API, eventos, MCP, painel, README, OpenAPI, site, spec.
5. gofmt, vet, `go test -p 2 -race ./...`; revisão independente; smoke em 127.0.0.1:18800.
