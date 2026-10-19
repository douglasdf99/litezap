---
title: Configuração
description: Arquivo litezap.yaml, variáveis de ambiente e padrões de envio por instância.
---

A configuração vem de um arquivo YAML (padrão `litezap.yaml` no diretório atual, ou `--config <caminho>`) e de
variáveis `LITEZAP_*`, que **têm precedência** sobre o arquivo. Arquivo ausente significa só padrões + variáveis.
`LITEZAP_CONFIG_JSON` aceita o documento inteiro em JSON (as mesmas chaves do YAML), aplicado depois do arquivo.

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
| `webhook_allow_private_urls` | `LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS` | `false` | Libera webhooks para `127.0.0.1`, rede local etc. Só para uso local. |
| `webhook_retention_days` | `LITEZAP_WEBHOOK_RETENTION_DAYS` | `7` | Dias que entregas de webhook concluídas ficam guardadas. |
| `webhook_failed_retention_days` | `LITEZAP_WEBHOOK_FAILED_RETENTION_DAYS` | `30` | Dias que a dead-letter fica guardada. |
| `disable_dashboard` | `LITEZAP_DISABLE_DASHBOARD` | `false` | `true` desliga o painel web em `/app/`; a API continua igual. |
| `instance_defaults` | `LITEZAP_INSTANCE_DEFAULTS_JSON` | veja abaixo | Padrões de envio de toda instância. |
| `message_retention_days` | `LITEZAP_MESSAGE_RETENTION_DAYS` | `0` | Apaga de hora em hora mensagens mais velhas que N dias, a mídia delas e os envios concluídos. `0`: guarda para sempre. |
| `media_retention_days` | `LITEZAP_MEDIA_RETENTION_DAYS` | `0` | Apaga a cópia local de um anexo N dias depois de baixada. `0`: nunca. |
| `zombie_timeout` | `LITEZAP_ZOMBIE_TIMEOUT` | `0` | Watchdog de conexão (ex.: `5m`, mínimo `30s`). Veja [Observabilidade](/docs/self-hosted/observabilidade/#watchdog-de-conexão). `0`: desligado. |
| `shutdown_timeout` | `LITEZAP_SHUTDOWN_TIMEOUT` | `0` | Prazo total do desligamento gracioso (ex.: `25s` para caber num `docker stop -t 30`). `0`: fases padrão, até ~40s. |
| `ops_listen_addr` | `LITEZAP_OPS_LISTEN_ADDR` | vazio | Endereço privado com `/healthz`, `/readyz` e `/metrics` sem token. Nunca exponha à internet. |
| `trusted_proxies` | `LITEZAP_TRUSTED_PROXIES` | vazio | IPs/CIDRs dos proxies reversos cujo `X-Forwarded-For` vale para as allowlists de IP (lista separada por vírgula na variável). Vazio: o cabeçalho é ignorado. |
| `admin_allowed_ips` | `LITEZAP_ADMIN_ALLOWED_IPS` | vazio | IPs/CIDRs de onde o token admin é aceito. Vazio: qualquer. |
| `operator_allowed_ips` | `LITEZAP_OPERATOR_ALLOWED_IPS` | vazio | Idem para o token do operador. |
| `pairing_frame_ancestors` | `LITEZAP_PAIRING_FRAME_ANCESTORS` | vazio | Origens (`https://app.exemplo.com`, `https://*.exemplo.com`) que podem embutir a página de pareamento num iframe. Vazio: não pode ser embutida. |

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

# Webhooks (marco 3)
webhook_allow_private_urls: false
webhook_retention_days: 7
webhook_failed_retention_days: 30

# Painel web em /app/ (pede o token admin). true desliga o painel; a API continua.
disable_dashboard: false

# Plataforma e parceiros (marco 11)
trusted_proxies: ["127.0.0.1"]        # proxy reverso na mesma máquina
admin_allowed_ips: []                 # vazio = qualquer IP
pairing_frame_ancestors: []           # ex.: ["https://app.seu-produto.com"]

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
