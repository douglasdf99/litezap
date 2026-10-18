---
title: Grupos
description: Criar, administrar e moderar grupos e comunidades sem pôr o número em risco.
---

Desde o marco 8 o LiteZap gerencia os grupos em que o número está: listar, criar, ler e alterar nome,
descrição, foto e configurações, administrar participantes, pedidos de entrada e links de convite, e
comunidades. Mensagens de grupo continuam chegando com `is_group: true` e o participante em `sender`; para
enviar a um grupo, use o JID `…@g.us` em `to`. A referência completa está em [Grupos](/docs/api/grupos/) e
[Comunidades](/docs/api/comunidades/).

```bash
G=localhost:8787/v1/instances/$ID/groups
curl -s -X POST $G -H "Authorization: Bearer $TOK" -d '{"name":"Equipe","participants":["5511988887777"]}'
curl -s $G/120363000000000001@g.us -H "Authorization: Bearer $TOK"
curl -s -X POST $G/120363000000000001@g.us/participants -H "Authorization: Bearer $TOK" \
  -d '{"action":"promote","participants":["5511988887777"]}'
```

## Resultado por participante

Criar grupo, adicionar, remover, promover, rebaixar e decidir pedidos respondem `results`, um item por pessoa
pedida: `ok`, `not_on_whatsapp`, `unknown_contact`, `privacy_invite_required` (a pessoa só aceita convite:
mande o link), `recently_left`, `already_participant`, `not_participant`, `group_full`, `error_<código>` ou
`unknown`. Uma falha nunca derruba o lote.

## Cuidados anti-ban

Adicionar desconhecidos em grupos é um dos gatilhos de banimento mais rápidos. O LiteZap:

- só adiciona quem já tem conversa no log da instância ou é confirmado no WhatsApp pela consulta de números
  (gasta a cota de verificação); `@lid` sem conversa é recusado;
- aceita no máximo 10 participantes por requisição (também ao criar) e exige 10 segundos entre lotes (criar com
  participantes também conta);
- limita adições por hora e por dia, criações de grupo e entradas por link por dia, por instância
  (`settings.groups`: padrões 10/h, 50/dia, 3 criações e 10 entradas; tetos 30, 200, 10 e 30, que só sobem
  com `unsafe_pacing`, ligado pelo admin); as contagens sobrevivem a reinícios;
- recusa criar, adicionar, aprovar pedidos e entrar quando o operador suspende os envios.

A checagem não distingue um desconhecido que tem WhatsApp de um amigo.

Mesmo dentro dos limites, adicione só pessoas que esperam ser adicionadas. Para listas grandes, prefira
compartilhar o link de convite.

## Eventos

[`group.updated`](/docs/eventos/group-updated/) sai a cada mudança, feita pela API, pelo celular ou por outro
membro, com `kind` (`participants_added`, `participants_removed`, `participants_promoted`,
`participants_demoted`, `join_request`, `join_request_revoked`, `subject`, `description`, `photo`, `settings`,
`invite_link_reset`, `joined`, `deleted`, `suspended`, `unsuspended`, `community_linked`, `community_unlinked`). Webhooks com `filters.chat: "group"` recebem.
O nome dos chats de grupo no log é preenchido sozinho e acompanha as mudanças.

## Limitações

- Convite por mensagem para quem só aceita convite ainda não é enviado (o `invite_code` vem no resultado).
- Desativar comunidade não existe no whatsmeow.
- Pedidos de entrada vêm de notificações que o whatsmeow não interpreta; o valor da aprovação de entrada num
  evento vem de uma consulta ao grupo.
