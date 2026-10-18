# LiteZap Marco 6 — Mensagens completas (plano curto)

Escopo: matriz de paridade (`docs/zapi-parity`, seção 4, M6): MSG-2, 3, 7, 9, 10, 11, 12, 13, 15, 16, 17, 18, 19, 21, 22, 23; QUE-2, 3, 4. Botões, listas e carrossel ficam fora (instáveis).

APIs do whatsmeow conferidas no cache (`v0.0.0-20261016130820-0b0a1cdac3e2`): `BuildEdit` + `EditWindow` (20 min), `BuildRevoke`/`BuildMessageKey` (admin revoga de outro com o sender), `BuildPollCreation` (guarda o segredo no store ao enviar, `send.go`), `DecryptPollVote` (votos trazem SHA-256 das opções), `DecryptSecretEncryptedMessage` (edições novas cifradas), `waE2E.ContextInfo{StanzaID, Participant, QuotedMessage, MentionedJID, IsForwarded, ForwardingScore}`, `LocationMessage`, `ContactMessage`/`ContactsArrayMessage`, `StickerMessage{IsAnimated, Width, Height}`, `VideoMessage.GifPlayback`, `Message.PtvMessage`, `ExtendedTextMessage{MatchedText, Title, Description, JPEGThumbnail}`, `PinInChatMessage{PIN_FOR_ALL, UNPIN_FOR_ALL}` + `MessageContextInfo.MessageAddOnDurationInSecs` (sem helper; `getEditAttribute` já marca o nó).

## Decisões

- **Um RPC só**: `Send` ganha campos (citação, menções, prévia, localização, contatos, enquete, alvo de edit/revoke/pin, encaminhamento). Tudo continua na fila: edição, apagar e fixar também são pacing, janela e rampa como qualquer envio.
- **Tipos novos** em `POST /messages`: `sticker` (WebP, até 1MB, animado detectado pelo chunk `ANIM`), `gif` (MP4 com `gif_playback`), `ptv` (vídeo redondo), `location`, `contact` (1 a 10 vCards), `poll` (2 a 12 opções), `edit`, `revoke`, `pin` (fixar/desafixar 24h/7d/30d), `forward`.
- **Campos transversais**: `reply_to` (id LiteZap ou WhatsApp de mensagem do chat; fora do log → `validation_failed`), `mentions` (lista de números/JIDs ou `["all"]` em grupo, resolvido no worker), `link_preview` (`"auto"`, `"off"` ou objeto custom), `delay_ms` e `typing_ms` (0–15000; só somam espera: os pisos de ritmo, rampa e janela continuam valendo).
- **Prévia de link**: buscada no enfileiramento com o `media.Fetcher` (filtro SSRF, sem proxy de ambiente), 5s no total, HTML até 512KB, só `text/html`; miniatura opcional (og:image até 2MB, decodificada só se ≤ 4096×4096, reduzida para 300px JPEG). Falha ou timeout: envia sem prévia.
- **Editar**: só mensagens de texto `from_me`, dentro de 20 min (confere no enfileiramento e de novo no despacho → `edit_window_expired`). **Apagar**: as próprias em até 48h (`revoke_window_expired`); de outra pessoa só em grupo, e o worker confere se a instância é admin (`forbidden`). **Encaminhar**: texto, localização, contato e mídia recebida (reenvia o proto original com `IsForwarded`, sem novo upload); mídia enviada pela API não tem o arquivo guardado → `validation_failed`.
- **Log**: migração 0004 adiciona `meta_json` (enquete, localização, contatos, citação, menções), `edited_at` e `revoked_at` em `messages`. Edit/revoke (da API ou recebidos) atualizam a mensagem alvo. Itens de edit/revoke/pin não viram linha no log.
- **Eventos**: `message.sent`/`message.received` com os campos novos; `message.edited`, `message.revoked` e `message.poll_vote` (opções decifradas pelo hash contra as opções guardadas da enquete).
- **Fila visível**: `GET /instances/{id}/queue?status=` (cursor), `DELETE /instances/{id}/queue/{msg_id}` (só `queued`; `in_flight` → `conflict`), `DELETE /instances/{id}/queue` (esvazia os `queued`). Cancelado = `failed` com `canceled:` (evento `message.status` failed; espera e idempotência coerentes).
- **MCP**: `send_message` ganha `reply_to` e `mentions`; novas `send_reaction`, `send_location`, `send_contact`, `edit_message`, `delete_message` (com avisos de segurança).

## Tarefas (TDD)

1. Proto + worker: build de cada tipo (função pura testada), recepção de enquete/voto/edit/revoke/tipos novos.
2. Store: migração, `meta_json`, edit/revoke no log, listar/cancelar fila.
3. Queue: payload novo, `delay_ms`/`typing_ms` com relógio falso, janela de edição no despacho, efeitos de edit/revoke e eventos.
4. Service: validação de cada tipo, `reply_to`, menções, prévia de link (SSRF), encaminhamento, fila.
5. API + MCP + ingest (eventos novos).
6. README, spec, revisão independente, smoke test em 127.0.0.1:18792.

Cada commit: `gofmt`, `go vet ./...`, `go test -p 2 -race ./...`.
