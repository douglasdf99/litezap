---
title: Mídia e URLs assinadas
description: Enviar imagem, vídeo, áudio e documento; baixar mídia recebida e compartilhar por URL temporária.
---

## Enviar mídia

Informe a mídia por **URL** (o servidor baixa) ou em **base64**:

```bash
# por URL
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"image","caption":"Comprovante","media":{"url":"https://exemplo.com/a.jpg"}}'

# áudio como mensagem de voz
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"audio","media":{"base64":"<...>","mime":"audio/ogg; codecs=opus","ptt":true}}'
```

| Tipo | Limite |
|---|---|
| `image` | 16 MB |
| `video`, `audio` | 64 MB |
| `document` | 100 MB |
| Corpo da requisição | 140 MB |

- `caption` vale para `image`, `video` e `document`; `file_name` (até 255 bytes) para documentos.
- O conteúdo é conferido: algo que não é imagem, vídeo ou áudio de verdade é recusado (SVG nunca vale como imagem),
  e um `Content-Type` remoto que contradiz o tipo da mensagem é ignorado.
- Acima do limite: `413 media_too_large`. URL que não baixa: `422 media_fetch_failed` (retentável).

### Proteção contra SSRF

O download de `media.url` tem timeout de 30s, ignora proxies do ambiente e só aceita endereços públicos: IPs privados,
loopback, link-local e faixas reservadas são bloqueados, conferidos depois da resolução DNS em toda conexão, inclusive
redirecionamentos. URL que não é `http(s)` ou aponta para endereço interno responde `400 validation_failed`. Para uso
local (ex.: arquivos num servidor da sua rede), ligue `media_allow_private_urls: true`. Mantenha desligado em servidores.

## Mídia recebida

Mensagens recebidas com mídia trazem `media.id` e `media.url` no histórico. O arquivo é baixado **sob demanda**:

```bash
curl -s "localhost:8787/v1/instances/$ID/media/med_5b8e1f3c9d2a7046" -H "Authorization: Bearer $TOK" -o arquivo
```

Na primeira vez o worker baixa do WhatsApp e guarda em `data/media/<instância>/`; downloads simultâneos compartilham
uma única transferência. Mídia que já expirou no WhatsApp responde `410 media_unavailable`.

Por segurança, toda mídia servida leva `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox` e
`Content-Disposition: attachment`, salvo JPEG, PNG, WebP, GIF, áudio e vídeo, que vão `inline`.

## URLs assinadas

Para repassar uma mídia a terceiros (um serviço de transcrição ou uma IA, por exemplo) sem entregar seu token:

```bash
curl -s "localhost:8787/v1/instances/$ID/media/med_5b8e1f3c9d2a7046?signed=1" -H "Authorization: Bearer $TOK"
# {"url":"https://api.seu-dominio.com/media/inst_.../med_...?exp=...&sig=...","expires_at":"..."}
```

- A URL vale **1 hora** e funciona sem token (rota `/media/...`, fora de `/v1`).
- É assinada com HMAC-SHA256 usando o segredo em `data/media_key`. Apagar esse arquivo invalida todas as URLs já
  emitidas.
- A base da URL é `public_url`. Se o servidor escuta fora de loopback e `public_url` está vazio, `?signed=1` responde
  `409 conflict`: uma URL com o endereço interno não serviria a terceiros.
