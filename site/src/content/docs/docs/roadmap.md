---
title: Status e roadmap
description: O que já existe no LiteZap, o que está em construção e o que vem depois.
---

O motor é construído em marcos, cada um testável sozinho e entregue com REST, OpenAPI, docs e testes. Datas não são
prometidas; a ordem é. O detalhe de cada entrega está no [changelog](/docs/changelog/).

## Status

| Marco | Escopo | Status |
|---|---|---|
| M1 | Esqueleto: binário, config, banco, supervisor + worker por número, estados, pareamento por QR e código | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M2 | Mensagens: fila com ritmo, rampa, janela e verificação de número; recebimento, chats, histórico; mídia; proxy por instância | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M3 | Eventos: envelope v1, webhooks assinados com retry e dead-letter, SSE | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M4 | MCP: `/mcp` (HTTP) e `litezap mcp` (stdio), `GET /v1/me`; hoje com 54 ferramentas | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M5 e M5b | Atendente IA: hook de agente com pausa por humano; atendente embutido com perfis prontos (vendedor, pedido, SDR, suporte, personalizado), base de conhecimento, simulador, provedores Anthropic e OpenAI-compatível, chaves cifradas, áudio, imagem/PDF, resposta em voz, follow-up de ligação, eventos `agent.*` e tetos de gasto | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M6 | Mensagens completas: figurinha, GIF, vídeo redondo, localização, contatos, enquete, citação, menções, prévia de link, editar, apagar, fixar, encaminhar, atraso por mensagem; fila visível | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M7 | Eventos e chamadas: `chat.presence`, chamadas recebidas e rejeição automática, status `played`, respostas de botão, filtros de webhook | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M8 | Grupos e comunidades, `group.updated`, cotas anti-ban | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M9 | Chats, contatos, perfil e privacidade, leitura automática, `chat.updated` | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M10 | Status, canais, etiquetas e perfil comercial | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M11 | Plataforma e parceiros: pareamento hospedado, chaves com escopo, allowlist de IP, lote, uso por instância | <span class="lz-badge lz-badge--ok">Entregue</span> |
| M12a | Perfil comercial editável: descrição, endereço, localização, e-mail, sites, categorias e horário, `business.profile_updated`; `clear` para remover campos | <span class="lz-badge lz-badge--soon">Validado em parte num número Business (o pin do mapa só sai no celular)</span> |
| M12b | Catálogo e pedidos, fase 1: ler catálogos, enviar produto, `order.received` e detalhes do pedido, atendente com o catálogo | <span class="lz-badge lz-badge--soon">Pronto, aguardando validação num número Business</span> |
| M12c | Catálogo, fase 2: criar e editar produtos e coleções (fluxo de token de anúncios, opt-in por instância) | <span class="lz-badge lz-badge--no">Avaliar (risco para o número)</span> |
| M2b | Complementos: retenção de mensagens e mídia, `/metrics`, watchdog de conexão e nome de grupos entregues; falta a busca full-text | <span class="lz-badge lz-badge--soon">Parcial</span> |
| App local v1 | Painel web em `/app/`, app de bandeja `litezap-app`, downloads para macOS (`.dmg`), Windows e Linux, **Conectar a IA** em um clique | <span class="lz-badge lz-badge--ok">Entregue</span> |
| Distribuição | CI, binários pelo GoReleaser, imagem Docker em `ghcr.io`, instalador `install.sh` | <span class="lz-badge lz-badge--ok">Entregue</span> |
| Nuvem C1 (motor) | Modo nuvem no binário: token de operador, settings travadas, limite de instâncias, proxy obrigatório, envio suspenso, chaves com escopo, allowlist de IP, links de pareamento, logs JSON, endpoint de ops | <span class="lz-badge lz-badge--ok">Entregue</span> |
| App desktop | Janela nativa e ícone no Dock | <span class="lz-badge lz-badge--soon">Em construção</span> |
| Nuvem C1 (control plane) | Cadastro, provisionamento por cliente na AWS, cobrança | <span class="lz-badge lz-badge--soon">Em construção</span> |
| Repositório público | Código aberto no GitHub | <span class="lz-badge lz-badge--soon">Em breve</span> |

Os marcos entregues foram validados com um celular real. Status da nuvem (uptime, incidentes): a página pública de
status será publicada junto com a nuvem LiteZap.

## Próximos

- **App desktop com janela nativa**: o app local ganha janela própria e ícone no Dock, além da bandeja.
- **Control plane da nuvem**: cadastro, um projeto pré-instalado por cliente na AWS (`sa-east-1`), backups e
  upgrades gerenciados, e cobrança. O lado do motor (modo nuvem) já está pronto.
- **Lançamento público**: repositório aberto no GitHub, releases com binários, imagem Docker e o app para download.
- **Lacunas de paridade com a Z-API**: limpar conversa, mensagens temporárias por chat, contatos na agenda, exceções
  de privacidade, leitura automática de status, votar em enquete, responder status com GIF ou figurinha, marcar
  publicações de canal como vistas, proteção anti-golpe em grupos de avisos. Veja [Migrar da Z-API](/docs/migrar-da-z-api/).

## Depois

- **Busca full-text** nas mensagens (fecha o M2b), também como ferramenta MCP.
- **Fotos WebP** para grupo e perfil (hoje JPEG, PNG ou GIF).
- **Atendente IA**: roteamento avançado entre vários agentes por conversa e integração com agenda.
- **Pagamentos na nuvem** (gateway de cobrança).
- **App local v2**: assinatura de código e notarização, instaladores `.msi`/`.deb`, atualização automática, iniciar
  com o sistema, túnel opcional.
- **Pesquisa**: atender ligações de voz com IA (API oficial de chamadas ou alternativa experimental), só se couber
  sem risco de banimento.

## Fora da trilha principal

- **Experimental**: botões, listas, OTP/PIX e carrossel, atrás de opção explícita e só se houver demanda. Veja
  [mensagens interativas](/docs/guias/mensagens-interativas/).
- **Em avaliação**: catálogo e coleções, administração avançada de canais, eventos de
  agenda, pedidos e pagamentos no WhatsApp, desativar comunidade.
- **Fora**: registro do número sem celular, fazer chamadas de voz/SIP, listas de transmissão, recursos de
  "aquecimento" artificial de números, cobrança no motor (fica na nuvem).

A correspondência recurso a recurso com a Z-API está em [Migrar da Z-API](/docs/migrar-da-z-api/).
