---
title: Status e roadmap
description: O que já existe no LiteZap, o que está em construção e o plano até a paridade com as APIs comerciais.
---

O motor é construído em marcos, cada um testável sozinho e entregue com REST, OpenAPI, docs e testes. Datas não são
prometidas; a ordem é.

## Status

| Marco | Escopo | Status |
|---|---|---|
| M1 | Esqueleto: binário, config, banco, supervisor + worker por número, estados, pareamento por QR e código | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M2 | Mensagens: fila com ritmo, rampa, janela e verificação de número; recebimento, chats, histórico; mídia; proxy por instância | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M3 | Eventos: envelope v1, webhooks assinados com retry e dead-letter, SSE | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M4 | MCP: `/mcp` (HTTP) e `litezap mcp` (stdio), 13 ferramentas, `GET /v1/me` | <span class="lz-badge lz-badge--ok">Entregue</span> |
| App local v1 | Painel web em `/app/` e app de bandeja `litezap-app` com login em um clique | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M2b | Complementos: busca full-text, retenção e limpeza de mídia, `/metrics`, watchdog de conexão, nome de grupos | <span class="lz-badge lz-badge--soon">Planejado</span> |
| M5 | Hook de agente (atendente IA) com pausa por humano | <span class="lz-badge lz-badge--soon">Próximo</span> |
| M6 | Mensagens completas | <span class="lz-badge lz-badge--soon">Planejado</span> |
| M7 | Eventos completos e chamadas | <span class="lz-badge lz-badge--soon">Planejado</span> |
| M8 | Grupos e comunidades | <span class="lz-badge lz-badge--soon">Planejado</span> |
| M9 | Chats, contatos, perfil, privacidade e conta | <span class="lz-badge lz-badge--soon">Planejado</span> |
| M10 | Status, canais e Business | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M11 | Plataforma e parceiros | <span class="lz-badge lz-badge--ok">Entregue</span> |
| App local v2 | Instaladores `.msi`/`.deb`, assinatura de código, atualização automática, iniciar com o sistema, túnel opcional | <span class="lz-badge lz-badge--soon">Planejado</span> |
| Nuvem | Projeto pré-instalado por cliente, IPs gerenciados, backups e upgrades | <span class="lz-badge lz-badge--soon">Planejado</span> |

Status da nuvem (uptime, incidentes): a página pública de status será publicada junto com a nuvem LiteZap.

## Próximos marcos

### M5: hook de agente

Contrato HTTP para um agente responder mensagens, com contexto, ações e pausa automática quando um humano responde
pelo celular. Veja [hook de agente](/docs/mcp/hook-de-agente/).

### M6: mensagens completas

Figurinha, GIF, vídeo redondo, localização, contato(s), enquete com votos, link com prévia, responder citando,
menções, editar, apagar, fixar, encaminhar, atraso por mensagem. Fila visível: listar, cancelar item, esvaziar.

### M7: eventos completos e chamadas

`message.revoked`, `message.edited`, `message.button_reply`, `chat.presence`, `call.received`, `call.ended`; status
`played`; rejeição automática de chamadas com mensagem; filtros de webhook por tipo de chat e de mensagem.

### M8: grupos e comunidades

Gestão completa de grupos (criar, dados, configurações, participantes, admins, pedidos, convites) e comunidades, com
evento `group.updated` e limite diário de criação.

### M9: chats, contatos, perfil e privacidade

Arquivar, fixar, silenciar, não lido, limpar, apagar e mensagens temporárias; foto de perfil, bloqueio e metadados de
contatos; nome, recado e foto do perfil; privacidade; leitura automática; passkey no pareamento.

### M10: status, canais e Business

Publicar e receber status (`status.received`, opt-in), responder e apagar; criar, seguir, silenciar, ler, reagir e
publicar em canais; etiquetas (`label.updated`) e perfil comercial (leitura). Adiados: leitura automática de status,
eventos de agenda, produto/catálogo e download da mídia de canais.

### M11: plataforma e parceiros

Entregue: página de pareamento hospedada (link assinado de até 10 minutos, uso único) e web component
`<litezap-pair>`, evento `instance.paired`, renomear e etiquetar instâncias, operações em lote (iniciar, parar,
reiniciar, apagar), filtros e paginação em `GET /v1/instances`, allowlist de IP por instância, por chave e para os
tokens admin/operador (com `trusted_proxies`), chaves de API com escopo (`read`, `send`), uso por instância,
`instance.proxy_failed` com o erro do worker e métricas de latência da fila e memória por worker.

## Fora da trilha principal

- **Experimental**: botões, listas, OTP/PIX e carrossel, atrás de opção explícita, só depois do M10 e se houver
  demanda. Veja [mensagens interativas](/docs/guias/mensagens-interativas/).
- **Em avaliação**: catálogo e coleções, perfil comercial editável, administração avançada de canais, pedidos e
  pagamentos.
- **Fora**: registro do número sem celular, chamadas de voz/SIP, listas de transmissão, recursos de "aquecimento"
  artificial de números, cobrança no motor (fica na nuvem).

A correspondência recurso a recurso com a Z-API está em [Migrar da Z-API](/docs/migrar-da-z-api/).
