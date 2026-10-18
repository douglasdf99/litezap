---
title: Configuração
description: Arquivo litezap.yaml, variáveis de ambiente e padrões de envio por instância.
---

A configuração vem de um arquivo YAML (padrão `litezap.yaml` no diretório atual, ou `--config <caminho>`) e de
variáveis `LITEZAP_*`, que **têm precedência** sobre o arquivo. Arquivo ausente significa só padrões + variáveis.

## Chaves

| Chave | Variável | Padrão | Descrição |
|---|---|---|---|
| `listen_addr` | `LITEZAP_LISTEN_ADDR` | `127.0.0.1:8787` | Endereço da API. Fora de loopback, `/healthz` e `/readyz` passam a exigir o token admin. |
| `public_url` | `LITEZAP_PUBLIC_URL` | vazio | URL pública (ex.: `https://api.seu-dominio.com`), usada nas URLs assinadas de mídia. Vazio usa `http://<listen_addr>`. |
| `data_dir` | `LITEZAP_DATA_DIR` | `data` | Pasta de dados. |
| `admin_token` | `LITEZAP_ADMIN_TOKEN` | gerado | Token admin. Vazio: gerado em `data/admin_token`. |
| `worker_memory_max_mb` | `LITEZAP_WORKER_MEMORY_MAX_MB` | `512` | Limite de memória de cada worker. |
| `default_proxy_url` | `LITEZAP_DEFAULT_PROXY_URL` | vazio | Proxy padrão para instâncias sem `proxy_url` (http, https ou socks5). Vazio: conexão direta. |
| `media_allow_private_urls` | `LITEZAP_MEDIA_ALLOW_PRIVATE_URLS` | `false` | Libera `media.url` para IPs privados e loopback. Só para uso local. |
| `instance_defaults` | só no arquivo | veja abaixo | Padrões de envio de toda instância. |

## Exemplo completo

```yaml
# litezap.yaml — variáveis LITEZAP_* sobrescrevem estes valores.
listen_addr: 127.0.0.1:8787
public_url: "https://api.seu-dominio.com"
data_dir: /var/lib/litezap/data
# admin_token: vazio = gerado em data/admin_token na primeira execução
worker_memory_max_mb: 512

default_proxy_url: ""
media_allow_private_urls: false

# Padrões de envio por instância (cada instância pode sobrescrever via PUT /settings).
# Pisos: min_interval_ms >= 1000 e max_per_minute <= 60, salvo unsafe_pacing: true.
instance_defaults:
  min_interval_ms: 3000
  jitter_ms: 2000
  max_per_minute: 20
  typing_simulation: true
  ramp:
    enabled: true
    start_per_day: 50
    end_per_day: 150
    days: 14
  send_window:
    start: "08:00"
    end: "20:00"
  timezone: "America/Sao_Paulo"   # vazio = fuso do servidor
```

O repositório traz `litezap.example.yaml` como ponto de partida.

## Padrões × configurações da instância

Cada instância guarda só os campos que você alterou com `PUT /settings`. O valor efetivo é
`instance_defaults` + overrides da instância. Assim, mudar um padrão na config vale para todas as instâncias que não
o sobrescreveram (depois de reiniciar o `serve`). O proxy padrão vem de `default_proxy_url`, nunca de
`instance_defaults`.

A config é validada na subida: valores fora dos limites (ex.: `min_interval_ms: 500` sem `unsafe_pacing`), fuso
inválido ou proxy com esquema errado impedem o servidor de iniciar, com a mensagem do problema.
