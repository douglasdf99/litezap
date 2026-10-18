---
title: Verificação de números
description: Como o LiteZap confere se um número tem WhatsApp antes de enviar, e o endpoint check-numbers.
---

Enviar para números que não existem no WhatsApp é um sinal ruim. Por isso o LiteZap **verifica o número antes do
primeiro envio** e oferece um endpoint para verificar em lote.

## Na fila de envio

- Antes do primeiro envio a um número sem chat, o despachante pergunta ao WhatsApp se ele tem conta.
- **Sem WhatsApp**: a mensagem falha com `invalid_number`, sem enviar nada.
- **Com outro formato**: no Brasil, o WhatsApp pode conhecer o número com ou sem o 9º dígito. O LiteZap usa o JID que
  o WhatsApp devolve e redireciona os itens daquele destinatário. Por isso o chat pode aparecer com um `jid`
  diferente do número digitado: use o `jid` de `GET /chats` ou de `check-numbers` nas rotas `/chats/{jid}/...`.
- **Falha na consulta**: adia só aquele item, com espera de 1, 2, 5 e 15 minutos; na 5ª falha seguida ele vira
  `failed` (`worker_error`). Worker indisponível não conta como falha de consulta.
- **Ritmo**: cada consulta que vai à rede conta como um envio para `min_interval_ms` e `max_per_minute`, tem cota de
  30 consultas novas por minuto e, com a rampa ligada, respeita o limite diário dela. Acima disso, o item espera.

## Endpoint `check-numbers`

```bash
curl -s -X POST localhost:8787/v1/instances/$ID/check-numbers -H "Authorization: Bearer $TOK" \
  -d '{"phones":["5511999999999","5511888888888"]}'
```

```json
{
  "results": [
    { "phone": "5511999999999", "exists": true, "jid": "5511999999999@s.whatsapp.net" },
    { "phone": "5511888888888", "exists": false }
  ]
}
```

| Regra | Valor |
|---|---|
| Números por chamada | 1 a 10 (DDI + DDD + número, 10 a 15 dígitos) |
| Consultas novas por minuto | 10 por instância (cota própria, separada da fila) |
| Consultas novas por dia | 200 por instância (dia em UTC) |
| Cache de quem tem WhatsApp | 30 dias |
| Cache de quem não tem | 24 horas |

Números já em cache não gastam cota. Acima dos limites, a resposta é `429 rate_limited` (`retryable: true`).

:::note
`check-numbers` serve para higienizar listas pequenas e conferir contatos pontuais, não para varrer bases inteiras.
Consultas em massa de números são, por si só, um padrão de risco.
:::
