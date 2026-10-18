---
title: Grupos
description: Criar, administrar e moderar grupos e comunidades. Em breve (marco 8).
---

<span class="lz-badge lz-badge--soon">Em breve · M8</span>

Hoje o LiteZap **recebe** mensagens de grupos (com `is_group: true` e o participante em `sender`), lista os chats de
grupo e **envia** mensagens para grupos em que o número já está (use o JID `...@g.us` em `to`). A gestão de grupos
chega no marco 8.

## O que será possível

- Listar grupos, criar grupo, ler e alterar nome, descrição, foto e configurações (só admins enviam, só admins
  editam, aprovação de entrada, quem pode adicionar).
- Adicionar, remover, promover e rebaixar participantes, com o resultado por participante (sem falhar o lote todo).
- Aprovar ou rejeitar pedidos de entrada; obter e redefinir o link de convite; entrar por convite; sair.
- Comunidades: criar, vincular e desvincular grupos, participantes e configurações.
- Evento `group.updated` para entradas, saídas, promoções e mudanças de nome/descrição.
- Nome correto dos chats de grupo no log.

## Cuidados que virão junto

Criar grupos adicionando desconhecidos é um gatilho conhecido de banimento. A criação terá limite diário por
instância, conservador por padrão. Veja a [referência planejada](/docs/api/em-breve/grupos/).
