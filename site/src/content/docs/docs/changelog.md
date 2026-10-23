---
title: Changelog
description: O que mudou em cada marco do LiteZap.
---

As versões seguem os marcos do motor. Mudanças que quebram compatibilidade são marcadas com **Quebra**.

## Unreleased (v0.1.1)

Recursos de paridade com a Z-API. Todos estão prontos e testados com o worker simulado, mas ainda **aguardam
validação num aparelho real**.

**Novo**

- **Limpar conversa** (CHT-7): `POST /chats/{jid}/clear` (corpo opcional `{keep_starred, delete_media}`) limpa
  as mensagens da conversa no WhatsApp desta conta pelo app state (celular e aparelhos vinculados; a conversa
  fica, o contato não é afetado) e apaga essas mensagens do log do LiteZap, com as mídias baixadas. Responde a
  conversa e `deleted_messages`; publica `chat.updated` com `cleared: true` e `source: "api"`. É destrutivo:
  a ferramenta MCP `clear_chat` é marcada como destrutiva e só deve ser usada a pedido do usuário. Botão
  **Limpar…** na aba Conversas do painel. Uma limpeza feita no celular continua só gerando o evento (o log
  mantém as mensagens).

## Cardápio removido

O LiteZap é uma API de WhatsApp: o catálogo próprio (Cardápio) saiu do produto. O catálogo nativo do WhatsApp
continua igual: `GET /catalog*`, `type: "product"`, `catalog_link`, `order.received`, `GET /orders` e o Atendente
IA com `knowledge_source: "catalog"`.

**Removido** (**Quebra**)

- As rotas `/v1/instances/{id}/menu/*` (itens, categorias, opções, fotos, coleções, importações, pedidos montados e
  CSV) e a aba **Cardápio** do painel.
- Os tipos `menu_item` e `menu` em `POST /messages` (agora respondem `400 validation_failed`).
- As ferramentas MCP do Cardápio (`get_menu`, `get_menu_item`, `search_menu`, `get_collection`, `send_menu`,
  `send_menu_item`, `send_collection`, `send_order_summary`, as de montar e editar o cardápio e as de pedidos
  montados) e o comando `/litezap:montar-cardapio` do plugin. Os agentes deixam de recebê-las; um rascunho
  pendente de uma delas falha ao ser aprovado ("tool is not available").
- `knowledge_source: "menu"` do Atendente IA. Na migração, os perfis que o usavam passam para `catalog` quando a
  instância tinha catálogo do WhatsApp (cardápio copiado dele ou pedidos recebidos) e para `manual` nos outros
  casos; um perfil antigo que ainda diga `menu` é lido como `manual`. O `agent.order_draft` volta ao comportamento
  anterior ao Cardápio (preços da base de conhecimento ou do catálogo, sem `order_draft_id`).
- Dados: a migração `0018_drop_menu.sql` apaga as tabelas do Cardápio (`menu_categories`, `menu_items`,
  `menu_options`, `menu_item_images`, `menu_collections`, `menu_collection_items` e `order_drafts`), e a pasta
  `data/menu/` (fotos) é apagada na primeira inicialização.

## Cardápio completo (paridade com o catálogo do WhatsApp)

Tudo continua no LiteZap: nada é escrito na Meta ou no WhatsApp. Migração nova `0017_menu_catalog.sql`.

**Novo**

- Coleções ("Promoções", "Mais vendidos"): itens de qualquer categoria, capa de
  uma foto, ordem, ativa/inativa, texto e envio com as fotos dos primeiros itens. Até 50 coleções com 100 itens.
- Preço promocional com data final: menor que o preço, mostrado como
  `~R$ 50,00~ *R$ 39,90*`; pedidos, Atendente IA e agentes usam o preço efetivo.
- Até 10 fotos por item, com a principal; envio em álbum (`album: true`). A API de
  foto única continua e troca a foto principal.
- Link e SKU em todos os itens; oculto (`hidden`): nunca aparece para
  clientes, atendente ou agentes, diferente de esgotado.
- Busca e filtros (`GET /menu/items?q=…`, `search_menu`),
  duplicar item e alterações em massa
  com prévia (`POST /menu/items/bulk`).
- Pedidos montados: o motor de pedidos calcula o carrinho com opções,
  promoções e disponibilidade (`POST /menu/orders`), guarda o rascunho, envia o resumo pela fila
  (`POST /menu/orders/{id}/send`) e acompanha o status (draft, sent, confirmed, cancelled). O
  `agent.order_draft` do Atendente IA usa o mesmo motor, grava o mesmo rascunho e traz `order_draft_id`.
- Exportar CSV (`GET /menu/export.csv`), com proteção contra
  injeção de fórmula; a importação lê as colunas novas `preco_promocional`, `promocao_ate`, `link`, `colecoes`,
  `fotos` (links separados por `|`) e `oculto`.
- Cópia do catálogo do WhatsApp (só leitura): todas as fotos, link, `retailer_id` como SKU, preço promocional e as
  coleções do WhatsApp como coleções do LiteZap.
- Texto para compartilhar por coleção (`GET /menu/collections/{id}/text`, `GET /menu/text?collection_id=…`).
- MCP: `search_menu`, `get_collection`, `send_collection`, `send_order_summary` (também para os agentes, nos grupos
  `ler` e `responder_no_chat`, com os limites de envio) e, só para a IA do usuário, `create_order_draft`,
  `list_order_drafts`, `get_order_draft`, `update_order_draft_status`, `get_menu_text`, `export_menu_csv`,
  `create_menu_collection`, `update_menu_collection`, `delete_menu_collection`, `set_menu_collection_items`,
  `reorder_menu_collections`, `add_menu_item_photo`, `remove_menu_item_photo`, `reorder_menu_item_photos`,
  `set_menu_item_main_photo`, `duplicate_menu_item` e `bulk_update_menu_items`. Veja Ferramentas MCP.
- Painel: abas Itens, Coleções e Pedidos montados; galeria de fotos; prévia do preço riscado; seleção de vários
  itens com ações em massa.

**Corrigido**

- `get_menu` e `get_menu_item` devolvem as `tags` dos itens.
- A prévia da importação lista em `errors` o motivo de cada linha inválida (`linha 3: preço inválido 'abc' …`),
  com linha e campo em `error_details` e um `error` por linha; `file_errors` traz os problemas do arquivo.
- `get_menu` aceita `include_empty: true` para achar e apagar categorias vazias pelo nome; `GET /menu` aceita
  `include_empty=false` e `include_hidden=false`.

**Mudou**

- Itens copiados do catálogo do WhatsApp guardam a origem em `source_ref` (`wa:<id>`) e o `sku` passa a ser o
  `retailer_id`; itens copiados antes continuam com o `sku` antigo e não são copiados de novo.
- O `agent.order_draft` ganhou `on_sale` por item e `order_draft_id`.

## Cardápio com a sua assinatura (Claude/ChatGPT)

**Novo**

- Montar o cardápio com a sua assinatura: o seu
  Claude (Desktop ou Code) ou ChatGPT lê o texto, a foto do cardápio impresso ou a planilha e usa as ferramentas do
  LiteZap; sem Ollama e sem chave de API.
- MCP: [ferramentas para montar e editar o cardápio](/docs/mcp/ferramentas/#montar-o-cardápio) —
  `import_menu_rows` (prévia com `dry_run`, o padrão, e salvar só depois da confirmação), `create_menu_item`,
  `update_menu_item`, `delete_menu_item`, `set_menu_item_available`, `create_menu_category`,
  `update_menu_category`, `delete_menu_category`, `add_menu_option_group`, `update_menu_option_group`,
  `delete_menu_option_group`, `set_menu_item_image_url` e `reorder_menu`. As de apagar e o `replace` vêm marcadas
  como destrutivas. Só para a IA do usuário: os agentes do LiteZap nunca as recebem.
- `get_menu_item` mostra o id de cada grupo de opções.
- `POST /menu/import/rows` aceita o preço como texto em `price` ("45,90"); um texto que não é preço vira erro da
  linha e uma linha sem preço ganha um aviso.
- Plugin do Claude Code: comando `/litezap:montar-cardapio`.
- Painel: **Montar com IA** abre primeiro "Monte com o seu Claude ou ChatGPT (assinatura)", com os passos e um
  pedido pronto para copiar; montar com o provedor do Atendente IA continua como segunda opção quando há um pronto.

## Cardápio (catálogo do LiteZap)

**Novo**

- Cardápio: o catálogo próprio do LiteZap, guardado no LiteZap por instância e **nunca
  escrito na Meta** (sem conta de anúncios nem conta Business). Categorias, itens com preço em centavos, foto
  (upload ou link, reduzida para JPEG de até 1080 px), esgotado, SKU, tags e grupos de opções com acréscimos.
  Até 500 itens e 50 categorias.
- API Cardápio: `GET /menu`, `/menu/text`, criar, alterar, apagar e reordenar categorias,
  itens e opções, `PUT /menu/items/{id}/available` e a foto do item.
- Importações: planilha CSV com pré-visualização (`dry_run`) e preços brasileiros ("45,90", "R$ 1.234,50"),
  **Copiar do catálogo do WhatsApp** (só leitura, com as fotos) e **Montar com IA** com o provedor do Atendente IA
  (inclusive Ollama), sempre com revisão antes de salvar.
- Envios pela fila: tipos `menu_item` (foto e legenda) e `menu` em `POST /messages`,
  `POST /menu/send` (divide cardápios longos, idempotente por
  `client_message_id`) e `POST /menu/items/{id}/send`.
- Atendente IA: `knowledge_source: "menu"`, sugerido para agentes novos quando há cardápio; o `agent.order_draft`
  recalcula os preços pelo cardápio somando as opções escolhidas (itens ganham `options`).
- Agentes e MCP: `get_menu` e `get_menu_item` (grupo `ler`), `send_menu` (`responder_no_chat`) e
  `send_menu_item` (`midia`).
- Painel: aba **Cardápio** com categorias, cartões com foto, preço e esgotado, reordenar arrastando, editor com
  opções e prévia, importações e **Enviar cardápio para…**.

**Segurança**

- Textos do cardápio chegam ao atendente como dados neutralizados, num bloco próprio fora das informações da
  empresa. O texto do "Montar com IA" vai ao modelo entre marcadores aleatórios e a resposta é validada campo a
  campo; nada é salvo sem confirmação. Fotos por link seguem as regras de `media.url`.
- Cada mensagem de `send_menu` conta no limite de envios do agente. Chaves de API `read` só leem o cardápio.

## Catálogo e pedidos, fase 1 (M12b)

**Novo**

- [Catálogo e pedidos](/docs/guias/catalogo-e-pedidos/): ler o catálogo do WhatsApp Business do próprio número ou
  o catálogo público de outra empresa ([`GET /catalog`](/docs/api/catalogo/ler-o-catalogo/), produtos, coleções e
  a foto de cada produto), pela proxy da instância e com cache de 5 minutos. O catálogo vem do serviço GraphQL
  que o WhatsApp Web usa; o token e os `doc_id` ficam em `whatsapp_catalog` na config, para serem trocados sem
  nova versão quando a Meta mudar (a API responde `upstream_changed`).
- Mensagem `type: "product"` em `POST /messages` (`product_id`, `catalog_owner`, `body`, `footer`): cartão de
  produto com foto, pela fila de envio como qualquer mensagem. `catalog_link` (`https://wa.me/c/<número>`) para
  compartilhar o catálogo inteiro como texto.
- Pedidos: mensagens de pedido chegam como tipo `order` e no evento novo
  [`order.received`](/docs/eventos/order-received/); [`GET /orders`](/docs/api/catalogo/pedidos-recebidos/) e
  [`GET /orders/{order_id}`](/docs/api/catalogo/detalhes-de-um-pedido/) (pelo socket com `GetOrderDetails`, com o
  GraphQL como alternativa). O token do pedido fica no servidor. Mensagens de produto recebidas também ganham
  `product` nos dados.
- MCP: `get_catalog`, `get_product`, `send_product` e `get_order`. Agentes: `get_catalog` e `get_product` no grupo
  `ler`, `send_product` no grupo `midia` (com as regras de chat e os limites de envio da execução).
- Atendente IA: `knowledge_source: "catalog"` no perfil usa o catálogo do WhatsApp como lista de produtos (nome,
  preço, descrição e disponibilidade), atualizada a cada 10 minutos.
- Painel: aba **Catálogo** (só contas Business), com a grade de produtos, preço em reais, produtos ocultos ou em
  análise marcados, filtro por coleção, **Enviar para…** e os pedidos recentes.
- Perfil comercial: `clear` remove campos de forma explícita (`hours`, `email`, `websites`, `description`,
  `address`); é o único jeito de tirar o horário (validado no aparelho em 2026-10-21). Botão **Remover horário** no
  painel e `clear` em `update_business_profile`.
- O pin do mapa **não pode ser removido** por um aparelho conectado: `clear: ["location"]` responde
  `422 not_supported` sem chamar o WhatsApp. Num número Business real (2026-10-21), `<latitude/><longitude/>` e
  `<latitude></latitude><longitude></longitude>` voltaram `406 not-acceptable` e `<address/>` foi aceito mas manteve
  o pin; o WhatsApp Web também só oferece "editar o mapa no celular". Remova no app WhatsApp Business (Perfil
  comercial > Endereço); o painel mostra esse aviso ao lado da localização. Trocar latitude/longitude continua
  funcionando. `location_clear` e `business_location_clear` saíram.

**Quebra**

- `PATCH /business/profile`: `null` em `description`, `address`, `email`, `websites`, `hours` e `categories` agora
  responde `400 validation_failed` (antes era ignorado como "ausente"); em `latitude` e `longitude`,
  `422 not_supported`. Para remover, use `clear`; categorias não podem ser removidas (uma conta Business tem ao
  menos uma).

**Segurança**

- Nomes e descrições do catálogo chegam ao Atendente IA neutralizados, num bloco de dados separado das
  informações da empresa; a lista é atualizada em segundo plano (falhas só são tentadas de novo depois de 2 min).
- Pedidos ficam presos à conversa que os enviou; até 20 por conversa e 1000 por instância a cada 24 horas.
- Produtos ocultos, em análise ou recusados não são enviados. Agentes só leem e enviam o catálogo do próprio
  número. Leituras simultâneas iguais viram uma só chamada à Meta; fotos ficam 5 minutos em cache (60 por minuto).

**Ainda não (de propósito)**

- Criar, editar, ocultar ou apagar produtos e coleções: no WhatsApp isso exige um token de conta de anúncios por
  um fluxo não documentado, que pode pedir verificação por e-mail e tem código de bloqueio próprio. Os produtos
  continuam sendo criados no app WhatsApp Business (Ferramentas comerciais > Catálogo).

**Validado no aparelho em 2026-10-21** (número Business real): a leitura do catálogo pelo GraphQL (1 produto
aprovado lido), o cartão de produto (entregue e exibido no celular), o pedido recebido (com os detalhes pelo
GraphQL, porque o `GetOrderDetails` do socket não respondeu) e o `clear` do horário. O `clear` da localização não é
possível (veja acima).

## Perfil comercial editável (M12a)

**Novo**

- [`PATCH /v1/instances/{id}/business/profile`](/docs/api/business/editar-perfil-comercial/): edita o perfil do
  WhatsApp Business da conta (descrição, endereço, localização, e-mail, até 2 sites, até 3 categorias por id e o
  horário de cada dia: aberto com horário ou turnos, fechado, 24 horas ou com agendamento). Só os campos enviados
  mudam; a resposta é o perfil lido de novo do WhatsApp. Token admin ou da instância (chaves `lzk_` não podem);
  5 alterações por minuto e 50 por dia.
- `GET /business/profile` passa a trazer `description`, `websites`, `location`, `category_list` (com ids) e
  `business_hours` (a semana no formato do PATCH). Os campos antigos continuam iguais.
- Evento [`business.profile_updated`](/docs/eventos/business-profile-updated/), ferramenta MCP
  `update_business_profile` e a seção **Perfil da empresa** na aba Perfil do painel (só contas Business; o mapa só
  abre quando você clica, nada é enviado a serviços de geocodificação).
- Status: pronto, aguardando validação num número Business real. O envio segue o formato do WhatsApp Web e do
  Baileys (IQ `w:biz`, `business_profile v="3" mutation_type="delta"`); localização, categorias e remover os sites
  seguem o formato da leitura. Se o WhatsApp recusar algo, a resposta é `400 validation_failed` com o código do
  servidor.

## IA grátis (A2)

**Novo**

- [IA grátis](/docs/guias/ia-gratis/): o Atendente IA e os Agentes funcionam sem pagar e sem cartão. Presets
  `ollama` (no seu computador, `qwen2.5:7b` ou `llama3.2:3b`), `gemini_free` (Google Gemini) e `groq_free` (Groq),
  com o campo `preset` em `PUT /v1/instances/{id}/attendant`; o custo dessas opções aparece como **Grátis**
  (`free: true`).
- `GET /v1/ai/local` verifica o Ollama local (versão, modelos, memória e modelo recomendado) e `POST /v1/ai/local/pull`
  baixa um modelo com progresso (`GET /v1/ai/local/pull`). Só o Ollama em `127.0.0.1:11434`; só o admin.
- Painel: **Qual IA usar?** no Atendente IA e na aba Agentes, com instalação guiada do Ollama, download do modelo com
  barra de progresso e expectativas honestas de qualidade, velocidade e privacidade.
- Modelos locais pequenos: instruções mais curtas em português com a lista de ferramentas, chamadas de ferramenta mal
  formatadas são corrigidas ou recusadas sem executar nada, com uma nova chance para o modelo; na segunda, a execução
  termina com `invalid_tool_call`.

## Agentes LiteZap (A1)

**Novo**

- [Agentes](/docs/guias/agentes/): o Claude operando o WhatsApp com as ferramentas do LiteZap, num loop de *tool use*
  executado dentro do motor (as mesmas ferramentas e validações do MCP, sem rede).
- Permissões em grupos (`ler`, `responder_no_chat`, `enviar_outros_chats`, `etiquetas`, `grupos`, `midia`), aplicadas
  pelo motor: envio para outro chat sem permissão é recusado e há no máximo 3 destinatários distintos por execução.
- Modos `aprovacao` (padrão: cada escrita vira um rascunho que você aprova), `autonomo` e `sugerir`; limite de 8 passos
  por execução; custo e limites do Atendente IA em cada chamada ao modelo.
- Gatilho "mensagem nova" pelo hook de agente com `target: "agents"` (filtros por chat, etiqueta, palavra-chave e
  grupos opcionais), "Executar agora" e o simulador (escritas sempre faz de conta).
- API `/v1/instances/{id}/agents` (agentes, execuções, rascunhos, simulador) e eventos `agent.run.completed`,
  `agent.draft.created` e `agent.draft.decided`. Aba **Agentes** no painel.
- O provedor OpenAI-compatível ganha *function calling* para os agentes.

## App local: download e Conectar a IA

**Novo**

- Downloads prontos em cada versão: `LiteZap-macos.dmg` (universal, com ícone), `LiteZap-windows.zip` e
  `LiteZap-linux-{amd64,arm64}.tar.gz`. Veja [Baixar o app](/docs/baixar/).
- **Conectar a IA** no menu da bandeja e no painel: um clique configura Claude Desktop, Claude Code ou Cursor
  (`mcpServers.litezap`), com cópia `.litezap.bak` do original, escrita atômica e **Desconectar**.
- `litezap-app mcp`: servidor MCP stdio que usa o motor do app aberto, sem copiar token; com o app fechado, as
  ferramentas respondem `app_not_running`.

**Ainda não**: assinatura de código e notarização, instaladores `.msi`/`.deb`, atualização automática.

## Distribuição

**Novo**

- CI no GitHub Actions: `gofmt`, `go vet`, `go test -race`, código gerado em dia e build do site.
- Releases pelo GoReleaser: binários para Linux e macOS (amd64 e arm64) e Windows (amd64), com checksums.
- Imagem Docker multi-arquitetura em `ghcr.io/douglasdf99/litezap` (distroless, usuário não root).
- `scripts/install.sh`: instalador POSIX com verificação de sha256.
- Arquivos da comunidade: CONTRIBUTING, SECURITY, código de conduta, modelos de issue e PR.

## Marco 5b: Atendente LiteZap embutido

**Novo**

- Atendente IA dentro do motor (`target: "builtin"` no hook de agente), sem nada para hospedar. Herda fila, pausa por
  humano, loop guard, um chat por vez e a suspensão do operador.
- Provedores: Anthropic (padrão `claude-sonnet-5-5`) e qualquer servidor OpenAI-compatível (OpenAI, Ollama, LM
  Studio). Chaves cifradas com AES-256-GCM (`data/attendant_key`), nunca devolvidas; provedor e chaves só com token
  admin.
- Agentes com modos prontos e editáveis: `vendedor`, `pedido` (facilitador de pedido), `sdr` (qualificação e
  agendamento), `suporte` e `custom`; base de conhecimento (produtos, preços, horários, regras); simulador de conversa
  sem envio.
- Multimodal: transcrição de mensagens de voz, imagens (e PDF no Claude) para o modelo, resposta em voz
  (`voice_reply`) e mensagem de follow-up quando uma ligação é recusada (`call_followup`).
- Eventos `agent.lead`, `agent.appointment_requested` e `agent.order_draft`; contexto com a origem de cada mensagem
  (`contact`, `agent`, `human`, `automatic`, `api`, `history`).
- Tetos de gasto: `daily_call_cap` (padrão 300/dia), `chat_daily_call_cap` (40 por conversa/dia) e
  `simulate_daily_cap` (50/dia); `GET /attendant/usage` com chamadas, tokens e custo estimado por dia.
- Rotas `/v1/instances/{id}/attendant` (provedor, modos, agentes, ativar, simular, uso), ferramentas MCP
  `get_attendant` e `get_attendant_agent`, aba **Atendente IA** no painel. Veja
  [Atendimento com IA](/docs/guias/atendimento-ia/).

## Marco 11: plataforma e parceiros

**Novo**

- Página de pareamento hospedada (`POST/DELETE /v1/instances/{id}/pairing-links`, página `/pair/`): link assinado,
  até 10 minutos ou até o primeiro pareamento, sem expor o token; web component `<litezap-pair>`;
  `pairing_frame_ancestors` para embutir.
- Evento `instance.paired`; `instance.proxy_failed` ganha `error` (sem credenciais).
- `PATCH /v1/instances/{id}` (nome, etiquetas, allowlist de IP); `GET /v1/instances` com `state`, `tag`, `q`,
  `limit` e `cursor`; `POST /v1/instances/bulk`; `GET /v1/instances/{id}/usage`.
- Chaves de API `lzk_` com escopo `read`/`send`; allowlist de IP por instância, por chave e para admin/operador
  (`admin_allowed_ips`, `operator_allowed_ips`), com `trusted_proxies`.
- Métricas `litezap_queue_send_latency_seconds` e `litezap_worker_memory_rss_bytes` (Linux).
- Painel: editar instância (nome, etiquetas, IPs) e gerar/revogar links de pareamento.

`GET /v1/instances` sem `limit` responde como antes; a resposta das instâncias ganha `tags` e `ip_allowlist`.

## Marco 10: status, canais e etiquetas

**Novo**

- Status (stories): `POST /status` de texto, imagem ou vídeo pela fila (até 10 por dia), `GET /status` com os status
  recebidos e os nossos, `DELETE /status/{msg_id}`, `POST /status/{msg_id}/reply` (texto) e
  `GET /status/privacy`. Evento opt-in `status.received`.
- Canais: listar seguidos, criar, prévia por convite, metadados, seguir/deixar de seguir, silenciar, ler publicações,
  publicar (texto e mídia, só dono/admin, até 20 por dia) e reagir. Evento opt-in `channel.message`.
- Etiquetas do WhatsApp Business: listar, criar, renomear/recolorir, apagar, etiquetar conversas e mensagens, com
  evento `label.updated` (também para mudanças feitas no celular). `GET /business/profile` lê o perfil comercial.
- Cotas anti-ban para canais e etiquetas; ferramentas MCP de leitura mais `follow_channel` e `label_chat`; abas
  Status, Canais e Etiquetas no painel.

**Ainda não**: leitura automática de status, responder status com GIF ou figurinha, mídia de publicações de canal
recebidas, marcar publicações como vistas, eventos de agenda, produtos e catálogo.

## Marco 9: chats, contatos, perfil e privacidade

**Novo**

- Chats: `GET /chats/{jid}` (arquivado, fixado, silenciado, não lido), `PATCH /chats/{jid}` (arquivar, fixar,
  silenciar com `mute_seconds`, lido/não lido), `DELETE /chats/{jid}`, `PUT /messages/{msg_id}/star`. Evento
  `chat.updated`, inclusive para mudanças feitas no celular.
- Contatos: foto de perfil (`GET /contacts/{jid}/picture`), recado e perfil comercial
  (`GET /contacts/{jid}/profile`), bloquear, desbloquear e `GET /blocklist`, com cota própria de consultas (30/min,
  1000/dia).
- Perfil da conta: `GET`/`PATCH /profile` (nome, recado, plataforma, nome comercial) e foto (`GET`/`PUT`/`DELETE
  /profile/picture`, JPEG, PNG ou GIF reduzidos a 640x640).
- Privacidade: `GET`/`PATCH /privacy` (visto por último, online, foto, recado, quem adiciona em grupos, confirmação de
  leitura, chamadas, mensagens) e mensagens temporárias padrão.
- Leitura automática por instância (`auto_read`), em lote e com ritmo.
- Ferramentas MCP de contatos, bloqueio e `update_chat`; abas no painel.

**Ainda não**: limpar conversa, mensagens temporárias por chat, adicionar contato à agenda, listas de exceção da
privacidade. Pareamento por passkey não é suportado (use QR ou código).

## Marco 8: grupos e comunidades

**Novo**

- Grupos: listar, criar (até 10 participantes), metadados, alterar nome, descrição e configurações (`announce`,
  `locked`, `approval_required`, `admins_add_members`, `ephemeral_seconds`), trocar e remover foto, gerenciar
  participantes (`add`, `remove`, `promote`, `demote`) com resultado por pessoa, pedidos de entrada, link de convite
  e redefinição, sair, prévia e entrada por convite.
- Comunidades: listar, criar, metadados com os grupos, vincular e desvincular grupos.
- Evento `group.updated`, um por tipo de mudança (`kind`), inclusive as feitas no celular.
- Anti-ban: adições só de contatos conhecidos, 10 por lote, 10s entre lotes e cotas por hora e dia em
  `settings.groups` (persistidas); criação e entrada por link com cota diária.
- Nome dos chats de grupo no log preenchido e atualizado.
- Ferramentas MCP de grupos e aba **Grupos** no painel. Veja o [guia de grupos](/docs/guias/grupos/).

**Ainda não**: convite por mensagem para quem só aceita convite, desativar comunidade, proteção anti-golpe em grupos
de avisos.

## Marco 7: eventos e chamadas

**Novo**

- Eventos `chat.presence` (digitando, gravando, online/offline; opt-in com `presence.online`), `call.received`,
  `call.accepted`, `call.ended` (com `missed`) e `message.button_reply`.
- `POST /presence/subscribe` para acompanhar se um contato está online.
- Rejeição automática de chamadas individuais (`calls.auto_reject`, em menos de 3s) com mensagem pela fila
  (`calls.reject_message`, uma por contato a cada 10 min).
- Status `played` (áudio de voz, vídeo ou visualização única reproduzidos) em `message.status` e no log.
- Filtros de webhook: `filters.chat` (`direct`/`group`), `chats`, `exclude_chats`, `message_types` e `from_me`.

## Marco 5: hook de agente

**Novo**

- `PUT`/`GET`/`DELETE /v1/instances/{id}/agent-hook`: um `POST` assinado para o seu agente a cada mensagem elegível,
  com o contexto da conversa; a resposta (texto, mídia e ações `mark_read`, `react`, `pause`, `handoff`) passa pela
  fila normal.
- Pausa automática quando um humano responde pelo celular (`human_pause`), cancelando respostas ainda na fila; opção
  de pausar também em envios pela API.
- Uma chamada por vez por chat, loop guard (`max_replies_per_window`), liga/desliga por conversa
  (`PUT /chats/{jid}/agent`) e `GET /agent-hook/calls` com as últimas chamadas.
- Eventos `agent.failed` e `agent.handoff`; exemplo `examples/agent-claude`; aba **Atendente IA** no painel. Veja
  [hook de agente](/docs/mcp/hook-de-agente/).

## Nuvem C1: modo nuvem do motor

**Novo**

- Configuração inteira por ambiente (`LITEZAP_CONFIG_JSON` e variáveis das opções novas).
- Token de operador (`operator_token`) com settings travadas (`locked_settings`), limite de instâncias
  (`max_instances`), proxy obrigatório (`require_proxy`), webhooks do operador e envio suspenso global ou por
  instância (`PUT /v1/operator/sending`).
- Uso diário persistido, `/metrics` no formato Prometheus, endpoint de ops (`ops_listen_addr`), evento
  `instance.proxy_failed` e desligamento gracioso (`shutdown_timeout`).
- Login de um clique emitido pelo operador e exportação lógica (`POST /v1/export`).
- Logs em JSON (`LITEZAP_LOG_FORMAT=json`), watchdog de conexão (`zombie_timeout`) e retenção de mensagens e mídia
  (`message_retention_days`, `media_retention_days`).

O control plane da nuvem (cadastro, provisionamento por cliente e cobrança) ainda está em construção.

## Marco 6: mensagens completas

**Novo**

- Tipos `sticker`, `gif`, `ptv`, `location`, `contact` (1 a 10 cartões) e `poll`, mais as ações `edit` (até 15
  min), `revoke` (até 48h), `pin` e `forward`. Veja [tipos de mensagem](/docs/api/mensagens/tipos/).
- Campos transversais: `reply_to`, `mentions` (inclusive `["all"]` em grupos), `link_preview` (com filtro SSRF e
  limite de 5s), `delay_ms` e `typing_ms`.
- Recebimento de edições, apagadas e votos: eventos `message.edited`, `message.revoked` e `message.poll_vote`; o log
  guarda `edited_at` e `revoked_at`.
- Fila visível: `GET /queue`, `DELETE /queue/{msg_id}` e `DELETE /queue`.
- Ferramentas MCP `send_reaction`, `send_location`, `send_contact`, `edit_message` e `delete_message`;
  `send_message` e `send_media` com `reply_to`, menções e os tipos novos.

**Ainda não**: votar em enquete pela API; fixar não emite evento.

## App local v1

**Novo**

- Painel web embutido em `/app/` (`/` redireciona): instâncias com estado ao vivo, pareamento por QR e código,
  conversas e envio com explicação da fila, configurações, webhooks com entregas e reenvio, eventos ao vivo.
- App de bandeja `litezap-app`: motor no próprio processo, pasta de dados por usuário, login no painel em um clique
  (código de uso único, só em loopback), Abrir pasta de dados, Ver log e Sair gracioso.
- Configuração `disable_dashboard` (`LITEZAP_DISABLE_DASHBOARD`).

**Ainda não**: instaladores, assinatura de código, atualização automática, iniciar com o sistema, túnel.

## Marco 4: MCP (v0.4)

**Novo**

- Servidor MCP em `/mcp` (streamable HTTP, *stateless*, com os mesmos tokens da API) e ponte stdio `litezap mcp`
  (`LITEZAP_URL`, `LITEZAP_TOKEN`).
- 13 ferramentas: `list_instances`, `get_instance_status`, `get_qr`, `send_message`, `send_media`,
  `get_message_status`, `list_chats`, `get_messages`, `mark_read`, `list_contacts`, `get_contact`, `check_numbers`,
  `wait_for_events`.
- `GET /v1/me`: escopo do token (`admin`, `instance_id`).
- Proteção contra DNS rebinding no `/mcp` em loopback, com exceção para o host de `public_url`.

## Marco 3: eventos (v0.3)

**Novo**

- Barramento de eventos com envelope versionado (`version: 1`).
- Eventos `instance.status`, `instance.qr`, `instance.pair_code`, `message.received`, `message.sent` (com
  `source: api|device`), `message.status`, `message.reaction` e `history.synced`.
- Webhooks: `POST/GET /v1/webhooks`, `GET/PATCH/DELETE /v1/webhooks/{id}`, globais ou por instância, com filtros
  (`message.*`, `*`), assinatura `X-LiteZap-Signature` (HMAC-SHA256 com timestamp), outbox persistente, retry com
  backoff por até 24h, backoff por endpoint, dead-letter com `GET .../deliveries` e `POST .../retry`, retenção
  configurável.
- SSE: `GET /v1/instances/{id}/events` e `GET /v1/events` (admin), com filtro `?types=` e heartbeat.
- Configuração: `webhook_allow_private_urls`, `webhook_retention_days`, `webhook_failed_retention_days`.

**Limitações conhecidas**: `message.revoked`, `chat.presence` e `group.updated` ainda não são emitidos; SSE sem replay.

## Marco 2: mensagens (v0.2)

**Novo**

- Envio pela fila persistente: `text`, `image`, `video`, `audio` (com voz), `document`, `reaction`; mídia por URL
  (com proteção SSRF) ou base64.
- `?wait=true` (modo síncrono), idempotência por `client_message_id`, `expires_after`, até 5 tentativas.
- Ritmo (intervalo, variação, teto por minuto, "digitando"), rampa para números novos, janela de envio para contatos
  frios, carência pós-conexão, verificação do número antes do primeiro envio.
- Recebimento e log local: `GET /chats`, `GET /chats/{jid}/messages` com cursor, `POST /chats/{jid}/read`, history
  sync.
- Contatos e `POST /check-numbers` (cache e cotas).
- Mídia recebida sob demanda e URLs assinadas (`?signed=1`, 1 hora).
- Configurações por instância (`GET/PUT /settings`) e proxy HTTP/HTTPS/SOCKS5 por instância.

## Marco 1: esqueleto (v0.1)

**Novo**

- Binário único (`serve`, `worker`, `version`), configuração por YAML e `LITEZAP_*`, SQLite com migrações.
- Supervisor com um worker por número: heartbeat, restart com backoff, circuit breaker, workers que encerram com o
  supervisor, limpeza de órfãos.
- Instâncias: criar, listar, consultar, iniciar, parar, reiniciar, logout, apagar, rotacionar token.
- Pareamento por QR (JSON ou PNG) e por código no telefone.
- Tokens admin e de instância; `/healthz` e `/readyz`.
