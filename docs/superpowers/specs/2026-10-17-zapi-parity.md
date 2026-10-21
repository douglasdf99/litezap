# LiteZap × Z-API — Matriz de paridade de recursos e roadmap

Data: 2026-10-17
Status: pesquisa (insumo para os specs dos marcos 6+); nada aqui está aprovado como escopo

## 1. Objetivo e método

Mapear **todo** recurso que a Z-API oferece, dizer se o whatsmeow permite implementá-lo, onde o LiteZap está hoje e em que marco ele entra. O objetivo de produto é oferecer o mesmo conjunto de recursos da Z-API com distribuição open source estilo Supabase (spec do motor, seção 2.7).

Fontes e verificações:

- **Z-API**: índice público da documentação (`developer.z-api.io/llms.txt` e `/_llms/en/api-reference.txt`, 250 páginas de referência) e páginas avulsas (introduções de webhooks, fila, chamadas, filtros de webhook, token de segurança, funcionamento dos botões). Os agrupamentos abaixo seguem a navegação deles; a redação é nossa.
- **whatsmeow**: código no cache de módulos, versão fixada no `go.mod`: `go.mau.fi/whatsmeow@v0.0.0-20261016130820-0b0a1cdac3e2`. Cada célula cita a função e o arquivo conferidos.
- **LiteZap**: este worktree, branch base `feat/m3-events-impl` (commit `a2dd3a9`): rotas em `internal/api/api.go` e `internal/api/events.go`, RPCs em `proto/worker/v1/worker.proto`, tipos de envio em `internal/service/messages.go` (`payload`), recepção em `internal/worker/messaging.go`, catálogo de eventos em `internal/events/events.go`. As branches `feat/m4-mcp` e `feat/local-app` ainda apontam para o mesmo commit (sem código próprio); `feat/site-docs` tem o site/docs.

Legenda:

- **whatsmeow**: `Sim` = função pronta; `Manual` = não há helper, mas o proto existe e sai por `SendMessage`/`SendAppState` montado à mão; `Parcial` = cobre só parte; `Não` = sem suporte (precisaria de engenharia reversa); `N/A` = recurso da camada LiteZap, não do protocolo.
- **LiteZap hoje**: `Pronto`, `Parcial`, `Falta`, `Fora` (decidido não fazer no motor).
- **Esforço** (uma pessoa, com testes no padrão do projeto): `S` ≤ 2 dias, `M` 3–7 dias, `L` > 1 semana.
- **Marco**: `M1`–`M3` já entregues; `M2b` complementos já planejados; `M4` MCP; `M5` hook de agente; `M6+` propostos na seção 4. `Exp` = experimental atrás de flag, fora da trilha principal.
- `(incerto)` marca o que não foi possível confirmar só pelo código/docs e precisa de teste E2E com número real.

## 2. Matriz

### 2.1 Segurança e acesso (Z-API: Segurança)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| SEC-1 | Credencial por instância (ID + token) | ID e Token | N/A | Pronto — token `lzi_` por instância, só hash SHA-256 guardado, `Authorization: Bearer` | — | LiteZap usa header Bearer em vez de token na URL (não vaza em logs de proxy) | M1 |
| SEC-2 | Token de conta que vale para todas as instâncias | Token de segurança da conta (`Client-Token`) | N/A | Pronto — token admin | — | Equivalente; o admin também cria/lista/apaga instâncias | M1 |
| SEC-3 | Rotação de token | (não documentado) | N/A | Pronto — `POST /instances/{id}/token/rotate` | — | Vantagem LiteZap | M1 |
| SEC-4 | Restrição de chamadas por IP | Restrição por IP | N/A | Pronto — `ip_allowlist` por instância e por chave de API, `admin_allowed_ips`/`operator_allowed_ips`; `X-Forwarded-For` só de `trusted_proxies` (M11) | S | Lista de CIDRs por token; atrás de proxy reverso precisa de `trusted_proxies` | M11 |
| SEC-5 | 2FA no painel | Autenticação de dois fatores | N/A | Fora | — | Não há painel no motor; é da nuvem (peça #3) e do app local | — |
| SEC-6 | Proteção anti-golpe em grupos de avisos (apaga mensagens de não-admins) | Proteção Anti-Golpe | Manual — detectar remetente não-admin em grupo `announce` (`GetGroupInfo`, group.go) e apagar com `BuildRevoke(chat, sender, id)` (send.go) | Falta | M | Exige a instância ser admin; risco de falso positivo em grupos com muitos admins; depende de M8 | M8 |

### 2.2 Instância, conexão e pareamento (Z-API: Instance)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| INS-1 | QR code (string) | Pegar QRCode | Sim — `GetQRChannel` (qrchan.go) | Pronto — `GET /instances/{id}/qr` + evento `instance.qr` | — | | M1 |
| INS-2 | QR como imagem / bytes | QRCode imagem e bytes | Sim (string → imagem é local) | Pronto — `GET .../qr?format=png` | — | Z-API devolve base64; LiteZap devolve PNG direto. Avaliar `?format=base64` | M1 |
| INS-3 | Conectar por código no telefone | Conectar com telefone | Sim — `PairPhone` (pair-code.go) | Pronto — `POST .../pair-phone` + `instance.pair_code` | — | | M1 |
| INS-4 | Desafio de passkey no pareamento (concluir / reiniciar) | Complete/Reset the Challenge | Sim — `SendPasskeyResponse`, `SendPasskeyConfirmation` (pair-passkey.go); eventos `PairPasskeyRequest/Error/Confirmation` | Fora — passkey exige asserção WebAuthn do site do WhatsApp (um servidor não produz); o worker só registra o pedido no log (M9) | M | Fluxo novo do WhatsApp; se exigido, o pareamento hoje falha sem explicação. Expor estado `passkey_pending` + rota de resposta (incerto: frequência real) | M9 |
| INS-5 | Reiniciar instância | Reiniciar | N/A (Disconnect/Connect) | Pronto — `POST .../restart` (+ `start`/`stop`) | — | | M1 |
| INS-6 | Desconectar (logout) | Desconectar | Sim — `Logout` (client.go) | Pronto — `POST .../logout` | — | | M1 |
| INS-7 | Status da instância | Status da instância | Sim — `IsConnected`, `IsLoggedIn` (client.go) | Pronto — `GET /instances/{id}` (estado, motivo, jid) + `instance.status` | — | Máquina de estados mais rica que a da Z-API (banido, temp_banned, failed) | M1 |
| INS-8 | Dados do celular (plataforma, nome, conta business) | Dados do celular | Parcial — `store.Device.Platform/PushName/BusinessName` (store/store.go); modelo do aparelho não exposto (incerto) | Pronto — `GET /profile` (`platform`, `name`, `business_name`, `about`, foto) (M9) | S | | M9 |
| INS-9 | Renomear instância | Renomear instância | N/A | Pronto — `PATCH /instances/{id}` (`name`; `tags` e `ip_allowlist` com admin) (M11) | S | `PATCH /instances/{id}` | M11 |
| INS-10 | Dados da instância (me) | Dados da instância | N/A | Pronto — `GET /instances/{id}` + `GET /profile` (M9) | S | Juntar com INS-8 | M9 |
| INS-11 | Token de extensão (conector no navegador) | Gerar token de extensão | Não (produto próprio da Z-API) | Fora | — | O equivalente LiteZap é o app local (peça #2) | — |
| INS-12 | Leitura automática de mensagens | Ler mensagens automaticamente | Sim — `MarkRead` (receipt.go) | Pronto — setting `auto_read {enabled, groups}`: lote por chat, 2–5s, 1 recibo/s; `read-self` com confirmação de leitura desligada (M9) | S | Setting por instância; respeitar privacidade de confirmação de leitura | M9 |
| INS-13 | Leitura automática de status | Ler status automaticamente | Sim — `MarkRead` com chat `types.StatusBroadcastJID` | Falta (adiado no M10: os status já chegam no log; marcar como visto fica para depois) | S | Depende de receber status (STA-5) | M10 |
| INS-14 | Rejeitar chamadas automaticamente | Rejeitar chamadas | Sim — `RejectCall` (call.go) + evento `CallOffer` (types/events/call.go) | Pronto — setting `calls.auto_reject`; rejeição de chamadas individuais ao vivo pelo worker (só as que ele viu, pelo `CallCreator`) em < 3s, evento `call.received` com `rejected`; grupo só informado | S | | M7 |
| INS-15 | Mensagem automática ao rejeitar chamada | Mensagem de rejeição | Sim — `SendMessage` | Pronto — `calls.reject_message` pela fila (ritmo/janela/rampa), 1 por contato a cada 10 min, expira em 1h; só chamadas 1:1 | S | Passa pela fila (ritmo/janela) | M7 |

### 2.3 Registro como aparelho principal (Z-API: Mobile)

A Z-API registra um número como aparelho **principal** (sem celular): disponibilidade do número, pedido e confirmação de código, captcha, transferência de aparelho, PIN de duas etapas (verificar, cadastrar, confirmar, recuperar, remover), e-mail da conta (ler, cadastrar, verificar, remover) e pedido de desbanimento — 15 páginas.

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| MOB-1 | Registro do número sem celular (código, captcha, transferência) | Mobile (6 páginas) | Não — whatsmeow é dispositivo companheiro (multi-device) | Fora | — | Exigiria implementar o cliente primário; alto risco de banimento e de ToS | — |
| MOB-2 | PIN de duas etapas (5 operações) | Mobile | Não | Fora | — | Idem | — |
| MOB-3 | E-mail da conta (4 operações) | Mobile | Não | Fora | — | Idem | — |
| MOB-4 | Pedido de desbanimento | Mobile | Não | Fora | — | Fora do espírito do projeto; documentar o processo manual no guia de banimentos | — |

### 2.4 Mensagens (Z-API: Messages)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| MSG-1 | Texto | Enviar texto | Sim — `SendMessage` (send.go) | Pronto — `POST /messages` `type:text` (fila, `?wait=true`, idempotência, `expires_after`) | — | | M2 |
| MSG-2 | Atraso e "digitando" por mensagem | `delayMessage`, `delayTyping` | Sim — `SendChatPresence` (presence.go) | Pronto — `delay_ms` e `typing_ms` por mensagem (0–15000), sem furar os pisos de ritmo (M6) | S | Aceitar `delay_ms`/`typing_ms` por mensagem, sem furar os pisos de ritmo | M6 |
| MSG-3 | Link com prévia | Enviar link | Manual — `ExtendedTextMessage` com `Title`, `Description`, `MatchedText`, `JPEGThumbnail`; whatsmeow não gera prévia | Pronto — `link_preview` `off\|auto\|{url,title,description,image_url}`, busca com filtro SSRF e limite de 5s (M6) | M | Buscar Open Graph reaproveitando o fetcher com proteção SSRF de `media`; prévia opcional (`link_preview: auto\|off\|custom`) | M6 |
| MSG-4 | Imagem | Enviar imagem | Sim — `Upload`/`UploadReader` (upload.go) + `ImageMessage` | Pronto — `type:image` por URL ou base64 | — | | M2 |
| MSG-5 | Áudio e voz (PTT) | Enviar áudio | Sim | Pronto — `type:audio`, `ptt:true` | — | Voz exige OGG/Opus; conversão automática não existe (incerto se a Z-API converte) | M2 |
| MSG-6 | Vídeo | Enviar vídeo | Sim | Pronto — `type:video` | — | | M2 |
| MSG-7 | Vídeo redondo (PTV) | Enviar PTV | Manual — campo `Message.PtvMessage` (`VideoMessage`) em waE2E | Pronto — `type:ptv` (M6) | S | | M6 |
| MSG-8 | Documento | Enviar documento | Sim | Pronto — `type:document` | — | | M2 |
| MSG-9 | Figurinha | Enviar sticker | Manual — `StickerMessage` + `Upload(MediaImage)` | Pronto — `type:sticker` (WebP até 1MB, animada detectada) (M6) | S | WebP 512×512; já listado no M2b | M6 (M2b) |
| MSG-10 | GIF | Enviar GIF | Manual — `VideoMessage.GifPlayback=true` (MP4) | Pronto — `type:gif` (MP4 em loop) (M6) | S | | M6 |
| MSG-11 | Localização | Enviar localização | Manual — `LocationMessage` | Pronto — `type:location` (M6) | S | Já listado no M2b | M6 (M2b) |
| MSG-12 | Contato (vCard) | Enviar contato | Manual — `ContactMessage{Vcard}` | Pronto — `type:contact` com `contacts` (`name`+`phone` ou `vcard`) (M6) | S | Já listado no M2b | M6 (M2b) |
| MSG-13 | Vários contatos | Enviar vários contatos | Manual — `ContactsArrayMessage` | Pronto — `type:contact` com 2 a 10 cartões (M6) | S | | M6 |
| MSG-14 | Reação e remoção de reação | Enviar / remover reação | Sim — `BuildReaction` (send.go) | Pronto — `type:reaction` (`emoji:""` remove) + evento `message.reaction` | — | | M2 |
| MSG-15 | Responder citando | Responder mensagem | Manual — `ContextInfo{StanzaID, Participant, QuotedMessage}` | Pronto — `reply_to` (id LiteZap ou `wa_message_id`; fora do log = `validation_failed`) (M6) | M | Precisa da mensagem original (texto/tipo) do log; spec M2b item 6 | M6 |
| MSG-16 | Menções (membro, todos, grupo) | Mencionar membro / todos / grupo | Manual — `ContextInfo.MentionedJID`, `ContextInfo.GroupMentions` | Pronto — `mentions` (números/JIDs ou `["all"]` em grupos de até 256) (M6) | S | "Mencionar todos" exige `GetGroupInfo`; risco de spam em grupos grandes | M6 |
| MSG-17 | Editar mensagem enviada | `editMessageId` no envio | Sim — `BuildEdit` (send.go) | Pronto — `type:edit`, até 15 min (`edit_window_expired`), evento `message.edited` (M6) | S | WhatsApp limita a edição a ~15 min (incerto: janela atual) | M6 |
| MSG-18 | Apagar para todos (inclusive admin apagando de outro) | Apagar mensagens | Sim — `RevokeMessage`, `BuildRevoke` (send.go) | Pronto — `type:revoke`, até 48h; de outro só como admin do grupo; evento `message.revoked` (M6) | S | Janela de revogação do WhatsApp (~2 dias, incerto) | M6 |
| MSG-19 | Encaminhar | Encaminhar mensagem | Manual — reenviar o `waE2E.Message` original com `ContextInfo.IsForwarded` | Pronto — `type:forward` (texto, localização, contato e mídia recebida; não mídia enviada pela API nem visualização única) (M6) | M | O log não guarda o proto bruto; guardar o proto (ou reconstruir a partir de `media_json`) | M6 |
| MSG-20 | Marcar como lidas | Ler mensagens | Sim — `MarkRead` (receipt.go) | Pronto — `POST /chats/{jid}/read` | — | | M2 |
| MSG-21 | Fixar / desafixar mensagem | Fixar mensagens | Manual — `PinInChatMessage` via `SendMessage` | Pronto — `type:pin` (`24h`, `7d`, `30d`; `unpin`) (M6) | S | Duração e regras de grupo (incerto) | M6 |
| MSG-22 | Enquete | Enviar enquete | Sim — `BuildPollCreation` (msgsecret.go) | Pronto — `type:poll` (2 a 12 opções, `max_selections`) (M6) | S | | M6 |
| MSG-23 | Votar em enquete e receber votos | Enviar voto | Sim — `BuildPollVote`, `DecryptPollVote` (msgsecret.go) | Parcial — votos recebidos e decifrados em `message.poll_vote` (M6); votar pela API não | M | Votos chegam cifrados; precisa do segredo da enquete (o whatsmeow guarda no store) e de evento `message.poll_vote` | M6 |
| MSG-24 | Eventos (criar, editar, responder) | Enviar / editar / responder evento | Manual — `EventMessage`, `EncEventResponseMessage` (waE2E) | Falta (adiado no M10) | M | Recurso recente (incerto em contas comuns) | M10 |
| MSG-25 | Botões de ação, lista de botões, com imagem, com vídeo | 4 páginas | Manual — `ButtonsMessage`/`TemplateMessage`; o whatsmeow já anexa o nó `biz` (send.go, `getButtonTypeFromMessage`) | Falta | M | **Instável**: a própria Z-API mantém uma página sobre a instabilidade, exige aceite de termos e, na versão de 27/08/2024, o resumo diz que só lista de opções em chat individual é confiável (a tabela da mesma página diz que tudo funciona — contraditório). Pode quebrar a cada versão do WhatsApp | Exp |
| MSG-26 | Lista de opções | Enviar lista de opções | Manual — `ListMessage` (nó `biz` com `v=2` e tipo, send.go `getButtonAttributes`) | Falta | M | Mesma instabilidade; é o tipo interativo menos instável | Exp |
| MSG-27 | Botão OTP (copiar código) e botão PIX | 2 páginas | Manual — `InteractiveMessage` com native flow (incerto) | Falta | M | Instável; PIX é específico do Brasil | Exp |
| MSG-28 | Carrossel | Enviar carrossel | Manual — `InteractiveMessage` carrossel (incerto) | Falta | L | Instável | Exp |
| MSG-29 | Receber respostas de botões/listas | Responder botão / ação / lista | Sim — chegam em `events.Message` (`ButtonsResponseMessage`, `ListResponseMessage`, `InteractiveResponseMessage`) | Pronto — tipo `button_reply` no log e evento `message.button_reply` (botão, lista, template, interativa) | S | Útil mesmo sem enviar botões (clientes de outras ferramentas respondem) | M7 |
| MSG-30 | Produto e catálogo como mensagem | Enviar produto / catálogo | Manual — `ProductMessage` (waE2E); `mediatype="product"` automático em send.go | Pronto, validado no aparelho em 2026-10-21 (cartão entregue e exibido no celular) — `type: "product"` em `POST /messages` (produto lido do catálogo por GraphQL, primeira foto reenviada como imagem cifrada com miniatura, `businessOwnerJID`), pela fila; catálogo inteiro como link `wa.me/c/<número>` (`catalog_link`); MCP `send_product` (M12) | M | `CatalogSnapshot` e lista de produtos (`ListMessage`) não são enviados (renderização incerta em aparelhos vinculados) | M12 |
| MSG-31 | Pedido + atualizar status/pagamento do pedido | 3 páginas | Parcial — `OrderMessage` (proto) e `GetOrderDetails` (business.go) só leitura | Parcial — pedido recebido vira mensagem `order` e evento `order.received` (token guardado só no servidor); `GET /orders` e `GET /orders/{order_id}` (`GetOrderDetails` pelo socket, GraphQL `xwa_checkout_get_order_info` como alternativa); MCP `get_order`; validado no aparelho em 2026-10-21 (pedido recebido; detalhes pelo GraphQL, porque o `GetOrderDetails` do socket não respondeu); atualizar status ou pagamento: falta (M12) | L | O IQ de pedido do socket não respondeu no aparelho: o GraphQL é o caminho que funciona; pagamentos do WhatsApp Business no Brasil fora do escopo | M12 |
| MSG-32 | Convite de admin de canal | Enviar convite de admin de canal | Manual — `NewsletterAdminInviteMessage` (waE2E) | Falta | M | Aceitar/revogar não têm função (CHN-11) | Avaliar |
| MSG-33 | Conversar com a Meta AI | Meta AI | Parcial — `types.MetaAIJID`, `GetBotListV2` (user.go); envio comum ao JID do bot (incerto) | Falta | M | Valor baixo para atendimento | Avaliar |

### 2.5 Privacidade (Z-API: Privacy)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| PRV-1 | Visto por último, foto, recado, quem adiciona em grupos, online, confirmação de leitura | 6 páginas | Sim — `SetPrivacySetting`, `GetPrivacySettings`, `TryFetchPrivacySettings` (privacysettings.go); tipos `last`, `profile`, `status`, `groupadd`, `online`, `readreceipts`, `calladd`, `messages` (types/user.go) | Pronto — `GET/PATCH /privacy` (`last_seen`, `online`, `profile_photo`, `about`, `groups_add`, `read_receipts`, `calls`, `messages`) (M9) | S | | M9 |
| PRV-2 | Duração padrão de mensagens temporárias | Mensagens temporárias | Sim — `SetDefaultDisappearingTimer` (privacysettings.go) | Pronto — `PATCH /privacy {"default_disappearing_seconds"}` (M9) | S | | M9 |
| PRV-3 | Listar contatos excluídos de uma regra ("meus contatos, exceto…") | Contatos não permitidos | Parcial — só `GetStatusPrivacy` (broadcast.go) para status; demais listas sem função (incerto) | Falta | M | | M9 |

### 2.6 Contatos (Z-API: Contacts)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| CON-1 | Listar contatos | Pegar contatos | Sim — `Store.Contacts.GetAllContacts` (store) | Pronto — `GET /contacts` | — | | M2 |
| CON-2 | Adicionar / remover contato na agenda | Adicionar / remover contatos | Parcial — existe a ação de app state `contact` (evento `events.Contact`), sem builder em appstate/encode.go (incerto) | Falta | M | | M9 |
| CON-3 | Metadados do contato (nome, recado, business) | Metadata do contato | Sim — `GetUserInfo`, `GetBusinessProfile` (user.go) | Pronto — `GET /contacts/{jid}/profile` (recado, foto, nome verificado, perfil comercial) com cota (M9) | S | Consultar a rede só sob demanda (`?refresh=1`) com cota | M9 |
| CON-4 | Foto de perfil do contato | Pegar imagem do contato | Sim — `GetProfilePictureInfo` (user.go) | Pronto — `GET /contacts/{jid}/picture[?download=1]` com cache; sem foto ou escondida = 404 (M9) | S | Cache + URL assinada como mídia | M9 |
| CON-5 | Número tem WhatsApp? | Número com WhatsApp | Sim — `IsOnWhatsApp` (user.go) | Pronto — `POST /check-numbers` (cache 30d/24h, cota própria) | — | | M2 |
| CON-6 | Verificação em lote | Validação em lote | Sim | Pronto — mesmo endpoint, com ritmo e limite diário | — | Raspagem de números é gatilho de banimento; LiteZap limita de propósito | M2 |
| CON-7 | Bloquear / desbloquear | Bloquear contato | Sim — `UpdateBlocklist`, `GetBlocklist` (user.go) | Pronto — `POST /contacts/{jid}/block\|unblock`, `GET /blocklist` (M9) | S | | M9 |
| CON-8 | Denunciar contato | Denunciar contato | Não (sem função) | Fora | — | | — |

### 2.7 Chats (Z-API: Chats)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| CHT-1 | Listar chats | Pegar chats | Parcial — não há lista de chats; vem do history sync e das mensagens | Pronto — `GET /chats` (do log, ordenado por última mensagem) | — | Chats sem mensagem no log não aparecem | M2 |
| CHT-2 | Metadados do chat | Metadata do chat | Parcial | Pronto — `GET /chats/{jid}` com arquivado/fixado/silenciado/não lido (reconciliado com o worker) (M9) | S | Incluir arquivado/fixado/silenciado (vindos do app state) | M9 |
| CHT-3 | Ler / marcar como não lido | Ler chats | Sim — `MarkRead` e `appstate.BuildMarkChatAsRead` (appstate/encode.go) | Pronto — `PATCH /chats/{jid} {"unread"}` (lido também manda recibos) (M9) | S | | M9 |
| CHT-4 | Arquivar / desarquivar | Arquivar chat | Sim — `appstate.BuildArchive` + `SendAppState` (appstate.go) | Pronto — `PATCH /chats/{jid} {"archived"}` + `chat.updated` (M9) | S | | M9 |
| CHT-5 | Fixar chat | Fixar chat | Sim — `appstate.BuildPin` | Pronto — `PATCH /chats/{jid} {"pinned"}` (limite 3) (M9) | S | | M9 |
| CHT-6 | Silenciar | Silenciar chat | Sim — `appstate.BuildMute`, `BuildMuteAbs` | Pronto — `PATCH /chats/{jid} {"muted","mute_seconds"}` (M9) | S | | M9 |
| CHT-7 | Limpar conversa | Limpar chat | Parcial — índice `clearChat` (appstate/keys.go) e evento `ClearChat`, sem builder | Falta | M | Montar a mutação à mão (incerto) | M9 |
| CHT-8 | Apagar chat | Deletar chat | Sim — `appstate.BuildDeleteChat` | Pronto — `DELETE /chats/{jid}` (só marca no log; some da lista até mensagem nova) (M9) | S | Também apagar do log local? (decidir: só flag) | M9 |
| CHT-9 | Mensagens temporárias do chat | Expiração do chat | Sim — `SetDisappearingTimer` (send.go) | Falta | S | | M9 |
| CHT-10 | Histórico de mensagens do chat | (não há) | N/A | Pronto — `GET /chats/{jid}/messages` com cursor | — | Vantagem LiteZap | M2 |
| CHT-11 | Busca full-text | (não há) | N/A | Falta — planejado | S | `GET /messages/search` (FTS5) | M2b |

### 2.8 Chamadas (Z-API: Calls)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| CAL-1 | Fazer chamada, "chamada de atenção", áudio na chamada | Fazer ligação | Não — whatsmeow não implementa VoIP/mídia de chamada | Fora | — | A Z-API vende instância com chamadas como plano à parte | — |
| CAL-2 | Token de chamada / SIP | 3 páginas | Não | Fora | — | | — |
| CAL-3 | Eventos de chamada (oferta, aceite, término, rejeição) | via webhooks de mensagem | Sim — `CallOffer`, `CallAccept`, `CallTerminate`, `CallReject`, `CallOfferNotice` (types/events/call.go) | Pronto — `call.received`, `call.accepted`, `call.ended` (`reason`, `missed`); `CallRelayLatency`/`Transport`/`PreAccept` ignorados | S | Evento `call.received` / `call.ended` | M7 |

### 2.9 Grupos (Z-API: Groups)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| GRP-1 | Listar grupos | Buscar grupos | Sim — `GetJoinedGroups` (group.go) | Pronto — `GET /groups` (M8) | S | | M8 |
| GRP-2 | Criar grupo | Criar grupo | Sim — `CreateGroup` | Pronto — `POST /groups`, até 10 participantes, cota diária `groups.max_creates_per_day` (M8) | S | Criar grupos adicionando desconhecidos é gatilho de banimento; limitar por dia | M8 |
| GRP-3 | Nome, foto, descrição | 3 páginas | Sim — `SetGroupName`, `SetGroupPhoto` (JPEG), `SetGroupDescription`/`SetGroupTopic` | Pronto — `PATCH /groups/{jid}` (nome, descrição), `PUT\|DELETE /groups/{jid}/photo` (M8) | S | | M8 |
| GRP-4 | Adicionar / remover participantes | 2 páginas | Sim — `UpdateGroupParticipants` (add/remove) | Pronto — `POST /groups/{jid}/participants` (add/remove), resultado por participante; adições só de contatos conhecidos, com ritmo e cotas (M8) | S | Quem restringe "quem me adiciona" gera erro 403 com convite; devolver por participante | M8 |
| GRP-5 | Aprovar / rejeitar pedidos de entrada | 2 páginas | Sim — `GetGroupRequestParticipants`, `UpdateGroupRequestParticipants` | Pronto — `GET\|POST /groups/{jid}/requests` (M8) | S | | M8 |
| GRP-6 | Promover / rebaixar admin | 2 páginas | Sim — `UpdateGroupParticipants` (promote/demote) | Pronto — `POST /groups/{jid}/participants` (promote/demote) (M8) | S | | M8 |
| GRP-7 | Sair do grupo | Sair do grupo | Sim — `LeaveGroup` | Pronto — `POST /groups/{jid}/leave` (M8) | S | | M8 |
| GRP-8 | Metadados (completo e leve) | 2 páginas | Sim — `GetGroupInfo` | Pronto — `GET /groups/{jid}` (completo), `GET /groups` (leve) (M8) | S | Também preenche o nome dos chats de grupo no log (M2b item 6) | M8 |
| GRP-9 | Metadados por link de convite | Metadata pelo convite | Sim — `GetGroupInfoFromLink` | Pronto — `POST /groups/preview` (M8) | S | | M8 |
| GRP-10 | Configurações (só admins enviam, editam, aprovação, quem adiciona) | Configurações do grupo | Sim — `SetGroupAnnounce`, `SetGroupLocked`, `SetGroupJoinApprovalMode`, `SetGroupMemberAddMode` | Pronto — `settings` no `PATCH` (announce, locked, approval_required, admins_add_members, ephemeral_seconds) (M8) | S | | M8 |
| GRP-11 | Link de convite (obter / redefinir) | 2 páginas | Sim — `GetGroupInviteLink(reset)` | Pronto — `GET /groups/{jid}/invite`, `POST …/invite/reset` (M8) | S | | M8 |
| GRP-12 | Entrar por convite | Aceitar convite | Sim — `JoinGroupWithLink`, `JoinGroupWithInvite` | Pronto — `POST /groups/join` (link; convite por mensagem adiado), cota `max_joins_per_day` (M8) | S | | M8 |
| GRP-13 | Eventos de grupo (entrou, saiu, promovido, nome/descrição mudou) | (webhook de recebimento) | Sim — `events.GroupInfo`, `events.JoinedGroup` | Pronto — `group.updated` com `kind` (M8) | S | | M8 |

### 2.10 Comunidades (Z-API: Communities)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| COM-1 | Criar comunidade | Criar comunidade | Sim — `CreateGroup` com `IsParent` (group.go) | Pronto — `POST /communities` (M8) | S | | M8 |
| COM-2 | Listar comunidades | Listar comunidades | Sim — `GetJoinedGroups` filtrando `IsParent` | Pronto — `GET /communities` (M8) | S | | M8 |
| COM-3 | Vincular / desvincular grupos | 2 páginas | Sim — `LinkGroup`, `UnlinkGroup` | Pronto — `POST /communities/{jid}/groups`, `DELETE …/groups/{group}` (M8) | S | | M8 |
| COM-4 | Metadados | Metadata da comunidade | Sim — `GetGroupInfo`, `GetSubGroups`, `GetLinkedGroupsParticipants` | Pronto — `GET /communities/{jid}` (com sub-grupos; `GetLinkedGroupsParticipants` adiado) (M8) | S | | M8 |
| COM-5 | Redefinir link | Redefinir link | Sim — `GetGroupInviteLink(reset=true)` | Pronto — `POST /groups/{jid}/invite/reset` (M8) | S | | M8 |
| COM-6 | Participantes e admins | 4 páginas | Sim — `UpdateGroupParticipants` no grupo pai/avisos (incerto: semântica exata em comunidades) | Pronto — rotas de `/groups/{jid}/participants` aplicadas à comunidade (semântica não verificada em aparelho real) (M8) | M | | M8 |
| COM-7 | Configurações e descrição | 2 páginas | Sim — mesmas funções de grupo aplicadas ao pai (incerto) | Pronto — `PATCH /groups/{jid}` aplicado à comunidade (não verificado em aparelho real) (M8) | S | | M8 |
| COM-8 | Desativar comunidade | Desativar | Não — sem função pública | Falta | — | Avaliar engenharia reversa | Avaliar |

### 2.11 Canais (Z-API: Newsletter)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| CHN-1 | Criar canal | Criar canal | Sim — `CreateNewsletter` (newsletter.go) | Pronto — `POST /channels` (com foto), 2/dia (M10) | S | | M10 |
| CHN-2 | Atualizar nome, descrição, foto | 3 páginas | Não — sem função; exigiria query MEX via `DangerousInternals` (frágil) | Falta | L | | Avaliar |
| CHN-3 | Seguir / deixar de seguir | 2 páginas | Sim — `FollowNewsletter`, `UnfollowNewsletter` | Pronto — `POST /channels/{jid}/follow` e `/unfollow`, 3/min, 10/h e 30/dia (M10) | S | | M10 |
| CHN-4 | Silenciar / reativar | 2 páginas | Sim — `NewsletterToggleMute` | Pronto — `POST /channels/{jid}/mute` e `/unmute` (M10) | S | | M10 |
| CHN-5 | Apagar canal | Deletar canal | Não | Falta | — | | Avaliar |
| CHN-6 | Metadados (por JID ou convite) | Metadata do canal | Sim — `GetNewsletterInfo`, `GetNewsletterInfoWithInvite` | Pronto — `GET /channels/{jid}` e `POST /channels/preview {"invite"}` (M10) | S | | M10 |
| CHN-7 | Listar assinantes | Listar inscritos | Não | Falta | — | | Avaliar |
| CHN-8 | Listar canais seguidos | Listar canais | Sim — `GetSubscribedNewsletters` | Pronto — `GET /channels` (M10) | S | | M10 |
| CHN-9 | Buscar canais (diretório) | Buscar canais | Não | Falta | — | | Avaliar |
| CHN-10 | Configurações do canal (reações) | Configurações | Não | Falta | — | | Avaliar |
| CHN-11 | Admins: aceitar convite, remover, revogar convite, transferir posse | 4 páginas | Não | Falta | — | | Avaliar |
| CHN-12 | Publicar no canal (texto e mídia) | (via endpoints de mensagem) | Sim — `SendMessage` para JID `@newsletter` + `UploadNewsletter` (upload.go) | Pronto — `POST /channels/{jid}/messages` (texto, imagem, vídeo, áudio, documento) pela fila, só dono/admin (M10) | M | Mídia de canal não é cifrada (upload próprio) | M10 |
| CHN-13 | Reagir, marcar visto, ler mensagens do canal | (não há) | Sim — `NewsletterSendReaction`, `NewsletterMarkViewed`, `GetNewsletterMessages` | Parcial — `POST /channels/{jid}/reactions`, `GET /channels/{jid}/messages`, evento opt-in `channel.message`; marcar visto e mídia recebida não (M10) | S | | M10 |

### 2.12 Listas de transmissão (Z-API: Transmission List)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| BRD-1 | Criar, atualizar, listar, apagar lista e enviar para ela | 4 páginas | Não — `ErrBroadcastListUnsupported` (broadcast.go) | Fora | — | Envio em massa é o principal gatilho de banimento; já fora do escopo no spec do motor | — |

### 2.13 Status / Stories (Z-API: Status)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| STA-1 | Status de texto | Enviar status de texto | Manual — `ExtendedTextMessage` (cor de fundo, fonte) para `types.StatusBroadcastJID`; destinatários resolvidos por `getStatusBroadcastRecipients` (broadcast.go) | Pronto — `POST /status {"type":"text"}` (fundo, cor do texto, fonte) pela fila, 10/dia (M10) | S | Envio cifrado para todos os contatos: lento com agendas grandes | M10 |
| STA-2 | Status de imagem | Enviar status de imagem | Manual — idem com `ImageMessage` | Pronto — `POST /status {"type":"image"}` (M10) | S | | M10 |
| STA-3 | Status de vídeo | Enviar status de vídeo | Manual — idem com `VideoMessage` | Pronto — `POST /status {"type":"video"}` (M10) | S | | M10 |
| STA-4 | Responder status (texto, GIF, figurinha) | 3 páginas | Manual — mensagem ao autor com `ContextInfo` citando o status (`RemoteJID` `status@broadcast`) | Parcial — `POST /status/{msg_id}/reply` só texto (GIF/figurinha não) (M10) | S | Depende de MSG-15 | M10 |
| STA-5 | Receber status dos contatos | (webhook) | Sim — `events.Message` com chat `status@broadcast` | Pronto — status no log (`GET /status`, mídia baixável) e evento opt-in `status.received` (M10) | S | Evento `status.received`; desligado por padrão (volume) | M10 |
| STA-6 | Privacidade do status | (não há) | Sim — `GetStatusPrivacy` (broadcast.go) | Pronto — `GET /status/privacy` (M10) | S | | M10 |

### 2.14 Fila de mensagens (Z-API: Message Queue)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| QUE-1 | Fila persistente, ordem por destinatário, envio após reconexão | Introdução da fila | N/A | Pronto — `outbound_queue`, TTL (`expires_after`), retry, rampa, janela, verificação de número | — | LiteZap vai além: rampa para números novos, janela de horário, pisos de ritmo | M2 |
| QUE-2 | Listar fila | Fila | N/A | Pronto — `GET /instances/{id}/queue` com `status`, `limit` e cursor (M6) | S | `GET /instances/{id}/queue?status=queued` com cursor | M6 |
| QUE-3 | Apagar a fila inteira | Apagar fila | N/A | Pronto — `DELETE /instances/{id}/queue` (`{"canceled": n}`) (M6) | S | Importante antes de reconectar uma instância parada há dias | M6 |
| QUE-4 | Apagar um item da fila | Apagar mensagem da fila | N/A | Pronto — `DELETE /instances/{id}/queue/{msg_id}` (só `queued`; `in_flight` = `409 conflict`) (M6) | S | `DELETE /instances/{id}/messages/{msg_id}` se ainda `queued` | M6 |
| QUE-5 | Configurar a fila (intervalos) | Atualizar config da fila | N/A | Pronto — `PUT /settings` (`min_interval_ms`, `jitter_ms`, `max_per_minute`, rampa, janela) | — | | M2 |

### 2.15 WhatsApp Business (Z-API: WhatsApp Business)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| BUS-1 | Produtos: criar/editar, listar (várias versões), obter, apagar | 7 páginas | Não — sem API de catálogo no whatsmeow; os IQs `w:biz:catalog` do Baileys não respondem mais; o WhatsApp Web lê por GraphQL público (`graph.whatsapp.com/graphql/catalog`) e escreve por `graph.facebook.com` com token de conta de anúncios | Parcial — listar e obter prontos; leitura validada no aparelho em 2026-10-21 (1 produto aprovado lido) (`GET /catalog`, `GET /catalog/products/{id}`, foto pelo LiteZap; pela proxy da instância, cache de 5 min, token e `doc_id` na config); criar, editar e apagar: fora por ora (fluxo de token de anúncios, risco de bloqueio) (M12) | L | Leitura depende de token e `doc_id` do WhatsApp Web, que mudam (`upstream_changed`); o endpoint público pode omitir ocultos e em análise | M12 (escrita: avaliar) |
| BUS-2 | Coleções: criar, listar, editar, apagar, produtos da coleção | 7 páginas | Não — leitura por GraphQL público; escrita só por `graph.facebook.com` (token de anúncios) | Parcial — listar coleções com os primeiros produtos (`GET /catalog/collections`, filtro por coleção no painel), aguardando validação no aparelho; criar, editar e apagar: fora por ora (M12) | L | Coleções novas levam até 24h na revisão | M12 (escrita: avaliar) |
| BUS-3 | Configuração do catálogo | Config do catálogo | Não — carrinho por `graph.facebook.com` (token de anúncios); IQ `commerce_settings` antigo talvez ainda responda | Falta — só leitura do catálogo; ligar/desligar carrinho e visibilidade não são oferecidos (M12) | — | Mesmo risco das escritas do catálogo | Avaliar |
| BUS-4 | Etiquetas: listar, cores, criar, editar, apagar | 5 páginas | Parcial — `appstate.BuildLabelEdit` (criar/editar/apagar); lista vem dos eventos `LabelEdit` do app state; cores são índices fixos | Pronto — `GET`/`POST /labels`, `PATCH`/`DELETE /labels/{id}` (só Business: `409 business_account_required`) (M10) | M | Só contas Business; guardar etiquetas no store a partir do sync | M10 |
| BUS-5 | Atribuir / remover etiqueta de chat | 2 páginas | Sim — `appstate.BuildLabelChat` (+ `BuildLabelMessage`) | Pronto — `PUT`/`DELETE /labels/{id}/chats/{jid}` e `/messages/{msg_id}`, evento `label.updated` (M10) | S | Evento `label.updated` | M10 |
| BUS-6 | Notas no chat | Notas | Não (incerto) | Fora | — | | — |
| BUS-7 | Perfil comercial: descrição, e-mail, endereço, sites, horário, categorias (editar) | 7 páginas | Não — só leitura; IQ `w:biz` set (`business_profile v=3 mutation_type=delta`, como o Baileys e o WhatsApp Web) montado no worker | Pronto; validado num número Business em 2026-10-21 para descrição, endereço, localização e horário (e apagar descrição e endereço com `""`); `clear` para remover campos (horário validado no aparelho em 2026-10-21; o pin do mapa não pode ser removido por aparelho conectado: as codificações voltaram 406 ou foram ignoradas, e `clear: ["location"]` responde `422 not_supported`, a remoção é no app WhatsApp Business) — `PATCH /business/profile` (descrição, endereço, localização, e-mail, até 2 sites, até 3 categorias por id, horário por dia), evento `business.profile_updated`, ferramenta MCP `update_business_profile`, seção "Perfil da empresa" no painel (M12) | L | Localização, categorias e remover sites seguem o formato de leitura (não estão no Baileys); busca de categorias não implementada (IQ desconhecido) | M12 |
| BUS-8 | Ler perfil comercial | Dados da conta business | Sim — `GetBusinessProfile` (user.go); o parser do whatsmeow ignora descrição, sites e localização, então o worker lê o mesmo IQ com parser próprio (M12) | Pronto, aguardando validação no aparelho dos campos novos — `GET /business/profile` (própria conta, agora com `description`, `websites`, `location`, `category_list` e `business_hours`); de contatos em `GET /contacts/{jid}/profile` (M9/M10) | S | | M10/M12 |

### 2.16 Perfil da conta (Z-API: dentro de Instance)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| PRF-1 | Nome do perfil | Atualizar nome do perfil | Sim — `appstate.BuildSettingPushName` + `SendAppState` | Pronto — `PATCH /profile {"name"}` (M9) | S | | M9 |
| PRF-2 | Recado (About) | Atualizar descrição do perfil | Sim — `SetStatusMessage` (user.go) | Pronto — `PATCH /profile {"about"}` (M9) | S | | M9 |
| PRF-3 | Foto do perfil | Atualizar foto do perfil | Parcial — `SetGroupPhoto` com o próprio JID como alvo (namespace `w:profile:picture`, group.go); uso comum na comunidade, sem helper dedicado (incerto) | Pronto — `PUT/DELETE /profile/picture` (JPEG 640x640, `SetGroupPhoto` sem target) (M9; validar com celular) | S | JPEG; testar em E2E | M9 |

### 2.17 Webhooks e eventos (Z-API: Webhooks)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| WHK-1 | Mensagem recebida | Ao receber | Sim — `events.Message` | Pronto — `message.received` (+ `message.reaction`) | — | | M3 |
| WHK-2 | Mensagem enviada (API e celular, "enviadas por mim") | Ao enviar; Notificar enviadas por mim | Sim | Pronto — `message.sent` com `source: api\|device` | — | | M3 |
| WHK-3 | Status da mensagem (enviada, entregue, lida, tocada, apagada) | Status da mensagem | Sim — `events.Receipt` (inclui `played`) | Pronto — `played` é status próprio (`read` < `played`); `revoked` via `message.revoked` (M6) | S | | M7 |
| WHK-4 | Conectou / desconectou | Ao conectar; Ao desconectar | Sim — `Connected`, `Disconnected`, `LoggedOut`, `TemporaryBan`… | Pronto — `instance.status` para toda transição | — | | M3 |
| WHK-5 | Presença do chat (digitando, gravando, online, visto por último) | Status do chat | Sim — `events.ChatPresence`, `events.Presence` + `SubscribePresence` (presence.go) | Pronto — `chat.presence` (composing/recording/paused, online/offline); opt-in: `presence.online` + tipo nomeado; `POST /presence/subscribe` (20/min, 200/dia) | M | Presença exige assinatura por contato e a própria instância "online" (`SendPresence`); volume alto | M7 |
| WHK-6 | Mensagem apagada / editada | (no recebimento) | Sim — `ProtocolMessage` REVOKE/EDIT em `events.Message` | Pronto — `message.edited`/`message.revoked` (M6), log atualizado; só do autor ou admin | S | Base em M2b item 5 | M7 |
| WHK-7 | URLs por tipo e "atualizar todos" | Atualizar todos os webhooks | N/A | Pronto — vários webhooks, globais ou por instância, com filtro de tipos (`message.*`) | — | Superconjunto | M3 |
| WHK-8 | Filtros por tipo de mensagem (grupo, privado, texto, imagem…) | Filtros de webhook | N/A | Pronto — `filters {chat, chats, exclude_chats, message_types, from_me}` no webhook | S | Campo `filters` no webhook (`chat: group\|direct`, `message_types`) | M7 |
| WHK-9 | Webhook de falha de proxy | Webhook de falha de proxy | N/A | Pronto — `instance.proxy_failed` (nuvem E6) com `error` do worker sem credenciais (M11) | S | Evento `instance.proxy_failed` (o estado `reconnecting` com motivo já aparece) | M11 |
| WHK-10 | Assinatura HMAC, anti-replay, retry 24h, dead-letter, SSE | (não há) | N/A | Pronto | — | Vantagem LiteZap | M3 |

### 2.18 Parceiros e multi-instância (Z-API: Partners)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| PAR-1 | Criar instância por API | Criar instância | N/A | Pronto — `POST /instances` (admin) | — | | M1 |
| PAR-2 | Assinar / cancelar / atualizar assinatura | 3 páginas | N/A | Fora do motor | — | É cobrança: control plane da nuvem (peça #3) | — |
| PAR-3 | Apagar instância (uma e em lote) | 2 páginas | N/A | Pronto — `DELETE /instances/{id}` e `POST /instances/bulk` (start/stop/restart/delete por ids ou etiqueta, `confirm` no delete) (M11) | S | | M11 |
| PAR-4 | Listar instâncias | Listar instâncias | N/A | Pronto — `GET /instances` com `state`, `tag`, `q`, `limit`, `cursor` (M11) | — | | M1 |
| PAR-5 | Proxy por instância | Configurar proxy | Sim — `SetProxyAddress` (client.go) | Pronto — `proxy_url` (admin) | — | | M2 |
| PAR-6 | Componente de conexão embutível (modal de QR para o software do parceiro) | SDK de conexão; Gerar token do SDK | N/A | Pronto — `POST /instances/{id}/pairing-links` (HMAC, ≤10 min, uso único), página `/pair/` e `<litezap-pair>`, evento `instance.paired` (M11) | M | Página de pareamento hospedada com token de uso único e curto (link assinado) + web component | M11 |
| PAR-7 | Conexão por extensão para parceiros | Extensão para parceiros | N/A | Fora | — | | — |

### 2.19 IA (Z-API: MCP Server)

| ID | Recurso | Z-API | whatsmeow | LiteZap hoje | Esf. | Risco / notas | Marco |
|---|---|---|---|---|---|---|---|
| AI-1 | Servidor MCP | MCP Server (introdução, como conectar) | N/A | Pronto — `/mcp` (streamable HTTP) e `litezap mcp` (stdio), 40 ferramentas (M4, ampliado do M6 ao M10) | M | LiteZap terá HTTP e stdio | M4 |
| AI-2 | Agente que responde mensagens (hook) | (não há) | N/A | Pronto — hook de agente com pausa por humano (M5) e atendente embutido com perfis, provedores Anthropic/OpenAI-compatível e multimodal (M5b) | M | Vantagem LiteZap planejada | M5 |

## 3. Contagens

Contagem por linha da matriz (cada linha agrupa as páginas equivalentes da Z-API; as ~250 páginas de referência viram 159 linhas). Atualizada em 2026-10-19 com o código do `main` (marcos 1 a 11 e atendente IA). As linhas LiteZap foram recontadas em 2026-10-21 com o script do Anexo A, depois do M12 (MSG-30, MSG-31, BUS-1…3 e BUS-7).

| | Linhas |
|---|---|
| **Total de recursos** | **159** |
| Recursos de protocolo (dependem do whatsmeow) | 132 |
| — whatsmeow `Sim` | 78 |
| — whatsmeow `Manual` (proto existe, sem helper) | 23 |
| — whatsmeow `Parcial` | 10 |
| — whatsmeow `Não` | 21 |
| Recursos só da camada LiteZap (`N/A`) | 27 |
| **LiteZap `Pronto`** (inclui "Pronto, aguardando validação") | **117** |
| LiteZap `Parcial` | 6 |
| LiteZap `Falta` | 22 |
| LiteZap `Fora` | 14 |

Viáveis com whatsmeow (`Sim` + `Manual` + `Parcial`): **111 de 132** recursos de protocolo. Os 21 inviáveis hoje: INS-11 (extensão, produto próprio da Z-API), MOB-1…4 (registro como aparelho principal), CON-8 (denúncia), CAL-1/2 (chamadas de voz e SIP), COM-8 (desativar comunidade), CHN-2/5/7/9/10/11 (administração avançada de canais), BRD-1 (listas de transmissão), BUS-1/2/3/6/7 (catálogo, coleções, notas, perfil comercial editável).

Várias linhas `Pronto` são recursos em que o LiteZap já vai além da Z-API (rotação de token, máquina de estados, histórico por chat, fila com rampa/janela, webhooks assinados com dead-letter, SSE, vários webhooks com filtro por tipo).

As contagens vêm do script do anexo A (lê a coluna de status de cada linha); rode de novo ao editar a matriz.

## 4. Roadmap proposto (depois do M4 MCP e do app local)

Premissas: M2b (complementos), M3 (eventos), M4 (MCP), M5 (hook de agente) e a peça #2 (app local) seguem como estão. Os marcos novos começam em M6 e são ordenados por **valor para quem migra da Z-API**: primeiro o que quebra integrações existentes (tipos de mensagem e eventos), depois gestão (grupos, chats, perfil), por último o que tem uso de nicho ou suporte frágil no protocolo.

Diferença em relação à ordem sugerida no pedido: eventos e chamadas sobem para **M7** (antes de grupos), porque CRMs e atendentes da Z-API dependem de webhooks de presença, apagadas/editadas, respostas de botões e rejeição de chamadas para funcionar; gestão de grupos é usada por menos clientes.

Regras que valem para todos os marcos: cada recurso novo sai na REST, no OpenAPI, no MCP (quando fizer sentido para IA), na referência da documentação (anatomia da seção 4 do spec de arquitetura de docs) e com teste de contrato via `httptest` + worker falso. Operações que mudam algo no WhatsApp passam pela fila quando são mensagens; operações de gestão (grupos, chats, perfil) são síncronas com timeout e cota por minuto por instância.

### M6 — Mensagens completas

Escopo: MSG-2, 3, 7, 9, 10, 11, 12, 13, 15, 16, 17, 18, 19, 21, 22, 23; QUE-2, 3, 4. Absorve o item 2 (sticker/location/contact) e o item 6 parte "respostas citadas" do M2b se ainda não tiverem saído.

- Novos `type`: `sticker`, `gif`, `ptv`, `location`, `contact` (um ou vários), `poll`, `edit`, `revoke`, `pin`, `forward`.
- Campos transversais: `reply_to` (id LiteZap ou WhatsApp), `mentions` (lista ou `"all"`), `link_preview`, `delay_ms`, `typing_ms`.
- Fila visível: listar, cancelar item, esvaziar.
- Votos de enquete: evento `message.poll_vote` com opções decifradas.

Aceite:

1. Cada tipo é enviado e aparece corretamente num celular real (roteiro E2E com número de teste, `-tags=e2e`), em chat direto e em grupo.
2. `reply_to` de uma mensagem que não está no log responde `validation_failed`; com mensagem do log, o celular mostra a citação.
3. Editar/apagar só funcionam em mensagens `from_me` (apagar de outro só em grupo onde a instância é admin); fora da janela do WhatsApp o erro é estável (`edit_window_expired`/`revoke_window_expired`).
4. Prévia de link usa o mesmo filtro SSRF da mídia (teste com URL privada recusada) e nunca atrasa o envio além de 5s (sem prévia em caso de timeout).
5. `DELETE` de item `queued` o remove sem enviar; item `in_flight` responde `conflict`.
6. Pisos de ritmo continuam valendo com `delay_ms` (teste com relógio falso).

### M7 — Eventos completos e chamadas

Entregue em 2026-10-18 (branch `feat/m7-events`, plano `docs/superpowers/plans/2026-10-18-litezap-m7-events.md`). Diferenças em relação ao texto abaixo: além de `call.received`/`call.ended` há `call.accepted`; `message.revoked`/`message.edited` já tinham saído no M6; presença por rota explícita (`POST /presence/subscribe`), sem assinatura automática; `chat.presence` é opt-in (setting `presence.online` + tipo nomeado no webhook/SSE). Fora: `group.updated` (M8).

Escopo: WHK-3 (`played`, `revoked`), WHK-5, WHK-6, WHK-8, MSG-29, CAL-3, INS-14, INS-15. Fecha os eventos adiados do M3 (`message.revoked`, `chat.presence`) — `group.updated` fica no M8, junto dos grupos.

- Novos eventos: `message.revoked`, `message.edited`, `message.button_reply`, `chat.presence`, `call.received`, `call.ended`.
- Setting `calls: {auto_reject: bool, reject_message: string}`.
- Filtros no webhook: `filters.chat` (`direct|group`), `filters.message_types`, `filters.from_me`.
- Rota `POST /instances/{id}/presence/subscribe` (ou assinatura automática dos chats ativos, com limite).

Aceite:

1. Apagar/editar no celular do contato atualiza o log (texto antigo guardado em histórico de edição) e emite o evento uma única vez.
2. Chamada recebida com `auto_reject` é rejeitada em < 3s, gera `call.received` com `rejected: true` e a mensagem de rejeição sai pela fila.
3. `chat.presence` respeita filtro e não derruba o SSE em rajada (teste de carga com 1.000 eventos/s no worker falso).
4. Webhook com `filters.chat=direct` nunca recebe mensagem de grupo (teste de contrato).
5. Catálogo de eventos do README, do OpenAPI e da docs atualizados no mesmo PR.

### M8 — Grupos e comunidades

Entregue em 2026-10-18 (branch `feat/m8-groups`, plano `docs/superpowers/plans/2026-10-18-litezap-m8-groups.md`). Diferenças em relação ao texto abaixo: SEC-6 (`announcement_guard`) ficou para depois; convite por mensagem (`JoinGroupWithInvite`) para quem só aceita convite também (devolvemos o `invite_code` do 403); `group.updated` sai um por tipo de mudança (`kind`), incluindo foto, pedido de entrada, link redefinido e vínculo de comunidade; anti-ban além da criação: adições só de contatos conhecidos (conversa no log ou confirmados pela consulta), 10 por lote, 10s entre lotes e cotas por hora/dia, entradas por link com cota diária, todas persistidas e configuráveis em `settings.groups` com tetos.

Escopo: GRP-1…13, COM-1…7, SEC-6, nome de grupos no log (M2b item 6).

- Recurso REST `/instances/{id}/groups` (listar, criar, `GET/PATCH /groups/{jid}`, participantes, admins, pedidos, convite, sair, entrar) e `/communities`.
- Evento `group.updated` (participantes, admins, nome, descrição, configurações).
- Setting opcional `announcement_guard` (SEC-6).

Aceite:

1. CRUD completo validado em E2E num grupo de teste; erros por participante (`not_on_whatsapp`, `privacy_invite_required`) devolvidos em lista, sem falhar o lote inteiro.
2. Criação de grupos limitada por dia por instância (padrão conservador, configurável; acima responde `rate_limited`).
3. Chats de grupo no log passam a ter nome correto após mensagem ao vivo.
4. `group.updated` emitido para entradas/saídas feitas no celular.

### M9 — Chats, contatos, perfil, privacidade e conta

Entregue em 2026-10-18 (branch `feat/m9-chats-profile`, plano `docs/superpowers/plans/2026-10-18-litezap-m9-chats-profile.md`). Diferenças em relação ao texto abaixo: rotas `GET/PATCH/DELETE /chats/{jid}` (ações num `PATCH` com campos `archived`, `pinned`, `muted`+`mute_seconds`, `unread`), `PUT /messages/{id}/star`, `GET /contacts/{jid}/picture|profile` (em vez de `?refresh=1`), `POST /contacts/{jid}/block|unblock`, `GET /blocklist`, `GET/PATCH /profile`, `GET/PUT/DELETE /profile/picture`, `GET/PATCH /privacy`; evento `chat.updated` (inclui estrela e limpeza feita no celular); cota própria de consultas de contato (30/min, 1000/dia), não a do `check-numbers`; a foto não usa URL assinada de mídia (URL do CDN do WhatsApp ou download autenticado pelo proxy da instância). Adiados: `clear` (CHT-7: sem builder no whatsmeow, índice 2 da mutação desconhecido), `disappearing` por chat (CHT-9: manda mensagem de protocolo ao contato, fica para quando passar pela fila), CON-2 (agenda), PRV-3 (listas de exceção) e INS-4 (passkey: exige asserção WebAuthn do site do WhatsApp; fica `Fora`). A confirmar com celular real: JID das ações em conversa direta (LID quando a conta migrou), foto do perfil sem `target`, eco do app state.

Escopo: CHT-2…9, CON-2, 3, 4, 7, PRF-1…3, PRV-1…3, INS-4, 8, 10, 12.

- Ações de chat: `archive`, `pin`, `mute`, `unread`, `clear`, `delete`, `disappearing`.
- Contatos: foto de perfil (com cache e URL assinada), metadados atualizados sob demanda, bloquear/desbloquear, lista de bloqueados.
- Perfil: nome, recado, foto. Privacidade: ler e alterar todas as configurações.
- Leitura automática (`auto_read`) e passkey no pareamento.

Aceite:

1. Cada ação de chat reflete no celular em < 10s (E2E) e nos campos de `GET /chats/{jid}`.
2. Mudanças feitas no celular (arquivar, fixar, silenciar) chegam pelo app state e atualizam o log (evento `chat.updated`).
3. Foto de perfil inexistente responde `404 not_found`, nunca `502`.
4. Consultas de rede de contato têm cota por minuto (reuso do mecanismo de `check-numbers`).

### M10 — Status, canais e Business

Escopo: STA-1…6, INS-13, CHN-1, 3, 4, 6, 8, 12, 13, BUS-4, 5, 8, MSG-24, MSG-30.

- Status: publicar texto/imagem/vídeo, responder, receber (`status.received`, desligado por padrão).
- Canais: criar, seguir, silenciar, metadados, publicar, reagir.
- Business: etiquetas (CRUD + atribuição + evento `label.updated`), ler perfil comercial.

Entregue em 2026-10-18 (plano `docs/superpowers/plans/2026-10-18-litezap-m10-status-channels.md`): serviço gRPC `Social` no worker, status e posts de canal pela fila de envio (10 status/dia, 20 publicações em canal/dia), status recebidos no log (`status@broadcast`, fora de `GET /chats`), eventos `status.received` e `channel.message` (opt-in) e `label.updated`, etiquetas em tabelas próprias, cotas de canal e etiquetas (seguir 3/min, 10/h e 30/dia, criar 2/dia, reagir 30/min, etiquetar 30/min), MCP (leitura + `follow_channel`, `label_chat`) e abas Status, Canais e Etiquetas no painel. Adiados: INS-13, MSG-24, MSG-30, mídia de canais recebida, marcar visto em canal.

Aceite:

1. Status publicado aparece para um contato de teste; `status.received` só sai com o tipo assinado explicitamente.
2. Publicação em canal criado pela instância funciona com texto e imagem.
3. Rotas de etiqueta em conta não-Business respondem `business_account_required` (409).

### M11 — Plataforma e parceiros

Entregue em 2026-10-18 (branch `feat/m11-platform`, plano `docs/superpowers/plans/2026-10-18-litezap-m11-platform.md`). Além do escopo: chaves de API `lzk_` com escopos `read`/`send` presas a uma instância (negadas no `/mcp`), allowlist também para os tokens admin e operador, `GET /instances/{id}/usage`, etiquetas (`tags`) para filtro e lote, evento `instance.paired`, métricas `litezap_queue_send_latency_seconds` e `litezap_worker_memory_rss_bytes` (Linux), e painel com edição e gerador de links. O link vale até 10 min ou até 2 min depois do pareamento (para a página mostrar o sucesso); o token vai no fragmento da URL e nas chamadas da página pelo cabeçalho `X-Pairing-Token`; `frame-ancestors` vem de `pairing_frame_ancestors` (padrão: não embutível). Adiados: metadados livres além de `tags`, escopos além de `read`/`send` e chaves multi-instância, memória por worker fora do Linux. A confirmar com celular real: pareamento completo pela página (QR e código) e `via_link`.

Escopo: SEC-4, INS-9, PAR-3, PAR-6, WHK-9.

- Allowlist de IP por token, renomear instância, exclusão em lote, paginação de instâncias, evento `instance.proxy_failed`.
- Página de pareamento hospedada (link assinado de uso único, curto) e web component para o parceiro embutir o QR no próprio software.

Aceite:

1. Token com allowlist recusa IP fora da lista com `403 forbidden`, respeitando `trusted_proxies`.
2. Link de pareamento expira em 10 min ou no primeiro pareamento concluído; nunca expõe o token da instância.

### Fora da trilha principal

- **Experimental (`Exp`)**: botões, listas, OTP/PIX, carrossel (MSG-25…28) atrás de `interactive_messages: experimental` no setting da instância, com aviso na docs de que podem parar de funcionar a qualquer atualização do WhatsApp. Só depois do M10 e só se houver demanda.
- **Avaliar** (exigem engenharia reversa ou têm valor baixo): catálogo/coleções/perfil comercial editável, administração avançada de canais, pedidos/pagamentos, Meta AI, desativar comunidade.
- **Fora** (decisão de produto ou impossível com whatsmeow): registro mobile, chamadas de voz/SIP, listas de transmissão, denúncia de contato, extensão de navegador, cobrança (vai para a nuvem).

## 5. Top 10 lacunas por valor

Lista original (2026-10-17). Todos os itens abaixo foram entregues entre o M4 e o M9; as lacunas que restam são as linhas `Falta`/`Parcial` da matriz.

1. Responder citando + menções (MSG-15, MSG-16) — M6
2. Figurinha, GIF, localização, contato(s), PTV (MSG-7, 9–13) — M6
3. Editar e apagar mensagens + eventos de apagada/editada (MSG-17, 18, WHK-6) — M6/M7
4. Prévia de link (MSG-3) — M6
5. Eventos de presença, chamadas e rejeição automática (WHK-5, CAL-3, INS-14/15) — M7
6. Gestão de grupos e evento `group.updated` (GRP-1…13) — M8
7. Enquetes com votos (MSG-22, 23) — M6
8. Fila visível e cancelável + atraso por mensagem (QUE-2…4, MSG-2) — M6
9. Ações de chat: arquivar, fixar, silenciar, não lido, apagar (CHT-3…8) — M9
10. Perfil (nome, recado, foto) e contatos (foto, bloqueio) (PRF-1…3, CON-4, CON-7) — M9

O MCP (AI-1) também é lacuna frente à Z-API, mas já está no M4.

## Anexo A — script de contagem

As contagens da seção 3 vêm deste comando (rodar na raiz do repositório); ele lê a 4ª e a 5ª colunas de toda linha de tabela cujo ID segue o padrão `XXX-n`:

```bash
awk -F'|' '/^\| [A-Z]+-[0-9]+ \|/ {
  n++; split($5,w," "); split($6,l," ");
  W[w[1]]++; L[l[1]]++
} END {
  print "total", n;
  for (k in W) print "whatsmeow", k, W[k];
  for (k in L) print "litezap", k, L[k]
}' docs/superpowers/specs/2026-10-17-zapi-parity.md
```
