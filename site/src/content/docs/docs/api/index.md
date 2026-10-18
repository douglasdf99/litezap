---
title: Referência da API
description: Convenções gerais da API REST do LiteZap. Autenticação, formato, paginação, idempotência, modo síncrono e limites.
sidebar:
  label: Visão geral
---

A API do LiteZap é REST com JSON. Esta página reúne as convenções que valem para todas as rotas; as páginas por
recurso (no menu ao lado) são geradas a partir da especificação [OpenAPI 3.1](/openapi.yaml), que você pode importar
no Postman, Insomnia, Bruno ou num gerador de cliente.

## URL base

| Ambiente | URL |
|---|---|
| Local | `http://127.0.0.1:8787` |
| Self-hosted | o seu `public_url`, ex.: `https://api.seu-dominio.com` |

Rotas de negócio ficam sob `/v1`. Fora dele há só `/healthz`, `/readyz` e `/media/...` (URLs assinadas).

Nos exemplos, `$LITEZAP_URL` é a URL base, `$ADMIN` o token admin, `$TOK` o token da instância e `$ID` o id da
instância.

## Autenticação

```http
Authorization: Bearer lzi_...
```

O token admin acessa tudo. O token de instância só acessa `/v1/instances/{id}/...` da própria instância. Detalhes em
[tokens](/docs/conceitos/autenticacao/).

## Formato

- Corpo em JSON (`Content-Type: application/json` é recomendado; o servidor lê o corpo como JSON de qualquer forma).
- Corpo até 1 MB, exceto `POST /messages` (até 140 MB, para mídia em base64).
- Datas em RFC 3339 (UTC), ex.: `2026-10-17T13:45:00Z`.
- Ids com prefixo: `inst_` (instância), `msg_` (mensagem), `med_` (mídia), `wh_` (webhook), `dlv_` (entrega), `evt_` (evento).
- JIDs: `5511999999999@s.whatsapp.net` (pessoa), `...@g.us` (grupo), `...@lid` (id oculto). Nas rotas que recebem
  `{jid}`, um número com DDI também é aceito.

## Erros

Todo erro usa o mesmo envelope:

```json
{ "error": { "code": "instance_not_found", "message": "instance not found", "retryable": false } }
```

Trate pelo `code` (estável), não pela `message`. `retryable: true` indica que tentar de novo, com espera, pode dar
certo. A [tabela de erros](/docs/api/erros/) lista todos os códigos.

## Paginação

`GET /chats`, `GET /chats/{jid}/messages` e `GET /webhooks/{id}/deliveries` usam **cursor**:

```bash
curl -s "$LITEZAP_URL/v1/instances/$ID/chats?limit=50" -H "Authorization: Bearer $TOK"
# { "chats": [...], "next_cursor": "eyJ0Ijo..." }
curl -s "$LITEZAP_URL/v1/instances/$ID/chats?limit=50&cursor=eyJ0Ijo..." -H "Authorization: Bearer $TOK"
```

- `limit`: padrão 50, máximo 200 (100 em `GET /webhooks/{id}/deliveries`).
- `next_cursor` ausente: última página. O cursor é opaco; cursor inválido responde `400 validation_failed`.
- Ordem: chats pela última mensagem, mensagens da mais recente para a mais antiga.

## Idempotência

Envie `client_message_id` (até 128 caracteres) em `POST /messages`:

- mesmo id e mesmo conteúdo: devolve a mensagem já criada (sem duplicar);
- mesmo id com conteúdo diferente: `409 conflict`.

Assim você pode repetir com segurança um envio que deu timeout de rede. A verificação acontece antes de baixar
qualquer mídia.

## Modo síncrono (`?wait`)

Por padrão, `POST /messages` responde `202` assim que a mensagem entra na fila. Com `?wait=true` a requisição espera o
resultado:

| Parâmetro | Valor |
|---|---|
| `wait` | `true` |
| `timeout` | duração (`30s`, `90s`, `2m`); padrão `30s`, teto efetivo `2m` |

| Resultado dentro do prazo | Resposta |
|---|---|
| Enviada | `200` com `status: sent` e `wa_message_id` |
| Falhou | o erro correspondente (ex.: `400 invalid_number`, `410 message_expired`) |
| Prazo acabou | `202` com `status` `queued` ou `in_flight`; a mensagem **continua na fila** |

Lembre que a fila espera a carência pós-conexão (15–30s), o ritmo, a janela de envio e a rampa: em muitos casos o
`202` é o comportamento esperado. Consulte depois com `GET /messages/{msg_id}`.

## Limites

| O quê | Limite |
|---|---|
| `check-numbers` | 1 a 10 números por chamada; 10 consultas novas por minuto e 200 por dia (UTC) por instância → `429 rate_limited` |
| Envios com corpo grande (> 1 MB) simultâneos | 3; os demais esperam vaga; se o cliente desistir antes → `503 rate_limited` |
| Mídia | imagem 16 MB, vídeo/áudio 64 MB, documento 100 MB → `413 media_too_large` |
| Texto | 64 KB |
| `expires_after` | de `1m` a `168h` (padrão `24h`) |
| Ritmo da fila | `min_interval_ms` ≥ 1000 e `max_per_minute` ≤ 60, salvo `unsafe_pacing` (admin) |

A fila de envio nunca responde `429`: ela segura as mensagens e envia no ritmo configurado.

## Recursos

- [Instâncias](/docs/api/instancias/) · [Pareamento](/docs/api/pareamento/) · [Configurações](/docs/api/configuracoes/)
- [Mensagens](/docs/api/mensagens/) · [Chats](/docs/api/chats/) · [Contatos](/docs/api/contatos/)
- [Números](/docs/api/numeros/) · [Mídia](/docs/api/midia/) · [Webhooks](/docs/api/webhooks/) · [Eventos em tempo real](/docs/api/sse/) · [Operação](/docs/api/operacao/)

Cada operação tem a própria página, com parâmetros, exemplos em cURL, Node.js, Python, Go e PHP, resposta, erros, eventos gerados e o equivalente na Z-API. Os payloads dos eventos estão no [catálogo de eventos](/docs/eventos/). Recursos planejados (grupos, perfil, status, canais...) estão em **Em breve** no menu, sem endpoints até existirem.
