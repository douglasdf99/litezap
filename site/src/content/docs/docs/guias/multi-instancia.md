---
title: Multi-instância para SaaS
description: Oferecer WhatsApp aos seus clientes com uma instância por cliente. O que já existe e o que vem no M11.
---

Se você tem um produto (CRM, ERP, plataforma de atendimento) e quer que cada cliente conecte o próprio número, o
LiteZap já tem o essencial.

## O que já funciona

| Necessidade | Como |
|---|---|
| Criar uma instância por cliente | `POST /v1/instances` com o token admin, guardando o `token` da resposta no registro do cliente. |
| Isolar clientes | Cada instância tem token próprio; um token só acessa a própria instância. |
| Mostrar o QR no seu produto | `GET /qr` (JSON ou PNG) com o token da instância, ou o evento `instance.qr` por webhook/SSE. |
| Saber quando conectou | Evento `instance.status` (ou polling em `GET /v1/instances/{id}`). |
| Receber eventos de todos | Um webhook global (admin, sem `instance_id`); o `instance_id` do envelope diz de quem é. |
| IP por cliente | `proxy_url` por instância (admin). |
| Encerrar um cliente | `DELETE /v1/instances/{id}`. |
| Rotacionar credenciais | `POST /v1/instances/{id}/token/rotate`. |

O token admin fica só no seu backend. O front do cliente conversa com o seu backend, que usa o token da instância.

## Em breve (M11)

<span class="lz-badge lz-badge--soon">Em breve · M11</span>

- Página de pareamento hospedada: link assinado, de uso único e curto, para o cliente escanear o QR sem você expor o
  token da instância.
- Web component para embutir o QR no seu produto.
- Renomear instância, apagar em lote, paginação e filtro por estado em `GET /v1/instances`.
- Allowlist de IP por token e evento `instance.proxy_failed`.

Veja o [roadmap](/docs/roadmap/).

## Dimensionamento

Cada número conectado é um processo worker. Veja [dimensionamento](/docs/self-hosted/dimensionamento/) para estimar
quantas instâncias cabem por máquina.
