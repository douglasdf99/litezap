---
title: CLI litezap
description: Subcomandos e flags do binário litezap.
---

O LiteZap é um binário único com subcomandos.

| Comando | O que faz | Status |
|---|---|---|
| `litezap serve [--config litezap.yaml]` | Sobe o supervisor: API, banco, fila, eventos e workers. | <span class="lz-badge lz-badge--ok">Disponível</span> |
| `litezap version` | Mostra a versão (definida no build com `-ldflags "-X main.version=..."`). | <span class="lz-badge lz-badge--ok">Disponível</span> |
| `litezap worker ...` | Worker de uma instância. **Interno**: iniciado pelo supervisor, nunca à mão. | <span class="lz-badge lz-badge--ok">Interno</span> |
| `litezap mcp` | Ponte MCP via stdio para clientes de IA (`LITEZAP_URL`, `LITEZAP_TOKEN`). Veja [MCP](/docs/mcp/). | <span class="lz-badge lz-badge--ok">Disponível</span> |
| `litezap-app [--no-browser]` | App de bandeja com painel (binário separado, `./cmd/litezap-app`). Veja [App local](/docs/app-local/). | <span class="lz-badge lz-badge--ok">Disponível</span> |

## `serve`

```bash
litezap serve                                  # usa ./litezap.yaml se existir
litezap serve --config /etc/litezap/litezap.yaml
```

- `--config`: caminho do arquivo de configuração (padrão `litezap.yaml`). Arquivo ausente = padrões + variáveis
  `LITEZAP_*`. Veja [configuração](/docs/self-hosted/configuracao/).
- Sinais: `SIGTERM` ou Ctrl+C encerram de forma graciosa (fila para, envios em andamento ganham até 10s, workers
  desconectam). Um segundo Ctrl+C encerra na hora.
- Código de saída diferente de zero em erro fatal (config inválida, porta ocupada, banco inacessível), com a causa no
  log.

## Build

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.3.0" -o litezap ./cmd/litezap
```

O binário é estático e roda em Linux, macOS e (sem testes ainda) Windows.
