---
title: Migrar da Z-API
description: Mapeamento recurso a recurso entre a Z-API e o LiteZap, diferenças de autenticação e de webhooks, e o que ainda não existe.
---

Esta página ajuda quem já integra com a Z-API a levar a integração para o LiteZap. Os nomes à esquerda são os
recursos como a Z-API os apresenta na documentação dela; à direita, o equivalente no LiteZap. Z-API é marca da
respectiva empresa; o LiteZap é um projeto independente.

## Diferenças que mudam o seu código

| Tema | Z-API | LiteZap |
|---|---|---|
| Credencial | ID e token da instância na URL, mais token de segurança da conta | `Authorization: Bearer <token>`: token da instância (`lzi_...`) ou token admin (`lza_...`). Nada de token na URL. |
| URL | Por instância | `/v1/instances/{id}/...` |
| Envio | Um endpoint por tipo de mensagem | Um endpoint, `POST /v1/instances/{id}/messages`, com `type` (`text`, `image`, `video`, `audio`, `document`, `reaction`). |
| Resposta do envio | Id da mensagem | `202` com o item da fila; com `?wait=true`, `200` com `wa_message_id`. Idempotência por `client_message_id`. |
| Fila | Fila com intervalo configurável | Fila persistente por instância com ritmo, rampa para números novos, janela de horário e verificação de número. |
| Webhooks | Uma URL por tipo de evento | Vários webhooks (globais ou por instância), cada um com filtros (`message.*`, `*`), assinatura HMAC, retry por 24h e dead-letter. |
| Payload | Formato próprio por evento | Envelope único `{id, type, version, instance_id, timestamp, data}`. Veja [eventos](/docs/eventos/). |
| Tempo real | (via webhook) | Também por SSE, sem servidor público. |
| Erros | Variados | Envelope único `{error: {code, message, retryable}}`. Veja [erros](/docs/api/erros/). |
| Hospedagem | Nuvem da Z-API | Seu PC, seu servidor ou nuvem LiteZap (em breve). Open source. |

## Mapeamento

Legenda: <span class="lz-badge lz-badge--ok">Disponível</span> já funciona;
<span class="lz-badge lz-badge--soon">Mx</span> planejado no marco indicado;
<span class="lz-badge lz-badge--exp">Exp</span> experimental;
<span class="lz-badge lz-badge--no">Fora</span> não planejado.

### Instância e conexão

| Z-API | LiteZap | |
|---|---|---|
| Pegar QRCode / QRCode imagem | [`GET /qr`](/docs/api/pareamento/obter-qr-code/) (JSON ou `?format=png`) e evento [`instance.qr`](/docs/eventos/instance-qr/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Conectar com telefone | [`POST /pair-phone`](/docs/api/pareamento/parear-por-codigo/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status da instância / Dados da instância | [`GET /v1/instances/{id}`](/docs/api/instancias/consultar-instancia/) e [`instance.status`](/docs/eventos/instance-status/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Reiniciar | [`POST /restart`](/docs/api/instancias/reiniciar-worker/) (também `start` e `stop`) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Desconectar | [`POST /logout`](/docs/api/instancias/desconectar-numero/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Dados do celular | parcial em `GET /v1/instances/{id}` (`phone`) | <span class="lz-badge lz-badge--soon">M9</span> |
| Renomear instância | [`PATCH /v1/instances/{id}`](/docs/api/instancias/editar-instancia/) (também etiquetas e allowlist de IP) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ler mensagens automaticamente | — (marcar como lido manual existe) | <span class="lz-badge lz-badge--soon">M9</span> |
| Rejeitar chamadas / mensagem de rejeição | — | <span class="lz-badge lz-badge--soon">M7</span> |
| Desafio de passkey no pareamento | — | <span class="lz-badge lz-badge--soon">M9</span> |
| Registro como aparelho principal (Mobile) | — | <span class="lz-badge lz-badge--no">Fora</span> |

### Mensagens

| Z-API | LiteZap | |
|---|---|---|
| Enviar texto | [`POST /messages`](/docs/api/mensagens/enviar-mensagem/) `type: text` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Enviar imagem / áudio / vídeo / documento | `type: image` / `audio` (`ptt: true` = voz) / `video` / `document` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Enviar / remover reação | `type: reaction` (`emoji: ""` remove) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ler mensagens | [`POST /chats/{jid}/read`](/docs/api/chats/marcar-chat-como-lido/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Atraso e "digitando" | por instância (`min_interval_ms`, `typing_simulation`); por mensagem no M6 | <span class="lz-badge lz-badge--soon">M6</span> |
| Figurinha, GIF, PTV, localização, contato(s) | — (recebidos e registrados) | <span class="lz-badge lz-badge--soon">M6</span> |
| Responder citando, menções | — | <span class="lz-badge lz-badge--soon">M6</span> |
| Editar, apagar, fixar, encaminhar | — | <span class="lz-badge lz-badge--soon">M6</span> |
| Link com prévia | — | <span class="lz-badge lz-badge--soon">M6</span> |
| Enquete e votos | — | <span class="lz-badge lz-badge--soon">M6</span> |
| Eventos (agenda) | — | <span class="lz-badge lz-badge--soon">M10</span> |
| Botões, listas, OTP/PIX, carrossel | — | <span class="lz-badge lz-badge--exp">Exp</span> |

### Fila

| Z-API | LiteZap | |
|---|---|---|
| Configurar fila (intervalos) | [`PUT /settings`](/docs/api/configuracoes/alterar-configuracoes/) (ritmo, rampa, janela) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status de uma mensagem na fila | [`GET /messages/{id}`](/docs/api/mensagens/consultar-mensagem/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Listar fila, apagar item, apagar fila | — | <span class="lz-badge lz-badge--soon">M6</span> |

### Chats e contatos

| Z-API | LiteZap | |
|---|---|---|
| Pegar chats | [`GET /chats`](/docs/api/chats/listar-chats/) (do log local, com cursor) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| (histórico do chat) | [`GET /chats/{jid}/messages`](/docs/api/chats/historico-do-chat/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Pegar contatos / Metadata do contato | [`GET /contacts`](/docs/api/contatos/listar-contatos/), [`GET /contacts/{jid}`](/docs/api/contatos/consultar-contato/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Número com WhatsApp / Validação em lote | [`POST /check-numbers`](/docs/api/numeros/verificar-numeros/) (até 10 por chamada, cache e cota) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Arquivar, fixar, silenciar, limpar, apagar chat, expiração | — | <span class="lz-badge lz-badge--soon">M9</span> |
| Foto do contato, bloquear, adicionar/remover contato | — | <span class="lz-badge lz-badge--soon">M9</span> |

### Grupos, comunidades, perfil e privacidade

| Z-API | LiteZap | |
|---|---|---|
| Grupos (criar, participantes, admins, convite, configurações, metadados) | — | <span class="lz-badge lz-badge--soon">M8</span> |
| Comunidades | — | <span class="lz-badge lz-badge--soon">M8</span> |
| Nome, recado e foto do perfil | — | <span class="lz-badge lz-badge--soon">M9</span> |
| Privacidade (visto por último, foto, recado, online, confirmação de leitura) | — | <span class="lz-badge lz-badge--soon">M9</span> |

### Status, canais e Business

| Z-API | LiteZap | |
|---|---|---|
| Status (texto, imagem, vídeo, responder) | — | <span class="lz-badge lz-badge--soon">M10</span> |
| Canais (criar, seguir, silenciar, publicar) | — | <span class="lz-badge lz-badge--soon">M10</span> |
| Etiquetas e perfil comercial (leitura) | — | <span class="lz-badge lz-badge--soon">M10</span> |
| Catálogo, coleções, pedidos | — | <span class="lz-badge lz-badge--no">Avaliar</span> |
| Listas de transmissão | — (envio em massa é gatilho de banimento) | <span class="lz-badge lz-badge--no">Fora</span> |
| Chamadas de voz / SIP | — (não suportado pela biblioteca de protocolo) | <span class="lz-badge lz-badge--no">Fora</span> |

### Webhooks

| Z-API | LiteZap | |
|---|---|---|
| Ao receber | [`message.received`](/docs/eventos/message-received/) e [`message.reaction`](/docs/eventos/message-reaction/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ao enviar / Notificar enviadas por mim | [`message.sent`](/docs/eventos/message-sent/) com `source: api` ou `device` | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status da mensagem | [`message.status`](/docs/eventos/message-status/) (`sent`, `delivered`, `read`, `failed`) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Ao conectar / Ao desconectar | [`instance.status`](/docs/eventos/instance-status/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Atualizar webhooks | [`POST /v1/webhooks`](/docs/api/webhooks/criar-webhook/), [`PATCH`](/docs/api/webhooks/atualizar-webhook/) | <span class="lz-badge lz-badge--ok">Disponível</span> |
| Status do chat (presença) | — | <span class="lz-badge lz-badge--soon">M7</span> |
| Filtros por tipo de mensagem | — (filtro por tipo de evento já existe) | <span class="lz-badge lz-badge--soon">M7</span> |
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
| MCP Server | [`/mcp` e `litezap mcp`](/docs/mcp/), 13 ferramentas | <span class="lz-badge lz-badge--ok">Disponível</span> |

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
