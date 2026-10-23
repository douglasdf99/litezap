---
title: Tipos de mensagem
description: Campos de cada type aceito em POST /messages e os campos transversais (citação, menções, prévia de link, atraso).
---

Todos os tipos usam [Enviar mensagem](/docs/api/mensagens/enviar-mensagem/) (`POST /v1/instances/{id}/messages`).
Campos comuns: `to` (obrigatório, exceto em `edit`, `revoke` e `pin` com id do LiteZap), `type` (obrigatório),
`client_message_id`, `expires_after`.

## Conteúdo

### Texto <span class="lz-badge lz-badge--ok">M2</span>

```json
{ "to": "5511999999999", "type": "text", "text": "Olá! Seu pedido saiu para entrega." }
```

`text` obrigatório, até 64 KB. Com `typing_simulation` ligado, o contato vê "digitando" antes (cerca de 50ms por
caractere, até 5s).

### Imagem <span class="lz-badge lz-badge--ok">M2</span>

```json
{ "to": "5511999999999", "type": "image", "caption": "Comprovante", "media": { "url": "https://exemplo.com/a.jpg" } }
```

Até 16 MB. JPEG, PNG ou WebP. `caption` opcional.

### Vídeo <span class="lz-badge lz-badge--ok">M2</span>

```json
{ "to": "5511999999999", "type": "video", "caption": "Tutorial", "media": { "url": "https://exemplo.com/v.mp4" } }
```

Até 64 MB. MP4 (H.264) é o mais compatível.

### Áudio e voz <span class="lz-badge lz-badge--ok">M2</span>

```json
{ "to": "5511999999999", "type": "audio", "media": { "url": "https://exemplo.com/r.ogg", "ptt": true } }
```

Até 64 MB. `ptt: true` envia como mensagem de voz (use OGG/Opus).

### Documento <span class="lz-badge lz-badge--ok">M2</span>

```json
{ "to": "5511999999999", "type": "document", "caption": "Boleto", "media": { "url": "https://exemplo.com/b.pdf", "file_name": "boleto.pdf" } }
```

Até 100 MB, qualquer formato. `file_name` até 255 bytes.

### Reação <span class="lz-badge lz-badge--ok">M2</span>

```json
{ "to": "5511999999999", "type": "reaction", "reaction": { "message_id": "msg_a41c9e07d2b35f68", "emoji": "👍" } }
```

`message_id` aceita o id LiteZap ou o `wa_message_id` de uma mensagem do chat. `emoji` vazio remove a reação.

### Mídia: URL ou base64

`media` aceita `url` (o servidor baixa) **ou** `base64`, mais `mime` e `file_name` opcionais. Detalhes em
[Enviar mídia](/docs/guias/enviar-midia/).

### Figurinha, GIF e vídeo redondo <span class="lz-badge lz-badge--ok">M6</span>

```json
{ "to": "5511999999999", "type": "sticker", "media": { "url": "https://exemplo.com/f.webp" } }
```

`sticker`: WebP até 1 MB (animada é detectada sozinha). `gif`: MP4 tocado em loop, com `caption` opcional. `ptv`:
vídeo redondo, sem legenda. Todos usam `media` como os outros anexos.

### Localização <span class="lz-badge lz-badge--ok">M6</span>

```json
{ "to": "5511999999999", "type": "location", "location": { "latitude": -23.5614, "longitude": -46.6559, "name": "MASP" } }
```

### Contatos <span class="lz-badge lz-badge--ok">M6</span>

```json
{ "to": "5511999999999", "type": "contact", "contacts": [{ "name": "Ana", "phone": "5511977776666" }] }
```

1 a 10 cartões, cada um com `name` + `phone` ou um `vcard` pronto.

### Enquete <span class="lz-badge lz-badge--ok">M6</span>

```json
{ "to": "120363000000000000@g.us", "type": "poll", "poll": { "name": "Almoço?", "options": ["12h", "13h"], "max_selections": 1 } }
```

2 a 12 opções distintas; `max_selections` padrão 1 (`0` = qualquer número). Os votos chegam no evento
[`message.poll_vote`](/docs/eventos/message-poll-vote/), já decifrados, e o último de cada pessoa fica na
enquete: `GET /messages/{id}` mostra `poll.votes` (`me` = este número) e `poll.results` (votos por opção).
`POST /messages/{id}/poll-vote` com `{"options": ["12h"]}` vota (troca o voto anterior; `[]` retira), pela
fila. Enquetes recebidas antes do pareamento não podem ser votadas (a chave delas não existe na sessão).
Votar ainda aguarda validação num aparelho real.

### Evento <span class="lz-badge lz-badge--soon">v0.1.1</span>

```json
{ "to": "120363000000000000@g.us", "type": "event", "event": { "name": "Reunião de pais",
  "description": "Pauta: passeio", "start_time": "2026-11-05T19:00:00-03:00", "end_time": "2026-11-05T20:00:00-03:00",
  "location": { "name": "Escola", "address": "Rua Exemplo, 100" }, "extra_guests_allowed": true } }
```

Convite com data, local e link de chamada opcional (`join_link`, `https://call.whatsapp.com/...`); todos do chat
respondem "vou / não vou / talvez". `PATCH /messages/{id}` edita ou cancela (`{"event": {"canceled": true}}`),
`POST /messages/{id}/event-response` responde (`going`, `not_going`, `maybe`). As respostas chegam em
[`message.event_response`](/docs/eventos/message-event-response/) e a última de cada pessoa fica em
`event.responses`. Eventos recebidos antes do pareamento não podem ser respondidos (a chave deles não existe na
sessão). Ainda aguardando validação num aparelho real.

### Produto do catálogo <span class="lz-badge lz-badge--soon">M12</span>

```json
{ "to": "5511999999999", "type": "product", "product_id": "8364718273645", "body": "Chegou hoje!", "footer": "Loja X" }
```

Cartão com foto, nome, preço e botão "Ver", de um produto do catálogo do próprio número (ou de `catalog_owner`,
o número de outra empresa). O LiteZap lê o produto no catálogo, baixa a primeira foto e envia como imagem com
miniatura, pela fila como qualquer envio. Produtos ocultos, em análise ou sem foto: `400`. `body` até 1024 caracteres,
`footer` até 60. Veja [Catálogo e pedidos](/docs/guias/catalogo-e-pedidos/). Para mandar o catálogo inteiro,
envie o `catalog_link` de `GET /catalog` como texto com `link_preview: "auto"`.

## Ações sobre mensagens <span class="lz-badge lz-badge--ok">M6</span>

`edit`, `revoke` e `pin` usam `message_id` (id do LiteZap ou `wa_message_id`); com id do LiteZap, `to` é opcional.
Passam pela fila como qualquer envio, mas não viram mensagem nova no log.

```json
{ "type": "edit", "message_id": "msg_a41c9e07d2b35f68", "text": "texto corrigido" }
{ "type": "revoke", "message_id": "msg_a41c9e07d2b35f68" }
{ "type": "pin", "message_id": "msg_a41c9e07d2b35f68", "pin": { "duration": "7d" } }
{ "to": "5511966665555", "type": "forward", "forward": { "message_id": "msg_e29d4b8a0f6c1357" } }
```

- **Editar**: só textos enviados por este número, até 15 minutos depois (`409 edit_window_expired`). Emite
  [`message.edited`](/docs/eventos/message-edited/).
- **Apagar para todos**: as próprias mensagens até 48h (`409 revoke_window_expired`); de outra pessoa, só em grupo
  onde o número é admin. Emite [`message.revoked`](/docs/eventos/message-revoked/).
- **Fixar**: `pin.duration` `24h`, `7d` (padrão) ou `30d`; `pin.unpin: true` desafixa.
- **Encaminhar**: textos, localizações, contatos e mídia **recebida** (`forward.message_id`, ou `wa_message_id` com
  `forward.chat`). Mídia enviada pela API e mídia de visualização única não são encaminhadas.

## Campos transversais <span class="lz-badge lz-badge--ok">M6</span>

Valem para mensagens novas (não para `reaction`, `edit`, `revoke` e `pin`):

| Campo | O que faz |
|---|---|
| `reply_to` | Responde citando uma mensagem do chat (id do LiteZap ou `wa_message_id`); fora do log responde `validation_failed`. |
| `mentions` | Números ou JIDs mencionados (escreva `@<número>` no texto); em grupo, `["all"]` menciona todos (até 256 participantes). |
| `link_preview` | Só em `text`: `"off"` (padrão), `"auto"` (lê o primeiro link do texto) ou `{url, title, description, image_url}`. Busca com filtro SSRF e limite de 5s; se falhar, sai sem prévia. |
| `delay_ms`, `typing_ms` | 0 a 15000: espera extra e "digitando..." antes do envio. Só somam tempo: ritmo, rampa e janela continuam valendo. |

## Ainda não

| Tipo | Situação |
|---|---|
| Evento de agenda | <span class="lz-badge lz-badge--no">Avaliar</span> |
| Vários produtos numa mensagem (lista de produtos) | <span class="lz-badge lz-badge--no">Avaliar</span>: listas não aparecem bem em aparelhos vinculados e no WhatsApp Web |
| Botões, listas, OTP/PIX, carrossel | <span class="lz-badge lz-badge--exp">Exp</span>: não são enviados (instáveis no WhatsApp); respostas a eles chegam em [`message.button_reply`](/docs/eventos/message-button-reply/). |

Um `type` desconhecido responde `400 validation_failed`. A fila desses envios pode ser vista e cancelada em
[Listar a fila](/docs/api/mensagens/listar-a-fila/).
