# Plano — Atendente LiteZap embutido (peça #4 + M5b)

Branch `feat/attendant`. Spec: seção 4.3 (hook) + nova seção 4.7 (atendente embutido).

## Arquitetura

- O atendente embutido é **um destino interno do hook de agente**: `agent-hook.target = "builtin"`.
  Todo o maquinário do `agenthook` continua valendo (um por vez por chat com coalescência, pausa
  por humano, loop guard, grupos desligados, suspensão do operador, fila com ritmo/janela/rampa,
  expiração de 1h, revalidação antes de enviar). Em vez do POST assinado, `call()` chama
  `Options.Builtin.Answer(ctx, Request)`.
- Pacote novo `internal/attendant`: perfis (agentes), presets de modo, prompt anti-injeção,
  provedores LLM (Anthropic SDK oficial; OpenAI-compatível por HTTP), transcrição e TTS
  OpenAI-compatíveis, cofre de chaves (AES-256-GCM, chave em `data/attendant_key` 0600, AAD =
  instância), teto diário de chamadas e custo por dia.
- Contexto rotulado (lição do teste real): `ContextMessage.origin` = `contact` | `agent` |
  `human` | `automatic` | `api` | `history`, derivado de `source` + `client_message_id` da fila
  (`agent:`/`agent-react:` → agent; `call-reject-` → automatic). Vale também para hooks externos.
- Eventos novos: `agent.lead`, `agent.appointment_requested`, `agent.order_draft` (emitidos pelo
  `act()` só depois da resposta aceita; campo interno, nunca vindo de hook externo).
- Ligação: `realtime` (cooldown por chamador existente) pergunta ao hook se o atendente embutido
  assume o follow-up; se sim, vira um job do chat (`agent:call:<id>`) e, se a IA falhar, sai o
  `reject_message` estático.

## Etapas (TDD com provedor falso)

1. Store: migração `0012_attendant.sql` (config, agentes, uso diário) + `ClientIDsForMessages`.
2. `attendant`: cofre, perfis/presets/validação, prompt/parse, provedores (testes com httptest),
   `Answer`/`Simulate`/follow-up de ligação, teto e custo.
3. `agenthook`: `target`, `Builtin`, `origin`, eventos internos, job de ligação.
4. `realtime`: gancho de follow-up. `serve`: fiação.
5. REST + MCP (leitura) + painel (aba Atendente IA estendida).
6. Docs: README, spec, OpenAPI, página do site.
7. Revisão independente (segurança), correções, smoke em 127.0.0.1:18802 com servidor
   OpenAI-compatível falso.

## Adiado

Integração de calendário real, roteamento de vários agentes por chat (um agente ativo por
instância), streaming, cache de prompt explícito, ferramentas do modelo além de handoff/eventos.
