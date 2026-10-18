---
title: Chats e contatos (ações)
description: "Arquivar, fixar, silenciar, limpar e apagar chats; foto, bloqueio e metadados de contatos. Em breve (M9)."
---

<span class="lz-badge lz-badge--soon">Em breve · M9</span>

Hoje existem listar chats, histórico, marcar como lido, listar e consultar contatos. O M9 completa as ações.

:::note[Ainda não disponível]
Nada nesta página existe na API hoje. As operações abaixo são o escopo planejado do marco M9 e os nomes podem mudar até a entrega. A referência completa (uma página por operação, com exemplos) sai junto com o código.
:::

## O que vai fazer

| Operação planejada | Descrição |
|---|---|
| Obter um chat | `GET` de um chat com arquivado, fixado e silenciado. |
| Arquivar, fixar, silenciar | E desfazer. |
| Marcar como não lido | — |
| Limpar e apagar chat | — |
| Mensagens temporárias do chat | — |
| Foto de perfil do contato | Com cache e URL assinada, como a mídia. |
| Bloquear e desbloquear | E lista de bloqueados. |
| Metadados atualizados | Consulta à rede sob demanda, com cota. |
| Leitura automática | Setting `auto_read` por instância. |
| Evento `chat.updated` | Mudanças feitas no celular (arquivar, fixar, silenciar) chegam ao log e aos eventos. |

Veja o [roadmap](/docs/roadmap/) e a [migração da Z-API](/docs/migrar-da-z-api/).
