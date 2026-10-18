---
title: MCP e IA
description: Servidor MCP para usar o WhatsApp a partir do Claude, ChatGPT, Cursor e outros clientes. Em breve (marco 4).
---

<span class="lz-badge lz-badge--soon">Em breve · M4</span>

O [Model Context Protocol](https://modelcontextprotocol.io) (MCP) permite que assistentes de IA usem ferramentas
externas. Com o MCP do LiteZap, um assistente poderá ler suas conversas e enviar mensagens pelo seu número, com as
mesmas regras de ritmo, janela e rampa da API.

:::note[Ainda não disponível]
Nada nesta página existe hoje. O conteúdo descreve o escopo planejado do marco 4.
:::

## Duas formas de conectar

| Forma | Como | Para |
|---|---|---|
| MCP HTTP | Endpoint `/mcp` no próprio `litezap serve` (streamable HTTP), autenticado com os mesmos tokens da API | Clientes que falam MCP por HTTP e servidores remotos |
| `litezap mcp` (stdio) | O cliente de IA executa o binário, que conversa com um `serve` já rodando via REST (`LITEZAP_URL`, `LITEZAP_TOKEN`) | Claude Desktop, Claude Code, Cursor e outros clientes locais |

Com um token de instância, as ferramentas operam só naquela instância e o `instance_id` é opcional. Com o token admin,
todas as instâncias ficam acessíveis.

## Ferramentas planejadas

| Ferramenta | O que faz |
|---|---|
| `list_instances` | Lista instâncias (com token de instância, só a própria) |
| `get_instance_status` | Estado e número conectado |
| `send_message` | Envia texto, esperando o resultado por padrão |
| `send_media` | Envia mídia por URL |
| `list_chats` | Chats recentes |
| `get_messages` | Histórico de um chat, paginado |
| `search_messages` | Busca de texto nas mensagens (depende da busca full-text do M2b) |
| `get_contact` | Dados de um contato |
| `check_numbers` | Verifica se números têm WhatsApp |
| `mark_read` | Marca um chat como lido |

As ferramentas são definidas uma única vez sobre os mesmos casos de uso da API, então os limites e as proteções são
idênticos.

## Clientes

Quando o marco for entregue, esta seção terá a configuração passo a passo para Claude Desktop, Claude Code, Cursor e
ChatGPT.

## Cuidados

Dar a um assistente a capacidade de enviar mensagens pelo seu número é dar a ele a sua voz no WhatsApp. Use um token
de instância (nunca o admin) e revise o que o assistente vai enviar, principalmente para contatos que não falaram com
você.

Veja também o [hook de agente](/docs/mcp/hook-de-agente/) (M5), para respostas automáticas.
