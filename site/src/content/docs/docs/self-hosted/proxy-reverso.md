---
title: Proxy reverso com TLS
description: Expor a API pela internet com Caddy ou Nginx e HTTPS.
---

Deixe o LiteZap escutando em `127.0.0.1:8787` e coloque um proxy reverso com TLS na frente. Nunca exponha a API em
HTTP puro: os tokens trafegam no cabeçalho `Authorization`.

Defina `public_url` com o endereço público, para que as URLs assinadas de mídia apontem para ele:

```yaml
listen_addr: 127.0.0.1:8787
public_url: "https://api.seu-dominio.com"
```

## Caddy

Certificado automático (Let's Encrypt) com duas linhas. `/etc/caddy/Caddyfile`:

```txt
api.seu-dominio.com {
	# opcional: não expor as rotas de operação pela internet
	@ops path /healthz /readyz
	respond @ops 404

	reverse_proxy 127.0.0.1:8787
}
```

O Caddy não limita o tamanho do corpo por padrão e mantém conexões longas, o que atende `?wait=true` (até 2 minutos).

## Nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name api.seu-dominio.com;

    ssl_certificate     /etc/letsencrypt/live/api.seu-dominio.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.seu-dominio.com/privkey.pem;

    # envios com mídia em base64 chegam a 140 MB
    client_max_body_size 140m;

    location ~ ^/(healthz|readyz)$ {
        return 404;   # opcional: monitore pelo loopback do servidor
    }

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # ?wait=true pode segurar a resposta por até 2 minutos; uploads lentos levam mais
        proxy_read_timeout 180s;
        proxy_send_timeout 300s;
        # necessário para o stream SSE (/events)
        proxy_buffering off;
    }
}

server {
    listen 80;
    server_name api.seu-dominio.com;
    return 301 https://$host$request_uri;
}
```

## Por que bloquear `/healthz` e `/readyz`

Com `listen_addr` em loopback, o LiteZap considera essas rotas públicas (só quem está na máquina chega nelas). Atrás
de um proxy reverso, porém, qualquer pessoa na internet chegaria. Elas não revelam dados sensíveis, mas não há motivo
para expô-las: bloqueie no proxy e monitore pelo loopback do servidor (`curl 127.0.0.1:8787/readyz`).

Se você preferir que o LiteZap escute direto numa interface de rede (sem proxy reverso), ele passa a exigir o token
admin nessas rotas automaticamente. Ainda assim, use TLS.
