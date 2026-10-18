---
title: Fila
description: "Ver, cancelar e esvaziar a fila de envio de uma instância. Em breve (M6)."
---

<span class="lz-badge lz-badge--soon">Em breve · M6</span>

Hoje a fila é persistente e configurável (ritmo, rampa, janela), e cada item pode ser consultado em `GET /messages/{id}`. No M6 ela fica visível e controlável.

:::note[Ainda não disponível]
Nada nesta página existe na API hoje. As operações abaixo são o escopo planejado do marco M6 e os nomes podem mudar até a entrega. A referência completa (uma página por operação, com exemplos) sai junto com o código.
:::

## O que vai fazer

| Operação planejada | Descrição |
|---|---|
| Listar a fila | Itens `queued` e `in_flight` da instância, com filtro por status e paginação por cursor. |
| Cancelar um item | Remove um item ainda `queued` sem enviar. Um item já `in_flight` responde `conflict`. |
| Esvaziar a fila | Remove todos os itens pendentes; útil antes de reconectar uma instância parada há dias. |
| Atraso por mensagem | `delay_ms` e `typing_ms` por envio, sem furar os pisos de ritmo. |

Equivale a Fila, Apagar fila e Apagar mensagem da fila na Z-API.

Veja o [roadmap](/docs/roadmap/) e a [migração da Z-API](/docs/migrar-da-z-api/).
