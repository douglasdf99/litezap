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
| `search_messages` | Busca full-text no histórico guardado (texto, legendas, nomes de arquivo), com filtros de chat, remetente e data; só leitura | [Buscar mensagens](/docs/api/mensagens/buscar-mensagens/) |
| `update_chat` | Arquivar, fixar ou silenciar um chat; `disappearing` muda as mensagens temporárias (visto pelo contato ou pelo grupo; num chat individual passa pela fila) | [Chats](/docs/api/chats/) |
| `clear_chat` | Limpa as mensagens de um chat no WhatsApp da conta e no histórico do LiteZap (a conversa fica). **Destrutiva**: só quando o usuário pedir, para o chat que ele indicar | [Limpar conversa](/docs/api/chats/limpar-conversa/) |
| `list_status_updates` | Status recebidos dos contatos e os nossos (publicar não é ferramenta) | [Status](/docs/api/status/) |
| `list_channels` · `get_channel` · `get_channel_messages` | Canais seguidos, um canal, publicações recentes | [Canais](/docs/api/canais/) |
| `follow_channel` | Segue ou deixa de seguir um canal | [Canais](/docs/api/canais/) |
| `list_labels` · `label_chat` · `get_business_profile` | Etiquetas e suas conversas; etiquetar um chat; perfil comercial | [Etiquetas e Business](/docs/api/business/) |
| `update_business_profile` | Edita o perfil comercial (descrição, endereço, localização, e-mail, sites, categorias, horário); `clear` remove campos (o pin do mapa não: só no app WhatsApp Business). Visível para todos os clientes: só quando o usuário pedir, com os valores dele; 5 por minuto | [Editar perfil comercial](/docs/api/business/editar-perfil-comercial/) |
| `get_catalog` · `get_product` | Catálogo do WhatsApp Business (o próprio ou de outra empresa) e um produto; cache de 5 minutos | [Catálogo e pedidos](/docs/api/catalogo/) |
| `send_product` | Envia o cartão de um produto do catálogo pela fila de envio. Só quando o usuário pedir, para quem ele indicar, com um `product_id` de `get_catalog` | [Tipos de mensagem](/docs/api/mensagens/tipos/) |
| `get_order` | Itens e total de um pedido recebido (do evento `order.received`); aceitar ou recusar é no app | [Detalhes de um pedido](/docs/api/catalogo/detalhes-de-um-pedido/) |
| `get_attendant` · `get_attendant_agent` | Leitura do atendente IA embutido (provedor e modelo, sem chaves) e de um agente | [Atendente IA](/docs/api/atendente-ia/) |

## O que não existe por MCP (de propósito)

Criar, apagar ou rotacionar instâncias, mudar settings, proxy ou `unsafe_pacing`, e gerenciar webhooks. Essas
operações ficam na API REST e no painel.
