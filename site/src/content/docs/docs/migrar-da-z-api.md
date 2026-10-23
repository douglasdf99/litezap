---
title: Migrar da Z-API
description: Mapeamento recurso a recurso entre a Z-API e o LiteZap, diferenças de autenticação e de webhooks, e o que ainda falta.
---

Esta página ajuda quem já integra com a Z-API a levar a integração para o LiteZap. Os nomes à esquerda são os
recursos como a Z-API os apresenta na documentação dela; à direita, o equivalente no LiteZap. Z-API é marca da
respectiva empresa; o LiteZap é um projeto independente.

## Diferenças que mudam o seu código

| Tema | Z-API | LiteZap |
|---|---|---|
| Credencial | ID e token da instância na URL, mais token de segurança da conta | `Authorization: Bearer <token>`: token da instância (`lzi_...`) ou token admin (`lza_...`). Nada de token na URL. |
| URL | Por instância | `/v1/instances/{id}/...` |
| Envio | Um endpoint por tipo de mensagem | Um endpoint, `POST /v1/instances/{id}/messages`, com `type` (`text`, `image`, `video`, `audio`, `document`, `sticker`, `gif`, `ptv`, `location`, `contact`, `poll`, `reaction`, `edit`, `revoke`, `pin`, `forward`). Veja [tipos](/docs/api/mensagens/tipos/). |
| Resposta do envio | Id da mensagem | `202` com o item da fila; com `?wait=true`, `200` com `wa_message_id`. Idempotência por `client_message_id`. |
| Fila | Fila com intervalo configurável | Fila persistente por instância com ritmo, rampa para números novos, janela de horário e verificação de número. |
| Webhooks | Uma URL por tipo de evento | Vários webhooks (globais ou por instância), cada um com filtros (`message.*`, `*`), assinatura HMAC, retry por 24h e dead-letter. |
| Payload | Formato próprio por evento | Envelope único `{id, type, version, instance_id, timestamp, data}`. Veja [eventos](/docs/eventos/). |
| Tempo real | (via webhook) | Também por SSE, sem servidor público. |
| Erros | Variados | Envelope único `{error: {code, message, retryable}}`. Veja [erros](/docs/api/erros/). |
| Hospedagem | Nuvem da Z-API | Seu PC ou seu servidor; nuvem LiteZap em construção. Licença Apache-2.0. |

## Mapeamento

Legenda: <span class="lz-badge lz-badge--ok">Disponível</span> já funciona; <span class="lz-badge lz-badge--soon">Parcial</span> cobre parte do recurso; <span class="lz-badge lz-badge--soon">Ainda não</span> viável, ainda não feito;
<span class="lz-badge lz-badge--exp">Exp</span> experimental, fora da trilha principal; <span class="lz-badge lz-badge--no">Avaliar</span> depende de engenharia reversa ou tem valor baixo;
<span class="lz-badge lz-badge--no">Fora</span> não planejado.

Na matriz completa (159 recursos da Z-API, revisada com o código atual), o LiteZap tem **115 prontos**, **3
parciais**, **23 faltando** (a maioria administração avançada de canais, catálogo e mensagens interativas) e **18
fora do escopo** de propósito.

### Instância e conexão

| Z-API | LiteZap | |
|---|---|---|
| Pegar QRCode / QRCode imagem | [`GET /qr`](/docs/api/pareamento/obter-qr-code/) (JSON ou `?format=png`) e evento [`instance.qr`](/docs/eventos/instance-qr/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Conectar com telefone | [`POST /pair-phone`](/docs/api/pareamento/parear-por-codigo/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status da instância / Dados da instância | [`GET /v1/instances/{id}`](/docs/api/instancias/consultar-instancia/) e [`instance.status`](/docs/eventos/instance-status/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Reiniciar | [`POST /restart`](/docs/api/instancias/reiniciar-worker/) (também `start` e `stop`) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Desconectar | [`POST /logout`](/docs/api/instancias/desconectar-numero/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Dados do celular | [`GET /profile`](/docs/api/perfil-e-privacidade/perfil-da-conta/) (plataforma, nome, nome comercial, recado, foto) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Renomear instância | [`PATCH /v1/instances/{id}`](/docs/api/instancias/editar-instancia/) (também etiquetas e allowlist de IP) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ler mensagens automaticamente | setting `auto_read` em [`PUT /settings`](/docs/api/configuracoes/alterar-configuracoes/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ler status automaticamente | — (os status chegam no log; marcar como visto não) | <span class="lz-badge lz-badge--soon">Ainda não</span> |
| Rejeitar chamadas / mensagem de rejeição | setting `calls` (`auto_reject`, `reject_message`) em [`PUT /settings`](/docs/api/configuracoes/alterar-configuracoes/) e [`call.received`](/docs/eventos/call-received/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Desafio de passkey no pareamento | — (exige asserção WebAuthn do site do WhatsApp; use QR ou código) | <span class="lz-badge lz-badge--no">Fora</span> |
| Registro como aparelho principal (Mobile) | — | <span class="lz-badge lz-badge--no">Fora</span> |

### Mensagens

| Z-API | LiteZap | |
|---|---|---|
| Enviar texto | [`POST /messages`](/docs/api/mensagens/enviar-mensagem/) `type: text` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Enviar imagem / áudio / vídeo / documento | `type: image` / `audio` (`ptt: true` = voz) / `video` / `document` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Enviar / remover reação | `type: reaction` (`emoji: ""` remove) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ler mensagens | [`POST /chats/{jid}/read`](/docs/api/chats/marcar-chat-como-lido/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Atraso e "digitando" | por instância (`min_interval_ms`, `typing_simulation`) e por mensagem (`delay_ms`, `typing_ms`) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Figurinha, GIF, PTV, localização, contato(s) | `type: sticker` / `gif` / `ptv` / `location` / `contact` ([tipos](/docs/api/mensagens/tipos/)) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Responder citando, menções | `reply_to`, `mentions` (inclusive `["all"]` em grupos) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Editar, apagar, fixar, encaminhar | `type: edit` / `revoke` / `pin` / `forward`, com [`message.edited`](/docs/eventos/message-edited/) e [`message.revoked`](/docs/eventos/message-revoked/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Link com prévia | `link_preview` (`auto` ou cartão pronto) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Enquete e votos | `type: poll` e [`message.poll_vote`](/docs/eventos/message-poll-vote/); votar pela API não | <span class="lz-badge lz-badge--soon">Parcial</span> |
| Eventos (criar, editar, responder) | `type: "event"` em [`POST /messages`](/docs/api/mensagens/tipos/), [`PATCH /messages/{id}`](/docs/api/mensagens/editar-ou-cancelar-evento/), [`POST /messages/{id}/event-response`](/docs/api/mensagens/responder-evento/) e [`message.event_response`](/docs/eventos/message-event-response/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Enviar produto do catálogo | `type: product` ([tipos](/docs/api/mensagens/tipos/)); catálogo inteiro pelo `catalog_link` | <span class="lz-badge lz-badge--soon">Disponível</span> (aguardando validação num número Business real) |
| Pedidos | [`order.received`](/docs/eventos/order-received/) e [`GET /orders/{order_id}`](/docs/api/catalogo/detalhes-de-um-pedido/) (só leitura) | <span class="lz-badge lz-badge--soon">Parcial</span> |
| Botões, listas, OTP/PIX, carrossel | — (respostas a eles chegam em [`message.button_reply`](/docs/eventos/message-button-reply/)) | <span class="lz-badge lz-badge--exp">Exp</span> |

### Fila

| Z-API | LiteZap | |
|---|---|---|
| Configurar fila (intervalos) | [`PUT /settings`](/docs/api/configuracoes/alterar-configuracoes/) (ritmo, rampa, janela) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status de uma mensagem na fila | [`GET /messages/{id}`](/docs/api/mensagens/consultar-mensagem/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Fila | [`GET /queue`](/docs/api/mensagens/listar-a-fila/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Apagar fila / Apagar mensagem da fila | [`DELETE /queue`](/docs/api/mensagens/esvaziar-a-fila/) e [`DELETE /queue/{msg_id}`](/docs/api/mensagens/cancelar-item-da-fila/) | <span class="lz-badge lz-badge--ok">Disponível</span> |

### Chats e contatos

| Z-API | LiteZap | |
|---|---|---|
| Pegar chats | [`GET /chats`](/docs/api/chats/listar-chats/) (do log local, com cursor) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| (histórico do chat) | [`GET /chats/{jid}/messages`](/docs/api/chats/historico-do-chat/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Metadata do chat | [`GET /chats/{jid}`](/docs/api/chats/consultar-chat/) (arquivado, fixado, silenciado, não lido) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Arquivar, fixar, silenciar, ler/não lido | [`PATCH /chats/{jid}`](/docs/api/chats/arquivar-fixar-silenciar-ou-marcar-como-nao-lido/) e [`chat.updated`](/docs/eventos/chat-updated/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Deletar chat | [`DELETE /chats/{jid}`](/docs/api/chats/apagar-chat/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Limpar chat | [`POST /chats/{jid}/clear`](/docs/api/chats/limpar-conversa/) (destrutivo; `keep_starred`) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Expiração do chat | [`PATCH /chats/{jid}`](/docs/api/chats/arquivar-fixar-silenciar-ou-marcar-como-nao-lido/) com `disappearing` (`off`, `24h`, `7d`, `90d`); padrão da conta em [`PATCH /privacy`](/docs/api/perfil-e-privacidade/mudar-a-privacidade/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Pegar contatos / Metadata do contato | [`GET /contacts`](/docs/api/contatos/listar-contatos/), [`GET /contacts/{jid}`](/docs/api/contatos/consultar-contato/), [recado e perfil comercial](/docs/api/contatos/recado-e-perfil-comercial-do-contato/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Pegar imagem do contato | [`GET /contacts/{jid}/picture`](/docs/api/contatos/foto-de-perfil-do-contato/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Bloquear contato | [`POST /contacts/{jid}/block`](/docs/api/contatos/bloquear-contato/), `unblock` e [`GET /blocklist`](/docs/api/contatos/listar-bloqueados/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Adicionar / remover contato na agenda | [`POST /contacts`](/docs/api/contatos/salvar-contato-na-agenda/) e [`DELETE /contacts/{jid}`](/docs/api/contatos/remover-contato-da-agenda/) (sincroniza com a agenda do celular) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Número com WhatsApp / Validação em lote | [`POST /check-numbers`](/docs/api/numeros/verificar-numeros/) (até 10 por chamada, cache e cota) | <span class="lz-badge lz-badge--ok">Disponível</span> |

### Grupos e comunidades

| Z-API | LiteZap | |
|---|---|---|
| Criar, metadados, nome, descrição, foto, configurações | [Grupos](/docs/api/grupos/) (`POST /groups`, `GET`/`PATCH /groups/{jid}`, foto) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Participantes e admins | [`POST /groups/{jid}/participants`](/docs/api/grupos/gerenciar-participantes/) (add, remove, promote, demote) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Pedidos de entrada | [`GET`/`POST /groups/{jid}/requests`](/docs/api/grupos/listar-pedidos-de-entrada/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Link de convite, redefinir, entrar, sair | [convite](/docs/api/grupos/link-de-convite/), [entrar](/docs/api/grupos/entrar-por-convite/), [sair](/docs/api/grupos/sair-do-grupo/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Eventos de grupo | [`group.updated`](/docs/eventos/group-updated/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Comunidades (criar, listar, vincular, metadados) | [Comunidades](/docs/api/comunidades/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Desativar comunidade | — | <span class="lz-badge lz-badge--no">Avaliar</span> |
| Proteção anti-golpe em grupos de avisos | — | <span class="lz-badge lz-badge--soon">Ainda não</span> |

### Perfil e privacidade

| Z-API | LiteZap | |
|---|---|---|
| Nome, recado e foto do perfil | [`PATCH /profile`](/docs/api/perfil-e-privacidade/mudar-nome-e-recado/) e [foto](/docs/api/perfil-e-privacidade/trocar-a-foto-da-conta/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Privacidade (visto por último, foto, recado, online, confirmação de leitura, grupos, chamadas) | [`GET`/`PATCH /privacy`](/docs/api/perfil-e-privacidade/mudar-a-privacidade/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Mensagens temporárias padrão | `default_disappearing_seconds` em `PATCH /privacy` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Contatos não permitidos (exceções) | — | <span class="lz-badge lz-badge--soon">Ainda não</span> |

### Status, canais e Business

| Z-API | LiteZap | |
|---|---|---|
| Status (texto, imagem, vídeo) | [Status](/docs/api/status/) (`POST /status`) e [`status.received`](/docs/eventos/status-received/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Responder status (texto, GIF, figurinha) | [`POST /status/{msg_id}/reply`](/docs/api/status/responder-status/) com `type` `text`, `gif` ou `sticker` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Canais (criar, seguir, silenciar, metadados, publicar) | [Canais](/docs/api/canais/) e [`channel.message`](/docs/eventos/channel-message/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Reagir e ler publicações de canal | reagir e listar publicações; marcar como visto não | <span class="lz-badge lz-badge--soon">Parcial</span> |
| Administração avançada de canais (editar, apagar, assinantes, admins) | — | <span class="lz-badge lz-badge--no">Avaliar</span> |
| Etiquetas e perfil comercial (leitura) | [Etiquetas e Business](/docs/api/business/) e [`label.updated`](/docs/eventos/label-updated/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Editar perfil comercial (descrição, endereço, e-mail, sites, horário, categorias) | [`PATCH /business/profile`](/docs/api/business/editar-perfil-comercial/) e [`business.profile_updated`](/docs/eventos/business-profile-updated/) | <span class="lz-badge lz-badge--ok">Disponível</span> (aguardando validação num número Business real) |
| Catálogo e coleções (leitura) | [Catálogo e pedidos](/docs/api/catalogo/) | <span class="lz-badge lz-badge--soon">Disponível</span> (aguardando validação num número Business real) |
| Criar, editar e apagar produtos e coleções | — (exige o fluxo de token de anúncios do WhatsApp; risco para o número) | <span class="lz-badge lz-badge--no">Avaliar</span> |
| Listas de transmissão | — (envio em massa é gatilho de banimento) | <span class="lz-badge lz-badge--no">Fora</span> |
| Chamadas de voz / SIP | — (não suportado pela biblioteca de protocolo) | <span class="lz-badge lz-badge--no">Fora</span> |

### Webhooks

| Z-API | LiteZap | |
|---|---|---|
| Ao receber | [`message.received`](/docs/eventos/message-received/) e [`message.reaction`](/docs/eventos/message-reaction/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ao enviar / Notificar enviadas por mim | [`message.sent`](/docs/eventos/message-sent/) com `source: api` ou `device` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status da mensagem | [`message.status`](/docs/eventos/message-status/) (`sent`, `delivered`, `read`, `played`, `failed`) e [`message.revoked`](/docs/eventos/message-revoked/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ao conectar / Ao desconectar | [`instance.status`](/docs/eventos/instance-status/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Atualizar webhooks | [`POST /v1/webhooks`](/docs/api/webhooks/criar-webhook/), [`PATCH`](/docs/api/webhooks/atualizar-webhook/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status do chat (presença) | [`chat.presence`](/docs/eventos/chat-presence/) (opt-in) e [`POST /presence/subscribe`](/docs/api/sse/assinar-presenca-de-um-contato/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Filtros por tipo de mensagem | `filters` no webhook (`chat`, `chats`, `exclude_chats`, `message_types`, `from_me`) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Falha de proxy | [`instance.proxy_failed`](/docs/eventos/instance-proxy-failed/) (com o erro do worker, sem credenciais) | <span class="lz-badge lz-badge--ok">Disponível</span> |

### Parceiros e IA

| Z-API | LiteZap | |
|---|---|---|
| Criar, listar e apagar instâncias | [`POST`](/docs/api/instancias/criar-instancia/), [`GET`](/docs/api/instancias/listar-instancias/), [`DELETE /v1/instances`](/docs/api/instancias/apagar-instancia/) (token admin) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Configurar proxy | `proxy_url` em [`PUT /settings`](/docs/api/configuracoes/alterar-configuracoes/) (admin) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Assinaturas e cobrança | — (é da nuvem, não do motor) | <span class="lz-badge lz-badge--no">Fora</span> |
| Componente de conexão embutível / token do SDK | [Link de pareamento hospedado](/docs/api/plataforma/gerar-link-de-pareamento/) + `<litezap-pair>` e evento [`instance.paired`](/docs/eventos/instance-paired/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Apagar instâncias em lote | [`POST /v1/instances/bulk`](/docs/api/plataforma/operacao-em-lote/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Restrição de chamadas por IP | `ip_allowlist` na instância e nas chaves; `admin_allowed_ips` e `trusted_proxies` no servidor | <span class="lz-badge lz-badge--ok">Disponível</span> |
| MCP Server | [`/mcp` e `litezap mcp`](/docs/mcp/), 40 ferramentas | <span class="lz-badge lz-badge--ok">Disponível</span> |
| (atendente que responde sozinho) | [Atendente IA](/docs/guias/atendimento-ia/) embutido ou [hook de agente](/docs/mcp/hook-de-agente/) | <span class="lz-badge lz-badge--ok">Disponível</span> |

## Roteiro de migração

1. **Suba o LiteZap** ([quickstart](/docs/quickstart/) ou [self-hosted](/docs/self-hosted/)) e crie uma instância
   por número.
2. **Pareie de novo.** Sessões não são transferíveis entre provedores: cada número escaneia um QR novo. Desconecte o
   número no provedor antigo antes, para não ter duas sessões disputando as mesmas mensagens.
3. **Troque a autenticação** para `Authorization: Bearer`.
4. **Troque os envios** pelo endpoint único com `type`, e passe a mandar `client_message_id` (o id do seu pedido,
   ticket etc.) para ter idempotência.
5. **Cadastre um webhook** com os tipos que você usava e **verifique a assinatura**
   ([como](/docs/eventos/assinatura/)). Adapte o parser ao envelope único.
6. **Revise o volume.** O LiteZap vem com ritmo, rampa e janela ligados. Se a sua operação disparava muito para
   contatos frios, leia as [boas práticas](/docs/boas-praticas/) antes de afrouxar os limites.
