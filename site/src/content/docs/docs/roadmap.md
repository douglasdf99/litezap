---
title: Roadmap
description: O que já existe no LiteZap e o que vem a seguir.
---

O motor é construído em marcos, cada um testável sozinho. Datas não são prometidas; a ordem é.

## Pronto

### Marco 1: esqueleto

Binário único, config, banco SQLite com migrações, supervisor com um worker por número, máquina de estados,
pareamento por QR e por código.

### Marco 2: mensagens

Fila persistente com ritmo, rampa, janela de envio, verificação de números, idempotência e modo síncrono; recebimento,
chats e histórico (inclusive o histórico sincronizado no pareamento); mídia com URLs assinadas; proxy por instância.

## Próximos

### Marco 2b: complementos

Busca full-text nas mensagens, envio de `sticker`, `location` e `contact`, limpeza automática de mídia e retenção do
log, confirmação de entrega worker → supervisor, mensagens editadas e apagadas no log, nome de grupos, métricas
Prometheus em `/metrics` e watchdog de conexão "zumbi".

### Marco 3: eventos em tempo real

- Barramento de eventos com envelope versionado (`message.received`, `message.status`, `instance.status`,
  `instance.qr` e outros).
- **Webhooks** assinados com HMAC-SHA256 (cabeçalho com timestamp contra replay), retry com backoff por até 24h e
  dead-letter com reenvio manual.
- **SSE** por instância e global (admin).

### Marco 4: MCP

Servidor MCP embutido (`/mcp`, HTTP) e a ponte `litezap mcp` (stdio) para clientes como o Claude Desktop, com
ferramentas para listar instâncias, enviar mensagens e mídia, ler chats, buscar mensagens e verificar números.

### Marco 5: hook de agente (atendente IA)

Contrato HTTP para um agente externo responder mensagens recebidas: contexto das últimas mensagens, ações
(`mark_read`, `react`, `pause`, `handoff`), uma mensagem por vez por chat, falha fechada e **pausa automática quando
um humano responde pelo celular**. Com um exemplo de referência usando o Claude.

## Além do motor

- **App desktop**: bandeja do sistema, tela de QR, atualização automática e túnel opcional. Veja [app local](/docs/app-local/).
- **Nuvem LiteZap**: um projeto pré-instalado e isolado por cliente, rodando o mesmo binário open source, com
  upgrades, backups, monitoramento e pool gerenciado de IPs de saída (no máximo um cliente por IP). Cobrança mensal
  por instância.
- **Atendente IA completo**: memória, base de conhecimento e painel, por cima do hook de agente.

## Fora do escopo

Listas de transmissão e chamadas (não suportadas pela biblioteca de protocolo), WebSocket (o stream será SSE) e
qualquer recurso de "aquecimento" artificial de números.
