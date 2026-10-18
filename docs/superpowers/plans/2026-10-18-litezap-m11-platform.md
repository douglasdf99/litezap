# LiteZap M11 — Plataforma e parceiros (plano)

Data: 2026-10-18 · Branch: `feat/m11-platform` (a partir de `main`) · Spec: `docs/superpowers/specs/2026-10-17-zapi-parity.md` (M11: SEC-4, INS-9, PAR-3, PAR-4, PAR-6, WHK-9)

Marco 10 corre em paralelo a partir de `main`: este marco mexe o mínimo em arquivos compartilhados (rotas novas em `internal/api/platform.go`, serviço novo em `internal/service/platform.go`, pacotes novos `internal/ipallow` e `internal/pairing`, migração `0011_m11_platform.sql`).

## Entregas

1. **Allowlist de IP (SEC-4)** — `internal/ipallow`: listas de IP/CIDR e IP do cliente. `X-Forwarded-For` só é lido quando a conexão vem de `trusted_proxies` (percorre da direita para a esquerda, pulando proxies confiáveis). Listas: por instância (`ip_allowlist` no `PATCH /v1/instances/{id}`, só admin), por chave de API, e globais `admin_allowed_ips` / `operator_allowed_ips` (config). Fora da lista: `403 forbidden` antes de qualquer rota.
2. **Renomear e etiquetar (INS-9)** — `PATCH /v1/instances/{id}` com `name` (admin ou a própria instância), `tags` e `ip_allowlist` (admin). Colunas `tags_json` e `ip_allowlist`.
3. **Listagem com filtros e paginação (PAR-4)** — `GET /v1/instances?state=&tag=&q=&limit=&cursor=`; sem `limit` responde como antes (todas).
4. **Operações em lote (PAR-3)** — `POST /v1/instances/bulk` `{action: start|stop|restart|delete, ids|tag, confirm}` (admin/operador), até 100 instâncias, resultado por instância; `delete` exige `confirm: true`.
5. **Uso por instância** — `GET /v1/instances/{id}/usage?from&to` (mesmos contadores do `GET /v1/usage`).
6. **Chaves de API com escopo** — `lzk_…` presas a uma instância, escopos `read` e/ou `send`, allowlist e validade opcionais; `POST/GET/DELETE /v1/instances/{id}/api-keys`. Lista de rotas permitida por escopo (fecha por padrão; nunca `/mcp`, `/qr`, `/settings`, hook de agente, convites). Tokens existentes não mudam.
7. **Página de pareamento hospedada (PAR-6)** — `POST /v1/instances/{id}/pairing-links` (admin/operador) gera link assinado (HMAC-SHA256, chave `pairing_key` no data dir) e registrado no banco; vale 10 min (ou menos) ou até o primeiro pareamento concluído; `DELETE` revoga. Página `/pair/#t=…` (token no fragmento, nunca vai para logs) mostra QR ao vivo e código por telefone; API da página só com header `X-Pairing-Token`. CSP estrita; `frame-ancestors` de `pairing_frame_ancestors` (padrão `'none'`). Web component `/pair/embed.js` (`<litezap-pair>`).
8. **Evento `instance.paired`** — pareamento concluído (qr_pending → connected), com `phone` e `via_link`; marca os links como usados.
9. **`instance.proxy_failed` completo (WHK-9)** — `ConnState.detail` (proto) com o erro do worker sem credenciais; o evento ganha `error`.
10. **Métricas** — histograma `litezap_queue_send_latency_seconds` (enfileirado → enviado) e `litezap_worker_memory_rss_bytes{instance_id}` (Linux, via `/proc`).
11. **Docs e painel** — README, spec, OpenAPI, páginas do site (sai de "em breve"); painel: renomear/etiquetas e gerador de link de pareamento.

## Testes (TDD, `workertest`)

- `ipallow`: parse, XFF com/sem proxy confiável, spoof de XFF por cliente não confiável, IPv6, IPv4-mapeado.
- API: allowlist por instância/admin/chave (+ `trusted_proxies`), PATCH (permissões), filtros/paginação, bulk (permissão, confirm, tag), chaves (escopos, revogação, expiração, `/mcp` negado, sem escalada), links (mint, página, expiração, revogação, uso único após pareamento, nada de token da instância, headers CSP/frame-ancestors).
- Observer de pareamento e `ProxyWatch` com detalhe; histograma.

## Fora / adiado

- Metadados livres (chave/valor) além de `tags`.
- Escopos além de `read`/`send` (ex.: `manage`) e chaves que valem para várias instâncias.
- Memória por worker fora do Linux.
