---
title: Cardápio (catálogo do LiteZap)
description: Monte o catálogo do seu negócio dentro do LiteZap — categorias, preços, opções e fotos — sem ligar o número a uma conta de anúncios da Meta.
---

O **Cardápio** é o catálogo próprio do LiteZap: categorias, itens com preço, foto e opções (tamanho, borda,
adicionais), e a marcação de **esgotado**. Ele fica guardado no seu LiteZap, por instância, e é usado pelo
[Atendente IA](/docs/guias/atendimento-ia/), pelos [agentes](/docs/guias/agentes/) e pelo MCP para responder e
montar pedidos, e pode ser enviado a um cliente como mensagem.

:::note[O LiteZap nunca escreve na Meta]
Nada do Cardápio é enviado para a Meta ou para o catálogo do WhatsApp. Não precisa de conta de anúncios, de
verificação por e-mail nem de conta Business. O WhatsApp só recebe as mensagens que você (ou o atendente)
mandar, pela fila anti-ban, como qualquer outra mensagem.
:::

## Cardápio × catálogo do WhatsApp

| | Cardápio do LiteZap | [Catálogo do WhatsApp](/docs/guias/catalogo-e-pedidos/) |
|---|---|---|
| Onde fica | No seu LiteZap (SQLite e `data/menu/`) | Nos servidores da Meta |
| Quem cria e edita | Você: painel, API, planilha, IA | Só o app WhatsApp Business no celular |
| Conta | Qualquer número | Só conta Business |
| Opções e acréscimos (tamanho, borda) | Sim | Não |
| Como o cliente vê | Foto com legenda ou texto no chat | Cartão de produto e carrinho do WhatsApp |
| Pedidos | O atendente monta o `agent.order_draft` com os preços do cardápio | `order.received` pelo carrinho |

Dá para usar os dois: copie o catálogo do WhatsApp para o Cardápio com um clique (só leitura no WhatsApp) e
complete com opções e esgotados.

## Montar o cardápio

No painel, abra a instância e a aba **Cardápio**. Com o cardápio vazio, ela mostra os três caminhos:

1. **Importar planilha (CSV).** No Excel ou Google Planilhas, *Arquivo > Fazer download > CSV*. A primeira linha
   tem os nomes das colunas (sem diferenciar acentos ou maiúsculas): `categoria`, `nome` (obrigatória),
   `descricao`, `preco`, `disponivel`, `imagem_url` e, opcionais, `opcoes`, `sku` e `tags`. Clique em
   **Pré-visualizar**: cada linha aparece com os erros (preço que não dá para ler, nome vazio, link inválido) antes
   de salvar. Só as linhas válidas são salvas.
2. **Copiar do catálogo do WhatsApp.** Lê os produtos do catálogo do próprio número (como `GET /catalog`) e copia
   nome, descrição, preço (o promocional, quando houver), disponibilidade e a primeira foto. Coleções viram
   categorias. Produtos ocultos ou em análise ficam de fora; os já copiados são pulados, mesmo com duas cópias
   ao mesmo tempo (rodar de novo busca as fotos que faltaram).
3. **Montar com IA.** O caminho principal é a IA que você **já assina** (Claude ou ChatGPT): veja
   [Montar com a sua assinatura](#montar-com-a-sua-assinatura-claudechatgpt). Se houver um provedor pronto no
   Atendente IA (qualquer um, inclusive o [Ollama gratuito](/docs/guias/ia-gratis/)), o painel também oferece
   montar ali mesmo: cole o texto livre — `pizza calabresa 45, marguerita 42, coca 2L 12` — e revise. **Nada é
   salvo** até você revisar (dá para corrigir nome e preço e desmarcar linhas) e confirmar.

Ou crie os itens à mão em **Novo item**: nome, preço, categoria, descrição, foto (arquivo ou link; o LiteZap
reduz para JPEG de até 1080 px), grupos de opções e a **prévia** de como o item chega no WhatsApp.

### Montar com a sua assinatura (Claude/ChatGPT)

Nem todo mundo tem um Ollama rodando ou uma chave de API. Com o MCP, quem pensa é o **seu** Claude (Pro/Max, no
Claude Desktop ou no Claude Code) ou o seu ChatGPT: ele lê o texto, a foto ou a planilha, e o LiteZap dá as
ferramentas, valida e salva. Nenhuma chamada de IA é cobrada pelo LiteZap.

1. Conecte a IA ao LiteZap: no app, **Conectar a IA** (no Claude Desktop, a extensão LiteZap; no Claude Code, o
   [plugin](/docs/mcp/claude-code-plugin/), que traz o comando `/litezap:montar-cardapio`). Para o ChatGPT, veja a
   nota abaixo.
2. No painel, **Cardápio > Montar com IA** mostra um pedido pronto com botão de copiar:

   > Monte o cardápio do LiteZap a partir deste texto: &lt;cole aqui&gt;. Use import_menu_rows com dry_run primeiro e
   > me mostre antes de salvar.

3. A IA transforma o material em linhas e chama `import_menu_rows` com `dry_run: true` (nada é salvo). Ela mostra
   uma tabela com categoria, item, preço e opções, com os **erros** (preço ilegível, nome vazio) e **avisos**
   (sem preço, item que já existe). Corrija o que precisar e diga "pode salvar": só então ela chama de novo com
   `dry_run: false`.
4. Clique em **Atualizar** no painel. Depois, peça "manda o cardápio para o 5511999999999" se quiser enviar
   (só para quem você indicar).

Exemplos do que pedir:

- **Texto colado**: "Monte o cardápio do LiteZap a partir deste texto: *Pizzas: calabresa 45, marguerita 42,
  portuguesa 48 (borda catupiry +8). Bebidas: coca 2L 12, suco de laranja 9 — esgotado.* Use import_menu_rows com
  dry_run primeiro e me mostre antes de salvar."
- **Foto de um cardápio impresso**: anexe a foto e escreva "Monte o cardápio do LiteZap a partir desta foto, com
  dry_run primeiro". A IA lê a imagem ela mesma; o que estiver ilegível fica sem preço e aparece como dúvida, em
  vez de um valor inventado.
- **Planilha**: anexe o CSV (ou cole as linhas) e diga quais colunas são o quê, se não for óbvio.
- **Ajustes depois**: "a calabresa agora custa 47,90", "marca o suco como esgotado", "põe tamanho P/M/G na
  marguerita, M +5 e G +10" — a IA usa `update_menu_item`, `set_menu_item_available` e `add_menu_option_group`.

Regras que as ferramentas passam para a IA: sempre a prévia antes; salvar só depois do seu "sim"; **nunca
inventar preço** nem link de foto; apagar itens ou categorias e substituir o cardápio (`replace`) só quando você
pedir. As ferramentas de edição do cardápio são só para a **sua** IA: os [agentes](/docs/guias/agentes/) do
LiteZap nunca as recebem, seja qual for a permissão. Lista completa em [Ferramentas MCP](/docs/mcp/ferramentas/#montar-o-cardápio).

:::note[ChatGPT]
O ChatGPT conecta servidores MCP **remotos**: em versões recentes, em *Configurações* (em contas com o modo de
desenvolvedor ou o recurso de servidores MCP personalizados liberado pelo plano ou pelo administrador do
workspace), você adiciona um servidor MCP informando um endereço **HTTPS público** que termina em `/mcp`. O ChatGPT
roda na nuvem da OpenAI, então ele não alcança o LiteZap que só escuta neste computador (`127.0.0.1`); seria preciso
publicar o LiteZap num endereço HTTPS acessível (com `public_url` e um proxy ou túnel) e conferir se o ChatGPT
aceita a forma de autenticação usada (o `/mcp` do LiteZap pede o token da instância). Os nomes dos menus e os
requisitos mudam com frequência: confira a [ajuda da OpenAI](https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt).
Se o seu LiteZap é só local, use o Claude Desktop ou o Claude Code (ou, quando existir, a versão em nuvem do
LiteZap).
:::

### Preços

Escreva como no Brasil: `45`, `45,9`, `45,90`, `R$ 1.234,50`. Também vale `45.90`. Na API o preço é
`price_cents` (centavos: R$ 45,90 = `4590`); na criação e alteração também dá para mandar `price` como texto.

### Opções

Um item tem até 10 grupos de opções, cada um com até 20 escolhas e um acréscimo (ou desconto) por escolha.
Ex.: **Tamanho** obrigatório (P, M +R$ 5,00, G +R$ 10,00) e **Borda** opcional (Catupiry +R$ 8,00).

Na planilha, a coluna `opcoes` usa grupos separados por `;`, cada um `Nome: escolha | escolha +preço`:

```text
Tamanho: P | M +5,00 | G +10,00; Borda (opcional): Catupiry +8 | Cheddar +8
```

Por padrão um grupo é obrigatório e de uma escolha. `(opcional)` deixa opcional, `(até 3)` permite até 3
escolhas e `(mín 2)` exige 2. Se a planilha usa `;` como separador, deixe a célula entre aspas (o Excel faz isso
sozinho ao exportar).

### Esgotado

A chave **Disponível** de cada cartão marca o item como esgotado na hora. Itens esgotados não são enviados, o
atendente avisa que estão indisponíveis e o rascunho de pedido os aponta.

### Limites

500 itens, 50 categorias, 10 grupos de opções por item e 20 escolhas por grupo, por instância. Fotos de até 8 MB
e 10 megapixels (JPEG, PNG ou GIF). A planilha deve estar em UTF-8 (no Excel: *Salvar como > CSV UTF-8*).
O "Montar com IA" aceita até 5 pedidos por minuto e 100 por dia por instância; essa cota fica em memória e
reiniciar o LiteZap a zera (os tetos diários do provedor, no Atendente IA, continuam valendo).

## Usar o cardápio

### Atendente IA

No agente do Atendente IA, **Lista de produtos > Cardápio do LiteZap** (ou `knowledge_source: "menu"`). É o
padrão sugerido para agentes novos quando a instância tem cardápio. O agente recebe itens, preços, categorias,
opções e esgotados, e o evento `agent.order_draft` **recalcula os preços pelo cardápio**, somando as opções
escolhidas (o preço que o modelo escreveu não vale):

```json
{ "items": [{ "name": "Calabresa", "quantity": 2, "unit_price": 55.9, "subtotal": 111.8, "in_catalog": true,
  "menu_item_id": "mitm_0a1b2c3d4e5f6a7b", "available": true,
  "options": [{ "group": "Tamanho", "choice": "G", "price_delta": 10 }] }],
  "total": 111.8, "all_items_in_catalog": true }
```

Escolhas que não existem vêm em `unknown_options`, grupos obrigatórios sem escolha em
`missing_required_options`; nesses casos (e com item esgotado ou fora do cardápio) `all_items_in_catalog` é
`false`.

Os textos do cardápio podem vir de uma planilha, do catálogo ou da IA, então o agente os recebe como **dados**,
num bloco separado das informações da empresa, com quebras de linha e marcações neutralizadas — nunca como
instruções.

### Agentes e MCP

| Ferramenta | Grupo do agente | O que faz |
|---|---|---|
| `get_menu`, `get_menu_item` | `ler` | Lê o cardápio |
| `send_menu` | `responder_no_chat` | Envia o cardápio (ou uma categoria) em texto, dividido em mensagens. Cada mensagem conta em `max_sends`; o motor limita `max_messages` aos envios que restam e recusa um cardápio maior (envie uma categoria) |
| `send_menu_item` | `midia` | Envia um item com foto e legenda |

As mesmas ferramentas existem no [MCP](/docs/mcp/ferramentas/), com os avisos de segurança de sempre: só enviar
quando pedirem, para quem indicarem. O MCP tem também as ferramentas para **montar e editar** o cardápio
(`import_menu_rows`, `create_menu_item` e outras), só para a IA do usuário: nenhum agente as recebe.

### API

```bash
B=localhost:8787/v1/instances/$ID
curl -s "$B/menu" -H "Authorization: Bearer $TOK"
curl -s -X POST "$B/menu/items" -H "Authorization: Bearer $TOK" \
  -d '{"name":"Calabresa","price":"45,90","options":[{"name":"Tamanho","required":true,"choices":[{"name":"P"},{"name":"G","price_delta_cents":1000}]}]}'
curl -s -X PUT "$B/menu/items/$ITEM/available" -H "Authorization: Bearer $TOK" -d '{"available":false}'
curl -s -X POST "$B/menu/import/csv" -H "Authorization: Bearer $TOK" --data-binary @<(jq -Rs '{csv: ., dry_run: true}' cardapio.csv)
curl -s -X POST "$B/messages" -H "Authorization: Bearer $TOK" -d '{"to":"5511999999999","type":"menu_item","menu_item_id":"'$ITEM'"}'
curl -s -X POST "$B/menu/send" -H "Authorization: Bearer $TOK" -d '{"to":"5511999999999","client_message_id":"cardapio-1"}'
```

Referência completa em [Cardápio](/docs/api/cardapio/). Chaves de API `read` leem o cardápio; chaves `send`
enviam (`/menu/send`, `/menu/items/{id}/send` e os tipos `menu_item` e `menu`); editar exige o token da
instância.

## Enviar para um cliente

No painel, **Enviar cardápio para…** mostra a prévia das mensagens (o cardápio todo ou uma categoria) e coloca
na fila. O botão de envio de cada cartão manda o item com foto. Tudo passa pela fila anti-ban, com o intervalo,
a rampa e a janela de envio da instância. Um cardápio longo vira várias mensagens de até ~3500 caracteres; para
clientes, prefira mandar a categoria que pediram.
