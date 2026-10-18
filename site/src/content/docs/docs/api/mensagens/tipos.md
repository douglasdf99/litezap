---
title: Tipos de mensagem
description: Campos de cada type aceito em POST /messages, e os tipos que vêm nos próximos marcos.
---

Todos os tipos usam [Enviar mensagem](/docs/api/mensagens/enviar-mensagem/) (`POST /v1/instances/{id}/messages`).
Campos comuns: `to` (obrigatório), `type` (obrigatório), `client_message_id`, `expires_after`.

## Disponíveis

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

## Recebidos, mas ainda sem envio

Figurinhas, localização e contatos **recebidos** já são registrados no log e aparecem em `message.received` (com
`type` correspondente). O envio desses tipos chega no M6.

## Próximos tipos

| Tipo ou campo | O que fará | Marco |
|---|---|---|
| `sticker` | Figurinha (WebP 512×512) | <span class="lz-badge lz-badge--soon">M6</span> |
| `gif` | Vídeo MP4 reproduzido como GIF | <span class="lz-badge lz-badge--soon">M6</span> |
| `ptv` | Vídeo redondo | <span class="lz-badge lz-badge--soon">M6</span> |
| `location` | Localização (latitude, longitude, nome) | <span class="lz-badge lz-badge--soon">M6</span> |
| `contact` | Um ou vários contatos (vCard) | <span class="lz-badge lz-badge--soon">M6</span> |
| `poll` | Enquete, com votos no evento `message.poll_vote` | <span class="lz-badge lz-badge--soon">M6</span> |
| `edit`, `revoke`, `pin`, `forward` | Editar, apagar para todos, fixar e encaminhar | <span class="lz-badge lz-badge--soon">M6</span> |
| `reply_to`, `mentions`, `link_preview` | Responder citando, mencionar (inclusive todos), prévia de link | <span class="lz-badge lz-badge--soon">M6</span> |
| `delay_ms`, `typing_ms` | Atraso e "digitando" por mensagem (sem furar os pisos de ritmo) | <span class="lz-badge lz-badge--soon">M6</span> |
| Evento de agenda | Criar, editar e responder eventos | <span class="lz-badge lz-badge--no">Avaliar</span> |
| Botões, listas, OTP/PIX, carrossel | Mensagens interativas | <span class="lz-badge lz-badge--exp">Exp</span> |

Os campos e nomes acima são a proposta atual e podem mudar até o marco ser entregue. Um `type` desconhecido hoje
responde `400 validation_failed`.
