# LiteZap — Marco 5: hook de agente + `examples/agent-claude`

Data: 2026-10-18 · Spec: `2026-10-17-litezap-engine-design.md` §4.3, §5.2, §6 · Branch: `feat/m5-agent-hook`

## Objetivo

Contrato HTTP para um atendente de IA externo: cada `message.received` elegível vira um
`POST` assinado para a URL do agente; a resposta (`reply` + `actions`) volta pela fila
normal (ritmo, janela, rampa, suspensão). Mais um servidor de referência em Go usando a API
da Anthropic.

## Decisões

- **Pacote novo `internal/agenthook`**, consumidor do barramento como `events.Sink`
  (igual aos webhooks): nenhuma mudança no ingest. Lê `message.received` (dispara) e
  `message.sent` com `source: device` (pausa por humano: o ingest já classifica um
  `from_me` cujo `wa_message_id` não está na `outbound_queue` como `device`).
- **Store**: migração `0006_agent_hook.sql` com `agent_hooks` (config JSON por instância)
  e `agent_calls` (log das últimas chamadas, para o painel e para o limitador de loop).
  `chats.agent_enabled` / `agent_paused_until` já existiam (marco 2).
- **Envio**: a resposta chama `GuardedMessaging.Send` (mesmo caminho da REST: fila,
  suspensão do operador) com `client_message_id = agent:<id da mensagem>` (idempotente).
  `react` também vai pela fila; `mark_read` usa `MarkRead`.
- **Uma chamada por vez por chat**: um goroutine por chat ativo; mensagens que chegam
  durante uma chamada são **coalescidas** (a próxima chamada leva a mais nova; as anteriores
  estão no `context`). Concorrência global limitada (8).
- **Fail-closed**: timeout, erro HTTP/rede, JSON inválido ou ação conhecida malformada →
  `agent.failed`, nada enviado, sem retry. Ação desconhecida é ignorada (log).
- **Revalidação**: depois da resposta, o estado do chat é relido; se um humano respondeu,
  ou o chat foi pausado/desligado durante a chamada, a resposta é descartada.
- **Proteção contra loop entre bots** (extra ao spec): no máximo `max_replies_per_window`
  respostas (padrão 10) por chat em `reply_window` (padrão 10min); estourou → chat pausado
  por `human_pause` e `agent.failed` com `reason: loop_guard`.
- **Sempre ignorados**: `status@broadcast`, `@newsletter`, grupos (salvo `groups_enabled`),
  lista `ignore`, override `false`, pausa ativa. Override `true` liga o hook no chat mesmo com
  `enabled: false` na instância (padrão herdado só quando `null`).
- **SSRF**: `media.SafeTransport(webhook_allow_private_urls)`, sem redirecionamentos, sem
  proxy de ambiente; assinatura `X-LiteZap-Signature` idêntica à dos webhooks.
- **Suspensão (nuvem C1)**: com envio suspenso o hook nem é chamado (registrado como
  `skipped`).

## REST

- `GET/PUT/DELETE /v1/instances/{id}/agent-hook` (segredo nunca devolvido; gerado e mostrado
  uma vez se ausente).
- `PUT /v1/instances/{id}/chats/{jid}/agent` `{enabled: true|false|null}` (true/null também
  limpa a pausa).
- `GET /v1/instances/{id}/agent-hook/chats` (chats com override ou pausa) e
  `GET /v1/instances/{id}/agent-hook/calls` (últimas chamadas).

## Tarefas (TDD)

1. Store: migração, config, estado do chat, log de chamadas (+ testes).
2. `agenthook`: config/validação; regras de elegibilidade; chamada assinada; parse da
   resposta; ações; pausa por humano; um-por-vez/coalescência; timeout; loop guard
   (testes com relógio falso e servidor `httptest`).
3. Integração: serviço real + fila + `workertest` — resposta sai pela fila com ritmo.
4. Eventos `agent.failed` / `agent.handoff` no catálogo; wiring no `serve`; rotas na API.
5. Painel: aba "Atendente IA" (config, override por chat, chamadas recentes).
6. `examples/agent-claude` (módulo Go próprio, SDK oficial da Anthropic) + README.
7. README + spec; revisão independente; smoke em `127.0.0.1:18794`.
