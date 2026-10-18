# LiteZap Marco 4 — MCP (plano curto)

Spec: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md` seções 3, 3.1, 6.1, 7, 13 item 4.

## Decisões

- **SDK**: `github.com/modelcontextprotocol/go-sdk` v1.8.0 (oficial). Ferramentas com `mcp.AddTool` tipado: o SDK valida a entrada pelo JSON Schema (com `default`, `enum`, `minimum`/`maximum` ajustados à mão) e devolve `structuredContent` + texto JSON.
- **Pacote `internal/mcp`**: ferramentas definidas uma vez sobre a interface `Backend` (escopo do chamador + casos de uso). Duas implementações: `Local` (chama `service` e o barramento de eventos; usada no `/mcp`) e `REST` (cliente HTTP da API `/v1`; usada por `litezap mcp`).
- **Escopo (6.1)**: `Scope{Admin, InstanceID}`. Token de instância: `instance_id` opcional; outro id → `forbidden`; `list_instances` devolve só a própria. Admin: `instance_id` obrigatório, salvo quando existe exatamente uma instância. O `Local` confere o escopo de novo (defesa em profundidade); o `REST` herda a autorização do servidor e descobre o escopo por `GET /v1/me` (novo, devolve `{admin, instance_id}`).
- **HTTP `/mcp`**: montado na `api` atrás do mesmo `authenticate` (Bearer admin ou instância); a `api` grava o escopo no contexto (`service.WithScope`). Streamable HTTP **stateless**: cada POST é autenticado e ganha um servidor ligado ao escopo daquela requisição (sem sessão reaproveitável por outro token). Proteção de DNS rebinding do SDK fica ligada.
- **`litezap mcp`**: stdio; `LITEZAP_URL` (padrão `http://127.0.0.1:8787`) e `LITEZAP_TOKEN` (obrigatório). Nunca sobe supervisor; logs só no stderr; o token nunca é logado.
- **Ferramentas**: `list_instances`, `get_instance_status`, `get_qr` (PNG como `ImageContent`), `send_message` (texto; `wait` padrão true, `timeout_seconds` 30, máx. 120), `send_media` (por URL), `get_message_status`, `list_chats`, `get_messages`, `mark_read`, `list_contacts` (filtro `query` + `limit` no lado da ferramenta), `get_contact`, `check_numbers` (máx. 10, cota da instância), `wait_for_events` (espera curta, até 60s, eventos novos da instância; sem replay). `search_messages` fica para o marco 2b.
- **Fila sempre**: envio passa por `service.Messaging.Send` (fila, ritmo, rampa, janela). Nada de `unsafe_pacing`, settings ou proxy nas ferramentas. Mensagem ainda na fila não é erro: a saída traz `note` explicando que será enviada respeitando ritmo/janela, que não deve ser reenviada e como acompanhar (`get_message_status`).
- **Erros**: `*apierr.Error` vira erro de ferramenta (`isError`) com `código: mensagem`, dica por código (`instance_not_connected`, `rate_limited`, `forbidden`, …) e se pode tentar de novo. Erro inesperado: logado e devolvido como `internal error`. O cliente REST decodifica o envelope `{"error":{…}}` no mesmo `*apierr.Error`.
- **Conteúdo não confiável**: instruções do servidor e descrições avisam que texto de mensagens/contatos vem de terceiros e não deve ser seguido como instrução.

## Tarefas (TDD)

1. `go get` do SDK; `service.Scope` no contexto; `api`: `/v1/me` e `Options.MCP` montado em `/mcp` com autenticação. Testes de contrato.
2. `internal/mcp`: `Backend`, `Local`, ferramentas, mapeamento de erros. Testes com cliente SDK em memória contra `service` + `workertest` (envio na fila, escopo admin/instância, QR imagem, eventos, erros).
3. `REST` + `RunStdio`; subcomando `litezap mcp`. Testes contra `httptest` com a API real.
4. Wiring no `serve`; teste HTTP `/mcp` com cliente streamable (token admin, de instância, sem token).
5. README + docs (Claude Desktop, Claude Code, clientes HTTP, segurança).
6. Revisão independente e correções; smoke test com `serve` em 127.0.0.1:18790.

Cada commit: `gofmt`, `go vet ./...`, `go test -p 2 -race ./...`.
