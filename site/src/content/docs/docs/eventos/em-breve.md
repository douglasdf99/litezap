---
title: Eventos em breve
description: Tipos de evento planejados para os próximos marcos.
---

<span class="lz-badge lz-badge--soon">Em breve</span>

Estes tipos ainda **não** são emitidos e não são aceitos em filtros de webhook ou SSE. Cada um ganha sua página no
catálogo, com payload e exemplos, quando o marco for entregue. Os nomes são a proposta atual.

| Tipo | Quando será emitido | Marco |
|---|---|---|
| `chat.updated` | Chat arquivado, fixado ou silenciado (inclusive pelo celular) | M9 |
| `agent.failed`, `agent.handoff` | Falha do hook de agente; agente transferiu para humano | M5 |

Quedas de proxy têm evento próprio desde o M11: [`instance.proxy_failed`](/docs/eventos/instance-proxy-failed/).
