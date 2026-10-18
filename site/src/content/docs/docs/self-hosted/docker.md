---
title: Docker e Compose
description: Imagem mínima do LiteZap com binário estático, usuário não root e volume de dados.
---

O repositório traz um `Dockerfile` multi-stage e um `docker-compose.yml` na raiz.

## A imagem

- **Build**: `golang:1.26-alpine`, `CGO_ENABLED=0` (o SQLite usado, `modernc.org/sqlite`, é Go puro).
- **Runtime**: `gcr.io/distroless/static-debian12:nonroot`: só o binário, certificados CA e `/tmp`. Sem shell, sem
  gerenciador de pacotes. Cerca de 30 MB.
- Roda como usuário `nonroot` (UID 65532).
- Dados em `/data` (volume). `LITEZAP_DATA_DIR=/data` e `LITEZAP_LISTEN_ADDR=0.0.0.0:8787` já vêm definidos.
- Config opcional em `/etc/litezap/litezap.yaml`.

## Subir com Compose

```bash
git clone https://github.com/douglasdf99/litezap.git && cd litezap
export LITEZAP_ADMIN_TOKEN="$(openssl rand -hex 24)"     # guarde este valor
export LITEZAP_PUBLIC_URL="https://api.seu-dominio.com"  # se for usar URLs assinadas
docker compose up -d --build
docker compose logs -f litezap
```

O `docker-compose.yml`:

```yaml
services:
  litezap:
    build: .
    image: litezap:local
    restart: unless-stopped
    init: true                      # tini como PID 1: repassa sinais e recolhe órfãos
    ports:
      - "127.0.0.1:8787:8787"       # só no loopback do host; exponha via proxy reverso
    environment:
      LITEZAP_PUBLIC_URL: ${LITEZAP_PUBLIC_URL:-}
      LITEZAP_ADMIN_TOKEN: ${LITEZAP_ADMIN_TOKEN:-}
      LITEZAP_DEFAULT_PROXY_URL: ${LITEZAP_DEFAULT_PROXY_URL:-}
    volumes:
      - litezap-data:/data
      # - ./litezap.yaml:/etc/litezap/litezap.yaml:ro
    stop_grace_period: 30s

volumes:
  litezap-data:
```

Variáveis vazias são ignoradas pelo LiteZap. Se você não definir `LITEZAP_ADMIN_TOKEN`, o token é gerado no volume;
como a imagem não tem shell, leia-o com:

```bash
docker compose cp litezap:/data/admin_token - | tar -xO
```

## Só com `docker`

```bash
docker build -t litezap .
docker run -d --name litezap --init --restart unless-stopped \
  -p 127.0.0.1:8787:8787 \
  -e LITEZAP_ADMIN_TOKEN=... -e LITEZAP_PUBLIC_URL=https://api.seu-dominio.com \
  -v litezap-data:/data \
  litezap
```

## O que muda dentro do container

Dentro do container o LiteZap escuta em `0.0.0.0`, que **não é loopback**. Por isso:

- `/healthz` e `/readyz` exigem o token admin. A imagem não define `HEALTHCHECK` (não há shell nem `curl`); monitore
  de fora com `curl -H "Authorization: Bearer $ADMIN" https://api.seu-dominio.com/readyz`.
- `?signed=1` (URLs assinadas de mídia) exige `LITEZAP_PUBLIC_URL`; sem ela, responde `409 conflict`.

## Atualizar e fazer backup

```bash
git pull && docker compose up -d --build     # sessões e fila ficam no volume
```

Para backup, pare o serviço e copie o volume inteiro (veja [segurança](/docs/self-hosted/seguranca/#backups)):

```bash
docker compose stop litezap
docker run --rm -v litezap_litezap-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/litezap-data-$(date +%F).tgz -C /data .
docker compose start litezap
```

O nome do volume tem o prefixo do projeto Compose (normalmente o nome da pasta); confira com `docker volume ls`.

:::note
Mídia passa entre supervisor e workers por arquivos em `/data`, e cada worker é um processo no mesmo container. Não
separe supervisor e workers em containers diferentes.
:::
