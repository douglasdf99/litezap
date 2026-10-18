---
title: Observabilidade
description: Logs, healthz, readyz, eventos de estado e métricas.
---

## Logs

O LiteZap escreve logs estruturados (`log/slog`, formato texto) no stderr, com `instance_id` em toda linha de
instância. Com systemd, use `journalctl -u litezap -f`; com Docker, `docker compose logs -f litezap`.

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

<span class="lz-badge lz-badge--soon">Em breve · M2b</span>

`GET /metrics` no formato Prometheus, com mensagens enviadas e recebidas, profundidade e latência da fila, restarts
de worker, estado por instância e entregas de webhook por status.

Desde o M11 também:

| Métrica | Tipo | O que mede |
|---|---|---|
| `litezap_queue_send_latency_seconds` | histograma | Segundos entre entrar na fila e sair enviado (inclui ritmo, agendamento e janela de envio). |
| `litezap_worker_memory_rss_bytes{instance_id}` | gauge | Memória residente de cada worker (só Linux, lida de `/proc`). |

## Watchdog de conexão

<span class="lz-badge lz-badge--soon">Em breve</span>

Um watchdog que força reconexão quando uma instância `connected` fica sem nenhum evento por muito tempo
(`zombie_timeout`) ainda não está implementado.
