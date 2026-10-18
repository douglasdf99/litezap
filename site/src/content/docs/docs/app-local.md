---
title: App local
description: LiteZap no seu computador, com painel web e ícone na bandeja, usando o seu IP e sem nuvem.
---

<span class="lz-badge lz-badge--ok">Disponível · v1</span>

O app local roda o mesmo motor no seu computador. A API e o painel ficam só em `127.0.0.1`, as sessões ficam numa
pasta sua e o WhatsApp vê a conexão da sua própria internet, como um WhatsApp Web comum.

Há duas formas:

| | `litezap-app` (bandeja) | `litezap serve` (terminal) |
|---|---|---|
| Para quem | Quem não quer terminal | Desenvolvedores, servidores |
| Como abre | Ícone na bandeja/barra de menus, painel já logado | Comando no terminal; painel pedindo o token admin |
| Pasta de dados | Pasta do usuário (abaixo) | `./data` ou `data_dir` |
| Painel em `/app/` | Sim | Sim (desligável) |

## Painel web (`/app/`)

`litezap serve` e o app de bandeja servem um painel embutido no binário (sem Node nem arquivos extras) em
`http://127.0.0.1:8787/app/`; `/` redireciona para lá. Ele usa a própria API com o token admin:

- **Instâncias**: estado ao vivo (SSE), criar (o token da instância aparece uma vez), iniciar, parar, reiniciar,
  deslogar e excluir.
- **Parear**: QR que se atualiza sozinho e pareamento por código no telefone.
- **Conversas**: chats, histórico e envio de texto com status ao vivo (na fila → enviada → entregue → lida). Quando
  um envio fica na fila, o painel explica por quê: carência após conectar, ritmo, rampa ou janela de envio (com o
  horário atual no fuso da instância).
- **Configurações**: ritmo, rampa, janela, fuso, proxy e ritmo inseguro.
- **Webhooks**: criar (o segredo aparece uma vez), ligar/desligar, excluir, ver entregas e reenviar as da
  dead-letter; também os webhooks globais.
- **Eventos ao vivo**: os mesmos eventos dos webhooks, com o JSON de cada um.

**Login**: no `litezap serve`, cole o token admin (`data/admin_token`). Ele fica só no navegador (`sessionStorage`,
ou `localStorage` se marcar "lembrar"), vai apenas no cabeçalho `Authorization` e "Sair" o apaga. Não marque
"lembrar" em computador compartilhado. O painel não usa cookies e tem CSP estrita.

Num servidor, desligue o painel com `disable_dashboard: true` (ou `LITEZAP_DISABLE_DASHBOARD=true`), ou só o exponha
por HTTPS: o token admin trafega em cada requisição.

## App de bandeja (`litezap-app`)

Um executável que sobe o motor no próprio processo, põe o ícone do LiteZap na bandeja e abre o painel já logado.

- **Menu**: estado (`N de M conectada(s)`), Abrir painel, Abrir pasta de dados, Ver log, Sair (encerra as instâncias
  com calma antes de sair).
- **Login em um clique**: cada "Abrir painel" gera um código de uso único que vale 1 minuto e abre
  `http://127.0.0.1:8787/app/#code=...`. O código vai no fragmento da URL (não chega a logs), o painel o apaga da barra
  de endereço e o troca pelo token admin. Essa troca só existe no app de bandeja, só em loopback e com proteção contra
  DNS rebinding.
- `litezap-app --no-browser` sobe sem abrir o navegador.
- API e painel em `127.0.0.1:8787`. Se a porta estiver ocupada (outro LiteZap aberto?), o menu mostra o erro.

### Pasta de dados

| Sistema | Pasta |
|---|---|
| macOS | `~/Library/Application Support/LiteZap` |
| Windows | `%APPDATA%\LiteZap` |
| Linux | `$XDG_DATA_HOME/litezap` ou `~/.local/share/litezap` |

Dentro dela: `data/` (sessões, banco, `admin_token`), `litezap.log` e, se quiser, um `litezap.yaml` com as mesmas
chaves do servidor (variáveis `LITEZAP_*` continuam valendo). `LITEZAP_APP_DIR` troca a pasta.

### Compilar e rodar

Ainda não há instaladores: compile a partir do código (Go na versão do `go.mod`).

| Sistema | Comando | Observações |
|---|---|---|
| macOS | `go build -o litezap-app ./cmd/litezap-app` ou `cmd/litezap-app/package-macos.sh` (gera `dist/LiteZap.app`, só na barra de menus) | Precisa de CGO e das Xcode Command Line Tools (`xcode-select --install`). Sem assinatura: na primeira vez, botão direito > Abrir. |
| Windows | `GOOS=windows go build -ldflags "-H windowsgui" -o litezap-app.exe ./cmd/litezap-app` | Sem CGO; `-H windowsgui` evita a janela de console. **Não testado no Windows ainda.** |
| Linux | `CGO_ENABLED=0 go build -o litezap-app ./cmd/litezap-app` | Sem CGO. A bandeja usa DBus (StatusNotifierItem); no GNOME, instale a extensão AppIndicator. |

O `litezap` principal continua sem CGO; só o app de bandeja usa a biblioteca de bandeja.

## Usar com IA no mesmo computador

Com o app aberto, conecte o Claude Desktop ou o Claude Code pelo [MCP](/docs/mcp/) apontando para
`http://127.0.0.1:8787` e o token de uma instância (criada no painel).

## Cuidados

- **Computador ligado.** Se a máquina dorme ou desliga, o número fica offline; mensagens na fila saem quando o
  LiteZap voltar (respeitando `expires_after`).
- **Não exponha a porta.** Para acesso de fora, vá para o [self-hosted](/docs/self-hosted/) com TLS.
- **Backup da pasta de dados.** Ela guarda as sessões; veja [backup](/docs/self-hosted/backup-e-upgrade/).

## Em breve

<span class="lz-badge lz-badge--soon">Em breve</span>

- Instaladores (`.dmg`, `.msi`, `.deb`) e ícone do `.app`.
- Assinatura de código e notarização (macOS) para abrir sem aviso.
- Atualização automática e iniciar com o sistema.
- Túnel seguro opcional para receber chamadas de sistemas na nuvem.
- Envio de mídia pelo painel e login no painel com token de instância.
