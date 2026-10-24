---
title: Solução de problemas
description: QR que não aparece, desconexões, mensagens presas na fila, webhooks que não chegam e número banido.
---

## O QR não aparece

- **`409 instance_not_pairing`**: a instância não está em `qr_pending`. Veja o estado com `GET /v1/instances/{id}`.
  Se `connected`, já está pareada; se `logged_out`, faça `POST /start` para voltar a `qr_pending`; se `stopped` ou
  `failed`, `POST /start`.
- **O QR expira rápido**: ele muda a cada poucos segundos. Busque de novo, ou assine `instance.qr` para receber cada
  código novo.
- **Fica em `reconnecting` com `proxy_unreachable`**: o proxy da instância não responde. Corrija ou remova o
  `proxy_url` (admin).

## A instância desconecta

| Estado | O que fazer |
|---|---|
| `reconnecting` | Rede ou proxy instável; o worker tenta sozinho. Se persistir, confira a conexão e o proxy. |
| `starting` repetidas vezes | O worker está caindo. Veja o log do `serve`. |
| `failed` | Circuit breaker (5 crashes em 10 min) ou banimento temporário (`state_reason: temp_banned:...`). Corrija a causa e faça `POST /start`. |
| `logged_out` | O aparelho foi removido no celular ou houve logout. `POST /start` e pareie de novo. |
| `banned` | O WhatsApp baniu o número. Só resta `DELETE`. |

Telefone desligado por muitos dias também derruba sessões de aparelhos conectados.

## A mensagem não sai (fica `queued`)

Em ordem de probabilidade:

1. **Janela de envio**: o contato não falou com você nas últimas 24h e está fora de 08:00–20:00 (no fuso da
   instância). Confira `GET /settings` e o `timezone`; servidores em UTC são a causa mais comum.
2. **Carência**: a instância conectou há menos de 30 segundos.
3. **Rampa**: o limite diário de destinatários novos foi atingido; a mensagem sai na janela do dia seguinte.
4. **Ritmo**: muitas mensagens na frente. Com 3s + jitter e 20/min, 100 mensagens levam uns 5 minutos.
5. **Instância fora de `connected`**: a fila pausa até reconectar.
6. **Mensagem anterior ao mesmo destinatário em espera**: a ordem é estrita por destinatário.

`GET /messages/{id}` mostra `status`, `attempts` e `error`. Mensagens que não saem até `expires_after` (padrão 24h)
falham com `message_expired`.

## `invalid_number`

O número não tem WhatsApp, ou foi digitado sem DDI. Use 10 a 15 dígitos com DDI (`55` para o Brasil). Confira com
`POST /check-numbers`.

## O chat não aparece em `/chats/{jid}`

Use o `jid` que vem de `GET /chats` ou `check-numbers`. No Brasil, o WhatsApp pode guardar o número sem o 9º dígito.

## Webhooks não chegam

1. `GET /v1/webhooks/{id}/deliveries?status=pending` mostra `attempts` e `last_error`.
2. `blocked: webhook URL points to a private or local address`: a URL resolve para IP privado. Em teste local, ligue
   `webhook_allow_private_urls`.
3. `http 3xx`: redirecionamentos não são seguidos; use a URL final.
4. `timeout`: o receptor demorou mais de 10s. Responda `2xx` antes de processar.
5. O webhook está `enabled: false`, ou os filtros de `events` não incluem o tipo esperado.
6. Assinatura inválida no receptor: você está usando o corpo já parseado. Use o corpo bruto.

## SSE desconecta

- `event: error` com `slow_consumer`: o cliente ficou mais de 256 eventos atrasado. Processe mais rápido e reconecte.
- Atrás de proxy reverso: desligue buffering e aumente o timeout de leitura.

## App state fora de sincronia (`mismatching LTHash`)

O estado das conversas (arquivar, fixar, silenciar, lidas, favoritas, limpar, apagar), a agenda e as etiquetas
ficam no *app state*: cinco coleções (`critical_block`, `critical_unblock_low`, `regular_low`, `regular_high`,
`regular`) replicadas em cada aparelho da conta. Cada aparelho confere cada mudança contra um hash (LT-hash) da
sua cópia. Se a cópia local de uma coleção diverge, toda mudança seguinte dela falha. Sintomas no log do
`serve`:

- `Failed to sync app state after notification: failed to decode app state critical_unblock_low patches: failed to verify patch v49: mismatching LTHash`
- numa ação pela API: `server returned error updating app state (regular_high): <error code="409" text="conflict"/>`,
  ou a resposta `409 app_state_out_of_sync`.

As mensagens continuam funcionando; o que para é a sincronização do estado das conversas nessa coleção. O
celular não é afetado.

**Recuperação automática.** Quando o WhatsApp acusa essa divergência, o worker agenda uma ressincronização
completa da coleção (15s, depois 2min, depois 10min; no máximo 3 por coleção por hora). Antes de uma ação pela
API numa coleção cuja última sincronização falhou, ele ressincroniza primeiro. As linhas do log são
`app state out of sync; full resync scheduled` e `app state full resync done` (com `version_before` e
`version_after`). Esgotadas as tentativas da hora, o log mostra `automatic resync limit reached`.

**Recuperação manual** (token de admin):

```bash
curl -X POST "$LITEZAP/v1/instances/$ID/app-state/resync" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"collections": ["critical_unblock_low", "regular_high"]}'
```

Sem corpo, ressincroniza as cinco coleções, uma de cada vez (pode levar alguns segundos por coleção). A resposta
traz, por coleção, `ok`, `version_before`, `version_after` e `error`:

```json
{ "ok": true, "collections": [
  { "name": "critical_unblock_low", "ok": true, "version_before": 48, "version_after": 52 },
  { "name": "regular_high", "ok": true, "version_before": 34, "version_after": 37 } ] }
```

A ressincronização apaga só a cópia local (versão, hash e MACs, voltando à versão 0) e baixa a coleção de novo do
WhatsApp. O estado que volta é gravado sem disparar um `chat.updated` por conversa. `version_before` é a versão
local antes do reparo: num aparelho recém-pareado ela já pode ser alta (a primeira sincronização parou no patch
que não conferiu, por exemplo 48 com erro no v49).

**Histórico do servidor que não confere (`lenient`).** Às vezes o problema não está na cópia local: o servidor
guarda um patch antigo cujo MAC de snapshot não bate com o LT-hash das próprias mutações, porque foi enviado por um
aparelho que estava com a cópia divergente (provavelmente uma versão anterior do LiteZap, na corrida do `409`). O
sinal é que **até um aparelho recém-pareado** falha logo na primeira sincronização, sempre na mesma versão
(`failed to verify patch v49: mismatching LTHash`, ou `failed to verify snapshot: failed to verify patch v41:
mismatching LTHash`), enquanto o WhatsApp Web oficial e o celular sincronizam normalmente. O whatsmeow recusa esse
histórico para sempre.

Só na ressincronização (automática ou manual), quando a busca estrita falha nesse LT-hash, o LiteZap baixa a
coleção de novo e aceita o patch (ou snapshot) que só falha nessa conferência, desde que as mutações dele confiram
(MAC do conteúdo e do índice) e o MAC do patch também. O estado gravado é o hash calculado das mutações, o mesmo dos
outros aparelhos, então os patches seguintes voltam a conferir normalmente: logo depois o LiteZap faz uma
sincronização estrita para confirmar, e as sincronizações do dia a dia continuam estritas. O log mostra um `WARN`
`app state: accepted unverifiable historical patch` com `collection` e `version`, e a resposta do resync traz
`"lenient": true` e `"skipped_patch_versions"`:

```json
{ "name": "critical_unblock_low", "ok": true, "version_before": 48, "version_after": 52,
  "lenient": true, "skipped_patch_versions": [49] }
```

Isso só lê do servidor e atualiza a cópia local; nada é enviado. Um patch com MAC de mutação ou de patch errado
continua recusado, assim como mais de 16 patches sem conferência numa coleção.

**Conferir.** Faça uma ação simples, por exemplo fixar e desafixar uma conversa
(`PATCH /chats/{jid}` com `{"pinned": true}` e depois `{"pinned": false}`), e confira no log que não aparecem
mais `mismatching LTHash` nem `409`. Se a própria ressincronização falhar com `mismatching LTHash` (o retrato
enviado pelo servidor não confere), mantenha o celular online por alguns minutos e tente de novo; se persistir,
desconecte e pareie o aparelho de novo.

**Limpar conversa e remover contato** mudam o app state com mutações montadas pelo LiteZap e ficam desligados
(`403 feature_disabled`) até a revalidação num aparelho real. Para ligar por sua conta e risco:
`experimental_app_state_writes: true` (ou `LITEZAP_EXPERIMENTAL_APP_STATE_WRITES=true`).

## Número banido ou restrito

Pare os envios para contatos frios, revise o público e o texto, e leia as [boas práticas](/docs/boas-praticas/). Não
há como desbanir um número pela API. Se o WhatsApp oferecer revisão no aplicativo, faça pelo celular.

## Onde olhar

- Log do `serve` (stderr): todas as linhas têm `instance_id`.
- `GET /readyz`: banco acessível.
- `kill -9 $(cat data/run/$ID.pid)`: força um restart do worker para testar a recuperação.
