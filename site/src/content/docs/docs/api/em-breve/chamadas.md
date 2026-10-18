---
title: Chamadas
description: "Rejeição automática de chamadas e eventos de chamada. Em breve (M7)."
---

<span class="lz-badge lz-badge--soon">Em breve · M7</span>

O LiteZap não faz nem atende chamadas de voz ou vídeo: a biblioteca de protocolo não implementa mídia de chamada. O M7 traz o que é possível: saber que alguém ligou e rejeitar automaticamente.

:::note[Ainda não disponível]
Nada nesta página existe na API hoje. As operações abaixo são o escopo planejado do marco M7 e os nomes podem mudar até a entrega. A referência completa (uma página por operação, com exemplos) sai junto com o código.
:::

## O que vai fazer

| Operação planejada | Descrição |
|---|---|
| Rejeitar chamadas automaticamente | Setting por instância (`calls.auto_reject`); a chamada é rejeitada em segundos. |
| Mensagem ao rejeitar | `calls.reject_message` envia uma mensagem pela fila (respeitando ritmo e janela). |
| Eventos de chamada | `call.received` e `call.ended`, com indicação de rejeição automática. |

Fazer chamadas, chamada de atenção e SIP estão fora do escopo.

Veja o [roadmap](/docs/roadmap/) e a [migração da Z-API](/docs/migrar-da-z-api/).
