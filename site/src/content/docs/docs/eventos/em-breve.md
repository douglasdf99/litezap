---
title: Eventos em breve
description: Tipos de evento planejados para os próximos marcos.
---

<span class="lz-badge lz-badge--soon">Em breve</span>

Estes tipos ainda **não** são emitidos e não são aceitos em filtros de webhook ou SSE. Cada um ganha sua página no
catálogo, com payload e exemplos, quando o marco for entregue. Os nomes são a proposta atual.

| Tipo | Quando será emitido | Marco |
|---|---|---|
| `group.updated` | Entradas, saídas, promoções, nome e descrição de grupos | M8 |
| `chat.updated` | Chat arquivado, fixado ou silenciado (inclusive pelo celular) | M9 |
| `status.received` | Status publicado por um contato (desligado por padrão) | M10 |
| `label.updated` | Etiqueta criada, alterada ou atribuída (Business) | M10 |
| `instance.proxy_failed` | Proxy da instância deixou de responder | M11 |
| `agent.failed`, `agent.handoff` | Falha do hook de agente; agente transferiu para humano | M5 |

Hoje, uma queda de proxy já aparece em [`instance.status`](/docs/eventos/instance-status/) com
`reason: proxy_unreachable`.
