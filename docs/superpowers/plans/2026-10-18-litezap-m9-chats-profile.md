# LiteZap M9 — Chats, contatos, perfil e privacidade (plano enxuto)

Data: 2026-10-18 · Branch: `feat/m9-chats-profile` (a partir de `main`) · Spec: `2026-10-17-zapi-parity.md`, seção 4 "M9" (CHT-2…8, CON-3, 4, 7, PRF-1…3, PRV-1, 2, INS-8, 10, 12).

Em paralelo, outro agente faz o M8 (grupos) a partir de `main`: este marco fica em arquivos novos sempre que possível (`worker/account.go`, `workertest/account.go`, `service/account*.go`, `api/account.go`, `events/account.go`, `autoread/`, migração `0009_m9_chats.sql`) e só toca de leve `worker.proto` (bloco no fim do serviço; evento `chat_update = 21` no oneof (o 20 ficou com `group` do M8)), `whatsmeow.go` (`handle`), `api.go` (Options + rotas), `serve.go`, `settings.go`, catálogo de eventos, MCP e painel.

## APIs do whatsmeow conferidas (cache de módulos, `v0.0.0-20261016130820-0b0a1cdac3e2`)

- `appstate/encode.go`: `BuildArchive(target, archive, lastTS, lastKey)` (arquivar também desafixa), `BuildPin`, `BuildMute(target, mute, dur)` / `BuildMuteAbs` (sem fim = `-1`), `BuildMarkChatAsRead(target, read, lastTS, lastKey)`, `BuildDeleteChat(target, lastTS, lastKey, deleteMedia)`, `BuildStar(target, sender, id, fromMe, starred)`, `BuildSettingPushName(name)` (coleção `critical_block`). Não há builder de `clearChat` (o próprio whatsmeow marca o índice 2 como desconhecido): **limpar conversa fica adiado**.
- `appstate.go`: `SendAppState(ctx, patch)` falha com "no app state keys found" antes do primeiro sync de chaves; depois de mandar, busca o patch de volta e despacha os eventos da nossa própria mutação (eco). Também grava `Store.ChatSettings` (mudo/fixado/arquivado) inclusive no sync completo, cujos eventos não são despachados (`EmitAppStateEventsOnFullSync` é falso).
- Eventos (`types/events/appstate.go`): `Archive`, `Pin`, `Mute` (`MuteEndTimestamp` ms, `<0` = para sempre), `MarkChatAsRead`, `DeleteChat`, `ClearChat`, `Star` (índice `star, chat, id, fromMe, sender|0`), `PushNameSetting`.
- `user.go`: `GetProfilePictureInfo(ctx, jid, &GetProfilePictureParams{Preview, ExistingID})` → `ErrProfilePictureNotSet` (404/204) e `ErrProfilePictureUnauthorized` (escondida); `GetUserInfo(ctx, jids)` → `Status` (recado), `PictureID`, `VerifiedName`, `LID`; `GetBusinessProfile(ctx, jid)`; `GetBlocklist(ctx, dhash)` e `UpdateBlocklist(ctx, jid, action, dhash)` (assinatura nova com `dhash`; itens trazem `LID` e `PN`); `SetStatusMessage(ctx, types.SetStatusInput{Text})` (mex IQ).
- `group.go`: `SetGroupPhoto(ctx, jid, jpeg)` manda `w:profile:picture` com `target` só quando o JID não é vazio (`request.go`); com `types.EmptyJID` vira a foto do próprio perfil (mesmo formato do Baileys/WhatsApp Web, sem `target`). `nil` remove. JPEG quadrado (usamos 640x640).
- `privacysettings.go`: `TryFetchPrivacySettings(ctx, ignoreCache)`, `SetPrivacySetting(ctx, name, value)`, `SetDefaultDisappearingTimer(ctx, d)`; tipos e valores válidos em `types/user.go`.
- `receipt.go`: `MarkRead` já manda `read-self` quando a confirmação de leitura está desligada na privacidade (a leitura automática respeita isso sem código extra).
- `pair-passkey.go`: `SendPasskeyResponse` exige uma asserção WebAuthn do autenticador para o RP do WhatsApp; um servidor terceiro não consegue produzi-la. **Passkey adiado** (só log no worker).

## Decisões

1. Proto: RPCs `ChatAction`, `GetChatSettings`, `GetProfilePicture`, `DownloadProfilePicture`, `GetUserInfo`, `GetBlocklist`, `UpdateBlocklist`, `GetProfile`, `UpdateProfile`, `GetPrivacy`, `SetPrivacy` numa interface opcional `worker.Account`; evento `ChatUpdate` (campo `field` + valor).
2. JID do chat no app state: em conversa direta o worker usa o LID quando a conta já migrou e o mapeamento existe (é como o celular indexa hoje), salvo se o `ChatSettings` local só conhece a forma de telefone. Eventos vindos do celular são traduzidos de volta para o JID de telefone. (Confirmar com celular real.)
3. Tabela `chats` ganha `archived`, `pinned`, `muted_until`, `marked_unread`, `deleted_at`; `messages.meta_json` ganha `starred`. Atualização idempotente: só publica `chat.updated` se o estado mudou (a ação via API aplica e publica; o eco do whatsmeow não duplica).
4. Apagar conversa: só marca (`deleted_at`), some de `GET /chats` até chegar mensagem nova; o log continua acessível.
5. Rotas: `GET/PATCH/DELETE /chats/{jid}`, `PUT /messages/{id}/star`, `GET /contacts/{jid}/picture` (`?preview=1`, `?download=1` passa pelo worker — e pelo proxy da instância), `GET /contacts/{jid}/profile`, `GET /blocklist`, `POST /contacts/{jid}/block|unblock`, `GET/PATCH /profile`, `PUT/DELETE /profile/picture`, `GET/PATCH /privacy`.
6. Consultas de rede sobre terceiros (foto, perfil) têm cota por instância (30/min, 1000/dia) e respeitam a suspensão de envio do operador; foto inexistente ou escondida → `404 not_found`.
7. Setting `auto_read {enabled, groups}` (padrão desligado): observador do manager marca como lidas as recebidas após 2–5s (agrupadas por chat/remetente), no máximo uma confirmação por segundo por instância.
8. Evento novo `chat.updated` (`chat`, `changed`, estado completo, `source: api|device`).

## Tarefas (TDD, worker falso)

1. Proto + `go tool buf generate`; `worker/account.go` + testes com stubs; fake em `workertest/account.go`.
2. Store: migração, estado do chat, estrela; testes.
3. Service `Account` (chats, contatos, perfil, privacidade) + observador `chatstate` (eventos do celular) + `autoread`; testes com manager + fake.
4. API, eventos, settings, MCP (leitura: foto, recado, bloqueados; escrita segura: bloquear, arquivar/fixar/silenciar), painel (ações na aba Conversas, seção Perfil/Privacidade), README, OpenAPI, spec.
5. gofmt, vet, `go test -p 2 -race ./...`; revisão independente; smoke em 127.0.0.1:18799.

## Revisão independente (feita) e correções

- Eco do `DELETE /chats` duplicava `chat.updated` (o horário da API e o do app state nunca batiam): chat já escondido não muda de novo, e a API marca com o horário da última mensagem (relógio do WhatsApp, não o local).
- O estado que já existia no celular não chegava à tabela (`EmitAppStateEventsOnFullSync` desligado): o worker agora liga a opção e o serviço grava o sync completo sem publicar um evento por chat.
- Estrela em mensagem nossa usava o próprio JID como remetente; o índice certo é `"0"` (remetente = alvo).
- Menores: cliente HTTP de foto sem keep-alive, eco do "desafixar" de um arquivamento marcado como `api`, formulário de privacidade não reenvia valores que não carregou, sem escrita concorrente do push name no worker.
- Não corrigidos (anotados): o download da foto em cache ainda refaz a consulta da URL no worker quando o arquivo expira; `actionLocks`/cache de fotos não são podados quando a instância é apagada; `auto_read` lê as settings do banco a cada mensagem recebida.
