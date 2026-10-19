---
title: App local
description: LiteZap no seu computador, com janela própria no macOS (ou ícone na bandeja e painel no navegador), usando o seu IP e sem nuvem.
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
| Como abre | macOS: janela própria e ícone no Dock, painel já logado. Windows/Linux: ícone na bandeja, painel no navegador já logado | Comando no terminal; painel pedindo o token admin |
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

Um executável que sobe o motor no próprio processo e mostra o painel já logado.

- **macOS: janela própria.** Abrir o LiteZap abre uma janela com o painel (sem navegador) e o ícone no Dock. Enquanto
  o motor sobe, a janela mostra "Iniciando o LiteZap…"; se ele não subir (porta ocupada, por exemplo), mostra o erro
  e o botão **Ver log**.
- **Fechar a janela não encerra o LiteZap**: o motor continua rodando (WhatsApp conectado, MCP funcionando). Clique no
  ícone do Dock, no ícone da barra de menus ou abra o app de novo para ver a janela (roda uma instância só por pasta
  de dados). **Sair do LiteZap** (⌘Q, Dock ou barra de menus) encerra as instâncias com calma e fecha o app.
- Na janela, links para outros sites abrem no navegador padrão; confirmações, seleção de arquivos e downloads
  (pergunta onde salvar) funcionam normalmente. **Visualizar > Recarregar painel** (⌘R) recarrega com um login novo
  (use se o painel pedir o token); **Visualizar > Abrir no navegador** abre o mesmo painel no navegador.
- **Windows e Linux**: por enquanto só o ícone na bandeja; **Abrir painel** abre o painel no navegador, já logado.

- **Menu da barra de menus/bandeja**: estado (`N de M conectada(s)`), Abrir painel (no macOS, mostra a janela),
  Abrir no navegador (macOS), Abrir pasta de dados, Ver log, Conectar a IA, Sair
  (encerra as instâncias com calma antes de sair).
- **Login em um clique**: cada "Abrir painel" gera um código de uso único que vale 1 minuto e abre
  `http://127.0.0.1:8787/app/#code=...`. O código vai no fragmento da URL (não chega a logs), o painel o apaga da barra
  de endereço e o troca pelo token admin. Essa troca só existe no app de bandeja, só em loopback e com proteção contra
  DNS rebinding.
- `litezap-app --no-browser` sobe sem mostrar o painel (no macOS, com a janela fechada).
- API e painel em `127.0.0.1:8787`. Se a porta estiver ocupada (outro LiteZap aberto?), a janela e o menu mostram o erro.

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
| macOS | `go build -o litezap-app ./cmd/litezap-app` ou `cmd/litezap-app/package-macos.sh` (gera `dist/LiteZap.app` com janela, ícone no Dock e na barra de menus; `UNIVERSAL=1` junta arm64 e amd64, `DMG=1` gera o `.dmg`) | Precisa de CGO e das Xcode Command Line Tools (`xcode-select --install`). Sem assinatura: na primeira vez, botão direito > Abrir. |
| Windows | `GOOS=windows go build -ldflags "-H windowsgui" -o litezap-app.exe ./cmd/litezap-app` | Sem CGO; bandeja + navegador (sem janela própria ainda); `-H windowsgui` evita a janela de console. **Não testado no Windows ainda.** |
| Linux | `CGO_ENABLED=0 go build -o litezap-app ./cmd/litezap-app` | Sem CGO; bandeja + navegador. A bandeja usa DBus (StatusNotifierItem); no GNOME, instale a extensão AppIndicator. |

O `litezap` principal continua sem CGO; só o app usa a biblioteca de bandeja e, no macOS, a janela
(`internal/appwindow`: Cocoa e WebKit do sistema via CGO, sem Electron).

## Conectar a IA (Claude Desktop, Claude Code, Cursor)

### Extensão do Claude Desktop (recomendado)

No Claude Desktop, o caminho recomendado é a extensão `LiteZap.mcpb` (formato [MCP Bundle](https://github.com/modelcontextprotocol/mcpb)):
**Conectar a IA > Claude Desktop > Instalar extensão (recomendado)**, no painel ou no menu do ícone. O app grava uma
cópia da extensão na pasta de cache do usuário e a abre; o Claude Desktop mostra a tela de instalação. Ela também é
publicada em cada versão: [`LiteZap.mcpb`](https://github.com/douglasdf99/litezap/releases/latest/download/LiteZap.mcpb)
(macOS e Windows; dois cliques para instalar).

A extensão não traz uma cópia das ferramentas: o binário dela (`litezap-mcpb`, sem CGO, poucos MB) só encontra o app
instalado e roda `<app> mcp`, então as ferramentas são sempre as da versão do app. Ordem de busca:

1. `LITEZAP_APP_EXE` (a cópia gerada pelo app já aponta para ele);
2. o arquivo `app-exe` da pasta de dados, que o app atualiza a cada abertura;
3. os lugares de costume: `/Applications/LiteZap.app` e `~/Applications` (macOS, e depois o Spotlight pelo bundle id
   `dev.litezap.app`, só em `/Applications` ou `~/Applications` e conferindo o `Info.plist`); `%LOCALAPPDATA%\Programs\LiteZap`, `%LOCALAPPDATA%\LiteZap` e `%ProgramFiles%\LiteZap` (Windows; uma cópia em Documentos é achada pelo `app-exe`).

Em qualquer caso, a extensão só executa um arquivo chamado `litezap-app` (`LiteZap.exe` no Windows) que nem ele nem a
pasta dele possam ser alterados por outros usuários (macOS e Linux). No Windows, o app roda num *job object* do shim:
quando o Claude encerra a extensão, o `LiteZap.exe mcp` encerra junto.

Sem o app, a extensão sai com "o app LiteZap não foi encontrado" (aparece no log do Claude). O ZIP do Windows traz o
`LiteZap.mcpb` ao lado do `LiteZap.exe`; no macOS ele fica em `LiteZap.app/Contents/Resources`.

### Arquivo de configuração

No menu do ícone, **Conectar a IA** tem um item por cliente; o painel tem a mesma coisa na aba **Conectar a IA** (no
Claude Desktop, como **Configurar manualmente (arquivo de config)**). Um
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
- Depois de conectar, reinicie o cliente de IA (no Claude Code, abra uma sessão nova; no Cursor, recarregue a
  janela).
- **Claude Desktop aberto.** Ele guarda a configuração na memória e a regrava ao fechar, apagando uma entrada gravada
  enquanto estava aberto. O app detecta o Claude aberto (macOS: processo `Claude.app/Contents/MacOS/Claude`, bundle
  `com.anthropic.claudefordesktop`; Windows: `Claude.exe`), avisa "O Claude está aberto: feche com Cmd+Q e abra de
  novo para ver as ferramentas do LiteZap" e mostra **conectado (reinicie o Claude)** até o Claude fechar. Enquanto
  isso, vigia o arquivo: se o Claude, ao fechar, apagar a entrada, o app a grava de novo (só a chave `litezap`, no
  máximo 3 vezes) e avisa. A vigia acaba quando o Claude abre de novo, 2 minutos depois de ele fechar, ou em 30
  minutos.

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
