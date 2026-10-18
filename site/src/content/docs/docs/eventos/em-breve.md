---
title: Eventos em breve
description: Tipos de evento planejados para os próximos marcos.
---

<span class="lz-badge lz-badge--soon">Em breve</span>

Estes tipos ainda **não** são emitidos e não são aceitos em filtros de webhook ou SSE. Cada um ganha sua página no
catálogo, com payload e exemplos, quando o marco for entregue. Os nomes são a proposta atual.

| Tipo | Quando será emitido | Marco |
|---|---|---|
| `message.revoked` | Uma mensagem foi apagada para todos | M7 |
| `message.edited` | Uma mensagem foi editada (texto antigo guardado no histórico) | M7 |
| `message.button_reply` | Resposta a botão ou lista enviados por outra ferramenta | M7 |
| `message.poll_vote` | Voto em enquete, com as opções decifradas | M6 |
| `message.status` com `played` e `revoked` | Áudio ouvido; mensagem apagada | M7 |
| `chat.presence` | Digitando, gravando, online | M7 |
| `call.received`, `call.ended` | Chamada recebida (e se foi rejeitada automaticamente) e encerrada | M7 |
| `group.updated` | Entradas, saídas, promoções, nome e descrição de grupos | M8 |
| `chat.updated` | Chat arquivado, fixado ou silenciado (inclusive pelo celular) | M9 |
| `status.received` | Status publicado por um contato (desligado por padrão) | M10 |
| `label.updated` | Etiqueta criada, alterada ou atribuída (Business) | M10 |
| `instance.proxy_failed` | Proxy da instância deixou de responder | M11 |
| `agent.failed`, `agent.handoff` | Falha do hook de agente; agente transferiu para humano | M5 |

Hoje, uma queda de proxy já aparece em [`instance.status`](/docs/eventos/instance-status/) com
`reason: proxy_unreachable`.
