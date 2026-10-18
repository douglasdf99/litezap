---
title: Grupos
description: "Gestão completa de grupos pela API. Em breve (M8)."
---

<span class="lz-badge lz-badge--soon">Em breve · M8</span>

Hoje é possível receber mensagens de grupos e enviar para grupos em que o número já está (JID `...@g.us`). A gestão chega no M8.

:::note[Ainda não disponível]
Nada nesta página existe na API hoje. As operações abaixo são o escopo planejado do marco M8 e os nomes podem mudar até a entrega. A referência completa (uma página por operação, com exemplos) sai junto com o código.
:::

## O que vai fazer

| Operação planejada | Descrição |
|---|---|
| Listar grupos | Grupos de que o número participa. |
| Criar grupo | Com limite diário por instância (criar grupos com desconhecidos é gatilho de banimento). |
| Ler e alterar dados | Nome, descrição, foto e metadados completos ou leves. |
| Configurações | Só admins enviam, só admins editam, aprovação de entrada, quem pode adicionar. |
| Participantes | Adicionar, remover, promover e rebaixar, com resultado por participante. |
| Pedidos de entrada | Listar, aprovar e rejeitar. |
| Convite | Obter e redefinir o link; entrar por convite; ver dados de um grupo pelo link. |
| Sair do grupo | — |
| Evento `group.updated` | Entradas, saídas, promoções, nome e descrição. |

Veja o [roadmap](/docs/roadmap/) e a [migração da Z-API](/docs/migrar-da-z-api/).
