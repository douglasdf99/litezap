---
title: Contribuir
description: Como rodar o projeto, os testes e enviar mudanças.
---

O LiteZap é open source (Apache-2.0) e aceita contribuições.

## Ambiente

- Go na versão do `go.mod`.
- Para o site: Node.js e pnpm.

```bash
git clone https://github.com/douglasdf99/litezap.git && cd litezap
go build -o litezap ./cmd/litezap
go test -race ./...
```

## Estrutura

| Pasta | O quê |
|---|---|
| `cmd/litezap` | Binário (`serve`, `worker`, `version`) |
| `internal/api` | REST (rotas chi, autenticação) |
| `internal/service` | Casos de uso compartilhados por REST e (em breve) MCP |
| `internal/manager`, `internal/spawner` | Ciclo de vida das instâncias e processos worker |
| `internal/worker` | Worker com whatsmeow, falando gRPC com o supervisor |
| `internal/queue`, `internal/lookup` | Fila de envio, ritmo e verificação de números |
| `internal/events`, `internal/webhook` | Barramento de eventos, webhooks e SSE |
| `internal/store` | SQLite e migrações |
| `proto/` | Contrato gRPC supervisor ↔ worker (`go tool buf generate` depois de mudar) |
| `site/` | Este site (Astro + Starlight) |

## Testes

- **Unitários** por pacote, com relógio falso para ritmo, backoff e janelas.
- **Worker falso** (`internal/workertest`): implementa o contrato gRPC e simula conectar, cair, travar, banimento e
  mensagens recebidas, para testar o supervisor inteiro sem WhatsApp real.
- **Contrato** da API via `httptest`.
- **E2E** com número de teste real, atrás de `-tags=e2e`, fora do CI.

## Documentação

Toda mudança de API atualiza no mesmo PR o `site/public/openapi.yaml` (a referência é gerada dele), o catálogo de
eventos e o changelog. Para ver o site:

```bash
cd site && pnpm install && pnpm dev
```

## Antes de abrir um PR

- `go test -race ./...` passando e `pnpm build` no `site/` sem erros.
- Mensagens de commit no formato convencional (`feat(api): ...`, `fix(queue): ...`, `docs: ...`).
- Recursos que aumentam o risco de banimento (envio em massa, rotação de IP) não serão aceitos; veja o
  [uso aceitável](/docs/uso-aceitavel/).

Vulnerabilidades: não abra issue pública; veja a [política de segurança](/docs/politica-de-seguranca/).
