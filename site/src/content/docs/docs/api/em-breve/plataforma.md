---
title: Plataforma e parceiros
description: "Recursos para quem oferece WhatsApp a clientes (SaaS, revendas). Em breve (M11)."
---

<span class="lz-badge lz-badge--soon">Em breve · M11</span>

Criar, listar e apagar instâncias pela API já funciona (token admin). O M11 completa o que falta para parceiros.

:::note[Ainda não disponível]
Nada nesta página existe na API hoje. As operações abaixo são o escopo planejado do marco M11 e os nomes podem mudar até a entrega. A referência completa (uma página por operação, com exemplos) sai junto com o código.
:::

## O que vai fazer

| Operação planejada | Descrição |
|---|---|
| Página de pareamento hospedada | Link assinado, de uso único, que expira em 10 minutos ou no primeiro pareamento; nunca expõe o token. |
| Componente embutível | Web component para mostrar o QR dentro do seu produto. |
| Renomear instância | — |
| Apagar em lote | — |
| Paginação e filtro em `GET /v1/instances` | — |
| Allowlist de IP por token | Respeitando `trusted_proxies` atrás de proxy reverso. |
| Evento `instance.proxy_failed` | — |

Cobrança e assinaturas ficam no control plane da nuvem, não no motor.

Veja o [roadmap](/docs/roadmap/) e a [migração da Z-API](/docs/migrar-da-z-api/).
