# LiteZap Peça #2 (v1) — App local: painel web + bandeja (plano curto)

Spec do motor: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md` (peça #2 na tabela da seção 1).

## Decisões

- **Enxuto**: o "app" é (1) um painel web embutido no binário (`go:embed`), servido por `litezap serve` em `/app/`
  (`/` redireciona), o mesmo no PC e no self-host; (2) um lançador de bandeja `litezap-app` que sobe o motor no
  próprio processo, mostra o estado e abre o navegador. Sem Electron/Wails.
- **Painel sem build**: JavaScript puro em módulos ES + CSS, versionados em `internal/dashboard/static/`. `go build`
  não precisa de Node e não há passo de build para refazer.
- **Autenticação do painel**: o painel chama a mesma API REST com `Authorization: Bearer <token admin>`. Sem cookies,
  então não há superfície de CSRF. O token fica em `sessionStorage` (ou `localStorage` se o usuário marcar
  "lembrar neste navegador"). Login padrão: colar o token (arquivo `data/admin_token`).
- **Login em um clique (só bandeja, só loopback)**: o `litezap-app` tem o token admin no próprio processo e cria um
  **código de uso único** (256 bits, vale 1 min, no máximo 8 pendentes; um resgate descarta os outros) a cada "Abrir painel"; abre
  `http://127.0.0.1:<porta>/app/#code=<código>`. O fragmento não vai ao servidor nem a logs/Referer; o painel o
  apaga da barra de endereço e troca o código pelo token em `POST /app/api/session`. O endpoint só existe quando o
  servidor escuta em loopback e o processo criou códigos; exige conexão de loopback, `Host` de loopback (contra DNS
  rebinding), `Origin` igual ao `Host` quando presente e `Content-Type: application/json` (força preflight CORS,
  que não respondemos). `litezap serve` puro não cria códigos: lá o login é colar o token.
- **Cabeçalhos**: CSP estrita (`script-src 'self'`, sem inline, `connect-src 'self'`, `img-src 'self' data: blob:`,
  `frame-ancestors 'none'`), `nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`. O JS monta o DOM
  só com `textContent`/atributos (nunca `innerHTML` com dados).
- **Tempo real**: SSE com `fetch` + leitura do stream (EventSource não manda header `Authorization`, e token em
  query string vazaria em logs). Reconexão com backoff.
- **QR**: `GET /qr?format=png` via `fetch` → `blob:`; recarrega a cada `instance.qr`.
- **Settings**: o painel envia só os campos alterados (o servidor guarda só overrides) e nunca reenvia o
  `proxy_url` mascarado.
- **Bandeja**: `cmd/litezap-app` com `fyne.io/systray` (macOS: CGO/Cocoa, sem dependências extras além do Xcode CLT;
  Windows: sem CGO; Linux: DBus/StatusNotifierItem, sem CGO). Fica num comando separado para o `litezap` continuar
  sem CGO. O binário também atende `worker` (o spawner reexecuta o próprio executável). Pasta de dados:
  `~/Library/Application Support/LiteZap` (macOS), `%APPDATA%\LiteZap` (Windows), `$XDG_DATA_HOME/litezap` ou
  `~/.local/share/litezap` (Linux); `litezap.yaml` opcional nela; logs em `litezap.log`. Ícone desenhado em Go
  (PNG/ICO) a partir da marca (balão + raio).
- **Config**: `disable_dashboard` / `LITEZAP_DISABLE_DASHBOARD` desliga o painel (API intacta).

## Tarefas

1. `internal/dashboard`: códigos de uso único, handler estático com cabeçalhos, `POST /app/api/session`; testes.
2. `serve.RunWith(Options)` (config base, callback ao escutar, códigos), montagem `/app/` + redirect de `/`;
   `config.disable_dashboard`; testes.
3. Painel (`static/`): login, instâncias (SSE), pareamento (QR/código), conversas + envio (fila explicada),
   settings, webhooks (entregas/retry), eventos ao vivo.
4. `internal/desktop` (pastas por SO, abrir navegador, ícone) + `cmd/litezap-app`; testes do que é puro.
5. README/docs, smoke test (serve em 127.0.0.1:18791 com pasta temporária), revisão independente, correções.

Cada commit: `gofmt`, `go vet ./...`, `go test -p 2 -race ./...`.

## Fora desta versão (em breve)

Instaladores (.dmg/.msi/.deb), assinatura e notarização, auto-update, iniciar com o sistema, túnel opcional,
login no painel com token de instância, envio de mídia pelo painel.

## Revisão independente (feita)

Sem críticos. Corrigido: tipos MIME fixos para os assets (no Windows o registro pode mapear `.js` para `text/plain`
e, com `nosniff`, o painel não carregaria); bandeja continua respondendo depois de um erro de inicialização ("Ver
log", "Abrir pasta", sinais e Sair); consulta de estado da bandeja fora do loop de eventos; abertura ignorada se o
Sair veio antes; código vale 1 min e um resgate descarta os pendentes; settings não manda 0 para campo vazio nem
salva a senha mascarada do proxy; status de mensagem nunca volta (evento antes da resposta do POST); hash malformado
não quebra o roteamento; `h()` ignora `on*` que não é função; aviso do "lembrar" (token por host:porta).
Aceito/documentado: outro usuário local que leia a linha de comando do navegador no instante da abertura poderia
disputar o código (máquinas compartilhadas; vale 1 vez, 1 min).
