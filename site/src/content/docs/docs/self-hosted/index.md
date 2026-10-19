---
title: Instalação self-hosted
description: Rodar o LiteZap no seu próprio servidor ou VPS.
sidebar:
  label: Visão geral
---

O LiteZap é um **binário único**, sem dependências externas: o banco é SQLite embutido e não precisa de CGO. Para
rodar num servidor você só precisa do binário, de uma pasta de dados e, se a API for acessada pela internet, de um
proxy reverso com TLS.

## Requisitos

- Linux (amd64 ou arm64) é o alvo principal; macOS também funciona. Windows compila, mas ainda não foi testado.
- Memória: o supervisor é leve; cada número conectado é um processo worker (limite padrão de 512 MB por worker,
  normalmente bem menos).
- Disco para `data/` (sessões, banco e mídia recebida).
- Saída para a internet (diretamente ou pelos proxies configurados).

## Escolha um caminho

| Caminho | Para quem |
|---|---|
| [Docker / Compose](/docs/self-hosted/docker/) | O jeito mais rápido num VPS. Imagem mínima, usuário não root, volume `/data`. |
| Binário + [systemd](/docs/self-hosted/systemd/) | Servidores sem Docker, controle total do sistema. |

Nos dois casos, coloque um [proxy reverso com TLS](/docs/self-hosted/proxy-reverso/) na frente e leia as
[notas de segurança](/docs/self-hosted/seguranca/).

## Compilar do código-fonte

Requer a versão do Go indicada no `go.mod`.

```bash
git clone https://github.com/douglasdf99/litezap.git && cd litezap
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.2.0" -o litezap ./cmd/litezap
./litezap version
```

Para outra plataforma, defina `GOOS` e `GOARCH` (ex.: `GOOS=linux GOARCH=arm64`). O binário é estático e pode ser
copiado direto para o servidor.

## Rodar

```bash
./litezap serve                              # lê ./litezap.yaml se existir
./litezap serve --config /etc/litezap/litezap.yaml
```

Sem arquivo de config, valem os padrões e as variáveis `LITEZAP_*`. Veja [configuração](/docs/self-hosted/configuracao/).
Na subida, o log mostra o endereço, a pasta de dados e onde está o token admin:

```
level=INFO msg="litezap listening" addr=127.0.0.1:8787 data_dir=/var/lib/litezap/data admin_token_file=/var/lib/litezap/data/admin_token
```

O processo encerra de forma graciosa com `SIGTERM` ou Ctrl+C: para a fila (envios em andamento ganham até 10s),
desconecta os workers e fecha o banco. Um segundo Ctrl+C encerra na hora.

## Pasta de dados

Tudo o que o LiteZap guarda fica em `data_dir` (padrão `./data`, sempre resolvido para caminho absoluto):

```
data/
├── admin_token            # token admin gerado (0600), se você não definiu um
├── media_key              # segredo das URLs assinadas (0600)
├── litezap.db             # banco SQLite (WAL): instâncias, fila, chats, mensagens, cache de números
├── instances/<id>/        # sessão do WhatsApp de cada instância (session.db)
├── media/<id>/            # mídia recebida já baixada
├── outbox/<id>/           # anexos aguardando envio
└── run/<id>.pid           # PID de cada worker em execução
```

Os workers conversam com o supervisor por sockets Unix em `$TMPDIR/lz-<hash>/`. Não rode duas cópias do LiteZap
sobre a mesma pasta de dados, nem a mesma sessão em duas máquinas ao mesmo tempo.

:::caution[Limpeza de mídia desligada por padrão]
Mídia recebida baixada fica em `data/media/` para sempre, a menos que `media_retention_days` esteja definido (e
`message_retention_days` para o log). Veja [Configuração](/docs/self-hosted/configuracao/) e acompanhe o uso de disco.
:::
