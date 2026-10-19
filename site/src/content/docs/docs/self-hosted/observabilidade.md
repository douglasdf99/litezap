---
title: Observabilidade
description: Logs, healthz, readyz, eventos de estado e métricas.
---

## Logs

O LiteZap escreve logs estruturados (`log/slog`, formato texto) no stderr, com `instance_id` em toda linha de
instância. `LITEZAP_LOG_FORMAT=json` troca os logs (supervisor e workers) para JSON por linha. Com systemd, use `journalctl -u litezap -f`; com Docker, `docker compose logs -f litezap`.

Linhas que valem alerta:

| Mensagem | Significado |
|---|---|
| `killing orphan worker` | Na subida, um worker de uma execução anterior foi encerrado. |
| `requeued sends interrupted by the last shutdown` | Envios interrompidos voltaram à fila. |
| `webhook: delivery moved to the dead-letter` | Uma entrega desistiu depois de 24h. |
| `request failed` | Erro inesperado numa requisição (resposta `500 internal`). |

## Saúde

| Rota | Responde | Uso |
|---|---|---|
| `GET /healthz` | `200 {"status":"ok"}` enquanto o processo vive | liveness |
| `GET /readyz` | `200 {"status":"ready"}` com o banco acessível; `503` caso contrário | readiness, balanceador |

Ambas são públicas quando o servidor escuta em loopback e exigem o token admin em qualquer outro endereço:

```bash
curl -s 127.0.0.1:8787/readyz
curl -s -H "Authorization: Bearer $ADMIN" https://api.seu-dominio.com/readyz
```

## Estado das instâncias

- `GET /v1/instances` (admin) mostra `state` e `state_reason` de todas.
- O evento [`instance.status`](/docs/eventos/instance-status/) num webhook global é a forma mais simples de alertar
  quando um número cai, é deslogado ou banido.

## Métricas

`GET /metrics` no formato Prometheus. Na porta da API exige sempre o token admin (mesmo em loopback); com
`ops_listen_addr` (ex.: `[::]:9090` numa rede privada), `/healthz`, `/readyz` e `/metrics` respondem sem token
nesse endereço, para o health check da plataforma e o coletor. Não exponha esse endereço à internet.

```bash
curl -s -H "Authorization: Bearer $ADMIN" 127.0.0.1:8787/metrics
```

| Métrica | Tipo | O que mede |
|---|---|---|
| `litezap_instances{state}` | gauge | Instâncias por estado. |
| `litezap_queue_messages{status}` | gauge | Itens da fila de envio por status. |
| `litezap_queue_send_latency_seconds` | histograma | Segundos entre entrar na fila e sair enviado (inclui ritmo, agendamento e janela de envio). |
| `litezap_messages_sent_total`, `litezap_messages_received_total` | contador | Mensagens enviadas e recebidas. |
| `litezap_media_bytes_total` | contador | Bytes de mídia trafegados. |
| `litezap_webhook_deliveries{status}` | gauge | Entregas de webhook por status. |
| `litezap_instance_state_changes_total{state}` | contador | Transições de estado das instâncias. |
| `litezap_proxy_failures_total`, `litezap_proxy_unreachable_instances` | contador, gauge | Falhas de proxy e instâncias com o proxy fora do ar. |
| `litezap_sending_suspended`, `litezap_sending_suspended_instances` | gauge | Envio suspenso pelo operador (global e por instância). |
| `litezap_ready`, `litezap_draining` | gauge | Pronto para atender; em desligamento gracioso. |
| `litezap_worker_memory_rss_bytes{instance_id}` | gauge | Memória residente de cada worker (só Linux, lida de `/proc`). |
| `litezap_build_info{version}` | gauge | Versão do binário. |

Também saem as métricas básicas do runtime Go do supervisor.

## Watchdog de conexão

Com `zombie_timeout` (ex.: `5m`; mínimo `30s`; padrão `0` = desligado), um worker logado cujo estado não bate com o
socket por mais tempo que isso (`connected` sem conexão, ou preso em `keepalive_timeout` com o socket aparentemente
aberto) é forçado a reconectar, com `state_reason: zombie_watchdog`. O whatsmeow já trata os casos comuns; o watchdog
é a rede de segurança para um evento perdido ou um socket travado.

## Retenção

`message_retention_days` apaga, de hora em hora, mensagens mais velhas que o prazo, a mídia delas e os envios já
concluídos. `media_retention_days` apaga a cópia local de um anexo N dias depois de baixada; o próximo download busca
de novo enquanto o WhatsApp ainda tiver o arquivo. Os dois são `0` (guardar para sempre) por padrão. Veja
[Configuração](/docs/self-hosted/configuracao/).
