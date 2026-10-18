---
title: Proxy por instância
description: IP de saída próprio por número, com HTTP, HTTPS ou SOCKS5.
---

Vários números atrás do mesmo IP de datacenter é um dos sinais que mais derrubam contas. O LiteZap permite que cada
instância saia por um **proxy próprio**.

## Como funciona

- `settings.proxy_url` por instância: `http://`, `https://` ou `socks5://`, com usuário e senha opcionais.
- `default_proxy_url` na config vale para toda instância sem proxy próprio.
- Vale para a conexão do WhatsApp e para upload e download de mídia.
- **Só o token admin altera** o proxy, porque ele decide o IP de saída.
- Trocar o proxy **reinicia o worker** da instância.

## Garantias

- **Fixo por sessão**: o motor nunca troca o IP sozinho. Mudar de IP com frequência é suspeito.
- **Sem vazamento**: se o proxy não responde, a instância fica `reconnecting` com `state_reason: proxy_unreachable`.
  Ela nunca cai para a conexão direta.
- **Sem herança do ambiente**: variáveis `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` e `NO_PROXY` do sistema não chegam
  aos workers. O proxy é passado ao worker por variável de ambiente, não por argumento (que apareceria no `ps`).
- **Senha protegida**: `GET /settings` mostra o proxy com a senha mascarada.

## Configurar

```bash
# proxy próprio (token admin)
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $ADMIN" \
  -d '{"proxy_url":"socks5://usuario:senha@proxy.exemplo.com:1080"}'

# voltar ao default_proxy_url
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $ADMIN" \
  -d '{"proxy_url":""}'
```

O LiteZap não fornece IPs, só usa os proxies que você configurar. No modo local, o padrão é a conexão da sua
própria máquina. Prefira proxies residenciais ou móveis estáveis, um número por IP.
