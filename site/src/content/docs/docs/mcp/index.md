---
title: MCP e IA
description: Use o WhatsApp a partir do Claude, ChatGPT, Cursor e outros clientes de IA pelo servidor MCP do LiteZap.
---

<span class="lz-badge lz-badge--ok">Disponível · M4</span>

O [Model Context Protocol](https://modelcontextprotocol.io) (MCP) permite que assistentes de IA usem ferramentas
externas. Com o MCP do LiteZap, um assistente lê suas conversas e envia mensagens pelo seu número, com as mesmas
regras de ritmo, rampa e janela da API. São [13 ferramentas](/docs/mcp/ferramentas/).

## Duas formas de conectar

| Forma | Como | Para |
|---|---|---|
| **HTTP** | `http://<listen_addr>/mcp` no próprio `litezap serve` (streamable HTTP, sem sessão), com o mesmo `Authorization: Bearer <token>` da API | Claude Code, clientes que aceitam URL + cabeçalho, serviços na nuvem |
| **stdio** | `litezap mcp`: uma ponte que o cliente inicia como processo local e que conversa via REST com um `litezap serve` já rodando | Claude Desktop, Claude Code, Cursor e outros clientes locais |

A ponte stdio não sobe um supervisor próprio. Ela lê:

| Variável | Padrão | Descrição |
|---|---|---|
| `LITEZAP_URL` | `http://127.0.0.1:8787` | Endereço do `litezap serve` |
| `LITEZAP_TOKEN` | obrigatório | Token de instância (recomendado) ou admin |

`instance_id` é opcional nas ferramentas quando o token é de instância (é sempre a dela) e, com token admin, quando
só existe uma instância.

## Claude Desktop

Em `claude_desktop_config.json` (Settings > Developer > Edit Config), com o caminho **absoluto** do binário:

```json
{
  "mcpServers": {
    "litezap": {
      "command": "/caminho/absoluto/litezap",
      "args": ["mcp"],
      "env": {
        "LITEZAP_URL": "http://127.0.0.1:8787",
        "LITEZAP_TOKEN": "lzi_..."
      }
    }
  }
}
```

Reinicie o Claude Desktop com o `litezap serve` (ou o [app local](/docs/app-local/)) rodando.

## Claude Code

```bash
# stdio (ponte local)
claude mcp add litezap -e LITEZAP_URL=http://127.0.0.1:8787 -e LITEZAP_TOKEN=lzi_... -- /caminho/absoluto/litezap mcp

# ou HTTP direto no serve
claude mcp add --transport http litezap http://127.0.0.1:8787/mcp --header "Authorization: Bearer lzi_..."
```

## Clientes HTTP (OpenAI e outros)

Qualquer cliente MCP que aceite URL + cabeçalho funciona: URL `https://<seu-host>/mcp`, cabeçalho
`Authorization: Bearer <token>`. O endpoint é *stateless*: não usa `Mcp-Session-Id` e cada requisição é autenticada
de novo. Exemplo na Responses API da OpenAI:

```json
{
  "type": "mcp",
  "server_label": "litezap",
  "server_url": "https://zap.exemplo.com/mcp",
  "headers": { "Authorization": "Bearer lzi_..." },
  "require_approval": "always"
}
```

Serviços na nuvem (ChatGPT, OpenAI) precisam alcançar o servidor: publique-o atrás de um
[proxy reverso com TLS](/docs/self-hosted/proxy-reverso/) e defina `public_url`. Num `listen_addr` de loopback,
pedidos com `Host` que não é loopback são recusados (proteção contra DNS rebinding); o host de `public_url` é a
exceção, para o proxy na mesma máquina.

## Teste rápido

1. Com o `serve` rodando e uma instância criada, configure o cliente com o token da instância.
2. "Liste minhas instâncias do WhatsApp": o assistente chama `list_instances` e vê só a instância do token.
3. Antes de parear, "mostre o QR para parear": `get_qr` devolve a imagem; escaneie e peça o status até `connected`.
4. "Quais são minhas conversas recentes?" e "o que Fulano disse por último?" (`list_chats`, `get_messages`).
5. Peça para enviar uma mensagem ao contato de teste. Fora da janela de envio, a resposta diz que ficou na fila
   (`sent: false`) e que sai sozinha depois.
6. "Espere a resposta dele" e responda pelo celular do contato em até 55s: `wait_for_events` devolve o
   `message.received`.

## Segurança

- **Use token de instância** (`lzi_...`) para dar ao assistente um único número: ele não lista nem opera outras
  instâncias, não cria, não apaga e não troca token. O token admin dá acesso a todas.
- **O assistente não fura o anti-ban**: todo envio passa pela mesma fila da API. Não há ferramenta para mudar
  settings, proxy ou `unsafe_pacing`. Uma mensagem que fica na fila volta com `sent: false` e uma nota dizendo que
  será enviada sozinha e não deve ser reenviada.
- **Idempotência**: cada envio usa um `client_message_id` (o do modelo ou um gerado, `mcp_…`). Num erro ambíguo
  (timeout, conexão perdida), o erro traz esse id e pede para conferir `get_message_status` antes de reenviar.
- **`check_numbers`** gasta a cota diária de consultas da instância e responde `rate_limited` quando ela acaba.
- **Texto de terceiros**: mensagens, nomes e eventos vêm de outras pessoas. As instruções do servidor pedem ao modelo
  para tratá-los como dados, mas prompt injection não é um problema resolvido: prefira clientes que pedem aprovação
  antes de `send_message`/`send_media` (por exemplo `require_approval`).
- Erros chegam ao modelo como erro de ferramenta, com código, dica e se dá para tentar de novo (ex.:
  `instance_not_connected` sugere `get_instance_status`/`get_qr`). Erros internos aparecem só como `internal error`;
  o detalhe vai para o log do servidor.

## Limites

- `wait_for_events` só vê eventos publicados enquanto espera; para o que chegou antes, use `get_messages`.
- Busca de mensagens (`search_messages`) depende da busca full-text do M2b.
- Respostas automáticas sem um assistente no meio ficam com o [hook de agente](/docs/mcp/hook-de-agente/) (M5).
