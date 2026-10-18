---
title: Multi-instância e parceiros
description: Oferecer WhatsApp aos seus clientes com uma instância por cliente — pareamento hospedado, chaves com escopo, lote e allowlist de IP.
---

Se você tem um produto (CRM, ERP, plataforma de atendimento) e quer que cada cliente conecte o próprio número, o
LiteZap tem o que precisa: uma instância por cliente, uma página de pareamento que você entrega ao cliente sem
expor token nenhum, chaves de API com escopo e operações em lote.

## Visão geral

| Necessidade | Como |
|---|---|
| Criar uma instância por cliente | [`POST /v1/instances`](/docs/api/instancias/criar-instancia/) com o token admin, guardando o `token` no registro do cliente. |
| Identificar e agrupar | [`PATCH /v1/instances/{id}`](/docs/api/instancias/editar-instancia/): `name` e `tags` (ex.: `cliente-acme`, `plano-pro`). |
| Achar instâncias | [`GET /v1/instances`](/docs/api/instancias/listar-instancias/) com `state`, `tag`, `q`, `limit` e `cursor`. |
| Cliente pareia sozinho | [Link de pareamento hospedado](/docs/api/plataforma/gerar-link-de-pareamento/) (QR ao vivo e código por telefone). |
| Saber quando pareou | Evento [`instance.paired`](/docs/eventos/instance-paired/) (`via_link: true` quando foi pelo link). |
| Dar acesso limitado a terceiros | [Chaves de API](/docs/api/plataforma/criar-chave-de-api/) `read` (painéis, BI) ou `send` (disparo). |
| Restringir de onde o token é usado | `ip_allowlist` na instância ou na chave; `admin_allowed_ips` para o token admin. |
| Parar, reiniciar ou apagar muitos | [`POST /v1/instances/bulk`](/docs/api/plataforma/operacao-em-lote/) por ids ou por etiqueta. |
| Cobrar ou acompanhar consumo | [`GET /v1/instances/{id}/usage`](/docs/api/plataforma/uso-da-instancia/) (contadores diários). |
| Receber eventos de todos | Um webhook global (admin, sem `instance_id`); o `instance_id` do envelope diz de quem é. |
| IP por cliente | `proxy_url` por instância (admin); quedas chegam em [`instance.proxy_failed`](/docs/eventos/instance-proxy-failed/). |
| Encerrar um cliente | `DELETE /v1/instances/{id}` ou o lote com `"action": "delete"`. |

O token admin fica só no seu backend. O front do cliente conversa com o seu backend, que usa o token da instância.

## Pareamento hospedado

Em vez de buscar o QR e desenhá-lo no seu produto, peça um link ao servidor e mande ao cliente (ou abra num iframe):

```bash
curl -X POST https://zap.seu-dominio.com/v1/instances/inst_7f3a9c2e5b1d4086/pairing-links \
  -H "Authorization: Bearer $LITEZAP_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"ttl_seconds": 600}'
# {"id":"...","url":"https://zap.seu-dominio.com/pair/#t=pl1....","token":"pl1....","expires_at":"..."}
```

A página mostra o QR (atualizado sozinho) e a opção de parear pelo número. Garantias:

- **Curto e de uso único**: vale até 10 minutos ou até o primeiro pareamento concluído; `DELETE` no mesmo endereço revoga.
- **Sem token da instância**: a página só vê o estado, o QR e o código de pareamento. O token do link fica no
  fragmento da URL (`#t=`), que o navegador não envia ao servidor; a página o retira da barra de endereço.
- **Assinado**: HMAC-SHA256 com a chave `data/pairing_key`, conferido antes de qualquer consulta, e registrado no
  banco para poder ser revogado.
- **CSP estrita**: só scripts do próprio servidor, `Referrer-Policy: no-referrer`, sem cache.

### Embutir no seu produto

Por padrão a página **não** pode ser embutida (`frame-ancestors 'none'`). Libere as origens do seu produto:

```yaml
# litezap.yaml
public_url: https://zap.seu-dominio.com
pairing_frame_ancestors:
  - https://app.seu-produto.com
  - https://*.seu-produto.com
```

E use o web component (ou um `<iframe>` comum):

```html
<script src="https://zap.seu-dominio.com/pair/embed.js" defer></script>
<litezap-pair url="https://zap.seu-dominio.com/pair/#t=pl1...."></litezap-pair>
<script>
  document.addEventListener('litezap-paired', () => location.reload());
  document.addEventListener('litezap-expired', () => alert('Link expirou, gere outro.'));
</script>
```

O componente dispara `litezap-state` (com `detail.state`), `litezap-paired` e `litezap-expired`. As mensagens não
carregam dado sensível; a confirmação definitiva vem pelo evento `instance.paired` no seu webhook.

## Chaves de API com escopo

O token da instância (`lzi_`) pode tudo naquela instância. Para entregar acesso a um terceiro, crie uma chave
(`lzk_`) com escopo, que o próprio cliente também pode criar com o token dele:

```bash
curl -X POST https://zap.seu-dominio.com/v1/instances/inst_7f3a9c2e5b1d4086/api-keys \
  -H "Authorization: Bearer $TOKEN_DA_INSTANCIA" -H "Content-Type: application/json" \
  -d '{"name":"dashboard","scopes":["read"],"ip_allowlist":["203.0.113.0/24"],"expires_in_seconds":2592000}'
```

| Escopo | Pode | Não pode |
|---|---|---|
| `read` | `GET` da instância, mensagens, fila, chats, contatos, mídia, grupos, perfil, privacidade, eventos (SSE), uso | QR, configurações, hook de agente, convites de grupo, qualquer escrita |
| `send` | `POST /messages` e `GET /messages/{msg_id}` | todo o resto |

Chaves nunca acessam `/mcp`, webhooks, ciclo de vida (start/stop/logout) nem a gestão de chaves. Uma chave também respeita a `ip_allowlist` da instância; no stream SSE ela nunca recebe `instance.qr` nem `instance.pair_code`, e um stream aberto é encerrado em até um heartbeat (15s) quando a chave é revogada, expira ou sai da allowlist. Rotacionar o token da instância revoga as chaves criadas com ele (as criadas pelo admin continuam).
Os tokens existentes não mudam.

## Allowlist de IP

- **Por instância**: `PATCH /v1/instances/{id}` com `{"ip_allowlist": ["203.0.113.7", "10.0.0.0/8"]}` (token admin).
- **Por chave**: `ip_allowlist` na criação da chave.
- **Tokens admin e operador**: `admin_allowed_ips` e `operator_allowed_ips` na configuração.

Fora da lista, a resposta é `403 forbidden` antes de qualquer rota. Atrás de um proxy reverso, declare-o em
`trusted_proxies`: só então o `X-Forwarded-For` é lido, da direita para a esquerda, parando no primeiro endereço que
não é um proxy confiável (um cliente não consegue forjar o próprio IP mandando o cabeçalho). Veja
[proxy reverso](/docs/self-hosted/proxy-reverso/).

## Dimensionamento

Cada número conectado é um processo worker. Veja [dimensionamento](/docs/self-hosted/dimensionamento/) para estimar
quantas instâncias cabem por máquina; `litezap_worker_memory_rss_bytes` no `/metrics` (Linux) mostra a memória de cada um.
