---
title: App local
description: LiteZap no seu computador, com painel web e ícone na bandeja, usando o seu IP e sem nuvem.
---

<span class="lz-badge lz-badge--ok">Disponível · v1</span>

**Quer só usar?** [Baixe o app](/docs/baixar/) para macOS, Windows ou Linux: abre com dois cliques e conecta ao
Claude em um.

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

- **Menu**: estado (`N de M conectada(s)`), Abrir painel, Abrir pasta de dados, Ver log, Conectar a IA, Sair
  (encerra as instâncias com calma antes de sair).
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

### Baixar

Cada versão publica o app pronto: [`LiteZap-macos.dmg`](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap-macos.dmg)
(universal, macOS 12+), [`LiteZap-windows.zip`](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap-windows.zip)
e `LiteZap-linux-{amd64,arm64}.tar.gz`. Instalação e primeira abertura (app sem assinatura) em [Baixar o app](/docs/baixar/).

### Compilar e rodar

Para compilar a partir do código (Go na versão do `go.mod`):

| Sistema | Comando | Observações |
|---|---|---|
| macOS | `go build -o litezap-app ./cmd/litezap-app` ou `cmd/litezap-app/package-macos.sh` (gera `dist/LiteZap.app`, só na barra de menus; `UNIVERSAL=1` junta arm64 e amd64, `DMG=1` gera o `.dmg`) | Precisa de CGO e das Xcode Command Line Tools (`xcode-select --install`). Sem assinatura: na primeira vez, botão direito > Abrir. |
| Windows | `GOOS=windows go build -ldflags "-H windowsgui" -o litezap-app.exe ./cmd/litezap-app` | Sem CGO; `-H windowsgui` evita a janela de console. **Não testado no Windows ainda.** |
| Linux | `CGO_ENABLED=0 go build -o litezap-app ./cmd/litezap-app` | Sem CGO. A bandeja usa DBus (StatusNotifierItem); no GNOME, instale a extensão AppIndicator. |

O `litezap` principal continua sem CGO; só o app de bandeja usa a biblioteca de bandeja.

## Conectar a IA (Claude Desktop, Claude Code, Cursor)

No menu do ícone, **Conectar a IA** tem um item por cliente; o painel tem a mesma coisa na aba **Conectar a IA**. Um
clique grava, na configuração do cliente, a entrada `mcpServers.litezap` apontando para o próprio executável do app
com `["mcp"]`. Não há token para copiar: `litezap-app mcp` lê o endereço e o token admin da pasta de dados (arquivo
`0600`, só do seu usuário).

| Cliente | Onde grava |
|---|---|
| Claude Desktop | macOS `~/Library/Application Support/Claude/claude_desktop_config.json`; Windows `%APPDATA%\Claude\claude_desktop_config.json`; Linux `~/.config/Claude/claude_desktop_config.json` |
| Claude Code | `claude mcp add --scope user litezap -- <app> mcp` quando o `claude` é encontrado; senão, `mcpServers` de `~/.claude.json` |
| Cursor | `~/.cursor/mcp.json` |

- Só a entrada `litezap` muda: outros servidores, ordem das chaves e formatação do resto do arquivo ficam byte a
  byte iguais. A escrita é atômica (arquivo temporário + renomear), não acontece se o arquivo mudou no meio, e um
  link simbólico continua link.
- Antes da primeira alteração, o original ganha uma cópia `<arquivo>.litezap.bak`, **uma vez só**: ela guarda o
  arquivo de antes do LiteZap, e alterações seguintes não geram nova cópia.
- No Claude Code, o `claude mcp add` só é usado quando ainda não há entrada; uma entrada antiga é corrigida no
  arquivo (assim um `add` que falhe nunca apaga a entrada que existia).
- JSON inválido: o LiteZap não toca no arquivo e mostra o erro.
- O item fica marcado quando conectado; "(atualizar)" quando a entrada aponta para outro executável (app movido), e
  um clique corrige. **Desconectar** remove só a entrada `litezap`.
- Conecte com o app num lugar fixo (Aplicativos no macOS, uma pasta como Documentos no Windows): rodando de dentro
  do `.dmg` ou do `.zip`, ou de uma pasta temporária, o app recusa e pede para movê-lo.
- Depois de conectar, reinicie o cliente de IA (no Claude Code, abra uma sessão nova).

**Com o app fechado**, `litezap-app mcp` continua respondendo ao cliente, mas as ferramentas devolvem
`app_not_running` ("abra o app LiteZap"). Ele não sobe um motor próprio: um segundo motor na mesma pasta brigaria com a
bandeja pela porta e pelas sessões do WhatsApp. Se o app nunca foi aberto (sem token), ele sai com erro. Ele também recusa um `listen_addr` que não seja local
(loopback ou `0.0.0.0`), para o token admin não trafegar sem criptografia pela rede.

Para usar o `litezap serve` (servidor) em vez do app, configure o [MCP](/docs/mcp/) com `LITEZAP_URL` e um token.

## Cuidados

- **Computador ligado.** Se a máquina dorme ou desliga, o número fica offline; mensagens na fila saem quando o
  LiteZap voltar (respeitando `expires_after`).
- **Não exponha a porta.** Para acesso de fora, vá para o [self-hosted](/docs/self-hosted/) com TLS.
- **Backup da pasta de dados.** Ela guarda as sessões; veja [backup](/docs/self-hosted/backup-e-upgrade/).

## Em breve

<span class="lz-badge lz-badge--soon">Em breve</span>

- Instaladores `.msi` e `.deb`.
- Assinatura de código e notarização (macOS) para abrir sem aviso.
- Atualização automática e iniciar com o sistema.
- Túnel seguro opcional para receber chamadas de sistemas na nuvem.
- Envio de mídia pelo painel e login no painel com token de instância.
