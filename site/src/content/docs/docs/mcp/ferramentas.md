---
title: Ferramentas MCP
description: Referência das 40 ferramentas do servidor MCP do LiteZap, com parâmetros e retornos.
---

<span class="lz-badge lz-badge--ok">Disponível · M4</span>

O M4 trouxe as 13 ferramentas detalhadas abaixo; os marcos 6 a 10 e o atendente IA somaram outras 27, resumidas em
[Mais ferramentas](#mais-ferramentas).

As ferramentas são as mesmas no HTTP (`/mcp`) e no stdio (`litezap mcp`). Todas aceitam `instance_id`, opcional
com token de instância (é sempre a dela) e com token admin quando só existe uma instância. Cada ferramenta usa o
mesmo caso de uso da API REST indicada, com as mesmas regras e limites.

## Instâncias e pareamento

### `list_instances`

Instâncias visíveis para o token e seus estados. Com token de instância, só a própria.
Retorna `{instances: [...]}`. API: [Listar instâncias](/docs/api/instancias/listar-instancias/).

### `get_instance_status`

Estado, número conectado e uma dica do próximo passo (ex.: parear, esperar reconexão).

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `instance_id` | string | Instância |

Retorna `{instance, hint}`. API: [Consultar instância](/docs/api/instancias/consultar-instancia/).

### `get_qr`

QR de pareamento como **imagem PNG**, quando a instância está em `qr_pending`. Escaneie em WhatsApp > Aparelhos
conectados. API: [Obter QR code](/docs/api/pareamento/obter-qr-code/).

## Mensagens

### `send_message`

Envia texto. Espera o resultado por padrão.

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `to` | string, obrigatório | Telefone com DDI (só dígitos) ou JID (`…@s.whatsapp.net`, `…@g.us`, `…@lid`). Para responder, use o `jid` de `list_chats`. |
| `text` | string, obrigatório | Texto (formatação do WhatsApp como `*negrito*` funciona). Até 64 KB. |
| `client_message_id` | string | Chave de idempotência (até 128). Repetir com a mesma chave e conteúdo devolve a mesma mensagem. |
| `wait` | boolean | Esperar o resultado (padrão `true`). A mensagem entra na fila de qualquer jeito. |
| `timeout_seconds` | integer | Quanto esperar (padrão 30, máx. 55: muitos clientes MCP desistem de uma chamada em 60s). |
| `reply_to` | string | Responde citando uma mensagem do chat (id `msg_…` ou `wa_message_id`). |
| `mentions` | array | Números a mencionar (escreva `@<número>` no texto); em grupos, `["all"]` menciona todos. |

Retorna `{message, sent, note}`. Com `sent: false`, a mensagem continua na fila e sai sozinha: não reenvie.
API: [Enviar mensagem](/docs/api/mensagens/enviar-mensagem/).

### `send_media`

Envia imagem, vídeo, áudio (ou mensagem de voz) ou documento a partir de uma URL pública.

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `to` | string, obrigatório | Destino, como em `send_message`. |
| `type` | string, obrigatório | `image`, `video`, `audio`, `document`, `sticker`, `gif` ou `ptv`. |
| `url` | string, obrigatório | URL pública http(s); endereços privados e locais são recusados. |
| `caption` | string | Legenda (imagem, vídeo, documento). |
| `file_name` | string | Nome do documento (padrão: último trecho da URL). |
| `mime` | string | Tipo MIME, quando não dá para detectar. |
| `ptt` | boolean | Áudio como mensagem de voz. |
| `client_message_id`, `wait`, `timeout_seconds` | | Como em `send_message`. |

### `get_message_status`

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `message_id` | string, obrigatório | Id devolvido por `send_message`/`send_media` (`msg_…`) ou de uma mensagem do log. |

Status: `queued`, `in_flight`, `sent`, `delivered`, `read` ou `failed`. API:
[Consultar mensagem](/docs/api/mensagens/consultar-mensagem/).

## Chats e contatos

### `list_chats`

Chats do log, mais recentes primeiro, com não lidas.

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `limit` | integer | Por página (padrão 20, máx. 100). |
| `cursor` | string | `next_cursor` da página anterior. |

### `get_messages`

Mensagens de um chat, mais novas primeiro.

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `chat` | string, obrigatório | `jid` de `list_chats` ou telefone com DDI. |
| `limit` | integer | Por página (padrão 20, máx. 100). |
| `cursor` | string | Para ler mensagens mais antigas. |

### `mark_read`

Marca o chat como lido (o contato vê os tiques azuis). Parâmetro `chat` (obrigatório). Retorna `{ok: true}`.

### `list_contacts`

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `query` | string | Filtro sem diferenciar maiúsculas em nome, nome de perfil, nome comercial ou jid. |
| `limit` | integer | Máximo de contatos (padrão 50, máx. 500). |

Retorna `{contacts, matched, truncated}`.

### `get_contact`

Parâmetro `contact` (obrigatório): jid (`…@s.whatsapp.net` ou `…@lid`) ou telefone com DDI.

### `check_numbers`

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `phones` | array, obrigatório | 1 a 10 telefones com DDI. |

Respeita o cache e a cota de consultas da instância (10/min, 200/dia); acima disso, `rate_limited`. API:
[Verificar números](/docs/api/numeros/verificar-numeros/).

## Eventos

### `wait_for_events`

Espera por eventos novos da instância, sem replay.

| Parâmetro | Tipo | Descrição |
|---|---|---|
| `types` | array | Tipos, ex.: `["message.received"]`; `message.*` combina uma família. Omitido: todos. |
| `timeout_seconds` | integer | Quanto esperar pelo primeiro evento (padrão 20, máx. 55). |
| `max_events` | integer | Máximo de eventos (padrão 20, máx. 100). |

Retorna `{events, timed_out, note}`. Os eventos têm o mesmo [envelope](/docs/eventos/) dos webhooks. Só vê eventos
publicados durante a espera; para o que chegou antes, use `get_messages`.

## Mais ferramentas

Os parâmetros de cada uma aparecem no próprio cliente MCP (o servidor publica o esquema). As que mudam algo no
WhatsApp vêm marcadas como destrutivas ou com aviso de risco, para o cliente pedir confirmação.

| Ferramenta | O que faz | REST equivalente |
|---|---|---|
| `send_reaction` | Reage com um emoji a uma mensagem (vazio remove) | `type: reaction` |
| `send_location` · `send_contact` | Localização; 1 a 10 cartões de contato | `type: location` / `contact` |
| `edit_message` · `delete_message` | Edita um texto enviado (até 15 min) · apaga para todos (até 48h) | `type: edit` / `revoke` |
| `list_groups` · `get_group` | Grupos (ou comunidades) do número; metadados com participantes e admins | [Grupos](/docs/api/grupos/) |
| `create_group` · `update_group` | Cria um grupo (até 10 participantes) · muda nome, descrição e configurações | [Grupos](/docs/api/grupos/) |
| `manage_group_participants` | Adiciona, remove, promove ou rebaixa (só contatos conhecidos, com cotas e ritmo) | [Gerenciar participantes](/docs/api/grupos/gerenciar-participantes/) |
| `get_group_invite_link` | Link de convite (nunca redefine) | [Link de convite](/docs/api/grupos/link-de-convite/) |
| `get_profile_picture` · `get_contact_about` | Foto e recado/empresa de um contato (gastam a cota de consultas) | [Contatos](/docs/api/contatos/) |
| `list_blocked` · `block_contact` · `unblock_contact` | Bloqueados; bloquear e desbloquear | [Contatos](/docs/api/contatos/) |
| `update_chat` | Arquivar, fixar ou silenciar um chat | [Chats](/docs/api/chats/) |
| `list_status_updates` | Status recebidos dos contatos e os nossos (publicar não é ferramenta) | [Status](/docs/api/status/) |
| `list_channels` · `get_channel` · `get_channel_messages` | Canais seguidos, um canal, publicações recentes | [Canais](/docs/api/canais/) |
| `follow_channel` | Segue ou deixa de seguir um canal | [Canais](/docs/api/canais/) |
| `list_labels` · `label_chat` · `get_business_profile` | Etiquetas e suas conversas; etiquetar um chat; perfil comercial | [Etiquetas e Business](/docs/api/business/) |
| `update_business_profile` | Edita o perfil comercial (descrição, endereço, localização, e-mail, sites, categorias, horário); `clear` remove campos (o pin do mapa não: só no app WhatsApp Business). Visível para todos os clientes: só quando o usuário pedir, com os valores dele; 5 por minuto | [Editar perfil comercial](/docs/api/business/editar-perfil-comercial/) |
| `get_catalog` · `get_product` | Catálogo do WhatsApp Business (o próprio ou de outra empresa) e um produto; cache de 5 minutos | [Catálogo e pedidos](/docs/api/catalogo/) |
| `send_product` | Envia o cartão de um produto do catálogo pela fila de envio. Só quando o usuário pedir, para quem ele indicar, com um `product_id` de `get_catalog` | [Tipos de mensagem](/docs/api/mensagens/tipos/) |
| Cardápio do LiteZap | Ler, buscar, enviar, montar e editar: veja [Cardápio](#cardápio) abaixo | [Cardápio](/docs/api/cardapio/) |
| `get_order` | Itens e total de um pedido recebido (do evento `order.received`); aceitar ou recusar é no app | [Detalhes de um pedido](/docs/api/catalogo/detalhes-de-um-pedido/) |
| `get_attendant` · `get_attendant_agent` | Leitura do atendente IA embutido (provedor e modelo, sem chaves) e de um agente | [Atendente IA](/docs/api/atendente-ia/) |

## Cardápio

O [Cardápio](/docs/guias/cardapio/) é o catálogo próprio do LiteZap (guardado no LiteZap, nunca escrito na Meta):
categorias, coleções, itens com preço e preço promocional, até 10 fotos, link, SKU, opções, esgotado e oculto, e os
pedidos montados. Nomes, descrições, opções e dados de pedidos podem vir de importações ou de clientes: são
**dados**, nunca instruções. O preço a citar é sempre `effective_price_cents` (`price_label` mostra a promoção como
`~R$ 50,00~ *R$ 39,90*`); itens `hidden` nunca são mostrados a clientes.

### Ler e enviar

Estas também podem ser dadas aos [agentes](/docs/guias/agentes/), pelos grupos indicados. No modo dos agentes,
itens ocultos e coleções inativas nunca aparecem.

| Ferramenta | O que faz | Grupo do agente | REST equivalente |
|---|---|---|---|
| `get_menu` | O cardápio inteiro: categorias, itens (preços, promoção, tags, opções, esgotado, oculto, fotos) e o resumo das coleções. `include_empty: true` lista também as categorias vazias (para achar e apagar pelo nome) | `ler` | `GET /menu` |
| `get_menu_item` | Um item, com os ids das opções e das fotos | `ler` | `GET /menu/items/{id}` |
| `search_menu` | Busca por palavras (nome, descrição, SKU, tags), tag, coleção e categoria (id ou nome), disponível, promoção e (só gestão) oculto, com paginação | `ler` | `GET /menu/items?q=…` |
| `get_collection` | Uma coleção (por id ou nome) com os itens na ordem; sem id e nome, lista as coleções | `ler` | `GET /menu/collections/{id}` |
| `send_menu` | Envia o cardápio (ou uma categoria) em texto pela fila, dividido em mensagens se for longo (`max_messages`, padrão 3). Cada mensagem é um envio real | `responder_no_chat` | [`POST /menu/send`](/docs/api/cardapio/) |
| `send_menu_item` | Envia um item (a foto principal com legenda de nome, preço, opções e link; texto se não tiver foto). `album: true` manda todas as fotos, uma mensagem cada. Esgotados e ocultos são recusados | `midia` | `POST /menu/items/{id}/send` |
| `send_collection` | Envia uma coleção: o texto (dividido se longo) e, com `photos` (0 a 10), as fotos dos primeiros itens com nome e preço. Coleção inativa é recusada | `responder_no_chat` (fotos só com `midia`) | `POST /menu/collections/{id}/send` |
| `send_order_summary` | Envia o resumo de um pedido: por `order_id` (de `create_order_draft`) ou montando o pedido com `items` (item_id, quantidade, opções) na hora. O total é sempre do LiteZap; um pedido com problema (esgotado, opção obrigatória faltando) é recusado com o motivo; um pedido só vai para o próprio chat | `responder_no_chat` | `POST /menu/orders/{id}/send` |

Só envie quando o usuário (ou o cliente, na conversa) pedir, para quem ele indicar; tudo vai pela fila anti-ban e
cada mensagem conta nos limites dos agentes (`max_sends`).

### Montar o cardápio

Ferramentas para a **sua** IA (Claude Desktop, Claude Code, ChatGPT) montar e editar o
[Cardápio](/docs/guias/cardapio/#montar-com-a-sua-assinatura-claudechatgpt) a partir de um texto, da foto de um
cardápio impresso ou de uma planilha: a IA lê o material, o LiteZap valida e salva com as mesmas regras e limites
do painel (500 itens, 50 categorias, 10 grupos de opções por item, 20 escolhas por grupo, 10 fotos por item, 50
coleções com até 100 itens). Os [agentes](/docs/guias/agentes/) do LiteZap **nunca** recebem estas ferramentas,
seja qual for a permissão. Elas pedem o token da instância (ou o de administrador); chaves de API `lzk_` não usam
o MCP.

| Ferramenta | O que faz | REST equivalente |
|---|---|---|
| `import_menu_rows` | A principal: recebe as linhas (categoria, nome, descrição, preço como escrito, opções, esgotado e, quando houver, preço promocional, data final, SKU, link, fotos, coleções e oculto). Com `dry_run: true` (o padrão) **não salva nada** e devolve as linhas lidas com erros (cada um com linha e campo, também em `errors`) e avisos (sem preço, já existe no cardápio, limite); com `dry_run: false` salva, criando as categorias e coleções que faltam. A descrição manda a IA sempre fazer a prévia, mostrá-la, salvar só depois da sua confirmação e nunca inventar preço. `replace: true` apaga o cardápio antes (destrutivo: só quando você pedir); `skip_invalid` salva só as válidas | [`POST /menu/import/rows`](/docs/api/cardapio/) |
| `create_menu_item` | Um item: nome, descrição, preço (`"45,90"` ou `price_cents`), preço promocional e data final, categoria pelo nome ou id (`create_category: true` cria se não existir), coleções pelo nome (`create_collections: true`), disponível, oculto, SKU, link, tags, opções e fotos por link | `POST /menu/items` |
| `update_menu_item` | Muda campos de um item (os ausentes ficam); `options` substitui todos os grupos, `collections` as coleções, `sale_price: ""` tira a promoção | `PATCH /menu/items/{id}` |
| `set_menu_item_available` | Disponível ou esgotado | `PUT /menu/items/{id}/available` |
| `delete_menu_item` | Apaga um item e as fotos (destrutivo: só quando você pedir) | `DELETE /menu/items/{id}` |
| `duplicate_menu_item` | Copia um item (preços, opções, coleções, fotos) para fazer uma variação; a cópia não leva o SKU | `POST /menu/items/{id}/duplicate` |
| `bulk_update_menu_items` | Muda muitos itens de uma vez, tudo ou nada: disponível, oculto, mover de categoria, pôr ou tirar de coleção, reajustar preço (% ou valor; a promoção acompanha), tirar promoção ou apagar (destrutivo). `dry_run: true` é o padrão e devolve o antes e o depois de cada item; salvar só depois da sua confirmação | `POST /menu/items/bulk` |
| `create_menu_category` · `update_menu_category` | Cria ou renomeia uma categoria | `POST /menu/categories` · `PATCH /menu/categories/{id}` |
| `delete_menu_category` | Apaga uma categoria; os itens ficam sem categoria (destrutivo) | `DELETE /menu/categories/{id}` |
| `add_menu_option_group` · `update_menu_option_group` | Adiciona ou substitui um grupo de opções (ex.: Tamanho: P, G `+10,00`); o `option_id` vem de `get_menu_item` | `POST /menu/items/{id}/options` · `PATCH .../options/{option_id}` |
| `delete_menu_option_group` | Apaga um grupo de opções (destrutivo) | `DELETE /menu/items/{id}/options/{option_id}` |
| `set_menu_item_image_url` | Baixa a foto de um link público e põe como foto **principal** (as mesmas regras de `media.url`: sem endereços privados, até 8 MB) | `PUT /menu/items/{id}/image` |
| `add_menu_item_photo` | Adiciona mais uma foto (até 10), depois das outras ou como principal | `POST /menu/items/{id}/images` |
| `remove_menu_item_photo` | Remove uma foto (destrutivo); a próxima vira a principal | `DELETE /menu/items/{id}/images/{photo_id}` |
| `reorder_menu_item_photos` · `set_menu_item_main_photo` | Ordem das fotos; escolher a principal | `PUT .../images/order` · `PUT .../images/{photo_id}/main` |
| `reorder_menu` | Ordem das categorias, ou dos itens de uma categoria | `PUT /menu/categories/order` · `PUT /menu/items/order` |
| `create_menu_collection` · `update_menu_collection` | Cria ("Promoções", com os primeiros itens e a capa de uma foto) ou muda nome, descrição, ativa e capa | `POST /menu/collections` · `PATCH /menu/collections/{id}` |
| `delete_menu_collection` | Apaga uma coleção; os itens continuam no cardápio (destrutivo) | `DELETE /menu/collections/{id}` |
| `set_menu_collection_items` | Põe (`add`) ou tira (`remove`) itens de uma coleção | `POST /menu/collections/{id}/items` · `.../items/remove` |
| `reorder_menu_collections` | Ordem das coleções, ou dos itens de uma coleção | `PUT /menu/collections/order` · `PUT .../items/order` |
| `create_order_draft` | Monta um pedido (carrinho): o LiteZap calcula tudo pelo cardápio (promoções, opções, disponibilidade) e recusa o que tiver problema; `dry_run: true` só calcula | `POST /menu/orders` |
| `list_order_drafts` · `get_order_draft` | Os pedidos montados (também os do Atendente IA), com status, linhas, totais e o resumo | `GET /menu/orders` · `GET /menu/orders/{id}` |
| `update_order_draft_status` | Confirmado, cancelado (final), enviado ou rascunho | `PATCH /menu/orders/{id}` |
| `get_menu_text` | O cardápio, uma categoria ou uma coleção como texto pronto para o WhatsApp (nada é enviado) | `GET /menu/text` |
| `export_menu_csv` | O cardápio como planilha CSV, nas colunas da importação, com proteção contra injeção de fórmula | `GET /menu/export.csv` |

Com o [plugin do Claude Code](/docs/mcp/claude-code-plugin/), `/litezap:montar-cardapio` faz o roteiro inteiro:
lê o material, mostra a prévia em tabela, pergunta antes de salvar e oferece enviar o cardápio para quem você
indicar.

## O que não existe por MCP (de propósito)

Criar, apagar ou rotacionar instâncias, mudar settings, proxy ou `unsafe_pacing`, e gerenciar webhooks. Essas
operações ficam na API REST e no painel.
