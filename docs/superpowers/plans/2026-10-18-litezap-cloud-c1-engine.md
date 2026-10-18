# LiteZap Cloud C1 — mudanças no motor (plano)

Data: 2026-10-18 · Branch: `feat/cloud-c1-engine` (a partir de `integration/preview`) · Spec: `docs/superpowers/specs/2026-10-18-litezap-cloud-design.md` (branch `docs/cloud-spec`), seção 9 e roteiro C1 (seção 10), decisões D5 e D13 aprovadas.

Regra geral: **sem a configuração nova o motor se comporta exatamente como hoje** (self-host e app local). Tudo é opt-in por `litezap.yaml` ou `LITEZAP_*`. Para reduzir conflitos com o marco 6 (`feat/m6-messages`), o código novo fica em arquivos/pacotes novos e as edições em arquivos compartilhados são pontuais.

## Escopo do C1 no motor (spec: "Motor: E1–E9, E11–E13, E15, E17")

| Item | O que entra agora | Onde |
|---|---|---|
| E1 config por ambiente | `LITEZAP_CONFIG_JSON` (documento inteiro, inclusive `instance_defaults`) + variável própria para cada opção nova e para as que eram só de arquivo (retenções, limites) | `internal/config` |
| E2 endpoint de ops | `ops_listen_addr`: `/healthz`, `/readyz`, `/metrics` sem token num listener separado (vazio = desligado) | `internal/ops`, `serve` |
| E3 `/metrics` | exposição Prometheus em texto, sem dependência nova: instâncias por estado, fila por status, entregas de webhook por status, contadores de mensagens/mídia/falhas de proxy/starts de worker, pronto/encerrando, runtime Go. Também em `/metrics` na API (mesma proteção do `/healthz`) | `internal/usage`, `internal/ops` |
| E4 operador e settings travadas | `operator_token` (até 2 valores, rotação); papel `operator` acima do admin; `locked_settings` (`proxy_url`, `unsafe_pacing`) → admin recebe `403 forbidden`; admin não vê credenciais do proxy travado; `require_proxy` (worker nunca sobe sem proxy); webhooks criados pelo operador ficam invisíveis para o admin | `internal/api/operator.go`, `internal/service/policy.go`, `store` |
| E5 limite de instâncias | `max_instances` (config/env) + ajuste em tempo de execução pelo operador (`PUT /v1/operator/limits`, persistido); admin acima do limite → `403 plan_limit`; operador não é limitado | `service`, `api` |
| E6 `instance.proxy_failed` | emitido ao entrar em `reconnecting`/`proxy_unreachable` e repetido a cada 5 min enquanto durar, com quedas na última hora, início e duração | `internal/usage` (observer) |
| E7 login de um clique pelo control plane | `POST /v1/operator/dashboard-login` (só operador) devolve código aleatório de uso único (TTL 2 min) e a URL `<public_url>/app/#code=…`; o código troca pelo **token admin do cliente** (nunca o de operador); vale fora de loopback, com checagem de mesma origem | `internal/dashboard`, `api` |
| E8 exportação | `POST /v1/export` (admin/operador): `tar.gz` com `litezap.db` (backup online `VACUUM INTO`, sem webhooks do operador), mídia e `manifest.json`; `upload_url` (PUT numa URL pré-assinada HTTPS) só para o operador. Importação fica para o C2 | `internal/export` |
| E9 logs JSON | `LITEZAP_LOG_FORMAT=json` (supervisor e workers) | `cmd/litezap` |
| E11 uso persistido | tabela `usage_daily` (instância × dia UTC: enviadas pela API, recebidas, bytes de mídia, segundos conectada, entregas de webhook) gravada a cada 30 s e no shutdown; `GET /v1/usage?from=&to=` (operador) | `internal/usage`, `store` |
| E12 upgrade gracioso | `/readyz` responde 503 assim que o sinal chega; `shutdown_timeout` limita o encerramento inteiro (para caber no `kill_timeout` do Fly) | `serve` |
| E13 watchdog de conexão zumbi | `zombie_timeout` (padrão 0 = desligado, para não mudar o self-host; nuvem usa 5m): worker que reportou `connected` mas está sem socket, ou preso em `keepalive_timeout` com socket "aberto", por mais que o limite → reconexão forçada (`reason: zombie_watchdog`) | `internal/worker/watchdog.go` |
| E15 limpeza | `media_retention_days` (apaga cópias locais vencidas; baixa de novo sob demanda) e `message_retention_days` (apaga log, mídia e itens finalizados da fila mais velhos); ambos 0 = desligado (hoje) | `internal/retention` |
| E17 envio suspenso | operador suspende o envio global ou por instância (`PUT /v1/operator/sending`, `PUT /v1/operator/instances/{id}/sending`): novos envios → `403 sending_suspended` (API e MCP), itens já na fila ficam retidos até liberar | `service`, `queue` (gancho mínimo) |

Fica para depois (não é C1 no spec): E8 importação, E10, E14, E16, E18, E20, E21 (C2); E19 (C3).

Desvios do texto do spec da nuvem (a atualizar no `docs/cloud-spec`): E7 usa código aleatório de 256 bits emitido por `POST /v1/operator/dashboard-login` (autenticado pelo token de operador) em vez de HMAC com um segredo compartilhado — mesmo efeito (curto, uso único, emitido pelo control plane) sem mais um segredo para guardar e rotacionar. E5 ganhou ajuste em tempo de execução para que mudar a quantidade de slots não exija reiniciar a Machine. E8 não exporta as sessões do WhatsApp (chaves do aparelho); migrar sem novo QR fica para a importação (C2), com opção explícita.

Limitações aceitas no C1: `instance.proxy_failed` não traz a mensagem de erro do worker (exigiria campo novo no proto, que o marco 6 está mudando); latência da fila em histograma fica para o C2; métricas de memória por worker ficam para o C2.

## Ordem (TDD, um commit por bloco)

1. Config (E1 + campos novos) e store (migração `0005_cloud.sql`: `usage_daily`, `engine_kv`, `instance_flags`, `webhooks.owner`).
2. Papéis e política: operador, settings travadas, visão do proxy, `max_instances`, `require_proxy`, webhooks do operador (E4, E5).
3. Envio suspenso (E17).
4. Uso, métricas e `instance.proxy_failed` (E11, E3, E6).
5. Ops, prontidão e shutdown com orçamento (E2, E12).
6. Login de um clique remoto (E7) e exportação (E8).
7. Logs JSON, watchdog, retenção (E9, E13, E15).
8. README, exemplo de configuração e spec; revisão independente; smoke test em 127.0.0.1:18793.

Verificação antes de cada commit: `gofmt`, `go vet ./...`, `go test -p 2 -race ./...`.
