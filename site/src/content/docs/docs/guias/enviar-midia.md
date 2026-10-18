---
title: Enviar mídia
description: Imagem, vídeo, áudio, mensagem de voz e documento, por URL ou base64.
---

Todos os envios usam [`POST /v1/instances/{id}/messages`](/docs/api/mensagens/enviar-mensagem/) com o `type`
da mídia e um objeto `media`.

## Por URL

O servidor baixa o arquivo (timeout 30s) e envia. A URL precisa ser pública: endereços privados e locais são
bloqueados por segurança (veja [mídia](/docs/conceitos/midia/)).

```bash
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"image","caption":"Seu comprovante","media":{"url":"https://exemplo.com/comprovante.jpg"}}'
```

## Por base64

Útil quando o arquivo está no seu servidor e não tem URL pública.

```bash
B64=$(base64 < contrato.pdf | tr -d '\n')
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d "{\"to\":\"5511999999999\",\"type\":\"document\",\"caption\":\"Contrato\",\"media\":{\"base64\":\"$B64\",\"file_name\":\"contrato.pdf\",\"mime\":\"application/pdf\"}}"
```

O corpo pode ter até 140 MB. Base64 aumenta o tamanho em ~33%, então um documento de 100 MB ainda cabe.

## Mensagem de voz

Áudio com `ptt: true` aparece como mensagem de voz (com a onda e o microfone). Use OGG com codec Opus; outros formatos
podem não tocar como voz. O LiteZap não converte formatos.

```json
{ "to": "5511999999999", "type": "audio", "media": { "url": "https://exemplo.com/recado.ogg", "mime": "audio/ogg; codecs=opus", "ptt": true } }
```

## Limites e validação

| Tipo | Limite | Observação |
|---|---|---|
| `image` | 16 MB | JPEG, PNG, WebP. SVG nunca vale como imagem. |
| `video` | 64 MB | MP4 (H.264) é o mais compatível. |
| `audio` | 64 MB | OGG/Opus para voz; MP3/AAC/M4A para áudio comum. |
| `document` | 100 MB | Qualquer tipo; informe `file_name`. |

O conteúdo é conferido: um arquivo que não é imagem, vídeo ou áudio de verdade é recusado (`400 validation_failed`),
e um `Content-Type` remoto que contradiz o tipo da mensagem é ignorado.

## Erros comuns

| Erro | Causa |
|---|---|
| `400 validation_failed` | `media.url` não é http(s), aponta para endereço privado, ou o conteúdo não bate com o tipo. |
| `413 media_too_large` | Acima do limite do tipo. |
| `422 media_fetch_failed` | O servidor não conseguiu baixar a URL (fora do ar, 404, lenta). Retentável. |
| `503 rate_limited` | Muitos envios grandes ao mesmo tempo e o cliente desistiu de esperar vaga. |

## Responder com a mídia recebida

Mídia recebida tem `media.id` no evento e no histórico. Para repassar a outro serviço (ex.: transcrição), gere uma
URL assinada de 1 hora com `GET /media/{media_id}?signed=1`. Veja [mídia](/docs/conceitos/midia/).
