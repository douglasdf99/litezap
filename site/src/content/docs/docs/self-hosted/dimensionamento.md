---
title: Dimensionamento
description: Como estimar memória, CPU e disco para o número de instâncias que você vai rodar.
---

O LiteZap roda um processo supervisor e **um processo worker por número conectado**. O custo cresce quase
linearmente com o número de instâncias.

## O que consome recursos

| Recurso | Quem usa | Observação |
|---|---|---|
| Memória | Cada worker (sessão do WhatsApp, criptografia, buffers) | Limitada por `worker_memory_max_mb` (padrão 512 MB): acima disso o worker é reiniciado. O uso real costuma ficar bem abaixo do limite, mas varia com o volume de mensagens e o tamanho do histórico. |
| CPU | Criptografia de mensagens e mídia, sincronização de histórico | Picos no pareamento (history sync) e em mídia grande. |
| Disco | `litezap.db` (log de mensagens), sessões, mídia recebida baixada | A mídia baixada não tem limpeza automática ainda; o log de mensagens cresce sem limite por padrão. |
| Rede | WebSocket por número, mídia, webhooks | Um proxy por instância distribui a saída. |

## Meça no seu cenário

Não publicamos números de capacidade sem medição com carga real. Para medir:

```bash
# memória (RSS, em KB) de cada worker
ps -o pid,rss,args -C litezap | grep worker

# tamanho da pasta de dados
du -sh data/ data/media/ data/litezap.db*
```

Comece com poucas instâncias, observe uma semana com o seu volume real e extrapole com folga de 50%.

## Regras práticas

- Reserve memória para o pior caso: `instâncias × worker_memory_max_mb` não deve passar muito da RAM da máquina. Se
  passar, reduza o limite ou divida em mais máquinas.
- Use SSD: SQLite em WAL faz muitas escritas pequenas.
- Monitore o disco de `data/media/`, ou ligue a limpeza automática com `media_retention_days` (e
  `message_retention_days` para o log).
- Vários números numa máquina: use um [proxy por instância](/docs/conceitos/proxy/) para não concentrar todos no
  mesmo IP.
- Uma máquina por cliente (ou um projeto da nuvem LiteZap, quando estiver disponível) isola falhas e reputação de IP.
