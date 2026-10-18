# LiteZap M8 — Grupos e comunidades (plano enxuto)

Data: 2026-10-18 · Branch: `feat/m8-groups` (a partir de `main`) · Spec: `2026-10-17-zapi-parity.md`, seção 4 "M8" (GRP-1…13, COM-1…7, nome de grupos no log).

## APIs do whatsmeow conferidas (cache de módulos, `v0.0.0-20261016130820-0b0a1cdac3e2`)

- `group.go`: `CreateGroup(ctx, ReqCreateGroup{Name, Participants, GroupEphemeral, GroupAnnounce, GroupLocked, GroupMembershipApprovalMode, MemberAddMode, GroupParent{IsParent, DefaultMembershipApprovalMode}, GroupLinkedParent{LinkedParentJID}})` → `*types.GroupInfo` (participantes com `Error` por pessoa; comunidade = `IsParent`, o servidor cria o grupo de avisos sozinho); `GetGroupInfo`, `GetJoinedGroups` (com participantes e descrição), `SetGroupName` (25 caracteres), `SetGroupTopic(ctx, jid, prevID, newID, topic)` (busca o `prev` sozinho; vazio apaga), `SetGroupPhoto(ctx, jid, jpeg)` (nil remove), `SetGroupAnnounce`, `SetGroupLocked`, `SetGroupJoinApprovalMode`, `SetGroupMemberAddMode(admin_add|all_member_add)`, `UpdateGroupParticipants(ctx, jid, []JID, add|remove|promote|demote)` → `[]GroupParticipant{JID, PhoneNumber, LID, Error, AddRequest}`, `GetGroupRequestParticipants` → `[]{JID, RequestedAt}`, `UpdateGroupRequestParticipants(approve|reject)`, `GetGroupInviteLink(ctx, jid, reset)` (erros `ErrGroupInviteLinkUnauthorized`, `ErrNotInGroup`, `ErrGroupNotFound`), `GetGroupInfoFromLink(code)` / `JoinGroupWithLink(code)` (aceitam o link inteiro; `ErrInviteLinkRevoked`/`ErrInviteLinkInvalid`; com aprovação devolve o JID do pedido), `LeaveGroup`, `LinkGroup(parent, child)`, `UnlinkGroup(parent, child)`, `GetSubGroups(community)` → `[]GroupLinkTarget{JID, Name, IsDefaultSubGroup}`.
- `send.go`: `SetDisappearingTimer(ctx, groupJID, d, _)` (grupo: `ephemeral`/`not_ephemeral` por IQ).
- `types/events`: `GroupInfo{JID, Sender, SenderPN, Timestamp, Name, Topic, Locked, Announce, Ephemeral, MembershipApprovalMode (só "ligou"), Delete, Link, Unlink, NewInviteLink, JoinReason, Join, Leave, Promote, Demote, UnknownChanges}`, `JoinedGroup{Reason, Type, Sender, GroupInfo}`, `Picture{JID, Author, Remove}` (foto de grupo quando `JID` é `@g.us`).
- LID: participantes podem vir `@lid` (`AddressingMode=lid`); `GroupParticipant.PhoneNumber` traz o telefone quando o servidor manda. Remover/promover/rebaixar/aprovar usam o endereço do grupo (`groupParticipant`, já existente, converte telefone → LID em grupos LID); adicionar usa o telefone (o whatsmeow anexa `phone_number` quando recebe LID).
- Pedidos de entrada recebidos chegam como `UnknownChanges` (tag `membership_approval_request` ou parecida): reportados como `join_request` (a confirmar no celular).

## Decisões

1. Serviço gRPC separado (`proto/worker/v1/groups.proto`, `service Groups`) no mesmo processo/socket do worker; `spawner.GroupsHandle` opcional (`Groups() GroupsClient`) e `manager.Groups(id)`. Evento novo `GroupUpdate` no oneof do `Event` (campo 20). Menos conflito com o M9.
2. `service.Groups` (arquivos novos): validação, cotas anti-ban, checagem de participantes, mapeamento de erros. Escritas que alcançam terceiros (criar, adicionar, entrar por link) recusadas com envio suspenso pelo operador.
3. Anti-ban (settings `groups`): `max_adds_per_hour` 10 (teto 30), `max_adds_per_day` 50 (teto 200), `max_creates_per_day` 3 (teto 10), `max_joins_per_day` 10 (teto 30); tetos sobem só com `unsafe_pacing` (admin). Intervalo mínimo de 10s entre lotes de adição, no máximo 10 participantes por lote. Contadores persistidos em `engine_kv` (sobrevivem a reinício). Acima: `429 rate_limited`.
4. Nunca adicionar desconhecidos em massa: cada número a adicionar precisa ter conversa no log da instância ou ser confirmado no WhatsApp pela consulta (cota do check-numbers); `@lid` só com conversa existente. Os que falham voltam como `not_on_whatsapp` / `unknown_contact` por participante, sem chamar o WhatsApp.
5. Resultado por participante: `ok`, `not_on_whatsapp`, `privacy_invite_required` (403), `recently_left` (408), `already_participant` (409), `not_participant` (404 em remover), `group_full` (500), `error_<código>`; o lote nunca falha inteiro por um participante.
6. Evento `group.updated`, um por tipo de mudança: `kind` ∈ `participants_added|participants_removed|participants_promoted|participants_demoted|subject|description|photo|settings|invite_link_reset|join_request|joined|deleted|community_linked|community_unlinked`; `actor`, `participants`, campos do tipo. Filtros de webhook por `chat` (é `@g.us`).
7. Nome de grupos no log: ao receber mensagem de grupo cujo chat está sem nome, busca `GetGroup` em segundo plano (uma vez por grupo a cada 30 min) e grava; `subject`/`joined` atualizam também.
8. MCP: `list_groups`, `get_group` (leitura); `create_group`, `add_group_participants`, `update_group` com avisos de segurança (risco de ban, nunca adicionar desconhecidos, só quando o usuário pedir).
9. Painel: aba "Grupos" (lista, metadados, participantes, link de convite).
10. Adiado: convite por mensagem (`JoinGroupWithInvite`) para quem bloqueia adição (devolvemos o `invite_code` do 403), SEC-6 `announcement_guard`, desativar comunidade (sem API), `GetLinkedGroupsParticipants`.

## Tarefas (TDD, worker falso)

1. Proto + `go tool buf generate`; worker `Groups` (servidor gRPC, conversões, eventos) com testes de conversão.
2. Fake (`workertest`): grupos em memória, `EmitGroupUpdate`; `manager.Groups`.
3. `service.Groups` + cotas + settings `groups`; testes.
4. Observador: `group.updated`, nome do chat; testes.
5. API `/instances/{id}/groups…` e `/communities…`; testes de contrato (`httptest` + fake), escopo do token.
6. MCP, painel, README, OpenAPI, spec.
7. gofmt, vet, `go test -p 2 -race ./...`; revisão independente; smoke em 127.0.0.1:18798.
