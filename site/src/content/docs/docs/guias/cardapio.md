---
title: Cardápio (catálogo do LiteZap)
description: Monte o catálogo do seu negócio dentro do LiteZap — categorias, coleções, preços e promoções, opções, várias fotos e pedidos — sem ligar o número a uma conta de anúncios da Meta.
---

O **Cardápio** é o catálogo próprio do LiteZap: categorias e **coleções**, itens com preço, **preço
promocional**, até **10 fotos**, link, SKU e opções (tamanho, borda, adicionais), a marcação de **esgotado** e a
de **oculto**, e os **pedidos montados** nas conversas. Ele fica guardado no seu LiteZap, por instância, e é usado
pelo [Atendente IA](/docs/guias/atendimento-ia/), pelos [agentes](/docs/guias/agentes/) e pelo MCP para responder
e montar pedidos, e pode ser enviado a um cliente como mensagem.

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
| Coleções ("Promoções", "Mais vendidos") | Sim | Sim |
| Preço promocional | Sim, com data final | Sim |
| Várias fotos por item | Até 10, com a principal | Sim |
| Link e código (SKU) | Sim | Sim (`url`, `retailer_id`) |
| Ocultar sem apagar | Sim | Sim |
| Opções e acréscimos (tamanho, borda) | Sim | Não |
| Como o cliente vê | Foto com legenda, álbum ou texto no chat | Cartão de produto e carrinho do WhatsApp |
| Pedidos | Pedidos montados com o total calculado pelo cardápio | `order.received` pelo carrinho |

Dá para usar os dois: copie o catálogo do WhatsApp para o Cardápio com um clique (só leitura no WhatsApp) e
complete com opções, promoções e esgotados.

## Montar o cardápio

No painel, abra a instância e a aba **Cardápio**. Com o cardápio vazio, ela mostra os três caminhos:

1. **Importar planilha (CSV).** No Excel ou Google Planilhas, *Arquivo > Fazer download > CSV*. A primeira linha
   tem os nomes das colunas (sem diferenciar acentos ou maiúsculas): `categoria`, `nome` (obrigatória),
   `descricao`, `preco`, `disponivel`, `imagem_url` e, opcionais, `opcoes`, `sku`, `tags`,
   `preco_promocional`, `promocao_ate` (`31/12/2026` ou `2026-12-31`), `link`, `colecoes` (nomes separados
   por `|`; as que não existem são criadas), `fotos` (links separados por `|`, até 10; o primeiro é a foto
   principal) e `oculto` (`sim`/`não`). Clique em **Pré-visualizar**: cada linha aparece com os erros antes de
   salvar. Só as linhas válidas são salvas.
2. **Copiar do catálogo do WhatsApp.** Lê os produtos do catálogo do próprio número (como `GET /catalog`) e copia
   nome, descrição, preço e preço promocional, disponibilidade, link, o código (`retailer_id`, que vira o SKU)
   e **todas as fotos** (até 10). As **coleções** do WhatsApp viram coleções do LiteZap (e a primeira dá a
   categoria). Produtos ocultos ou em análise ficam de fora; os já copiados são pulados, mesmo com duas cópias
   ao mesmo tempo (rodar de novo busca as fotos que faltaram e põe os itens nas coleções).
3. **Montar com IA.** O caminho principal é a IA que você **já assina** (Claude ou ChatGPT): veja
   [Montar com a sua assinatura](#montar-com-a-sua-assinatura-claudechatgpt). Se houver um provedor pronto no
   Atendente IA (qualquer um, inclusive o [Ollama gratuito](/docs/guias/ia-gratis/)), o painel também oferece
   montar ali mesmo: cole o texto livre — `pizza calabresa 45, marguerita 42, coca 2L 12` — e revise. **Nada é
   salvo** até você revisar (dá para corrigir nome e preço e desmarcar linhas) e confirmar.

Ou crie os itens à mão em **Novo item**: nome, preço, preço promocional, categoria, descrição, fotos (arquivo ou
link; o LiteZap reduz para JPEG de até 1080 px), link, SKU, tags, coleções, grupos de opções e a **prévia** de
como o item chega no WhatsApp.

### Erros da prévia

Toda linha com problema traz o motivo, em português, de três jeitos: na própria linha (`error` e `errors`), na
lista geral `errors` (`linha 3: preço inválido 'abc' (use 45,90 ou R$ 1.234,50)`) e em `error_details`, com a
linha e o campo (`preco`, `nome`, `preco_promocional`, `opcoes`…). Problemas do arquivo inteiro (sem cabeçalho,
fora de UTF-8) ficam em `file_errors`.

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
   uma tabela com categoria, item, preço e opções, com os **erros** (preço ilegível, nome vazio, promoção maior
   que o preço) e **avisos** (sem preço, item que já existe). Corrija o que precisar e diga "pode salvar": só
   então ela chama de novo com `dry_run: false`.
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
- **Promoções e coleções**: "põe a calabresa por 39,90 até domingo e coloca na coleção Promoções", "cria a
  coleção Mais vendidos com a calabresa, a marguerita e a coca", "manda a coleção Promoções para o 5511…, com 3
  fotos".
- **Muitos itens de uma vez**: "aumenta 10% todas as pizzas" — a IA busca os itens (`search_menu`), mostra a
  prévia de `bulk_update_menu_items` (antes e depois de cada item) e só salva depois do seu "sim".
- **Variações**: "duplica a calabresa como Calabresa Grande, por 59,90".

Regras que as ferramentas passam para a IA: sempre a prévia antes (importação e alterações em massa); salvar só
depois do seu "sim"; **nunca inventar preço** nem link de foto; apagar itens, categorias, coleções ou fotos e
substituir o cardápio (`replace`) só quando você pedir; textos do cardápio e dados de pedidos são **dados**, nunca
instruções. As ferramentas de edição são só para a **sua** IA: os [agentes](/docs/guias/agentes/) do LiteZap
nunca as recebem, seja qual for a permissão. Lista completa em [Ferramentas MCP](/docs/mcp/ferramentas/#cardápio).

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

## Itens

### Preços e promoções

Escreva como no Brasil: `45`, `45,9`, `45,90`, `R$ 1.234,50`. Também vale `45.90`. Na API o preço é
`price_cents` (centavos: R$ 45,90 = `4590`); na criação e alteração também dá para mandar `price` como texto.

O **preço promocional** (`sale_price_cents`, ou `sale_price` como texto) precisa ser **menor** que o preço. Com
`sale_until` (o último dia, `2026-12-31` ou `31/12/2026`), a promoção acaba sozinha no dia seguinte; sem data,
vale até você tirar (`sale_price: ""`, que também tira a data). Durante a promoção, o WhatsApp mostra o preço
antigo riscado e o novo em negrito:

```text
*Pizza Calabresa* — ~R$ 50,00~ *R$ 39,90*
```

O item traz `on_sale`, `effective_price_cents` (o preço de hoje) e `price_label` (o texto acima). Os pedidos, o
Atendente IA e os agentes usam sempre o preço efetivo.

### Opções

Um item tem até 10 grupos de opções, cada um com até 20 escolhas e um acréscimo (ou desconto) por escolha.
Ex.: **Tamanho** obrigatório (P, M +R$ 5,00, G +R$ 10,00) e **Borda** opcional (Catupiry +R$ 8,00). O
acréscimo soma ao preço efetivo (o promocional, durante a promoção).

Na planilha, a coluna `opcoes` usa grupos separados por `;`, cada um `Nome: escolha | escolha +preço`:

```text
Tamanho: P | M +5,00 | G +10,00; Borda (opcional): Catupiry +8 | Cheddar +8
```

Por padrão um grupo é obrigatório e de uma escolha. `(opcional)` deixa opcional, `(até 3)` permite até 3
escolhas e `(mín 2)` exige 2. Se a planilha usa `;` como separador, deixe a célula entre aspas (o Excel faz isso
sozinho ao exportar).

### Fotos

Até **10 fotos** por item, em ordem; a primeira é a **principal** (a que vai com o item e aparece no cartão). No
editor do item, a galeria tem ★ (tornar principal), ◀ ▶ (ordem) e a lixeira; num item novo, as fotos escolhidas
sobem logo depois de criar. Na API: `POST .../images` (adiciona; `main: true` põe na frente),
`PUT .../images/order`, `PUT .../images/{id}/main` e `DELETE .../images/{id}`. A rota antiga de foto única
(`PUT/DELETE .../image`) continua funcionando: ela troca (ou tira) só a foto principal.

### Link e SKU

`link_url` (http ou https) vai na legenda quando o item é enviado. `sku` é o seu código interno; a cópia do
catálogo do WhatsApp preenche com o `retailer_id`.

### Esgotado e oculto

- **Esgotado** (`available: false`, a chave **Disponível** do cartão): o item continua listado, marcado como
  esgotado; não é enviado sozinho, o atendente avisa que está indisponível e o pedido aponta o problema.
- **Oculto** (`hidden: true`): o item fica guardado, mas **nunca** aparece para clientes, para o Atendente IA nem
  para os agentes — fora dos textos, dos envios, das coleções enviadas, das buscas dos agentes e dos pedidos. Use
  para itens sazonais ou ainda não lançados. O painel e a sua IA continuam vendo, com o selo **Oculto**.

### Buscar e filtrar

A barra acima dos cartões busca por nome, descrição, SKU e tags (sem diferenciar acentos) e filtra por situação
(disponíveis, esgotados, ocultos), coleção e promoção. Na API é `GET /menu/items?q=…&tag=…&collection_id=…&
category_id=…&available=…&hidden=…&on_sale=…`; no MCP, `search_menu`.

### Duplicar

O botão de copiar do cartão (ou `POST .../duplicate`, ou `duplicate_menu_item`) cria uma cópia do item — preços,
opções, coleções e fotos — para fazer uma variação rápida ("Calabresa Grande"). A cópia não leva o SKU.

### Alterações em massa

Marque a caixa no canto de cada cartão: aparece a barra de ações para os selecionados — **Disponível**,
**Esgotado**, **Ocultar**, **Mostrar**, **Mover** (para uma categoria), **Coleção** (adicionar), **Reajustar
preço** (por % ou por valor, com a **prévia** do antes e depois de cada item antes de salvar; o preço promocional
muda do mesmo jeito), **Tirar promoção** e **Apagar** (pede confirmação). É tudo ou nada: se um item não aceita a
mudança (preço negativo, promoção que deixaria de ser menor que o preço), nada é salvo e o motivo aparece por
item. Na API, `POST /menu/items/bulk` (com `dry_run`); no MCP, `bulk_update_menu_items` (prévia por padrão).

## Coleções

Coleções agrupam itens de **qualquer categoria** — "Promoções", "Mais vendidos", "Novidades" — e um item pode
estar em várias. Na aba **Coleções**: criar, editar nome e descrição, escolher a **capa** (uma foto de um item),
escolher os itens (com busca) e a ordem deles, ativar ou desativar (inativa = guardada, mas não oferecida a
clientes; o Atendente IA só vê as ativas), reordenar, enviar e apagar (os itens continuam no cardápio).

O botão de enviar manda a coleção para um cliente: o texto (nome, descrição e itens com preços e promoções) e,
se quiser, as **fotos dos primeiros itens** (até 10, cada uma com nome e preço; cada foto é uma mensagem). Itens
ocultos ficam de fora; esgotados ficam fora das fotos.

## Exportar planilha (CSV)

**Exportar CSV** (ou `GET /menu/export.csv`, ou `export_menu_csv` no MCP) baixa o cardápio inteiro no mesmo
formato que a importação lê (separado por `;`, UTF-8 com BOM, como o Excel em português abre): dá para editar na
planilha e importar de volta. A coluna `fotos` traz os links de onde as fotos foram baixadas (fotos enviadas
como arquivo não têm link).

:::caution[Proteção contra injeção de fórmula]
Uma célula que começa com `=`, `+`, `-` ou `@` (ou com tab) poderia ser executada como fórmula pela planilha. A
exportação põe uma aspa simples na frente dessas células (`'=…`), e a importação tira a aspa de volta.
:::

## Pedidos montados

O **motor de pedidos** calcula um carrinho pelo cardápio: para cada linha, o preço efetivo do item (o
promocional durante a promoção) mais os acréscimos das opções escolhidas, vezes a quantidade. Ele recusa item
fora do cardápio ou oculto, esgotado, opção que não existe, escolhas demais num grupo e grupo obrigatório sem
escolha — com o motivo de cada linha. O preço que alguém escreveu (o modelo, o assistente) nunca vale.

```bash
curl -s -X POST "$B/menu/orders" -H "Authorization: Bearer $TOK" -d '{"chat":"5511999999999","customer_name":"Ana",
  "items":[{"item_id":"mitm_0a1b2c3d4e5f6a7b","quantity":2,"options":["G","Borda: Catupiry"]}]}'
```

O pedido salvo (status `draft`) tem as linhas, o total, a economia das promoções e o **resumo** pronto para o
WhatsApp:

```text
*Resumo do pedido*

2x Pizza Calabresa — R$ 115,80
   Tamanho: G (+R$ 10,00) · Borda: Catupiry (+R$ 8,00)
   promoção: ~R$ 50,00~ *R$ 39,90* cada

*Total: R$ 115,80*
Você economiza R$ 20,20
Cliente: Ana
```

`POST /menu/orders/{id}/send` (ou `send_order_summary`) põe o resumo na fila para o chat do pedido e marca
`sent` — só se o pedido ainda bate com o cardápio: se um item ficou oculto ou esgotado, ou um preço mudou (a
promoção acabou, por exemplo), o envio é recusado e é preciso montar o pedido de novo; depois, `PATCH` com `confirmed` ou `cancelled` (cancelado é final; confirmado só pode ser cancelado). A
aba **Pedidos montados** lista todos com status e origem, e tem enviar, confirmar e cancelar. O LiteZap guarda os
1000 pedidos mais recentes por instância.

O Atendente IA usa o **mesmo motor**: o `agent.order_draft` grava o pedido (origem `attendant`) e traz o
`order_draft_id`; quando o cliente muda o pedido na conversa, o mesmo rascunho é atualizado.

## Limites

500 itens, 50 categorias, 10 grupos de opções por item, 20 escolhas por grupo, 10 fotos por item, 50 coleções
e 100 itens por coleção, por instância (`409 menu_limit`, checado na mesma transação da escrita, então dois
pedidos ao mesmo tempo não passam do limite). Pedidos: até 50 linhas e quantidade até 999. Fotos de até 8 MB e
10 megapixels (JPEG, PNG, GIF ou WebP). A planilha deve estar em UTF-8 (no Excel: *Salvar como > CSV UTF-8*).
O "Montar com IA" aceita até 5 pedidos por minuto e 100 por dia por instância; essa cota fica em memória e
reiniciar o LiteZap a zera (os tetos diários do provedor, no Atendente IA, continuam valendo).

## Usar o cardápio

### Atendente IA

No agente do Atendente IA, **Lista de produtos > Cardápio do LiteZap** (ou `knowledge_source: "menu"`). É o
padrão sugerido para agentes novos quando a instância tem cardápio. O agente recebe itens, preços (o promocional
durante a promoção, com o normal depois de "de"), categorias, coleções ativas, opções e esgotados — **nunca os
itens ocultos** — e o evento `agent.order_draft` **recalcula os preços pelo cardápio**, somando as opções
escolhidas (o preço que o modelo escreveu não vale), e grava o [pedido montado](#pedidos-montados):

```json
{ "items": [{ "name": "Calabresa", "quantity": 2, "unit_price": 49.9, "subtotal": 99.8, "in_catalog": true,
  "menu_item_id": "mitm_0a1b2c3d4e5f6a7b", "available": true, "on_sale": true,
  "options": [{ "group": "Tamanho", "choice": "G", "price_delta": 10 }] }],
  "total": 99.8, "all_items_in_catalog": true, "order_draft_id": "odr_1a2b3c4d5e6f7a8b" }
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
| `get_menu`, `get_menu_item`, `search_menu`, `get_collection` | `ler` | Lê o cardápio, busca itens e lê coleções (sem itens ocultos nem coleções inativas) |
| `send_menu` | `responder_no_chat` | Envia o cardápio (ou uma categoria) em texto, dividido em mensagens. Cada mensagem conta em `max_sends`; o motor limita `max_messages` aos envios que restam e recusa um cardápio maior (envie uma categoria) |
| `send_collection` | `responder_no_chat` | Envia uma coleção em texto; as fotos dos itens só com `midia`. Texto e fotos contam em `max_sends` |
| `send_order_summary` | `responder_no_chat` | Monta o pedido pelo cardápio e envia o resumo para o chat do pedido (conta 1 envio) |
| `send_menu_item` | `midia` | Envia um item com foto e legenda; `album: true` manda todas as fotos (cada uma conta em `max_sends`) |

As mesmas ferramentas existem no [MCP](/docs/mcp/ferramentas/), com os avisos de segurança de sempre: só enviar
quando pedirem, para quem indicarem. O MCP tem também as ferramentas para **montar e editar** o cardápio
(`import_menu_rows`, `create_menu_item`, coleções, fotos, `bulk_update_menu_items`, `duplicate_menu_item`,
`create_order_draft` e outras), só para a IA do usuário: nenhum agente as recebe.

### API

```bash
B=localhost:8787/v1/instances/$ID
curl -s "$B/menu" -H "Authorization: Bearer $TOK"
curl -s -X POST "$B/menu/items" -H "Authorization: Bearer $TOK" \
  -d '{"name":"Calabresa","price":"50","sale_price":"39,90","sale_until":"31/12/2026","options":[{"name":"Tamanho","required":true,"choices":[{"name":"P"},{"name":"G","price_delta_cents":1000}]}]}'
curl -s -X PUT "$B/menu/items/$ITEM/available" -H "Authorization: Bearer $TOK" -d '{"available":false}'
curl -s -X PUT "$B/menu/items/$ITEM/hidden" -H "Authorization: Bearer $TOK" -d '{"hidden":true}'
curl -s -X POST "$B/menu/items/$ITEM/images" -H "Authorization: Bearer $TOK" -d '{"url":"https://exemplo.com/foto2.jpg"}'
curl -s "$B/menu/items?q=calabresa&on_sale=true" -H "Authorization: Bearer $TOK"
curl -s -X POST "$B/menu/items/bulk" -H "Authorization: Bearer $TOK" -d '{"item_ids":["'$ITEM'"],"action":"adjust_price","percent":10,"dry_run":true}'
curl -s -X POST "$B/menu/collections" -H "Authorization: Bearer $TOK" -d '{"name":"Promoções","item_ids":["'$ITEM'"]}'
curl -s -X POST "$B/menu/collections/$COL/send" -H "Authorization: Bearer $TOK" -d '{"to":"5511999999999","photos":3}'
curl -s -X POST "$B/menu/import/csv" -H "Authorization: Bearer $TOK" --data-binary @<(jq -Rs '{csv: ., dry_run: true}' cardapio.csv)
curl -s "$B/menu/export.csv" -H "Authorization: Bearer $TOK" -o cardapio.csv
curl -s -X POST "$B/messages" -H "Authorization: Bearer $TOK" -d '{"to":"5511999999999","type":"menu_item","menu_item_id":"'$ITEM'"}'
curl -s -X POST "$B/menu/send" -H "Authorization: Bearer $TOK" -d '{"to":"5511999999999","client_message_id":"cardapio-1"}'
```

`GET /menu` lista também as categorias vazias e os itens ocultos (para gerenciar); `include_empty=false` e
`include_hidden=false` os deixam de fora. Referência completa em [Cardápio](/docs/api/cardapio/). Chaves de API
`read` leem o cardápio, as coleções, os pedidos e a exportação; chaves `send` enviam (`/menu/send`,
`/menu/items/{id}/send`, `/menu/collections/{id}/send`, `/menu/orders/{id}/send` e os tipos `menu_item` e
`menu`); editar exige o token da instância.

## Enviar para um cliente

No painel, **Enviar cardápio para…** mostra a prévia das mensagens (o cardápio todo ou uma categoria) e coloca
na fila. O botão de envio de cada cartão manda o item com a foto principal; uma coleção vai com texto e fotos
(aba **Coleções**). Tudo passa pela fila anti-ban, com o intervalo, a rampa e a janela de envio da instância. Um
cardápio longo vira várias mensagens de até ~3500 caracteres; para clientes, prefira mandar a categoria ou a
coleção que pediram.
