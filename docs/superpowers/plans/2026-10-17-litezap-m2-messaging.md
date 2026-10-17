# LiteZap Marco 2 — Mensagens — Plano de implementação

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enviar mensagens por uma fila persistente por instância (ordem estrita por destinatário, ritmo com pisos, janela de envio, rampa para números novos, verificação do número antes do primeiro envio, retry, TTL, idempotência por `client_message_id`, `?wait=true`), receber e registrar mensagens, chats e o *history sync* num log consultável por REST (chats, mensagens com cursor, contatos, verificação de números com cache, marcar como lido, mídia sob demanda com URL assinada) e permitir proxy HTTP/SOCKS5 por instância.

**Architecture:** O worker ganha RPCs de mensagens (`Send`, `SendPresence`, `MarkRead`, `DownloadMedia`, `GetContacts`, `GetContact`, `CheckNumbers`) e eventos de dados (`Message`, `Receipt`, `HistoryChunk`) no stream `Events`, com eventos ao vivo sempre antes do histórico e contrapressão no history sync. Mídia nunca passa pelo gRPC: supervisor e worker trocam caminhos de arquivo dentro do `data_dir`. No supervisor, o `manager` repassa eventos e mudanças de estado a observers: o `ingest` grava o log (`messages`, `chats`, `media`) e o `queue` mantém um despachante (goroutine) por instância que só envia em `connected`, dentro da janela. O `lookup` resolve e guarda em cache número → JID. O `service.Messaging` valida, baixa mídia (com proteção SSRF), enfileira e consulta; a `api` expõe as rotas do spec 6. O proxy vai para o worker por variável de ambiente e é aplicado com `SetProxyAddress` antes de conectar.

**Tech Stack:** Go 1.26, `go.mau.fi/whatsmeow` (fixado no `go.mod`), `modernc.org/sqlite`, gRPC + protobuf (`go tool buf`), `chi`. Sem dependências novas.

**Spec:** `docs/superpowers/specs/2026-10-17-litezap-engine-design.md` — seção 13 item 2, com 3.1, 3.2, 4.1, 4.2 (só log e ingestão), 4.5, 4.6, 6, 8, 9.1, 9.2, 9.3, 10 e 11. O spec já foi atualizado com as decisões deste plano (janela de envio, códigos de erro, transições, colunas, contrato do worker).

**Base:** `main` com o marco 1 mergeado (`9a79f9f`). O código deste plano foi escrito contra o código real (que divergiu do plano do marco 1 na revisão) e validado: aplicando as tasks **como estão escritas** sobre essa base, `go build ./...`, `go vet ./...`, `gofmt -l` e `go test ./...` passam ao fim de **cada** task; a árvore final passa em `go test -race ./...` e `go mod tidy` não muda nada.

**Fora deste marco:** busca full-text (FTS5), envio de `sticker`/`location`/`contact` e limpeza de mídia/retenção vão para o **Marco 2b** (lista no fim). Barramento de eventos, SSE e webhooks (marco 3), MCP (marco 4) e hook de agente (marco 5) continuam fora; o marco 2 só grava os dados que eles vão consumir (incluindo os `wa_message_id` enviados pela API, pré-requisito da detecção de humano do 4.3).

## Decisões de desenho

- **Contrato worker→supervisor:** eventos de dados entram no mesmo stream `Events` do marco 1. O `worker.Server` mantém duas filas: a de eventos ao vivo (estado, QR, mensagens, recibos; até 10.000, nunca descarta um `ConnState`, descartes são logados e contados) e a de histórico (até 8 blocos; o handler de history sync do whatsmeow **bloqueia** quando ela enche, em vez de descartar). Ao vivo sempre sai antes do histórico. Sem inscrito, eventos ao vivo ficam num backlog (as mensagens offline chegam logo após conectar). No supervisor, blocos de histórico são gravados por uma goroutine própria, então um history sync grande não segura mudanças de estado nem heartbeats.
- **Mídia por arquivo:** `SendRequest.media_path` aponta para `data_dir/outbox/<instância>/` e `DownloadMediaRequest.dest_path` para `data_dir/media/<instância>/` (o worker recusa qualquer outro caminho). Primeiras leituras simultâneas da mesma mídia compartilham um download. O worker usa `UploadReader`/`DownloadToFile` do whatsmeow, sem carregar o arquivo inteiro na memória nem passar pelo limite do gRPC.
- **Mensagens são opcionais no `WAClient`:** um novo `worker.Messenger` é detectado por type assertion; stubs antigos continuam válidos e respondem `Unimplemented`.
- **Códigos de erro do worker:** `InvalidArgument`/`PermissionDenied` = permanente, `FailedPrecondition` = sessão deslogada, `Unavailable` = sem conexão agora, `DeadlineExceeded` = timeout, resto = retentável. A mensagem do status começa com um código estável (`invalid_number: ...`); um `instance_not_connected:` vindo do worker volta para a fila sem gastar tentativa, qualquer que seja o estado gravado.
- **Idempotência ponta a ponta:** o `wa_message_id` é escolhido no enfileiramento e reusado em toda tentativa (`whatsmeow.SendRequestExtra.ID`); `client_message_id` repetido devolve o item existente sem baixar mídia de novo, e repetido com outro corpo responde `409 conflict` (hash do pedido). Ao entrar em voo, o envio já é gravado no log como `pending` (um recibo que chegue antes do `MarkSent` não se perde). No boot, `in_flight` volta a `queued`, exceto se um recibo já provou o envio.
- **Fila:** `outbound_queue` com `seq` autoincremento e índice parcial dos itens ativos; o próximo item é o mais antigo que é cabeça do seu chat e está vencido (um item em backoff segura só o próprio destinatário). Um despachante por instância (pacote `queue`) é um `manager.Observer`: acorda em `connected` (depois de uma carência de 15–30s), novo item ou novos settings; fora de `connected` fica parado sem gastar tentativas. Itens têm TTL (`expires_after`, padrão 24h). Relógio injetável (`clock.Fake`) deixa os testes de ritmo, janela, retry e rampa determinísticos.
- **Janela de envio e rampa:** `send_window` (padrão 08:00–20:00) e `timezone` (padrão: fuso do servidor) por instância; fora da janela só saem respostas a conversas ativas (algo recebido nas últimas 24h, `chats.last_inbound_at`), e o início de cada janela tem alguns minutos de variação. Rampa: "destinatário novo" = chat (direto ou grupo) sem nenhuma mensagem recebida (`chats.has_inbound`, inclusive vinda do history sync); cada chat conta uma vez por dia (dia no fuso da instância); acima do limite o item é adiado para o início da janela do dia seguinte + variação, sem gastar tentativa — nunca para a meia-noite — e sem consultar o número. O intervalo dobrado para texto idêntico a outro destinatário novo em sequência vale com ou sem rampa.
- **Verificação de número:** antes do primeiro envio a um número sem chat, o despachante consulta `IsOnWhatsApp` (com cache em `number_cache` e cota por minuto): número inexistente falha como `invalid_number` sem enviar; número que o WhatsApp conhece com outro JID de telefone (9º dígito) tem os itens da fila redirecionados (nunca para um `@lid`). Falha ou limite na consulta adia só aquele item, com backoff (1, 2, 5, 15 min; `failed` na 5ª falha); uma consulta cortada pelo shutdown não conta. A rampa vem antes da consulta: acima do limite diário, números novos nem são consultados (uma sequência de consultas parece raspagem de contatos). `check-numbers` usa o mesmo cache com cota própria, para nunca esgotar a dos envios.
- **Settings:** `instances.settings_json` guarda só os overrides (`settings.Patch`); o efetivo = `instance_defaults` da config + `default_proxy_url` + overrides. Pisos `min_interval_ms ≥ 1000` e `max_per_minute ≤ 60`, salvo `unsafe_pacing` (só admin). `proxy_url` também é só admin. Trocar o proxy efetivo reinicia o worker.
- **Chats por telefone:** o worker converte chats LID (`@lid`) para o JID de telefone quando conhece o mapeamento, para que o mesmo contato não vire dois chats (e a rampa reconheça respostas).
- **Proxy:** passado ao worker na variável `LITEZAP_WORKER_PROXY_URL` (não em flag: flags aparecem no `ps` para qualquer usuário); o spawner remove `HTTP(S)_PROXY`/`ALL_PROXY`/`NO_PROXY` do ambiente do worker e o worker sempre chama `SetProxyAddress` (vazio = transporte sem proxy de ambiente).
- **Mídia recebida servida com cuidado:** `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'` e `Content-Disposition: attachment` para tudo fora de imagens JPEG/PNG/WebP/GIF, áudio e vídeo. URLs assinadas usam um segredo próprio (`data/media_key`) e são recusadas sem `public_url` quando o bind não é loopback.

## Estrutura de arquivos

```
proto/worker/v1/worker.proto            + RPCs de mensagens e eventos Message/Receipt/HistoryChunk; mídia por caminho
gen/worker/v1/*.pb.go                   regenerado
internal/settings/settings.go           Settings, Patch (overrides), Effective, pisos, janela/fuso, Ramp.DailyCap
internal/config/config.go               + default_proxy_url, media_allow_private_urls, instance_defaults
internal/wire/wire.go                   limite de mensagem gRPC (32MB) para servidor e clientes
internal/store/migrations/0002_messaging.sql   chats, messages, outbound_queue, media, number_cache, colunas em instances
internal/store/store.go                 + fmtTS/parseTS
internal/store/instances.go             + SettingsJSON, FirstConnectedAt
internal/store/messages.go              log de mensagens, chats, recibos, mídia, paginação
internal/store/queue.go                 fila de envio (enqueue idempotente, próximo item, pending, recuperação, rampa)
internal/store/numbers.go               cache número → JID
internal/instance/state.go              + qr_pending ↔ reconnecting
internal/worker/server.go               filas ao vivo/histórico, contrapressão; RPCs via Messenger
internal/worker/messaging.go            whatsmeow: envio, upload/download por arquivo, contatos, números, eventos
internal/worker/whatsmeow.go            + proxy_unreachable; handle() repassa eventos de dados
internal/worker/run.go                  + ProxyURL (SetProxyAddress sempre), logger do servidor, data dir
internal/worker/e2e_dup_test.go         (tag e2e) envio duplicado com o mesmo id, para o roteiro manual
internal/spawner/process.go             + Env por instância, sem variáveis de proxy herdadas; limites gRPC
internal/workertest/fake.go             FakeWA implementa Messenger; Emit{Message,Receipt,History}
internal/manager/manager.go             + Observer, Client(id), first_connected_at, histórico em goroutine própria
internal/ingest/ingest.go               grava eventos do worker no log
internal/clock/clock.go                 relógio real e falso (timers com Stop)
internal/lookup/lookup.go               resolução de números com cache e limite por minuto
internal/queue/pacing.go                ritmo, digitação, backoff
internal/queue/queue.go                 despachante por instância: janela, carência, TTL, verificação, rampa, wait
internal/media/media.go                 download de media.url com SSRF, MIME, base64, chave e URLs assinadas
internal/service/messages.go            envio, consulta de mensagem, settings
internal/service/inbox.go               chats, histórico, marcar lido, contatos, números, mídia
internal/service/instances.go           mapErr com códigos de mensagens; Delete apaga mídia/outbox
internal/api/api.go                     MessagingService, rotas novas, rota pública de mídia assinada
internal/api/messages.go                handlers de mensagens, cabeçalhos de mídia, limites de corpo
internal/serve/serve.go                 wiring: queue + ingest + lookup + messaging + env do proxy + media_key
cmd/litezap/main.go                     worker lê LITEZAP_WORKER_PROXY_URL
README.md, litezap.example.yaml         documentação do marco 2
```

## Convenções

- `go test ./...` deve passar ao fim de cada task. Rode com `-race` os pacotes concorrentes: `worker`, `workertest`, `spawner`, `manager`, `lookup`, `queue`, `service`, `api`.
- Antes de cada commit: `go mod tidy` (inclua `go.mod`/`go.sum` no commit se mudarem; o plano não adiciona dependências).
- Todo commit termina com o trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` (os comandos de commit já o incluem com um segundo `-m`).
- Store com **uma conexão** (`SetMaxOpenConns(1)`): nunca rode outra query enquanto itera `rows`; consultas compostas ficam numa única instrução SQL.
- Timestamps no store usam `tsLayout` (largura fixa, UTC) via `fmtTS`/`parseTS`; o zero vira `""`.
- Testes de fila usam `clock.Fake`; testes de service/API desligam janela, carência e pisos (`unsafe_pacing`) para rodar a qualquer hora.
- Código e comentários em inglês, como no marco 1. Trabalhe numa branch ou worktree própria (ex.: `marco-2`).

---

## Chunk 1: Settings

### Task 1: Settings por instância e configuração

**Files:**
- Create: `internal/settings/settings.go`
- Test: `internal/settings/settings_test.go`
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

Settings efetivos de uma instância: ritmo (com pisos `min_interval_ms ≥ 1000` e `max_per_minute ≤ 60`, levantados só por `unsafe_pacing`), rampa, janela de envio (`send_window`, padrão 08:00–20:00) e `timezone` (IANA; vazio = fuso do servidor), proxy. `WindowAt` devolve a janela que contém um instante ou a próxima; `DayStart` é o "dia" da rampa no fuso da instância. O que se grava por instância é um `Patch` (só overrides).

- [ ] **Step 1: Escrever os testes**

Criar `internal/settings/settings_test.go`:

```go
package settings

import (
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestDefaultsAreValid(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	bad := []func(*Settings){
		func(s *Settings) { s.MinIntervalMS = -1 },
		func(s *Settings) { s.MaxPerMinute = 0 },
		func(s *Settings) { s.Ramp.EndPerDay = s.Ramp.StartPerDay - 1 },
		func(s *Settings) { s.Ramp.Days = 0 },
		func(s *Settings) { s.ProxyURL = "ftp://x:1" },
		func(s *Settings) { s.ProxyURL = "socks5://" },
		func(s *Settings) { s.MinIntervalMS = 500 }, // floor
		func(s *Settings) { s.MaxPerMinute = 61 },   // ceiling
		func(s *Settings) { s.Timezone = "Mars/Olympus" },
		func(s *Settings) { s.SendWindow = Window{Start: "20:00", End: "08:00"} },
		func(s *Settings) { s.SendWindow = Window{Start: "8h", End: "20:00"} },
	}
	for i, f := range bad {
		s := Defaults()
		f(&s)
		if s.Validate() == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	s := Defaults()
	s.ProxyURL = "socks5://user:pw@10.0.0.1:1080"
	s.Timezone = "America/Sao_Paulo"
	s.SendWindow = Window{}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.UnsafePacing, s.MinIntervalMS, s.MaxPerMinute = true, 0, 600
	if err := s.Validate(); err != nil {
		t.Fatalf("unsafe pacing must lift the floors: %v", err)
	}
}

func TestSendWindowAndDay(t *testing.T) {
	s := Defaults()
	s.Timezone = "America/Sao_Paulo" // UTC-3, no DST
	at := func(h, m int) time.Time { return time.Date(2026, 10, 17, h, m, 0, 0, s.Location()) }
	cases := []struct {
		t     time.Time
		start time.Time
		open  bool
	}{
		{at(7, 59), at(8, 0), false},
		{at(8, 0), at(8, 0), true},
		{at(19, 59), at(8, 0), true},
		{at(20, 0), at(8, 0).AddDate(0, 0, 1), false},
		{at(23, 30), at(8, 0).AddDate(0, 0, 1), false},
	}
	for _, c := range cases {
		start, _, open := s.WindowAt(c.t)
		if !start.Equal(c.start) || open != c.open {
			t.Errorf("WindowAt(%v) = %v %v, want %v %v", c.t, start, open, c.start, c.open)
		}
	}
	// 01:00 UTC on the 18th is still the 17th in Sao Paulo.
	if got := s.DayStart(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)); !got.Equal(at(0, 0)) {
		t.Fatalf("day start = %v", got)
	}
	s.SendWindow = Window{}
	if start, _, open := s.WindowAt(at(3, 0)); !start.IsZero() || !open {
		t.Fatal("no window means always open")
	}
}

func TestMergeApplyAndEffective(t *testing.T) {
	stored := Patch{MinIntervalMS: ptr(5000), Ramp: &RampPatch{Enabled: ptr(false)}}
	merged := stored.Merge(Patch{JitterMS: ptr(0), Ramp: &RampPatch{Days: ptr(7)}, ProxyURL: ptr("http://p:3128"),
		SendWindow: &Window{Start: "09:00", End: "18:00"}, Timezone: ptr("UTC")})
	s := Defaults().Apply(merged)
	if s.MinIntervalMS != 5000 || s.JitterMS != 0 || s.MaxPerMinute != 20 {
		t.Fatalf("pacing = %+v", s)
	}
	if s.Ramp.Enabled || s.Ramp.Days != 7 || s.Ramp.StartPerDay != 50 {
		t.Fatalf("ramp = %+v", s.Ramp)
	}
	if s.ProxyURL != "http://p:3128" || s.SendWindow.Start != "09:00" || s.Timezone != "UTC" {
		t.Fatalf("proxy/window/timezone = %q %+v %q", s.ProxyURL, s.SendWindow, s.Timezone)
	}

	eff, err := Effective(Defaults(), "socks5://d:1080", `{"min_interval_ms":1000}`)
	if err != nil || eff.MinIntervalMS != 1000 || eff.ProxyURL != "socks5://d:1080" {
		t.Fatalf("effective = %+v, %v", eff, err)
	}
	// An empty proxy override falls back to the default proxy.
	eff, _ = Effective(Defaults(), "socks5://d:1080", `{"proxy_url":""}`)
	if eff.ProxyURL != "socks5://d:1080" {
		t.Fatalf("proxy = %q", eff.ProxyURL)
	}
	if _, err := Effective(Defaults(), "", "{nope"); err == nil {
		t.Fatal("expected json error")
	}
}

func TestRedactProxy(t *testing.T) {
	got := RedactProxy("http://bob:secret@proxy:3128")
	if strings.Contains(got, "secret") || !strings.Contains(got, "bob") {
		t.Fatalf("got %q", got)
	}
	if RedactProxy("") != "" {
		t.Fatal("empty must stay empty")
	}
}

func TestRampDailyCap(t *testing.T) {
	r := Defaults().Ramp
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want int
	}{
		{t0, 50},
		{t0.Add(24 * time.Hour), 57}, // 50 + 100*1/14
		{t0.Add(7 * 24 * time.Hour), 100},
		{t0.Add(14 * 24 * time.Hour), 150},
		{t0.Add(40 * 24 * time.Hour), 150},
	}
	for _, c := range cases {
		if got := r.DailyCap(t0, c.at); got != c.want {
			t.Errorf("cap at %v = %d, want %d", c.at, got, c.want)
		}
	}
	if got := r.DailyCap(time.Time{}, t0); got != 50 {
		t.Errorf("never connected: cap = %d", got)
	}
	r.Enabled = false
	if got := r.DailyCap(t0, t0); got != -1 {
		t.Errorf("disabled ramp cap = %d", got)
	}
}
```

Acrescentar ao fim de `internal/config/config_test.go`:

```go
func TestLoadMessagingSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "litezap.yaml")
	body := "instance_defaults:\n  min_interval_ms: 1000\n  ramp:\n    enabled: false\ndefault_proxy_url: socks5://file:1080\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LITEZAP_DEFAULT_PROXY_URL", "http://env:3128")
	t.Setenv("LITEZAP_MEDIA_ALLOW_PRIVATE_URLS", "true")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.InstanceDefaults
	// Fields missing from the file keep their defaults.
	if d.MinIntervalMS != 1000 || d.JitterMS != 2000 || d.Ramp.Enabled || d.Ramp.EndPerDay != 150 {
		t.Fatalf("instance defaults = %+v", d)
	}
	if cfg.DefaultProxyURL != "http://env:3128" || !cfg.MediaAllowPrivateURLs {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadRejectsBadProxy(t *testing.T) {
	t.Setenv("LITEZAP_DEFAULT_PROXY_URL", "ftp://nope")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/settings/ ./internal/config/`
Expected: FAIL — erro de compilação (`undefined: Defaults` em settings; `cfg.InstanceDefaults undefined` em config).

- [ ] **Step 3: Implementar o pacote settings**

Criar `internal/settings/settings.go`:

```go
// Package settings holds the per-instance send settings (spec 4.1 and 4.6): pacing,
// the new-number ramp and the outbound proxy.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

type Ramp struct {
	Enabled     bool `json:"enabled" yaml:"enabled"`
	StartPerDay int  `json:"start_per_day" yaml:"start_per_day"`
	EndPerDay   int  `json:"end_per_day" yaml:"end_per_day"`
	Days        int  `json:"days" yaml:"days"`
}

// Window is the daily send window, "HH:MM" in the instance's timezone; both empty means
// sending at any hour.
type Window struct {
	Start string `json:"start" yaml:"start"`
	End   string `json:"end" yaml:"end"`
}

// Settings are the effective settings of an instance.
type Settings struct {
	MinIntervalMS    int    `json:"min_interval_ms" yaml:"min_interval_ms"`
	JitterMS         int    `json:"jitter_ms" yaml:"jitter_ms"`
	MaxPerMinute     int    `json:"max_per_minute" yaml:"max_per_minute"`
	TypingSimulation bool   `json:"typing_simulation" yaml:"typing_simulation"`
	Ramp             Ramp   `json:"ramp" yaml:"ramp"`
	SendWindow       Window `json:"send_window" yaml:"send_window"`
	// Timezone is an IANA name ("America/Sao_Paulo"); empty means the server's local time.
	// It defines the send window and the ramp's day.
	Timezone string `json:"timezone" yaml:"timezone"`
	// UnsafePacing lifts the pacing floors (min_interval_ms >= 1000, max_per_minute <= 60).
	// Only an admin can set it.
	UnsafePacing bool `json:"unsafe_pacing" yaml:"unsafe_pacing"`
	// ProxyURL is never read from the instance_defaults config block: the global
	// default proxy is config.DefaultProxyURL.
	ProxyURL string `json:"proxy_url" yaml:"-"`
}

// Defaults follows spec 4.1: 3s + 0-2s jitter, 20/min, typing on, ramp 50 -> 150 over 14
// days, sending only between 08:00 and 20:00 server time.
func Defaults() Settings {
	return Settings{
		MinIntervalMS: 3000, JitterMS: 2000, MaxPerMinute: 20, TypingSimulation: true,
		Ramp:       Ramp{Enabled: true, StartPerDay: 50, EndPerDay: 150, Days: 14},
		SendWindow: Window{Start: "08:00", End: "20:00"},
	}
}

func (s Settings) MinInterval() time.Duration {
	return time.Duration(s.MinIntervalMS) * time.Millisecond
}
func (s Settings) Jitter() time.Duration { return time.Duration(s.JitterMS) * time.Millisecond }

func (s Settings) Validate() error {
	switch {
	case s.MinIntervalMS < 0 || s.MinIntervalMS > 600_000:
		return errors.New("min_interval_ms must be between 0 and 600000")
	case s.JitterMS < 0 || s.JitterMS > 600_000:
		return errors.New("jitter_ms must be between 0 and 600000")
	case s.MaxPerMinute < 1 || s.MaxPerMinute > 600:
		return errors.New("max_per_minute must be between 1 and 600")
	case s.Ramp.StartPerDay < 1 || s.Ramp.EndPerDay < s.Ramp.StartPerDay:
		return errors.New("ramp needs 1 <= start_per_day <= end_per_day")
	case s.Ramp.Days < 1 || s.Ramp.Days > 365:
		return errors.New("ramp.days must be between 1 and 365")
	case !s.UnsafePacing && s.MinIntervalMS < 1000:
		return errors.New("min_interval_ms must be at least 1000 (unsafe_pacing lifts this, admin only)")
	case !s.UnsafePacing && s.MaxPerMinute > 60:
		return errors.New("max_per_minute must be at most 60 (unsafe_pacing lifts this, admin only)")
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("timezone %q is not a valid IANA zone", s.Timezone)
		}
	}
	if err := s.SendWindow.validate(); err != nil {
		return err
	}
	return ValidateProxyURL(s.ProxyURL)
}

func parseClock(v string) (int, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, fmt.Errorf("send_window times must be HH:MM, got %q", v)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (w Window) validate() error {
	if w.Start == "" && w.End == "" {
		return nil
	}
	start, err := parseClock(w.Start)
	if err != nil {
		return err
	}
	end, err := parseClock(w.End)
	if err != nil {
		return err
	}
	if end <= start {
		return errors.New("send_window.end must be after send_window.start (same day)")
	}
	return nil
}

// Location is the instance's timezone (the server's local zone when unset or invalid).
func (s Settings) Location() *time.Location {
	if s.Timezone != "" {
		if loc, err := time.LoadLocation(s.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// DayStart is midnight of t's day in the instance's timezone (the ramp's day).
func (s Settings) DayStart(t time.Time) time.Time {
	y, m, d := t.In(s.Location()).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.Location())
}

// WindowAt returns the send window containing t or, when t is outside it, the next one.
// With no window configured, start is zero and open is always true.
func (s Settings) WindowAt(t time.Time) (start, end time.Time, open bool) {
	if s.SendWindow.Start == "" && s.SendWindow.End == "" {
		return time.Time{}, time.Time{}, true
	}
	sm, err1 := parseClock(s.SendWindow.Start)
	em, err2 := parseClock(s.SendWindow.End)
	if err1 != nil || err2 != nil || em <= sm {
		return time.Time{}, time.Time{}, true
	}
	day := s.DayStart(t)
	at := func(d time.Time, min int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), min/60, min%60, 0, 0, d.Location())
	}
	start, end = at(day, sm), at(day, em)
	switch {
	case t.Before(start):
		return start, end, false
	case t.Before(end):
		return start, end, true
	}
	next := day.AddDate(0, 0, 1)
	return at(next, sm), at(next, em), false
}

// ValidateProxyURL accepts "" (no proxy) or an http, https or socks5 URL with a host.
func ValidateProxyURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("proxy_url is not a valid URL")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("proxy_url scheme must be http, https or socks5, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("proxy_url needs a host")
	}
	return nil
}

// RedactProxy hides the password of a proxy URL for display.
func RedactProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}

// RampPatch and Patch are partial updates: nil fields keep their current value.
type RampPatch struct {
	Enabled     *bool `json:"enabled,omitempty"`
	StartPerDay *int  `json:"start_per_day,omitempty"`
	EndPerDay   *int  `json:"end_per_day,omitempty"`
	Days        *int  `json:"days,omitempty"`
}

// Patch is both the PUT body and what is stored per instance (instances.settings_json):
// only overridden fields are kept, so instances follow later changes of the defaults.
type Patch struct {
	MinIntervalMS    *int       `json:"min_interval_ms,omitempty"`
	JitterMS         *int       `json:"jitter_ms,omitempty"`
	MaxPerMinute     *int       `json:"max_per_minute,omitempty"`
	TypingSimulation *bool      `json:"typing_simulation,omitempty"`
	Ramp             *RampPatch `json:"ramp,omitempty"`
	SendWindow       *Window    `json:"send_window,omitempty"`
	Timezone         *string    `json:"timezone,omitempty"`
	UnsafePacing     *bool      `json:"unsafe_pacing,omitempty"` // admin only
	// ProxyURL "" clears the override (the instance falls back to the default proxy). Admin only.
	ProxyURL *string `json:"proxy_url,omitempty"`
}

// Merge returns p with every field set in q overriding it.
func (p Patch) Merge(q Patch) Patch {
	pick := func(a, b *int) *int {
		if b != nil {
			return b
		}
		return a
	}
	pickB := func(a, b *bool) *bool {
		if b != nil {
			return b
		}
		return a
	}
	out := Patch{
		MinIntervalMS:    pick(p.MinIntervalMS, q.MinIntervalMS),
		JitterMS:         pick(p.JitterMS, q.JitterMS),
		MaxPerMinute:     pick(p.MaxPerMinute, q.MaxPerMinute),
		TypingSimulation: pickB(p.TypingSimulation, q.TypingSimulation),
		UnsafePacing:     pickB(p.UnsafePacing, q.UnsafePacing),
		SendWindow:       p.SendWindow,
		Timezone:         p.Timezone,
		ProxyURL:         p.ProxyURL,
	}
	if q.SendWindow != nil {
		out.SendWindow = q.SendWindow
	}
	if q.Timezone != nil {
		out.Timezone = q.Timezone
	}
	if q.ProxyURL != nil {
		out.ProxyURL = q.ProxyURL
	}
	if p.Ramp != nil || q.Ramp != nil {
		var a, b RampPatch
		if p.Ramp != nil {
			a = *p.Ramp
		}
		if q.Ramp != nil {
			b = *q.Ramp
		}
		out.Ramp = &RampPatch{
			Enabled:     pickB(a.Enabled, b.Enabled),
			StartPerDay: pick(a.StartPerDay, b.StartPerDay),
			EndPerDay:   pick(a.EndPerDay, b.EndPerDay),
			Days:        pick(a.Days, b.Days),
		}
	}
	return out
}

// Apply returns s with the overrides of p.
func (s Settings) Apply(p Patch) Settings {
	setI := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setI(&s.MinIntervalMS, p.MinIntervalMS)
	setI(&s.JitterMS, p.JitterMS)
	setI(&s.MaxPerMinute, p.MaxPerMinute)
	if p.TypingSimulation != nil {
		s.TypingSimulation = *p.TypingSimulation
	}
	if p.UnsafePacing != nil {
		s.UnsafePacing = *p.UnsafePacing
	}
	if p.SendWindow != nil {
		s.SendWindow = *p.SendWindow
	}
	if p.Timezone != nil {
		s.Timezone = *p.Timezone
	}
	if r := p.Ramp; r != nil {
		if r.Enabled != nil {
			s.Ramp.Enabled = *r.Enabled
		}
		setI(&s.Ramp.StartPerDay, r.StartPerDay)
		setI(&s.Ramp.EndPerDay, r.EndPerDay)
		setI(&s.Ramp.Days, r.Days)
	}
	if p.ProxyURL != nil && *p.ProxyURL != "" {
		s.ProxyURL = *p.ProxyURL
	}
	return s
}

// ParsePatch decodes a stored overrides document ("" and "{}" mean no overrides).
func ParsePatch(raw string) (Patch, error) {
	var p Patch
	if raw == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("settings json: %w", err)
	}
	return p, nil
}

// Effective resolves an instance's settings: defaults, the global default proxy, then
// the instance's stored overrides.
func Effective(defaults Settings, defaultProxy, overrides string) (Settings, error) {
	p, err := ParsePatch(overrides)
	if err != nil {
		return Settings{}, err
	}
	base := defaults
	base.ProxyURL = defaultProxy
	return base.Apply(p), nil
}

// DailyCap is the ramp limit of new recipients for the day containing now, or -1 when
// the ramp is off. It grows linearly from StartPerDay (day 0) to EndPerDay (day Days).
func (r Ramp) DailyCap(firstConnected, now time.Time) int {
	if !r.Enabled {
		return -1
	}
	if firstConnected.IsZero() || now.Before(firstConnected) {
		return r.StartPerDay
	}
	days := int(now.Sub(firstConnected) / (24 * time.Hour))
	if days >= r.Days {
		return r.EndPerDay
	}
	return r.StartPerDay + (r.EndPerDay-r.StartPerDay)*days/r.Days
}
```

- [ ] **Step 4: Estender a config**

Substituir o conteúdo inteiro de `internal/config/config.go` por:

```go
// Package config loads LiteZap configuration from a YAML file and LITEZAP_* environment variables.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/douglasdf99/litezap/internal/settings"
	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr     string `yaml:"listen_addr"`
	PublicURL      string `yaml:"public_url"`
	DataDir        string `yaml:"data_dir"`
	AdminToken     string `yaml:"admin_token"`
	WorkerMemoryMB int64  `yaml:"worker_memory_max_mb"`
	// DefaultProxyURL is used by every instance without its own settings.proxy_url.
	DefaultProxyURL string `yaml:"default_proxy_url"`
	// MediaAllowPrivateURLs disables the SSRF block on media.url downloads (local use).
	MediaAllowPrivateURLs bool `yaml:"media_allow_private_urls"`
	// InstanceDefaults are the pacing and ramp defaults (spec 4.1); instances override them.
	InstanceDefaults settings.Settings `yaml:"instance_defaults"`
}

func Default() Config {
	return Config{
		ListenAddr: "127.0.0.1:8787", DataDir: "data", WorkerMemoryMB: 512,
		InstanceDefaults: settings.Defaults(),
	}
}

// Load reads path (a missing file means defaults) and applies env overrides.
// DataDir is always returned as an absolute path.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return cfg, fmt.Errorf("read config: %w", err)
		default:
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config: %w", err)
			}
		}
	}
	if v := os.Getenv("LITEZAP_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("LITEZAP_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := os.Getenv("LITEZAP_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv("LITEZAP_ADMIN_TOKEN")); v != "" {
		cfg.AdminToken = v
	}
	if v := os.Getenv("LITEZAP_WORKER_MEMORY_MAX_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_WORKER_MEMORY_MAX_MB: %w", err)
		}
		cfg.WorkerMemoryMB = n
	}
	if v := os.Getenv("LITEZAP_DEFAULT_PROXY_URL"); v != "" {
		cfg.DefaultProxyURL = v
	}
	if v := os.Getenv("LITEZAP_MEDIA_ALLOW_PRIVATE_URLS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_MEDIA_ALLOW_PRIVATE_URLS: %w", err)
		}
		cfg.MediaAllowPrivateURLs = b
	}
	if err := settings.ValidateProxyURL(cfg.DefaultProxyURL); err != nil {
		return cfg, fmt.Errorf("default_proxy_url: %w", err)
	}
	if err := cfg.InstanceDefaults.Validate(); err != nil {
		return cfg, fmt.Errorf("instance_defaults: %w", err)
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return cfg, fmt.Errorf("data dir: %w", err)
	}
	cfg.DataDir = abs
	return cfg, nil
}

// EnsureAdminToken keeps a configured token, otherwise loads or creates DataDir/admin_token (0600).
func EnsureAdminToken(cfg *Config) error {
	if cfg.AdminToken != "" {
		return nil
	}
	p := filepath.Join(cfg.DataDir, "admin_token")
	b, err := os.ReadFile(p)
	if err == nil {
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return fmt.Errorf("admin token file %s is empty", p)
		}
		cfg.AdminToken = tok
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read admin token: %w", err)
	}
	buf := make([]byte, 24)
	rand.Read(buf)
	tok := "lza_" + hex.EncodeToString(buf)
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return fmt.Errorf("write admin token: %w", err)
	}
	cfg.AdminToken = tok
	return nil
}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `go test ./internal/settings/ ./internal/config/`
Expected: `ok` nos dois pacotes.

- [ ] **Step 6: Commit**

```bash
go mod tidy
go test ./...
git add internal/settings internal/config
git commit -m "feat(settings): settings por instância (ritmo com pisos, janela, fuso, rampa, proxy) e defaults na config" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 2: Contrato gRPC

### Task 2: Contrato gRPC de mensagens e limite de mensagem

**Files:**
- Modify: `proto/worker/v1/worker.proto`
- Regenerate: `gen/worker/v1/*.pb.go`
- Create: `internal/wire/wire.go`
- Test: `internal/wire/wire_test.go`
- Modify: `internal/spawner/process.go`, `internal/worker/run.go`, `internal/workertest/fake.go`

Mídia não passa pelo gRPC (vai por caminho de arquivo), mas um bloco de history sync passa dos 4MB padrão; o limite sobe para 32MB dos dois lados. `worker.Server` embute `UnimplementedWorkerServer`, então o código existente continua compilando com as RPCs novas.

- [ ] **Step 1: Atualizar o contrato**

Substituir o conteúdo inteiro de `proto/worker/v1/worker.proto` por:

```protobuf
syntax = "proto3";

package worker.v1;

option go_package = "github.com/douglasdf99/litezap/gen/worker/v1;workerv1";

// Worker is served by `litezap worker` (one process per WhatsApp instance).
//
// Error codes of the messaging RPCs: InvalidArgument and PermissionDenied are permanent
// (the status message starts with a stable code such as "invalid_number:"),
// FailedPrecondition means the session is not logged in, Unavailable means not connected
// right now, DeadlineExceeded means WhatsApp did not answer in time; any other code is a
// retryable worker error.
//
// Media never travels inline: Send reads media_path and DownloadMedia writes dest_path,
// both inside the worker's data directory (shared with the supervisor).
service Worker {
  rpc Ping(PingRequest) returns (PingResponse);
  rpc PairPhone(PairPhoneRequest) returns (PairPhoneResponse);
  rpc Logout(LogoutRequest) returns (LogoutResponse);
  // Events streams the latest snapshot first (connection state, then QR if pairing), then
  // data events kept while nobody was subscribed, then live events. Live events always go
  // before history chunks; a connection state is never dropped.
  rpc Events(EventsRequest) returns (stream Event);

  rpc Send(SendRequest) returns (SendResponse);
  rpc SendPresence(SendPresenceRequest) returns (SendPresenceResponse);
  rpc MarkRead(MarkReadRequest) returns (MarkReadResponse);
  rpc DownloadMedia(DownloadMediaRequest) returns (DownloadMediaResponse);
  rpc GetContacts(GetContactsRequest) returns (GetContactsResponse);
  rpc GetContact(GetContactRequest) returns (GetContactResponse);
  rpc CheckNumbers(CheckNumbersRequest) returns (CheckNumbersResponse);
}

message PingRequest {}
message PingResponse {
  string state = 1;
}

message PairPhoneRequest {
  string phone = 1; // digits only, with country code
}
message PairPhoneResponse {
  string code = 1;
}

message LogoutRequest {}
message LogoutResponse {}

message EventsRequest {}

message Event {
  oneof kind {
    ConnState conn = 1;
    QR qr = 2;
    Message message = 3;
    Receipt receipt = 4;
    HistoryChunk history = 5;
  }
}

// ConnState.state uses the instance.State strings:
// qr_pending | connecting | connected | reconnecting | logged_out | banned | failed
message ConnState {
  string state = 1;
  string reason = 2;
  string jid = 3;
}

message QR {
  string code = 1;
  int64 timeout_ms = 2;
}

// Message is a received message, or one sent from another device of the account (from_me).
message Message {
  string id = 1;     // WhatsApp message id
  string chat = 2;   // chat JID; for direct chats always the phone-number JID when known
  string sender = 3; // sender JID (without device)
  bool from_me = 4;
  bool is_group = 5;
  int64 timestamp_ms = 6;
  string push_name = 7;
  // text | image | video | audio | document | sticker | location | contact | reaction | unknown
  string type = 8;
  string text = 9;      // body, caption, reaction emoji, or a short description
  Media media = 10;     // set for image | video | audio | document | sticker
  string reaction_to = 11; // WhatsApp id of the message a reaction refers to
}

message Media {
  string kind = 1; // image | video | audio | document | sticker
  string mime = 2;
  uint64 size = 3;
  string file_name = 4;
  uint32 seconds = 5;
  bool ptt = 6;
  bytes ref = 7; // opaque: pass back to DownloadMedia
}

message Receipt {
  repeated string message_ids = 1;
  string chat = 2;
  string type = 3; // delivered | read | played
  int64 timestamp_ms = 4;
}

// HistoryChunk carries messages of one chat from a history sync.
message HistoryChunk {
  string chat = 1;
  string name = 2;
  bool is_group = 3;
  repeated Message messages = 4;
}

message SendRequest {
  string to = 1;         // chat JID
  string message_id = 2; // WhatsApp message id chosen by the supervisor (same on every retry)
  string type = 3;       // text | image | video | audio | document | reaction
  string text = 4;       // body, caption, or reaction emoji ("" removes a reaction)
  string media_path = 5; // file with the attachment, inside the data directory
  string mime = 6;
  string file_name = 7;
  bool ptt = 8; // audio as a voice note
  string reaction_message_id = 9;
  bool reaction_from_me = 10;
  string reaction_sender = 11; // sender JID of the reacted message (groups)
}
message SendResponse {
  string message_id = 1;
  int64 timestamp_ms = 2;
}

message SendPresenceRequest {
  string to = 1;
  string state = 2; // composing | recording | paused
}
message SendPresenceResponse {}

message MarkReadRequest {
  string chat = 1;
  string sender = 2; // required in groups: all ids must be from this sender
  repeated string message_ids = 3;
}
message MarkReadResponse {}

message DownloadMediaRequest {
  string kind = 1;
  bytes ref = 2;
  string dest_path = 3; // file to write, inside the data directory
}
message DownloadMediaResponse {
  int64 size = 1;
}

message Contact {
  string jid = 1;
  string name = 2;
  string push_name = 3;
  string business_name = 4;
}
message GetContactsRequest {}
message GetContactsResponse {
  repeated Contact contacts = 1;
}
message GetContactRequest {
  string jid = 1;
}
message GetContactResponse {
  Contact contact = 1;
  bool found = 2;
}

message CheckNumbersRequest {
  repeated string phones = 1; // digits with country code
}
message NumberStatus {
  string phone = 1;
  bool exists = 2;
  string jid = 3;
}
message CheckNumbersResponse {
  repeated NumberStatus results = 1;
}
```

- [ ] **Step 2: Gerar o código**

Os arquivos `gen/` são gerados (não edite à mão).

Run: `go tool buf generate && go build ./...`
Expected: sem saída; `gen/worker/v1/worker.pb.go` e `worker_grpc.pb.go` ganham `SendRequest`, `Event_Message`, `WorkerClient.Send` etc.

- [ ] **Step 3: Escrever o teste de mensagens grandes**

Criar `internal/wire/wire_test.go`:

```go
package wire

import (
	"bytes"
	"context"
	"net"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type bigMessages struct {
	workerv1.UnimplementedWorkerServer
}

// A history chunk can exceed gRPC's 4MB default.
func (bigMessages) GetContacts(context.Context, *workerv1.GetContactsRequest) (*workerv1.GetContactsResponse, error) {
	return &workerv1.GetContactsResponse{Contacts: []*workerv1.Contact{{Name: string(bytes.Repeat([]byte{'a'}, 10<<20))}}}, nil
}

func (bigMessages) CheckNumbers(_ context.Context, req *workerv1.CheckNumbersRequest) (*workerv1.CheckNumbersResponse, error) {
	return &workerv1.CheckNumbersResponse{Results: []*workerv1.NumberStatus{{Phone: req.Phones[0][:3]}}}, nil
}

func TestLargeMessagesPass(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer(ServerOptions()...)
	workerv1.RegisterWorkerServer(g, bigMessages{})
	go g.Serve(lis)
	defer g.Stop()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()), DialOption())
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	cli := workerv1.NewWorkerClient(cc)
	resp, err := cli.GetContacts(context.Background(), &workerv1.GetContactsRequest{})
	if err != nil || len(resp.Contacts[0].Name) != 10<<20 {
		t.Fatalf("large response: %v", err)
	}
	big := string(bytes.Repeat([]byte{'5'}, 10<<20))
	if _, err := cli.CheckNumbers(context.Background(), &workerv1.CheckNumbersRequest{Phones: []string{big}}); err != nil {
		t.Fatalf("large request: %v", err)
	}
}
```

Run: `go test ./internal/wire/`
Expected: FAIL — `undefined: ServerOptions` / `DialOption`.

- [ ] **Step 4: Implementar `wire` e usar nos servidores e clientes**

Criar `internal/wire/wire.go`:

```go
// Package wire holds the gRPC settings shared by the supervisor and the workers.
package wire

import "google.golang.org/grpc"

// MaxMsgBytes bounds one gRPC message. Media goes through files, not gRPC; the largest
// messages are history chunks (hundreds of messages with media references).
const MaxMsgBytes = 32 << 20

// ServerOptions are used by every worker gRPC server (real and fake).
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.MaxRecvMsgSize(MaxMsgBytes), grpc.MaxSendMsgSize(MaxMsgBytes)}
}

// DialOption is used by every client of a worker.
func DialOption() grpc.DialOption {
	return grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMsgBytes), grpc.MaxCallSendMsgSize(MaxMsgBytes))
}
```

Em `internal/spawner/process.go` (imports), trocar:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
```

por:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/wire"
	"google.golang.org/grpc"
```

Em `internal/spawner/process.go` (dial do worker), trocar:

```go
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = h.Kill()
```

por:

```go
		grpc.WithTransportCredentials(insecure.NewCredentials()), wire.DialOption())
	if err != nil {
		_ = h.Kill()
```

Em `internal/worker/run.go` (imports), trocar:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
```

por:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/wire"
	"go.mau.fi/whatsmeow"
```

Em `internal/worker/run.go`, trocar:

```go
	g := grpc.NewServer()
```

por:

```go
	g := grpc.NewServer(wire.ServerOptions()...)
```

Em `internal/workertest/fake.go` (imports), trocar:

```go
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/wire"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
```

Em `internal/workertest/fake.go`, trocar:

```go
	f.grpc = grpc.NewServer()
```

por:

```go
	f.grpc = grpc.NewServer(wire.ServerOptions()...)
```

Em `internal/workertest/fake.go`, trocar:

```go
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	f.client
```

por:

```go
		grpc.WithTransportCredentials(insecure.NewCredentials()), wire.DialOption())
	f.client
```

- [ ] **Step 5: Rodar**

Run: `go test -race ./internal/wire/ ./internal/workertest/ ./internal/spawner/ ./internal/worker/`
Expected: `ok` em todos (o teste de `wire` troca mensagens de 10MB).

- [ ] **Step 6: Commit**

```bash
go mod tidy
go test ./...
git add proto gen internal/wire internal/spawner/process.go internal/worker/run.go internal/workertest/fake.go
git commit -m "feat(proto): RPCs e eventos de mensagens com mídia por caminho; limite gRPC de 32MB" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 3: Store de mensagens

### Task 3: Migração 0002, instâncias e log de mensagens/chats

**Files:**
- Create: `internal/store/migrations/0002_messaging.sql`
- Modify: `internal/store/store.go`
- Modify: `internal/store/instances.go`
- Create: `internal/store/messages.go`
- Test: `internal/store/messages_test.go`

A migração cria de uma vez todas as tabelas do marco (inclusive `outbound_queue`, `media` e `number_cache`, usadas nas tasks seguintes). O `migrate` do marco 1 executa o arquivo inteiro numa transação; o driver `modernc` aceita várias instruções num `ExecContext`. `ON DELETE CASCADE` funciona porque o DSN já liga `foreign_keys(1)`. Um chat nunca deixa de ser grupo nem de "ter falado conosco" (`MAX` no upsert), e todo JID `@g.us` é grupo. `chats.last_inbound_at` guarda a mensagem recebida mais nova: a janela de envio deixa passar respostas a conversas ativas (últimas 24h).

- [ ] **Step 1: Escrever os testes**

Criar `internal/store/messages_test.go`:

```go
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

func seedInstance(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.CreateInstance(context.Background(), Instance{ID: id, Name: id, State: instance.Created, TokenHash: "h-" + id}); err != nil {
		t.Fatal(err)
	}
}

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestInstanceSettingsAndFirstConnected(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	in, _ := s.GetInstance(ctx, "i1")
	if in.SettingsJSON != "{}" || !in.FirstConnectedAt.IsZero() {
		t.Fatalf("fresh instance = %+v", in)
	}
	if err := s.SetInstanceSettings(ctx, "i1", `{"jitter_ms":0}`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFirstConnected(ctx, "i1", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFirstConnected(ctx, "i1", t0.Add(time.Hour)); err != nil { // no-op
		t.Fatal(err)
	}
	in, _ = s.GetInstance(ctx, "i1")
	if in.SettingsJSON != `{"jitter_ms":0}` || !in.FirstConnectedAt.Equal(t0) {
		t.Fatalf("instance = %+v", in)
	}
	if err := s.SetInstanceSettings(ctx, "nope", "{}"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestInsertMessageDedupesAndUpdatesChat(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	chat := "5511988887777@s.whatsapp.net"
	in := Message{ID: "msg_1", WAMessageID: "WA1", ChatJID: chat, SenderJID: chat, Source: "contact",
		Type: "image", Text: "foto", Status: "received", Timestamp: t0,
		Media: &Media{ID: "med_1", Kind: "image", Mime: "image/jpeg", Size: 10, Ref: []byte{1, 2}}}
	ok, err := s.InsertMessage(ctx, "i1", in, ChatUpdate{Name: "João", CountUnread: true, Inbound: true})
	if err != nil || !ok {
		t.Fatalf("insert = %v, %v", ok, err)
	}
	in.ID = "msg_dup"
	if ok, _ := s.InsertMessage(ctx, "i1", in, ChatUpdate{CountUnread: true, Inbound: true}); ok {
		t.Fatal("duplicate WhatsApp id inserted")
	}
	out := Message{ID: "msg_2", WAMessageID: "WA2", ChatJID: chat, FromMe: true, Source: "api",
		Type: "text", Text: "oi", Status: "sent", Timestamp: t0.Add(time.Minute)}
	if _, err := s.InsertMessage(ctx, "i1", out, ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetChat(ctx, "i1", chat)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "João" || c.UnreadCount != 1 || !c.HasInbound || !c.LastMessageAt.Equal(t0.Add(time.Minute)) ||
		!c.LastInboundAt.Equal(t0) {
		t.Fatalf("chat = %+v", c)
	}
	md, err := s.GetMedia(ctx, "i1", "med_1")
	if err != nil || md.MessageID != "msg_1" || string(md.Ref) != "\x01\x02" {
		t.Fatalf("media = %+v, %v", md, err)
	}
	if err := s.SetMediaFile(ctx, "med_1", "/tmp/x", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetUnread(ctx, "i1", chat); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetChat(ctx, "i1", chat); c.UnreadCount != 0 {
		t.Fatalf("unread = %d", c.UnreadCount)
	}
	got, err := s.MessageByWAID(ctx, "i1", chat, "WA2")
	if err != nil || got.ID != "msg_2" || !got.FromMe {
		t.Fatalf("by wa id = %+v, %v", got, err)
	}
}

func TestApplyReceiptOnlyRaisesStatus(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	m := Message{ID: "msg_1", WAMessageID: "WA1", ChatJID: "c@s.whatsapp.net", FromMe: true, Source: "api",
		Type: "text", Status: "sent", Timestamp: t0}
	if _, err := s.InsertMessage(ctx, "i1", m, ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"read", "delivered", "bogus"} {
		if err := s.ApplyReceipt(ctx, "i1", []string{"WA1", "WA-other"}, st); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.GetMessage(ctx, "i1", "msg_1"); got.Status != "read" {
		t.Fatalf("status = %q, want read", got.Status)
	}
}

func TestListMessagesAndChatsPaginate(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	var msgs []Message
	var ups []ChatUpdate
	for i := 0; i < 5; i++ {
		msgs = append(msgs, Message{ID: "m" + string(rune('a'+i)), WAMessageID: "W" + string(rune('a'+i)),
			ChatJID: "a@s.whatsapp.net", Source: "history", Type: "text", Status: "received",
			Timestamp: t0.Add(time.Duration(i) * time.Second)})
		ups = append(ups, ChatUpdate{Inbound: true})
	}
	// Same timestamp as "md": ties are broken by insertion order.
	msgs = append(msgs, Message{ID: "mz", WAMessageID: "Wz", ChatJID: "a@s.whatsapp.net", Source: "history",
		Type: "text", Status: "received", Timestamp: t0.Add(3 * time.Second)})
	ups = append(ups, ChatUpdate{})
	n, err := s.InsertMessages(ctx, "i1", msgs, ups)
	if err != nil || n != 6 {
		t.Fatalf("inserted %d, %v", n, err)
	}
	page1, cur, err := s.ListMessages(ctx, "i1", "a@s.whatsapp.net", 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	page2, _, err := s.ListMessages(ctx, "i1", "a@s.whatsapp.net", 10, &cur)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range append(page1, page2...) {
		ids = append(ids, m.ID)
	}
	want := []string{"me", "mz", "md", "mc", "mb", "ma"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}

	for i, jid := range []string{"b@s.whatsapp.net", "c@s.whatsapp.net"} {
		m := Message{ID: "x" + jid, WAMessageID: "X", ChatJID: jid, Source: "contact", Type: "text",
			Status: "received", Timestamp: t0.Add(time.Duration(10+i) * time.Second)}
		if _, err := s.InsertMessage(ctx, "i1", m, ChatUpdate{Inbound: true}); err != nil {
			t.Fatal(err)
		}
	}
	chats, ccur, err := s.ListChats(ctx, "i1", 2, nil)
	if err != nil || len(chats) != 2 || chats[0].JID != "c@s.whatsapp.net" || chats[1].JID != "b@s.whatsapp.net" {
		t.Fatalf("chats = %+v, %v", chats, err)
	}
	rest, _, err := s.ListChats(ctx, "i1", 2, &ccur)
	if err != nil || len(rest) != 1 || rest[0].JID != "a@s.whatsapp.net" {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
	in, err := s.LatestInbound(ctx, "i1", "a@s.whatsapp.net", 2)
	if err != nil || len(in) != 2 || in[0].ID != "me" {
		t.Fatalf("latest inbound = %+v, %v", in, err)
	}
}

func TestDeleteInstanceCascades(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	m := Message{ID: "msg_1", WAMessageID: "WA1", ChatJID: "c@s.whatsapp.net", Source: "contact",
		Type: "text", Status: "received", Timestamp: t0}
	if _, err := s.InsertMessage(ctx, "i1", m, ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChat(ctx, "i1", "c@s.whatsapp.net"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chat survived instance delete: %v", err)
	}
	if _, err := s.GetMessage(ctx, "i1", "msg_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message survived instance delete: %v", err)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/store/`
Expected: FAIL — `in.SettingsJSON undefined`, `s.InsertMessage undefined` etc.

- [ ] **Step 3: Criar a migração**

Criar `internal/store/migrations/0002_messaging.sql`:

```sql
ALTER TABLE instances ADD COLUMN settings_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE instances ADD COLUMN first_connected_at TEXT NOT NULL DEFAULT '';

CREATE TABLE chats (
  instance_id        TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  jid                TEXT NOT NULL,
  name               TEXT NOT NULL DEFAULT '',
  is_group           INTEGER NOT NULL DEFAULT 0,
  last_message_at    TEXT NOT NULL DEFAULT '',
  unread_count       INTEGER NOT NULL DEFAULT 0,
  has_inbound        INTEGER NOT NULL DEFAULT 0, -- received at least one message (ramp: not a new recipient)
  last_inbound_at    TEXT NOT NULL DEFAULT '',   -- newest received message (send window: active conversation)
  agent_enabled      INTEGER,                    -- marco 5 (agent hook)
  agent_paused_until TEXT NOT NULL DEFAULT '',   -- marco 5 (agent hook)
  PRIMARY KEY (instance_id, jid)
);
CREATE INDEX chats_recent ON chats (instance_id, last_message_at DESC, jid DESC);

CREATE TABLE messages (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  id            TEXT NOT NULL UNIQUE,
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  wa_message_id TEXT NOT NULL,
  chat_jid      TEXT NOT NULL,
  sender_jid    TEXT NOT NULL DEFAULT '',
  from_me       INTEGER NOT NULL,
  source        TEXT NOT NULL,              -- contact | api | device | history
  type          TEXT NOT NULL,
  text          TEXT NOT NULL DEFAULT '',
  media_json    TEXT NOT NULL DEFAULT '',
  reaction_to   TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,              -- received | pending | sent | delivered | read
  timestamp     TEXT NOT NULL,
  UNIQUE (instance_id, chat_jid, wa_message_id)
);
CREATE INDEX messages_chat ON messages (instance_id, chat_jid, timestamp DESC, seq DESC);
CREATE INDEX messages_wa ON messages (instance_id, wa_message_id);

CREATE TABLE outbound_queue (
  seq               INTEGER PRIMARY KEY AUTOINCREMENT,
  id                TEXT NOT NULL UNIQUE,
  instance_id       TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  client_message_id TEXT,                    -- NULL when absent; UNIQUE ignores NULLs
  chat_jid          TEXT NOT NULL,
  wa_message_id     TEXT NOT NULL,           -- chosen at enqueue, reused on every attempt
  payload_json      TEXT NOT NULL,
  status            TEXT NOT NULL,           -- queued | in_flight | sent | failed
  attempts          INTEGER NOT NULL DEFAULT 0,
  next_attempt_at   TEXT NOT NULL,
  last_error        TEXT NOT NULL DEFAULT '',
  new_recipient     INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  sent_at           TEXT NOT NULL DEFAULT '',
  expires_at        TEXT NOT NULL DEFAULT '', -- not sent by then: failed with "expired"
  request_hash      TEXT NOT NULL DEFAULT '', -- client_message_id reuse with another body is a conflict
  lookup_failures   INTEGER NOT NULL DEFAULT 0, -- consecutive failed number lookups (5 = failed)
  UNIQUE (instance_id, client_message_id)
);
CREATE INDEX outbound_active ON outbound_queue (instance_id, chat_jid, seq) WHERE status IN ('queued', 'in_flight');
CREATE INDEX outbound_queued ON outbound_queue (instance_id, seq) WHERE status = 'queued';
CREATE INDEX outbound_wa ON outbound_queue (instance_id, wa_message_id);
CREATE INDEX outbound_ramp ON outbound_queue (instance_id, sent_at) WHERE new_recipient = 1;

CREATE TABLE media (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  message_id  TEXT NOT NULL,
  kind        TEXT NOT NULL,                 -- image | video | audio | document | sticker
  mime        TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL DEFAULT 0,
  file_name   TEXT NOT NULL DEFAULT '',
  ref         BLOB NOT NULL,                 -- opaque download reference from the worker
  path        TEXT NOT NULL DEFAULT '',      -- local copy, once downloaded
  expires_at  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

-- Phone number -> WhatsApp JID lookups (IsOnWhatsApp), cached to avoid repeated queries.
CREATE TABLE number_cache (
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  phone       TEXT NOT NULL,
  found       INTEGER NOT NULL,
  jid         TEXT NOT NULL DEFAULT '',
  checked_at  TEXT NOT NULL,
  PRIMARY KEY (instance_id, phone)
);
```

- [ ] **Step 4: Helpers de timestamp em `store.go`**

Em `internal/store/store.go`, trocar:

```go
	// One connection serializes writes and avoids SQLITE_BUSY; enough for marco 1.
	db.SetMaxOpenConns(1)
```

por:

```go
	// One connection serializes writes and avoids SQLITE_BUSY. Consequence: never run a
	// query while iterating another query's rows (the second query would wait forever).
	db.SetMaxOpenConns(1)
```

Acrescentar ao fim de `internal/store/store.go`:

```go
// fmtTS formats t for storage; the zero time is stored as "".
func fmtTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(tsLayout)
}

// parseTS is the inverse of fmtTS ("" and malformed values give the zero time).
func parseTS(v string) time.Time {
	t, _ := time.Parse(tsLayout, v)
	return t
}
```

- [ ] **Step 5: Instâncias com settings e primeira conexão**

Substituir o conteúdo inteiro de `internal/store/instances.go` por:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

type Instance struct {
	ID          string
	Name        string
	State       instance.State
	StateReason string
	JID         string
	TokenHash   string
	// SettingsJSON holds the instance's settings overrides (a settings.Patch document).
	SettingsJSON string
	// FirstConnectedAt is when the instance first reached connected (zero if never); the ramp counts from it.
	FirstConnectedAt time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const instanceCols = `id, name, state, state_reason, jid, token_hash, settings_json, first_connected_at, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanInstance(row scanner) (Instance, error) {
	var in Instance
	var state, firstConn, created, updated string
	err := row.Scan(&in.ID, &in.Name, &state, &in.StateReason, &in.JID, &in.TokenHash,
		&in.SettingsJSON, &firstConn, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	in.State = instance.State(state)
	in.FirstConnectedAt = parseTS(firstConn)
	in.CreatedAt, _ = time.Parse(tsLayout, created)
	in.UpdatedAt, _ = time.Parse(tsLayout, updated)
	return in, nil
}

func (s *Store) CreateInstance(ctx context.Context, in Instance) error {
	ts := now()
	if in.SettingsJSON == "" {
		in.SettingsJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO instances (`+instanceCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, string(in.State), in.StateReason, in.JID, in.TokenHash, in.SettingsJSON,
		fmtTS(in.FirstConnectedAt), ts, ts)
	return err
}

func (s *Store) GetInstance(ctx context.Context, id string) (Instance, error) {
	return scanInstance(s.db.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE id = ?`, id))
}

func (s *Store) InstanceByTokenHash(ctx context.Context, hash string) (Instance, error) {
	return scanInstance(s.db.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE token_hash = ?`, hash))
}

func (s *Store) ListInstances(ctx context.Context) ([]Instance, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+instanceCols+` FROM instances ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Instance{}
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) UpdateInstanceState(ctx context.Context, id string, st instance.State, reason string) error {
	return s.exec1(ctx, `UPDATE instances SET state = ?, state_reason = ?, updated_at = ? WHERE id = ?`,
		string(st), reason, now(), id)
}

func (s *Store) SetInstanceJID(ctx context.Context, id, jid string) error {
	return s.exec1(ctx, `UPDATE instances SET jid = ?, updated_at = ? WHERE id = ?`, jid, now(), id)
}

func (s *Store) SetInstanceTokenHash(ctx context.Context, id, hash string) error {
	return s.exec1(ctx, `UPDATE instances SET token_hash = ?, updated_at = ? WHERE id = ?`, hash, now(), id)
}

func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	return s.exec1(ctx, `DELETE FROM instances WHERE id = ?`, id)
}

func (s *Store) SetInstanceSettings(ctx context.Context, id, settingsJSON string) error {
	return s.exec1(ctx, `UPDATE instances SET settings_json = ?, updated_at = ? WHERE id = ?`, settingsJSON, now(), id)
}

// MarkFirstConnected records the first time an instance reached connected; later calls are no-ops.
func (s *Store) MarkFirstConnected(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE instances SET first_connected_at = ? WHERE id = ? AND first_connected_at = ''`, fmtTS(at), id)
	return err
}
```

- [ ] **Step 6: Log de mensagens, chats e mídia**

Criar `internal/store/messages.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Message is one entry of the message log (received, sent by the API or by the phone).
type Message struct {
	ID          string // LiteZap id (for API sends, the outbound_queue id)
	WAMessageID string
	ChatJID     string
	SenderJID   string
	FromMe      bool
	Source      string // contact | api | device | history
	Type        string
	Text        string
	MediaJSON   string
	ReactionTo  string
	Status      string // received | pending | sent | delivered | read
	Timestamp   time.Time
	// Media, when set, is inserted with the message (same transaction). Not read back by queries.
	Media *Media
}

// ChatUpdate says how inserting a message changes its chat.
type ChatUpdate struct {
	Name        string // kept only when non-empty
	IsGroup     bool
	CountUnread bool // increment unread_count
	Inbound     bool // the chat has talked to us (ramp)
}

type Chat struct {
	JID           string
	Name          string
	IsGroup       bool
	LastMessageAt time.Time
	UnreadCount   int
	HasInbound    bool
	LastInboundAt time.Time // newest message received in the chat (zero if none)
}

// Media is a received attachment, downloaded on demand through the worker.
type Media struct {
	ID         string
	InstanceID string
	MessageID  string
	Kind       string
	Mime       string
	Size       int64
	FileName   string
	Ref        []byte
	Path       string
	ExpiresAt  time.Time
}

const messageCols = `id, wa_message_id, chat_jid, sender_jid, from_me, source, type, text, media_json, reaction_to, status, timestamp`

func scanMessage(row scanner) (Message, error) {
	var m Message
	var ts string
	err := row.Scan(&m.ID, &m.WAMessageID, &m.ChatJID, &m.SenderJID, &m.FromMe, &m.Source, &m.Type,
		&m.Text, &m.MediaJSON, &m.ReactionTo, &m.Status, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.Timestamp = parseTS(ts)
	return m, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertMessage inserts m (ignoring duplicates of the same chat and WhatsApp id) and,
// when inserted, its media and (if cu is not nil) the chat update. It reports whether m was new.
func insertMessage(ctx context.Context, x execer, instanceID string, m Message, cu *ChatUpdate) (bool, error) {
	res, err := x.ExecContext(ctx, `INSERT OR IGNORE INTO messages (instance_id, `+messageCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		instanceID, m.ID, m.WAMessageID, m.ChatJID, m.SenderJID, m.FromMe, m.Source, m.Type,
		m.Text, m.MediaJSON, m.ReactionTo, m.Status, fmtTS(m.Timestamp))
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if md := m.Media; md != nil {
		if _, err := x.ExecContext(ctx, `INSERT INTO media
			(id, instance_id, message_id, kind, mime, size, file_name, ref, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			md.ID, instanceID, m.ID, md.Kind, md.Mime, md.Size, md.FileName, md.Ref, now()); err != nil {
			return false, err
		}
	}
	if cu == nil {
		return true, nil
	}
	return true, upsertChat(ctx, x, instanceID, m.ChatJID, m.Timestamp, *cu)
}

// upsertChat applies a message at time at to its chat. A chat never stops being a group
// (or having talked to us), whatever a later update says.
func upsertChat(ctx context.Context, x execer, instanceID, jid string, at time.Time, cu ChatUpdate) error {
	unread := 0
	if cu.CountUnread {
		unread = 1
	}
	inboundAt := ""
	if cu.Inbound {
		inboundAt = fmtTS(at)
	}
	_, err := x.ExecContext(ctx, `INSERT INTO chats (instance_id, jid, name, is_group, last_message_at, unread_count, has_inbound, last_inbound_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, jid) DO UPDATE SET
		  name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE chats.name END,
		  is_group = MAX(chats.is_group, excluded.is_group),
		  last_message_at = MAX(chats.last_message_at, excluded.last_message_at),
		  unread_count = chats.unread_count + excluded.unread_count,
		  has_inbound = MAX(chats.has_inbound, excluded.has_inbound),
		  last_inbound_at = MAX(chats.last_inbound_at, excluded.last_inbound_at)`,
		instanceID, jid, cu.Name, cu.IsGroup || strings.HasSuffix(jid, "@g.us"), fmtTS(at), unread, cu.Inbound, inboundAt)
	return err
}

// InsertMessage logs one message; duplicates (same chat and WhatsApp id) are ignored.
func (s *Store) InsertMessage(ctx context.Context, instanceID string, m Message, cu ChatUpdate) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	ok, err := insertMessage(ctx, tx, instanceID, m, &cu)
	if err != nil {
		return false, err
	}
	return ok, tx.Commit()
}

// InsertMessages logs a batch (e.g. one history sync conversation) in one transaction and
// returns how many were new. updates[i] applies to msgs[i].
func (s *Store) InsertMessages(ctx context.Context, instanceID string, msgs []Message, updates []ChatUpdate) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for i, m := range msgs {
		ok, err := insertMessage(ctx, tx, instanceID, m, &updates[i])
		if err != nil {
			return 0, err
		}
		if ok {
			n++
		}
	}
	return n, tx.Commit()
}

var statusRank = map[string]int{"sent": 1, "delivered": 2, "read": 3}

// ApplyReceipt raises the status of our own messages (pending < sent < delivered < read;
// never lowers it). A receipt can beat MarkSent: the row exists as pending from MarkInFlight.
// Receipts are matched by WhatsApp id only: the receipt's chat may be a LID while the log uses the phone JID.
func (s *Store) ApplyReceipt(ctx context.Context, instanceID string, waIDs []string, status string) error {
	rank, ok := statusRank[status]
	if !ok || len(waIDs) == 0 {
		return nil
	}
	args := []any{status, instanceID}
	for _, id := range waIDs {
		args = append(args, id)
	}
	args = append(args, rank)
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = ?
		WHERE instance_id = ? AND from_me = 1 AND wa_message_id IN (?`+strings.Repeat(",?", len(waIDs)-1)+`)
		AND (CASE status WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 0 END) < ?`, args...)
	return err
}

func (s *Store) GetMessage(ctx context.Context, instanceID, id string) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE instance_id = ? AND id = ?`, instanceID, id))
}

// MessageByWAID finds a message of a chat by its WhatsApp id.
func (s *Store) MessageByWAID(ctx context.Context, instanceID, chatJID, waID string) (Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ?`,
		instanceID, chatJID, waID))
}

// MessageCursor points at the last message of a page (newest-first order).
type MessageCursor struct {
	Timestamp time.Time
	Seq       int64
}

// ListMessages returns a chat's messages newest first, strictly older than before (if set),
// plus the cursor of the last returned row.
func (s *Store) ListMessages(ctx context.Context, instanceID, chatJID string, limit int, before *MessageCursor) ([]Message, MessageCursor, error) {
	q := `SELECT ` + messageCols + `, seq FROM messages WHERE instance_id = ? AND chat_jid = ?`
	args := []any{instanceID, chatJID}
	if before != nil {
		q += ` AND (timestamp < ? OR (timestamp = ? AND seq < ?))`
		ts := fmtTS(before.Timestamp)
		args = append(args, ts, ts, before.Seq)
	}
	q += ` ORDER BY timestamp DESC, seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, MessageCursor{}, err
	}
	defer rows.Close()
	out := []Message{}
	var cur MessageCursor
	for rows.Next() {
		var m Message
		var ts string
		if err := rows.Scan(&m.ID, &m.WAMessageID, &m.ChatJID, &m.SenderJID, &m.FromMe, &m.Source, &m.Type,
			&m.Text, &m.MediaJSON, &m.ReactionTo, &m.Status, &ts, &cur.Seq); err != nil {
			return nil, MessageCursor{}, err
		}
		m.Timestamp = parseTS(ts)
		cur.Timestamp = m.Timestamp
		out = append(out, m)
	}
	return out, cur, rows.Err()
}

// ChatCursor points at the last chat of a page (most recent first).
type ChatCursor struct {
	LastMessageAt time.Time
	JID           string
}

// ListChats returns chats ordered by last message (most recent first), after the cursor if set.
func (s *Store) ListChats(ctx context.Context, instanceID string, limit int, after *ChatCursor) ([]Chat, ChatCursor, error) {
	q := `SELECT jid, name, is_group, last_message_at, unread_count, has_inbound FROM chats WHERE instance_id = ?`
	args := []any{instanceID}
	if after != nil {
		q += ` AND (last_message_at < ? OR (last_message_at = ? AND jid < ?))`
		ts := fmtTS(after.LastMessageAt)
		args = append(args, ts, ts, after.JID)
	}
	q += ` ORDER BY last_message_at DESC, jid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, ChatCursor{}, err
	}
	defer rows.Close()
	out := []Chat{}
	var cur ChatCursor
	for rows.Next() {
		var c Chat
		var ts string
		if err := rows.Scan(&c.JID, &c.Name, &c.IsGroup, &ts, &c.UnreadCount, &c.HasInbound); err != nil {
			return nil, ChatCursor{}, err
		}
		c.LastMessageAt = parseTS(ts)
		cur = ChatCursor{LastMessageAt: c.LastMessageAt, JID: c.JID}
		out = append(out, c)
	}
	return out, cur, rows.Err()
}

func (s *Store) GetChat(ctx context.Context, instanceID, jid string) (Chat, error) {
	var c Chat
	var ts, inbound string
	err := s.db.QueryRowContext(ctx, `SELECT jid, name, is_group, last_message_at, unread_count, has_inbound, last_inbound_at
		FROM chats WHERE instance_id = ? AND jid = ?`, instanceID, jid).
		Scan(&c.JID, &c.Name, &c.IsGroup, &ts, &c.UnreadCount, &c.HasInbound, &inbound)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.LastMessageAt, c.LastInboundAt = parseTS(ts), parseTS(inbound)
	return c, err
}

// LatestInbound returns up to limit of the newest messages received in a chat (newest first).
func (s *Store) LatestInbound(ctx context.Context, instanceID, chatJID string, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE instance_id = ? AND chat_jid = ? AND from_me = 0 AND type <> 'reaction'
		ORDER BY timestamp DESC, seq DESC LIMIT ?`, instanceID, chatJID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ResetUnread(ctx context.Context, instanceID, chatJID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET unread_count = 0 WHERE instance_id = ? AND jid = ?`, instanceID, chatJID)
	return err
}

func (s *Store) GetMedia(ctx context.Context, instanceID, id string) (Media, error) {
	var m Media
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT id, instance_id, message_id, kind, mime, size, file_name, ref, path, expires_at
		FROM media WHERE instance_id = ? AND id = ?`, instanceID, id).
		Scan(&m.ID, &m.InstanceID, &m.MessageID, &m.Kind, &m.Mime, &m.Size, &m.FileName, &m.Ref, &m.Path, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.ExpiresAt = parseTS(exp)
	return m, err
}

// SetMediaFile records the local copy of a downloaded attachment.
func (s *Store) SetMediaFile(ctx context.Context, id, path string, expires time.Time) error {
	return s.exec1(ctx, `UPDATE media SET path = ?, expires_at = ? WHERE id = ?`, path, fmtTS(expires), id)
}

// MediaInfo is the JSON stored in messages.media_json (and shown by the API).
type MediaInfo struct {
	ID       string `json:"id,omitempty"` // media table id; empty for media we sent
	Kind     string `json:"kind"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	PTT      bool   `json:"ptt,omitempty"`
}
```

- [ ] **Step 7: Rodar e ver passar**

Run: `go test ./internal/store/`
Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
go mod tidy
go test ./...
git add internal/store
git commit -m "feat(store): migração 0002, log de mensagens, chats e mídia" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 4: Fila no store

### Task 4: Fila de envio e cache de números no store

**Files:**
- Create: `internal/store/queue.go`
- Create: `internal/store/numbers.go`
- Test: `internal/store/queue_test.go`

Regras (spec 4.1 e 9.2): idempotência por `(instance_id, client_message_id)` (o `UNIQUE` ignora `NULL`), com o hash do pedido guardado para detectar reuso com outro corpo; o próximo item é a cabeça do seu chat; `MarkInFlight` grava o envio no log como `pending` na mesma transação, e `MarkSent` o promove a `sent` (sem rebaixar um `delivered`/`read` que chegou antes) e atualiza o chat; `MarkFailed` apaga a linha `pending`; `RetargetChat` move os itens enfileirados de um JID para outro (9º dígito); `DeferLookup` adia um item cuja verificação de número falhou e conta as falhas seguidas (`lookup_failures`); `Requeue`/`Defer` não mexem em itens finais; a contagem da rampa considera só itens `sent` com `new_recipient = 1`. `number_cache` guarda o resultado de `IsOnWhatsApp` por instância.

- [ ] **Step 1: Escrever os testes**

Criar `internal/store/queue_test.go`:

```go
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func enqueue(t *testing.T, s *Store, id, chat, clientID string, due time.Time) Outbound {
	t.Helper()
	o, created, err := s.Enqueue(context.Background(), Outbound{ID: id, InstanceID: "i1", ClientMessageID: clientID,
		ChatJID: chat, WAMessageID: "WA-" + id, Payload: `{"type":"text"}`, NextAttemptAt: due})
	if err != nil || !created {
		t.Fatalf("enqueue %s = %v, %v", id, created, err)
	}
	return o
}

// inFlight marks an item in flight with its pending log row, as the dispatcher does.
func inFlight(t *testing.T, s *Store, id string, newRecipient bool) error {
	t.Helper()
	o, err := s.GetOutbound(context.Background(), "i1", id)
	if err != nil {
		t.Fatal(err)
	}
	return s.MarkInFlight(context.Background(), id, newRecipient, Message{ID: id, WAMessageID: o.WAMessageID,
		ChatJID: o.ChatJID, FromMe: true, Source: "api", Type: "text", Text: "oi", Timestamp: t0})
}

func TestEnqueueIsIdempotentByClientID(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	first, created, err := s.Enqueue(ctx, Outbound{ID: "q1", InstanceID: "i1", ClientMessageID: "c-1",
		ChatJID: "a@s.whatsapp.net", WAMessageID: "WA-q1", Payload: "{}", RequestHash: "h1", ExpiresAt: t0.Add(time.Hour)})
	if err != nil || !created || first.RequestHash != "h1" || !first.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("first = %+v, %v, %v", first, created, err)
	}
	again, created, err := s.Enqueue(ctx, Outbound{ID: "q2", InstanceID: "i1", ClientMessageID: "c-1",
		ChatJID: "a@s.whatsapp.net", WAMessageID: "WA-q2", Payload: "{}", RequestHash: "h2"})
	if err != nil || created || again.ID != first.ID || again.RequestHash != "h1" || again.Status != OutQueued {
		t.Fatalf("again = %+v, created=%v, err=%v", again, created, err)
	}
	if got, err := s.OutboundByClientID(ctx, "i1", "c-1"); err != nil || got.ID != "q1" {
		t.Fatalf("by client id = %+v, %v", got, err)
	}
	if _, err := s.OutboundByClientID(ctx, "i1", "c-404"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown client id = %v", err)
	}
	// Items without a client id never collide.
	enqueue(t, s, "q3", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q4", "a@s.whatsapp.net", "", t0)
}

func TestNextOutboundKeepsPerChatOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "a1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "a2", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "b1", "b@s.whatsapp.net", "", t0)

	next := func() string {
		t.Helper()
		o, err := s.NextOutbound(ctx, "i1", t0)
		if errors.Is(err, ErrNotFound) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return o.ID
	}
	if got := next(); got != "a1" {
		t.Fatalf("next = %q, want a1", got)
	}
	// a1 in backoff (due later) holds a2, but b1 goes ahead.
	if err := inFlight(t, s, "a1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Requeue(ctx, "a1", t0.Add(time.Minute), 1, "worker_timeout: slow"); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "b1" {
		t.Fatalf("next = %q, want b1", got)
	}
	if err := inFlight(t, s, "b1", true); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "" {
		t.Fatalf("next = %q, want none (a2 waits for a1)", got)
	}
	due, ok, err := s.NextDue(ctx, "i1")
	if err != nil || !ok || !due.Equal(t0.Add(time.Minute)) {
		t.Fatalf("next due = %v %v %v", due, ok, err)
	}
	if err := inFlight(t, s, "b1", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double in_flight = %v", err)
	}
}

func TestMarkSentLogsMessageAndCountsRamp(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q2", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q3", "b@s.whatsapp.net", "", t0)
	for _, id := range []string{"q1", "q2", "q3"} {
		if err := inFlight(t, s, id, true); err != nil {
			t.Fatal(err)
		}
		if m, _ := s.GetMessage(ctx, "i1", id); m.Status != "pending" {
			t.Fatalf("in-flight log row = %+v", m)
		}
		if err := s.MarkSent(ctx, id, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetOutbound(ctx, "i1", "q1")
	if got.Status != OutSent || !got.SentAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("q1 = %+v", got)
	}
	m, err := s.GetMessage(ctx, "i1", "q1")
	if err != nil || m.Source != "api" || m.Status != "sent" || !m.Timestamp.Equal(t0.Add(time.Minute)) {
		t.Fatalf("logged message = %+v, %v", m, err)
	}
	if c, err := s.GetChat(ctx, "i1", "a@s.whatsapp.net"); err != nil || !c.LastMessageAt.Equal(t0.Add(time.Minute)) || c.HasInbound {
		t.Fatalf("chat = %+v, %v", c, err)
	}
	n, counted, err := s.NewRecipientsSince(ctx, "i1", "a@s.whatsapp.net", t0)
	if err != nil || n != 2 || !counted {
		t.Fatalf("ramp count = %d %v %v", n, counted, err)
	}
	if n, counted, _ := s.NewRecipientsSince(ctx, "i1", "z@s.whatsapp.net", t0.Add(time.Hour)); n != 0 || counted {
		t.Fatalf("later window = %d %v", n, counted)
	}
	if own, _ := s.IsOwnSent(ctx, "i1", "WA-q1"); !own {
		t.Fatal("WA-q1 should be ours")
	}
	if own, _ := s.IsOwnSent(ctx, "i1", "WA-phone"); own {
		t.Fatal("WA-phone is not ours")
	}
}

// Regression: a read receipt can arrive between the worker's Send and MarkSent.
func TestReceiptBeforeMarkSentIsKept(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyReceipt(ctx, "i1", []string{"WA-q1"}, "read"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent(ctx, "q1", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.GetMessage(ctx, "i1", "q1"); m.Status != "read" {
		t.Fatalf("status = %q, want read", m.Status)
	}
}

// Regression: sending to a group (an update without IsGroup) must not turn it into a direct chat.
func TestSendDoesNotDowngradeGroup(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	group := "1203630@g.us"
	if _, err := s.InsertMessage(ctx, "i1", Message{ID: "in1", WAMessageID: "IN1", ChatJID: group, Source: "contact",
		Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{IsGroup: true, Inbound: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessage(ctx, "i1", Message{ID: "in2", WAMessageID: "IN2", ChatJID: group, Source: "contact",
		Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "q1", group, "", t0)
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent(ctx, "q1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetChat(ctx, "i1", group); !c.IsGroup {
		t.Fatalf("group downgraded: %+v", c)
	}
}

func TestDeferFailRetargetAndRecover(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q2", "b@s.whatsapp.net", "", t0)
	enqueue(t, s, "q3", "c@s.whatsapp.net", "", t0)
	enqueue(t, s, "q4", "551188887777@s.whatsapp.net", "", t0)
	enqueue(t, s, "q5", "551188887777@s.whatsapp.net", "", t0)
	if err := s.Defer(ctx, "q1", t0.Add(24*time.Hour), "ramp_limit"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.GetOutbound(ctx, "i1", "q1"); o.Status != OutQueued || o.Attempts != 0 || o.LastError != "ramp_limit" {
		t.Fatalf("deferred = %+v", o)
	}
	if err := s.DeferLookup(ctx, "q1", t0.Add(time.Minute), 2, "lookup_failed: boom"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.GetOutbound(ctx, "i1", "q1"); o.LookupFailures != 2 || o.Attempts != 0 || !o.NextAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("lookup-deferred = %+v", o)
	}
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, "q1", 1, "invalid_number: nope"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessage(ctx, "i1", "q1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed item left its pending log row: %v", err)
	}
	if err := s.RetargetChat(ctx, "i1", "551188887777@s.whatsapp.net", "5511988887777@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"q4", "q5"} {
		if o, _ := s.GetOutbound(ctx, "i1", id); o.ChatJID != "5511988887777@s.whatsapp.net" {
			t.Fatalf("%s not retargeted: %+v", id, o)
		}
	}
	// q2 crashed mid-send before any receipt; q3 was sent and already read.
	for _, id := range []string{"q2", "q3"} {
		if err := inFlight(t, s, id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ApplyReceipt(ctx, "i1", []string{"WA-q3"}, "delivered"); err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverInFlight(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d, %v", n, err)
	}
	for id, want := range map[string]string{"q1": OutFailed, "q2": OutQueued, "q3": OutSent} {
		if o, _ := s.GetOutbound(ctx, "i1", id); o.Status != want {
			t.Errorf("%s = %s, want %s", id, o.Status, want)
		}
	}
}

func TestNumberCache(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	if _, err := s.GetCachedNumber(ctx, "i1", "551188887777"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty cache = %v", err)
	}
	c := CachedNumber{Phone: "551188887777", Found: true, JID: "5511988887777@s.whatsapp.net", CheckedAt: t0}
	if err := s.PutCachedNumber(ctx, "i1", c); err != nil {
		t.Fatal(err)
	}
	c.Found, c.JID, c.CheckedAt = false, "", t0.Add(time.Hour)
	if err := s.PutCachedNumber(ctx, "i1", c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCachedNumber(ctx, "i1", "551188887777")
	if err != nil || got.Found || !got.CheckedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("cached = %+v, %v", got, err)
	}
}
```

Run: `go test ./internal/store/`
Expected: FAIL — `undefined: Outbound`, `s.Enqueue undefined`, `undefined: CachedNumber` etc.

- [ ] **Step 2: Implementar**

Criar `internal/store/queue.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Outbound statuses (spec 4.1).
const (
	OutQueued   = "queued"
	OutInFlight = "in_flight"
	OutSent     = "sent"
	OutFailed   = "failed"
)

// Outbound is one item of the persistent send queue.
type Outbound struct {
	ID              string
	InstanceID      string
	ClientMessageID string
	ChatJID         string
	WAMessageID     string
	Payload         string // JSON, owned by the queue package
	Status          string
	Attempts        int
	NextAttemptAt   time.Time
	LastError       string
	NewRecipient    bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	SentAt          time.Time
	ExpiresAt       time.Time // zero: never
	RequestHash     string
	LookupFailures  int
}

const outboundCols = `id, instance_id, COALESCE(client_message_id, ''), chat_jid, wa_message_id, payload_json, status,
	attempts, next_attempt_at, last_error, new_recipient, created_at, updated_at, sent_at, expires_at, request_hash, lookup_failures`

func scanOutbound(row scanner) (Outbound, error) {
	var o Outbound
	var next, created, updated, sent, expires string
	err := row.Scan(&o.ID, &o.InstanceID, &o.ClientMessageID, &o.ChatJID, &o.WAMessageID, &o.Payload, &o.Status,
		&o.Attempts, &next, &o.LastError, &o.NewRecipient, &created, &updated, &sent, &expires, &o.RequestHash, &o.LookupFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	o.NextAttemptAt, o.CreatedAt, o.UpdatedAt, o.SentAt = parseTS(next), parseTS(created), parseTS(updated), parseTS(sent)
	o.ExpiresAt = parseTS(expires)
	return o, err
}

// Enqueue stores a new queued item, due at o.NextAttemptAt (zero: now). With a
// ClientMessageID already used by the instance, nothing is inserted and the existing
// item is returned with created=false (idempotency).
func (s *Store) Enqueue(ctx context.Context, o Outbound) (Outbound, bool, error) {
	var clientID any
	if o.ClientMessageID != "" {
		clientID = o.ClientMessageID
	}
	ts := now()
	due := ts
	if !o.NextAttemptAt.IsZero() {
		due = fmtTS(o.NextAttemptAt)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO outbound_queue
		(id, instance_id, client_message_id, chat_jid, wa_message_id, payload_json, status, next_attempt_at,
		 created_at, updated_at, expires_at, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, client_message_id) DO NOTHING`,
		o.ID, o.InstanceID, clientID, o.ChatJID, o.WAMessageID, o.Payload, due, ts, ts, fmtTS(o.ExpiresAt), o.RequestHash)
	if err != nil {
		return Outbound{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		got, err := s.OutboundByClientID(ctx, o.InstanceID, o.ClientMessageID)
		return got, false, err
	}
	got, err := s.GetOutbound(ctx, o.InstanceID, o.ID)
	return got, true, err
}

// OutboundByClientID finds an item by the client_message_id given at enqueue.
func (s *Store) OutboundByClientID(ctx context.Context, instanceID, clientID string) (Outbound, error) {
	return scanOutbound(s.db.QueryRowContext(ctx,
		`SELECT `+outboundCols+` FROM outbound_queue WHERE instance_id = ? AND client_message_id = ?`,
		instanceID, clientID))
}

func (s *Store) GetOutbound(ctx context.Context, instanceID, id string) (Outbound, error) {
	return scanOutbound(s.db.QueryRowContext(ctx,
		`SELECT `+outboundCols+` FROM outbound_queue WHERE instance_id = ? AND id = ?`, instanceID, id))
}

// chatHead restricts q to the oldest unfinished item of its chat: strict per-recipient order.
const chatHead = `q.seq = (SELECT MIN(p.seq) FROM outbound_queue p
	WHERE p.instance_id = q.instance_id AND p.chat_jid = q.chat_jid AND p.status IN ('queued', 'in_flight'))`

// NextOutbound returns the oldest queued item that heads its chat and is due at now
// (ErrNotFound when there is none).
func (s *Store) NextOutbound(ctx context.Context, instanceID string, at time.Time) (Outbound, error) {
	return scanOutbound(s.db.QueryRowContext(ctx, `SELECT `+outboundCols+` FROM outbound_queue q
		WHERE q.instance_id = ? AND q.status = 'queued' AND q.next_attempt_at <= ? AND `+chatHead+`
		ORDER BY q.seq LIMIT 1`, instanceID, fmtTS(at)))
}

// NextDue is the earliest next_attempt_at among queued chat heads (ok=false when the queue is empty).
func (s *Store) NextDue(ctx context.Context, instanceID string) (time.Time, bool, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MIN(q.next_attempt_at) FROM outbound_queue q
		WHERE q.instance_id = ? AND q.status = 'queued' AND `+chatHead, instanceID).Scan(&v)
	if err != nil || !v.Valid {
		return time.Time{}, false, err
	}
	return parseTS(v.String), true, nil
}

// MarkInFlight moves a queued item to in_flight (newRecipient: it counts against the ramp)
// and logs m as "pending" in the same transaction, so a receipt that arrives before
// MarkSent still finds the row.
func (s *Store) MarkInFlight(ctx context.Context, id string, newRecipient bool, m Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var instanceID string
	if err := tx.QueryRowContext(ctx, `SELECT instance_id FROM outbound_queue WHERE id = ? AND status = 'queued'`, id).Scan(&instanceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbound_queue SET status = 'in_flight', new_recipient = ?, updated_at = ?
		WHERE id = ?`, newRecipient, now(), id); err != nil {
		return err
	}
	m.Status = "pending"
	if _, err := insertMessage(ctx, tx, instanceID, m, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSent finishes an in_flight item: the pending log row becomes sent (unless a receipt
// already raised it) and the chat is updated, in one transaction.
func (s *Store) MarkSent(ctx context.Context, id string, sentAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var instanceID, chat string
	if err := tx.QueryRowContext(ctx, `SELECT instance_id, chat_jid FROM outbound_queue WHERE id = ?`, id).Scan(&instanceID, &chat); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	ts := fmtTS(sentAt)
	if _, err := tx.ExecContext(ctx, `UPDATE outbound_queue SET status = 'sent', sent_at = ?, last_error = '', updated_at = ?
		WHERE id = ?`, ts, now(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET timestamp = ?,
		status = CASE WHEN status = 'pending' THEN 'sent' ELSE status END
		WHERE instance_id = ? AND id = ?`, ts, instanceID, id); err != nil {
		return err
	}
	if err := upsertChat(ctx, tx, instanceID, chat, sentAt, ChatUpdate{}); err != nil {
		return err
	}
	return tx.Commit()
}

// Requeue returns an in_flight item to the queue, due at next, with the given attempt count.
func (s *Store) Requeue(ctx context.Context, id string, next time.Time, attempts int, lastErr string) error {
	return s.exec1(ctx, `UPDATE outbound_queue SET status = 'queued', next_attempt_at = ?, attempts = ?,
		last_error = ?, new_recipient = 0, updated_at = ? WHERE id = ? AND status = 'in_flight'`,
		fmtTS(next), attempts, lastErr, now(), id)
}

// Defer postpones a queued item without counting an attempt (ramp limit reached).
func (s *Store) Defer(ctx context.Context, id string, next time.Time, reason string) error {
	return s.exec1(ctx, `UPDATE outbound_queue SET next_attempt_at = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status = 'queued'`, fmtTS(next), reason, now(), id)
}

// DeferLookup postpones a queued item whose recipient lookup failed, recording the count
// of consecutive lookup failures (no send attempt is spent).
func (s *Store) DeferLookup(ctx context.Context, id string, next time.Time, failures int, reason string) error {
	return s.exec1(ctx, `UPDATE outbound_queue SET next_attempt_at = ?, lookup_failures = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status = 'queued'`, fmtTS(next), failures, reason, now(), id)
}

// MarkFailed finishes an item for good and drops its pending log row.
func (s *Store) MarkFailed(ctx context.Context, id string, attempts int, lastErr string) error {
	if err := s.exec1(ctx, `UPDATE outbound_queue SET status = 'failed', attempts = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'in_flight')`, attempts, lastErr, now(), id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ? AND status = 'pending'`, id)
	return err
}

// RetargetChat moves the queued items of a chat to another JID (e.g. the number resolved
// with the extra 9th digit), keeping their order.
func (s *Store) RetargetChat(ctx context.Context, instanceID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbound_queue SET chat_jid = ?, updated_at = ?
		WHERE instance_id = ? AND chat_jid = ? AND status = 'queued'`, to, now(), instanceID, from)
	return err
}

// RecoverInFlight runs at boot: an in_flight item whose log row was already raised past
// pending (a receipt arrived) was sent; anything else goes back to queued.
// It returns how many items were requeued.
func (s *Store) RecoverInFlight(ctx context.Context) (int, error) {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `UPDATE outbound_queue SET status = 'sent', sent_at = ?, updated_at = ?
		WHERE status = 'in_flight' AND EXISTS (SELECT 1 FROM messages m
		  WHERE m.instance_id = outbound_queue.instance_id AND m.wa_message_id = outbound_queue.wa_message_id
		    AND m.from_me = 1 AND m.status <> 'pending')`,
		ts, ts); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE outbound_queue SET status = 'queued', new_recipient = 0, updated_at = ?
		WHERE status = 'in_flight'`, ts)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// NewRecipientsSince counts distinct chats that consumed the ramp since t, and whether chatJID is one of them.
func (s *Store) NewRecipientsSince(ctx context.Context, instanceID, chatJID string, t time.Time) (count int, counted bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT chat_jid), COALESCE(MAX(chat_jid = ?), 0) FROM outbound_queue
		WHERE instance_id = ? AND new_recipient = 1 AND status = 'sent' AND sent_at >= ?`,
		chatJID, instanceID, fmtTS(t)).Scan(&count, &counted)
	return count, counted, err
}

// IsOwnSent reports whether the API sent a message with this WhatsApp id (human detection, spec 4.3).
func (s *Store) IsOwnSent(ctx context.Context, instanceID, waID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound_queue WHERE instance_id = ? AND wa_message_id = ?`,
		instanceID, waID).Scan(&n)
	return n > 0, err
}
```

Criar `internal/store/numbers.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CachedNumber is a phone lookup result (IsOnWhatsApp).
type CachedNumber struct {
	Phone     string
	Found     bool
	JID       string
	CheckedAt time.Time
}

func (s *Store) GetCachedNumber(ctx context.Context, instanceID, phone string) (CachedNumber, error) {
	c := CachedNumber{Phone: phone}
	var checked string
	err := s.db.QueryRowContext(ctx, `SELECT found, jid, checked_at FROM number_cache WHERE instance_id = ? AND phone = ?`,
		instanceID, phone).Scan(&c.Found, &c.JID, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CheckedAt = parseTS(checked)
	return c, err
}

func (s *Store) PutCachedNumber(ctx context.Context, instanceID string, c CachedNumber) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO number_cache (instance_id, phone, found, jid, checked_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, phone) DO UPDATE SET found = excluded.found, jid = excluded.jid, checked_at = excluded.checked_at`,
		instanceID, c.Phone, c.Found, c.JID, fmtTS(c.CheckedAt))
	return err
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test ./internal/store/`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/store
git commit -m "feat(store): fila de envio com pending, idempotência, recuperação e cache de números" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 5: Proxy

### Task 5: Proxy por instância no worker e no spawner

**Files:**
- Modify: `internal/instance/state.go`
- Test: `internal/instance/state_test.go`
- Modify: `internal/worker/whatsmeow.go`
- Test: `internal/worker/whatsmeow_test.go`
- Modify: `internal/worker/run.go`
- Modify: `cmd/litezap/main.go`
- Modify: `internal/spawner/process.go`
- Test: `internal/spawner/process_test.go`

Spec 4.6: o proxy (`http`, `https`, `socks5`) é aplicado com `whatsmeow.Client.SetProxyAddress` antes de conectar (vale para websocket e mídia, ver `client.go` do whatsmeow: `SetProxy`/`SetSOCKSProxy` trocam o transport de `preLoginHTTP`, `websocketHTTP` e `mediaHTTP`). O worker chama `SetProxyAddress` **sempre**: com URL vazia ele instala um transport com `Proxy: nil`, que ignora `HTTPS_PROXY` (o transport padrão do Go honra essas variáveis). O spawner também remove `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` e `NO_PROXY` (maiúsculas e minúsculas) do ambiente do worker. Não há fallback para conexão direta: com proxy, o transport só fala com o proxy. Falha de conexão com proxy vira `reconnecting` com `reason: proxy_unreachable`.

Um worker ainda pareando também precisa reportar isso. A máquina de estados ganha `qr_pending → reconnecting` (perdeu o acesso no meio do pareamento) e `reconnecting → qr_pending` (voltou e mostra códigos de novo) — spec 9.1.

- [ ] **Step 1: Escrever os testes**

Em `internal/instance/state_test.go`, trocar:

```go
		{Reconnecting, Connected, true},
```

por:

```go
		{Reconnecting, Connected, true},
		{Reconnecting, QRPending, true}, // pairing worker reached WhatsApp again
		{QRPending, Reconnecting, true}, // pairing worker lost WhatsApp (e.g. proxy down)
```

Em `internal/worker/whatsmeow_test.go` (imports), trocar:

```go
import (
	"context"
	"io"
	"log/slog"
	"sync"
```

por:

```go
import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
```

Acrescentar ao fim de `internal/worker/whatsmeow_test.go`:

```go
func TestConnectFailureThroughProxyIsProxyUnreachable(t *testing.T) {
	var calls atomic.Int32
	w, r := newTestAdapter(t, true, func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return nil
	})
	w.proxied = true
	w.startConnectLoop(w.loopCtx, false, 0)
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	states, _ := r.snapshot()
	if len(states) == 0 || states[0] != (connEvt{"reconnecting", "proxy_unreachable"}) {
		t.Fatalf("states = %v", states)
	}
	// Without a proxy the same error is a plain connect failure.
	w.proxied = false
	if got := w.connectFailReason(&net.OpError{Op: "dial", Err: errors.New("x")}); got != "connect_failed" {
		t.Fatalf("reason = %q", got)
	}
}
```

Acrescentar ao fim de `internal/spawner/process_test.go`:

```go
func TestProcessEnvironAddsInstanceEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://leak:3128")
	t.Setenv("all_proxy", "socks5://leak:1080")
	p := &Process{MemoryLimitMB: 64, Env: func(_ context.Context, id string) ([]string, error) {
		return []string{"LITEZAP_WORKER_PROXY_URL=socks5://" + id + ":1080"}, nil
	}}
	env, err := p.environ(context.Background(), exec.Command("true"), "inst_e")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GOMEMLIMIT=64MiB") || !strings.Contains(joined, "LITEZAP_WORKER_PROXY_URL=socks5://inst_e:1080") {
		t.Fatalf("env misses entries: %v", env[len(env)-2:])
	}
	if strings.Contains(joined, "leak") {
		t.Fatal("proxy variables of the supervisor leaked into the worker environment")
	}
	p.Env = func(context.Context, string) ([]string, error) { return nil, io.ErrUnexpectedEOF }
	if _, err := p.environ(context.Background(), exec.Command("true"), "inst_e"); err == nil {
		t.Fatal("expected env error")
	}
}

func TestProcessEnvironStripsInheritedProxySettings(t *testing.T) {
	t.Setenv("LITEZAP_WORKER_PROXY_URL", "socks5://stale:1080")
	t.Setenv("Https_Proxy", "http://mixed-case:3128") // Windows environment names are case-insensitive
	p := &Process{}
	env, err := p.environ(context.Background(), exec.Command("true"), "inst_e")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "stale") || strings.Contains(joined, "mixed-case") {
		t.Fatal("inherited proxy settings leaked into the worker environment")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/instance/ ./internal/worker/ ./internal/spawner/`
Expected: FAIL — `Reconnecting -> qr_pending = false, want true`; `w.proxied undefined`; `p.environ undefined`.

- [ ] **Step 3: Máquina de estados**

Em `internal/instance/state.go`, trocar:

```go
	Starting:     {QRPending, Connecting, Connected, Reconnecting},
	QRPending:    {Connecting, Connected},
	Connecting:   {Connected, Reconnecting},
	Connected:    {Reconnecting},
	Reconnecting: {Connected},
```

por:

```go
	Starting: {QRPending, Connecting, Connected, Reconnecting},
	// A pairing worker that loses WhatsApp (e.g. proxy down) reports reconnecting, then
	// qr_pending again once it can show codes.
	QRPending:    {Connecting, Connected, Reconnecting},
	Connecting:   {Connected, Reconnecting},
	Connected:    {Reconnecting},
	Reconnecting: {Connected, QRPending},
```

- [ ] **Step 4: Adaptador whatsmeow: `proxy_unreachable`**

Em `internal/worker/whatsmeow.go` (imports), trocar:

```go
	"log/slog"
	"slices"
```

por:

```go
	"log/slog"
	"net"
	"slices"
```

Em `internal/worker/whatsmeow.go` (struct `Whatsmeow`), trocar:

```go
	// failureBase is the first reconnect delay after an unknown ConnectFailure.
	failureBase time.Duration
```

por:

```go
	// failureBase is the first reconnect delay after an unknown ConnectFailure.
	failureBase time.Duration
	// proxied is set when the client goes through a proxy (spec 4.6): connection errors
	// are then reported as proxy_unreachable. There is never a fallback to direct.
	proxied bool
```

Em `internal/worker/whatsmeow.go` (`connectLoop`), trocar:

```go
		w.sink.ConnState("reconnecting", "connect_failed", w.jid())
```

por:

```go
		w.sink.ConnState("reconnecting", w.connectFailReason(err), w.jid())
```

Em `internal/worker/whatsmeow.go` (nova função antes de `pairLoop`), trocar:

```go
// pairLoop connects an unpaired client
```

por:

```go
// connectFailReason names a failed connect: proxy_unreachable when a proxy is configured
// and the error is a network/proxy failure, connect_failed otherwise.
func (w *Whatsmeow) connectFailReason(err error) string {
	if w.proxied {
		var opErr *net.OpError
		msg := strings.ToLower(err.Error())
		if errors.As(err, &opErr) || strings.Contains(msg, "proxy") || strings.Contains(msg, "socks") {
			return "proxy_unreachable"
		}
	}
	return "connect_failed"
}

// pairLoop connects an unpaired client
```

Em `internal/worker/whatsmeow.go` (`pairLoop`), trocar:

```go
			w.log.Error("connect for pairing", "err", err, "in", backoff)
```

por:

```go
			w.log.Error("connect for pairing", "err", err, "in", backoff)
			if reason := w.connectFailReason(err); reason == "proxy_unreachable" {
				w.sink.ConnState("reconnecting", reason, "")
			}
```

- [ ] **Step 5: Worker aplica o proxy antes de conectar**

Em `internal/worker/run.go` (imports), trocar:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/wire"
```

por:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/wire"
```

Em `internal/worker/run.go` (`Options`), trocar:

```go
	DataDir    string
	// Parent
```

por:

```go
	DataDir    string
	// ProxyURL, when set, routes the websocket and media through this proxy (spec 4.6).
	ProxyURL string
	// Parent
```

Em `internal/worker/run.go`, trocar:

```go
	cli := whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false))
	srv := NewServer(NewWhatsmeow(cli, log))
```

por:

```go
	cli := whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false))
	// Always set, before connecting: applies to the websocket and to media upload/download,
	// and an empty URL replaces the default transports, which would honor HTTPS_PROXY.
	if err := cli.SetProxyAddress(opt.ProxyURL); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	if opt.ProxyURL != "" {
		log.Info("using proxy", "proxy", settings.RedactProxy(opt.ProxyURL))
	}
	wa := NewWhatsmeow(cli, log)
	wa.proxied = opt.ProxyURL != ""
	srv := NewServer(wa)
```

Em `cmd/litezap/main.go` (imports: zonas IANA embutidas para `timezone`), trocar:

```go
	"syscall"

	"github.com/douglasdf99/litezap/internal/serve"
```

por:

```go
	"syscall"
	_ "time/tzdata" // IANA zones for settings.timezone even where the OS has none (Windows, slim images)

	"github.com/douglasdf99/litezap/internal/serve"
```

Em `cmd/litezap/main.go`, trocar:

```go
			InstanceID: *id, Socket: *sock, DataDir: *dataDir,
			Parent: os.Stdin, // parent-liveness pipe: exit when the supervisor goes away
```

por:

```go
			InstanceID: *id, Socket: *sock, DataDir: *dataDir,
			// Set by the supervisor (env, not a flag: flags are visible to every user in ps).
			ProxyURL: os.Getenv("LITEZAP_WORKER_PROXY_URL"),
			Parent:   os.Stdin, // parent-liveness pipe: exit when the supervisor goes away
```

- [ ] **Step 6: Spawner: ambiente por instância, sem proxy herdado**

Em `internal/spawner/process.go` (struct `Process`), trocar:

```go
	StartTimeout  time.Duration // default 10s
	Log           *slog.Logger
}
```

por:

```go
	StartTimeout  time.Duration // default 10s
	Log           *slog.Logger
	// Env, when set, adds per-instance variables to the worker environment (e.g. its proxy).
	Env func(ctx context.Context, instanceID string) ([]string, error)
}
```

Em `internal/spawner/process.go`, trocar:

```go
func (p *Process) Start(ctx context.Context, id string) (WorkerHandle, error) {
```

por:

```go
// proxyVars are dropped from the worker environment: the worker's only proxy is the one
// the supervisor passes explicitly (spec 4.6), never one inherited from our environment
// (including a stale LITEZAP_WORKER_PROXY_URL; the per-instance value is appended after).
// Names are compared case-insensitively, as Windows environment names are.
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "LITEZAP_WORKER_PROXY_URL"}

func isProxyVar(name string) bool {
	for _, v := range proxyVars {
		if strings.EqualFold(name, v) {
			return true
		}
	}
	return false
}

// environ is the worker's environment: ours without proxy variables, plus GOMEMLIMIT and
// p.Env (later entries win).
func (p *Process) environ(ctx context.Context, cmd *exec.Cmd, id string) ([]string, error) {
	var env []string
	for _, kv := range cmd.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !isProxyVar(k) {
			env = append(env, kv)
		}
	}
	if p.MemoryLimitMB > 0 {
		env = append(env, fmt.Sprintf("GOMEMLIMIT=%dMiB", p.MemoryLimitMB))
	}
	if p.Env != nil {
		extra, err := p.Env(ctx, id)
		if err != nil {
			return nil, err
		}
		env = append(env, extra...)
	}
	return env, nil
}

func (p *Process) Start(ctx context.Context, id string) (WorkerHandle, error) {
```

Em `internal/spawner/process.go` (`Start`; o `stdin, err := cmd.StdinPipe()` logo abaixo continua válido), trocar:

```go
	cmd := p.command(id, sock)
	if p.MemoryLimitMB > 0 {
		cmd.Env = append(cmd.Environ(), fmt.Sprintf("GOMEMLIMIT=%dMiB", p.MemoryLimitMB))
	}
```

por:

```go
	cmd := p.command(id, sock)
	env, err := p.environ(ctx, cmd, id)
	if err != nil {
		return nil, fmt.Errorf("worker env: %w", err)
	}
	cmd.Env = env
```

- [ ] **Step 7: Rodar e ver passar**

Run: `go test -race ./internal/instance/ ./internal/worker/ ./internal/spawner/ && go build ./...`
Expected: `ok` nos três pacotes; build sem erros.

- [ ] **Step 8: Commit**

```bash
go mod tidy
go test ./...
git add internal/instance internal/worker internal/spawner cmd/litezap
git commit -m "feat(worker): proxy por instância, sem proxy herdado do ambiente e sem fallback direto" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 6: Worker — eventos de dados

### Task 6: Filas ao vivo e de histórico e interface Messenger no worker

**Files:**
- Modify: `internal/worker/server.go` (reescrita)
- Modify: `internal/worker/run.go`
- Test: `internal/worker/server_test.go`, `internal/worker/whatsmeow_test.go`

`Sink` ganha `Data(ev)`. Cada inscrito do `Events` tem uma fila de eventos ao vivo (estado, QR, mensagens, recibos) limitada a 10.000: quando enche, cai o evento mais antigo que **não** é `ConnState`, e o descarte é logado (no 1º e a cada 1.000) e contado (`Dropped()`). Sem inscrito, eventos ao vivo de dados vão para um backlog entregue ao próximo inscrito depois do snapshot; o mesmo limite vale quando eventos não enviados voltam ao backlog. Blocos de histórico ficam numa fila separada com 8 vagas: `Data` de um bloco **bloqueia** enquanto não há vaga (contrapressão no handler de history sync do whatsmeow) e só sai pelo stream quando não há nada ao vivo esperando. As RPCs de mensagens delegam a um `Messenger` opcional (type assertion); sem ele, respondem `Unimplemented`.

- [ ] **Step 1: Escrever os testes**

Os gravadores de `whatsmeow_test.go` implementam `Sink` e precisam do método novo:

Em `internal/worker/whatsmeow_test.go` (imports), trocar:

```go
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
```

por:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
```

Em `internal/worker/whatsmeow_test.go`, trocar:

```go
func (r *rec) ConnState(string, string, string) {}
```

por:

```go
func (r *rec) ConnState(string, string, string) {}
func (r *rec) Data(*workerv1.Event)             {}
```

Em `internal/worker/whatsmeow_test.go`, trocar:

```go
func (r *stateRec) QR(code string, _ time.Duration) {
```

por:

```go
func (r *stateRec) Data(*workerv1.Event) {}
func (r *stateRec) QR(code string, _ time.Duration) {
```

Em `internal/worker/server_test.go` (imports), trocar:

```go
import (
	"context"
	"net"
```

por:

```go
import (
	"context"
	"fmt"
	"net"
```

Em `internal/worker/server_test.go` (imports), trocar:

```go
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)
```

por:

```go
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)
```

Acrescentar ao fim de `internal/worker/server_test.go`:

```go
func msgEvent(id string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Message{Message: &workerv1.Message{Id: id, Chat: "c@s.whatsapp.net"}}}
}

func recvIDs(t *testing.T, stream workerv1.Worker_EventsClient, n int) []string {
	t.Helper()
	var ids []string
	for len(ids) < n {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv after %v: %v", ids, err)
		}
		if m := ev.GetMessage(); m != nil {
			ids = append(ids, m.Id)
		}
	}
	return ids
}

// Data events emitted while nobody is subscribed (offline messages right after connect)
// are delivered to the first subscriber, after the connection snapshot.
func TestServerKeepsDataEventsUntilSubscribed(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	srv.ConnState("connected", "", "5511@s.whatsapp.net")
	for i := 0; i < 300; i++ { // more than any channel buffer
		srv.Data(msgEvent(fmt.Sprint(i)))
	}
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetConn().GetState() != "connected" {
		t.Fatalf("snapshot = %v, %v", ev, err)
	}
	ids := recvIDs(t, stream, 300)
	if ids[0] != "0" || ids[299] != "299" {
		t.Fatalf("order lost: first=%s last=%s", ids[0], ids[299])
	}
	srv.Data(msgEvent("live"))
	if got := recvIDs(t, stream, 1); got[0] != "live" {
		t.Fatalf("live = %v", got)
	}
}

func TestServerReturnsUnsentDataToBacklog(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	cli := dial(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stream.Recv() // qr_pending snapshot
	cancel()      // the supervisor goes away...
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.subs)
		srv.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber not removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.Data(msgEvent("while-away")) // ...and comes back later
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	stream2, err := cli.Events(ctx2, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := recvIDs(t, stream2, 1); got[0] != "while-away" {
		t.Fatalf("got %v", got)
	}
}

func histEvent(chat string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{Chat: chat}}}
}

// Live messages and states go before history chunks queued earlier.
func TestServerSendsLiveBeforeHistory(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background()) // qr_pending + QR-1
	srv.Data(histEvent("h1"))
	srv.Data(histEvent("h2"))
	srv.Data(msgEvent("m1"))
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for len(order) < 5 {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case ev.GetConn() != nil:
			order = append(order, "conn")
		case ev.GetQr() != nil:
			order = append(order, "qr")
		case ev.GetMessage() != nil:
			order = append(order, ev.GetMessage().Id)
		case ev.GetHistory() != nil:
			order = append(order, ev.GetHistory().Chat)
		}
	}
	want := []string{"conn", "qr", "m1", "h1", "h2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// With historySlots chunks waiting, the producer (whatsmeow's history handler) blocks
// instead of dropping, until a subscriber takes one.
func TestServerHistoryBackpressure(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	for i := 0; i < historySlots; i++ {
		srv.Data(histEvent(fmt.Sprint(i)))
	}
	returned := make(chan struct{})
	go func() { srv.Data(histEvent("extra")); close(returned) }()
	select {
	case <-returned:
		t.Fatal("history producer did not block on a full queue")
	case <-time.After(50 * time.Millisecond):
	}
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for got < historySlots+1 {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if ev.GetHistory() != nil {
			got++
		}
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("history producer still blocked")
	}
	if srv.Dropped() != 0 {
		t.Fatalf("dropped = %d", srv.Dropped())
	}
}

func TestPushLiveNeverDropsConnState(t *testing.T) {
	srv := NewServer(&stubWA{})
	q := []*workerv1.Event{{Kind: &workerv1.Event_Conn{Conn: &workerv1.ConnState{State: "connected"}}}}
	for i := 0; i < maxLive+5; i++ {
		q = srv.pushLive(q, msgEvent(fmt.Sprint(i)))
	}
	if len(q) != maxLive || q[0].GetConn() == nil || q[1].GetMessage().Id != "6" {
		t.Fatalf("len=%d first=%v second=%v", len(q), q[0], q[1])
	}
	if srv.Dropped() != 6 {
		t.Fatalf("dropped = %d, want 6", srv.Dropped())
	}
	// Returning unsent events to a full backlog keeps the cap too.
	back := srv.capLive(append(q, q...))
	if len(back) != maxLive || back[0].GetConn() == nil {
		t.Fatalf("capped backlog len=%d first=%v", len(back), back[0])
	}
}

type stubMessenger struct {
	stubWA
	sent []*workerv1.SendRequest
}

func (m *stubMessenger) Send(_ context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	m.sent = append(m.sent, req)
	return &workerv1.SendResponse{MessageId: req.MessageId, TimestampMs: 1}, nil
}
func (m *stubMessenger) SendPresence(context.Context, *workerv1.SendPresenceRequest) error {
	return nil
}
func (m *stubMessenger) MarkRead(context.Context, *workerv1.MarkReadRequest) error { return nil }
func (m *stubMessenger) DownloadMedia(_ context.Context, kind string, ref []byte, dest string) (int64, error) {
	return int64(len(kind) + len(ref) + len(dest)), nil
}
func (m *stubMessenger) Contacts(context.Context) ([]*workerv1.Contact, error) {
	return []*workerv1.Contact{{Jid: "a@s.whatsapp.net", Name: "A"}}, nil
}
func (m *stubMessenger) Contact(_ context.Context, jid string) (*workerv1.Contact, bool, error) {
	return &workerv1.Contact{Jid: jid}, jid == "a@s.whatsapp.net", nil
}
func (m *stubMessenger) CheckNumbers(_ context.Context, phones []string) ([]*workerv1.NumberStatus, error) {
	return []*workerv1.NumberStatus{{Phone: phones[0], Exists: true}}, nil
}

func TestServerMessagingRPCs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	plain := dial(t, NewServer(&stubWA{}))
	if _, err := plain.Send(ctx, &workerv1.SendRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("send without messenger = %v", err)
	}

	m := &stubMessenger{}
	cli := dial(t, NewServer(m))
	resp, err := cli.Send(ctx, &workerv1.SendRequest{To: "a@s.whatsapp.net", MessageId: "ID1", Type: "text", Text: "oi"})
	if err != nil || resp.MessageId != "ID1" || len(m.sent) != 1 {
		t.Fatalf("send = %v, %v", resp, err)
	}
	dl, err := cli.DownloadMedia(ctx, &workerv1.DownloadMediaRequest{Kind: "image", Ref: []byte("r"), DestPath: "/d"})
	if err != nil || dl.Size != 8 {
		t.Fatalf("download = %v, %v", dl, err)
	}
	cs, err := cli.GetContacts(ctx, &workerv1.GetContactsRequest{})
	if err != nil || len(cs.Contacts) != 1 {
		t.Fatalf("contacts = %v, %v", cs, err)
	}
	c, err := cli.GetContact(ctx, &workerv1.GetContactRequest{Jid: "b@s.whatsapp.net"})
	if err != nil || c.Found {
		t.Fatalf("contact = %v, %v", c, err)
	}
	nums, err := cli.CheckNumbers(ctx, &workerv1.CheckNumbersRequest{Phones: []string{"5511"}})
	if err != nil || !nums.Results[0].Exists {
		t.Fatalf("check = %v, %v", nums, err)
	}
	if _, err := cli.MarkRead(ctx, &workerv1.MarkReadRequest{Chat: "a@s.whatsapp.net", MessageIds: []string{"X"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.SendPresence(ctx, &workerv1.SendPresenceRequest{To: "a@s.whatsapp.net", State: "composing"}); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/worker/`
Expected: FAIL — `srv.Data undefined`, `undefined: historySlots`, `srv.pushLive undefined`.

- [ ] **Step 3: Reescrever `server.go`**

Substituir o conteúdo inteiro de `internal/worker/server.go` por:

```go
// Package worker runs one WhatsApp connection and serves it over gRPC to the supervisor.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sink receives events from a WAClient.
type Sink interface {
	ConnState(state, reason, jid string)
	// QR delivers a pairing code; an empty code means the previous one expired.
	QR(code string, timeout time.Duration)
	// Data delivers a message, receipt or history event. Live events are queued (bounded,
	// never dropping a connection state); history chunks block the caller while
	// historySlots chunks are waiting (backpressure on the history sync).
	Data(ev *workerv1.Event)
}

// WAClient is the WhatsApp connection used by Server; whatsmeow in production, stubs in tests.
type WAClient interface {
	// Run starts connecting (pairing via QR when there is no session) and returns once started.
	Run(ctx context.Context, sink Sink) error
	PairPhone(ctx context.Context, phone string) (string, error)
	Logout(ctx context.Context) error
	Disconnect()
}

// Messenger is implemented by WAClients that can exchange messages. Errors should be gRPC
// statuses following the codes documented in worker.proto.
type Messenger interface {
	Send(ctx context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error)
	SendPresence(ctx context.Context, req *workerv1.SendPresenceRequest) error
	MarkRead(ctx context.Context, req *workerv1.MarkReadRequest) error
	// DownloadMedia writes the attachment to dest and returns its size.
	DownloadMedia(ctx context.Context, kind string, ref []byte, dest string) (int64, error)
	Contacts(ctx context.Context) ([]*workerv1.Contact, error)
	Contact(ctx context.Context, jid string) (*workerv1.Contact, bool, error)
	CheckNumbers(ctx context.Context, phones []string) ([]*workerv1.NumberStatus, error)
}

const (
	// maxLive bounds the live events queued for one subscriber, and the live data events
	// kept while there is none (offline messages arrive right after connecting, before the
	// supervisor subscribes). Beyond it the oldest event that is not a connection state is
	// dropped, logged and counted.
	maxLive = 10000
	// historySlots bounds the history chunks waiting to be sent; more block the producer.
	historySlots = 8
)

type subscriber struct {
	live   []*workerv1.Event
	notify chan struct{} // buffered (1)
}

func (sb *subscriber) wake() {
	select {
	case sb.notify <- struct{}{}:
	default:
	}
}

func isData(ev *workerv1.Event) bool { return ev.GetConn() == nil && ev.GetQr() == nil }

// Server implements workerv1.WorkerServer and fans out WAClient events to subscribers.
type Server struct {
	workerv1.UnimplementedWorkerServer

	wa      WAClient
	log     *slog.Logger
	dropped atomic.Uint64

	mu      sync.Mutex
	conn    *workerv1.ConnState
	qr      *workerv1.QR
	subs    map[*subscriber]struct{}
	backlog []*workerv1.Event // live data events waiting for a subscriber
	history []*workerv1.Event // history chunks waiting to be sent (one subscriber takes each)
	// histSlots holds one token per queued or in-flight history chunk.
	histSlots chan struct{}

	done     chan struct{}
	doneOnce sync.Once
}

func NewServer(wa WAClient) *Server {
	return &Server{
		wa: wa, log: slog.New(slog.DiscardHandler), subs: map[*subscriber]struct{}{},
		histSlots: make(chan struct{}, historySlots), done: make(chan struct{}),
	}
}

// SetLogger sets where dropped events are reported. Call it before Start.
func (s *Server) SetLogger(l *slog.Logger) { s.log = l }

// Dropped counts live events dropped because a queue was full.
func (s *Server) Dropped() uint64 { return s.dropped.Load() }

// Start runs the WAClient with the server as its sink.
func (s *Server) Start(ctx context.Context) error { return s.wa.Run(ctx, s) }

// Stop disconnects the WAClient and ends all open Events streams.
func (s *Server) Stop() {
	s.doneOnce.Do(func() { close(s.done) })
	s.wa.Disconnect()
}

// pushLive appends ev to a live queue, dropping the oldest non-connection event when full.
func (s *Server) pushLive(q []*workerv1.Event, ev *workerv1.Event) []*workerv1.Event {
	return s.capLive(append(q, ev))
}

// capLive trims q to maxLive by dropping its oldest non-connection events (logged, counted).
func (s *Server) capLive(q []*workerv1.Event) []*workerv1.Event {
	for len(q) > maxLive {
		i := 0
		for i < len(q) && q[i].GetConn() != nil {
			i++
		}
		if i == len(q) {
			return q // only connection states: never dropped
		}
		q = append(q[:i:i], q[i+1:]...)
		n := s.dropped.Add(1)
		if n == 1 || n%1000 == 0 {
			s.log.Warn("worker event queue full; dropped oldest events", "dropped_total", n)
		}
	}
	return q
}

func (s *Server) ConnState(state, reason, jid string) {
	c := &workerv1.ConnState{State: state, Reason: reason, Jid: jid}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
	if state != "qr_pending" {
		s.qr = nil
	}
	s.broadcast(&workerv1.Event{Kind: &workerv1.Event_Conn{Conn: c}})
}

// QR caches and broadcasts a pairing code. An empty code means the pairing round
// ended: the cached code is dropped (so snapshots stop replaying an expired code) and
// the empty code is broadcast so subscribers clear theirs too.
func (s *Server) QR(code string, timeout time.Duration) {
	q := &workerv1.QR{Code: code, TimeoutMs: timeout.Milliseconds()}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case code == "":
		s.qr = nil
	case s.conn.GetState() == "qr_pending":
		s.qr = q
	}
	s.broadcast(&workerv1.Event{Kind: &workerv1.Event_Qr{Qr: q}})
}

// Data delivers a data event. A history chunk waits for a free slot (backpressure).
func (s *Server) Data(ev *workerv1.Event) {
	if ev.GetHistory() != nil {
		select {
		case s.histSlots <- struct{}{}:
		case <-s.done:
			return
		}
		s.mu.Lock()
		s.history = append(s.history, ev)
		for sb := range s.subs {
			sb.wake()
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) == 0 {
		s.backlog = s.pushLive(s.backlog, ev)
		return
	}
	s.broadcast(ev)
}

// broadcast must be called with s.mu held.
func (s *Server) broadcast(ev *workerv1.Event) {
	for sb := range s.subs {
		sb.live = s.pushLive(sb.live, ev)
		sb.wake()
	}
}

func (s *Server) Events(_ *workerv1.EventsRequest, stream workerv1.Worker_EventsServer) error {
	sb := &subscriber{notify: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.conn != nil {
		sb.live = append(sb.live, &workerv1.Event{Kind: &workerv1.Event_Conn{Conn: s.conn}})
	}
	if s.qr != nil {
		sb.live = append(sb.live, &workerv1.Event{Kind: &workerv1.Event_Qr{Qr: s.qr}})
	}
	sb.live = s.capLive(append(sb.live, s.backlog...))
	s.backlog = nil
	s.subs[sb] = struct{}{}
	s.mu.Unlock()

	var unsent []*workerv1.Event
	defer func() {
		s.mu.Lock()
		delete(s.subs, sb)
		// Live data events this stream did not send wait for the next subscriber.
		if len(s.subs) == 0 {
			var keep []*workerv1.Event
			for _, ev := range append(unsent, sb.live...) {
				if isData(ev) {
					keep = append(keep, ev)
				}
			}
			s.backlog = s.capLive(append(keep, s.backlog...))
		}
		s.mu.Unlock()
	}()
	for {
		// Live events first; a history chunk only when no live event is waiting.
		s.mu.Lock()
		batch := sb.live
		sb.live = nil
		var hist *workerv1.Event
		if len(batch) == 0 && len(s.history) > 0 {
			hist, s.history = s.history[0], s.history[1:]
		}
		s.mu.Unlock()
		for i, ev := range batch {
			if err := stream.Send(ev); err != nil {
				unsent = batch[i:]
				return err
			}
		}
		if hist != nil {
			if err := stream.Send(hist); err != nil {
				s.mu.Lock()
				s.history = append([]*workerv1.Event{hist}, s.history...) // keeps its slot
				s.mu.Unlock()
				return err
			}
			<-s.histSlots
		}
		if len(batch) > 0 || hist != nil {
			continue
		}
		select {
		case <-stream.Context().Done():
			return nil
		case <-s.done:
			return nil
		case <-sb.notify:
		}
	}
}

func (s *Server) Ping(context.Context, *workerv1.PingRequest) (*workerv1.PingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.conn.GetState()
	if state == "" {
		state = "starting"
	}
	return &workerv1.PingResponse{State: state}, nil
}

func (s *Server) PairPhone(ctx context.Context, req *workerv1.PairPhoneRequest) (*workerv1.PairPhoneResponse, error) {
	code, err := s.wa.PairPhone(ctx, req.Phone)
	if err != nil {
		return nil, err
	}
	return &workerv1.PairPhoneResponse{Code: code}, nil
}

func (s *Server) Logout(ctx context.Context, _ *workerv1.LogoutRequest) (*workerv1.LogoutResponse, error) {
	if err := s.wa.Logout(ctx); err != nil {
		return nil, err
	}
	return &workerv1.LogoutResponse{}, nil
}

func (s *Server) messenger() (Messenger, error) {
	m, ok := s.wa.(Messenger)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "messaging not supported by this worker")
	}
	return m, nil
}

func (s *Server) Send(ctx context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	return m.Send(ctx, req)
}

func (s *Server) SendPresence(ctx context.Context, req *workerv1.SendPresenceRequest) (*workerv1.SendPresenceResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	if err := m.SendPresence(ctx, req); err != nil {
		return nil, err
	}
	return &workerv1.SendPresenceResponse{}, nil
}

func (s *Server) MarkRead(ctx context.Context, req *workerv1.MarkReadRequest) (*workerv1.MarkReadResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	if err := m.MarkRead(ctx, req); err != nil {
		return nil, err
	}
	return &workerv1.MarkReadResponse{}, nil
}

func (s *Server) DownloadMedia(ctx context.Context, req *workerv1.DownloadMediaRequest) (*workerv1.DownloadMediaResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	size, err := m.DownloadMedia(ctx, req.Kind, req.Ref, req.DestPath)
	if err != nil {
		return nil, err
	}
	return &workerv1.DownloadMediaResponse{Size: size}, nil
}

func (s *Server) GetContacts(ctx context.Context, _ *workerv1.GetContactsRequest) (*workerv1.GetContactsResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	list, err := m.Contacts(ctx)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetContactsResponse{Contacts: list}, nil
}

func (s *Server) GetContact(ctx context.Context, req *workerv1.GetContactRequest) (*workerv1.GetContactResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	c, found, err := m.Contact(ctx, req.Jid)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetContactResponse{Contact: c, Found: found}, nil
}

func (s *Server) CheckNumbers(ctx context.Context, req *workerv1.CheckNumbersRequest) (*workerv1.CheckNumbersResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	res, err := m.CheckNumbers(ctx, req.Phones)
	if err != nil {
		return nil, err
	}
	return &workerv1.CheckNumbersResponse{Results: res}, nil
}
```

Em `internal/worker/run.go` (descartes de eventos aparecem no log do worker), trocar:

```go
	srv := NewServer(wa)
```

por:

```go
	srv := NewServer(wa)
	srv.SetLogger(log)
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go test -race ./internal/worker/ ./internal/workertest/ ./internal/manager/ ./internal/api/`
Expected: `ok` em todos (o fake, o manager e a api usam o `Server` e não podem regredir).

- [ ] **Step 5: Commit**

```bash
go mod tidy
go test ./...
git add internal/worker
git commit -m "feat(worker): filas ao vivo e de histórico com contrapressão e RPCs de mensagens" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 7: Worker fake

### Task 7: Worker fake programável

**Files:**
- Modify: `internal/workertest/fake.go`
- Test: `internal/workertest/fake_test.go`

O `FakeWA` implementa `Messenger` com respostas programáveis: grava os `Send` (e o conteúdo do arquivo de mídia no momento do envio), escreve a mídia de `DownloadMedia` no `dest_path` (contando as chamadas, com um pequeno atraso para que chamadas concorrentes se sobreponham), `CheckNumbers` pode falhar sob demanda (`SetCheckErr`) e responde dizendo que todo número existe como `<número>@s.whatsapp.net`, salvo `SetUnregistered` ou um JID diferente via `SetRegistered` (9º dígito). O `Fake` ganha `EmitMessage`/`EmitReceipt`/`EmitHistory` para os testes do supervisor.

- [ ] **Step 1: Escrever o teste**

Em `internal/workertest/fake_test.go` (imports), trocar:

```go
import (
	"context"
	"testing"
	"time"
```

por:

```go
import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
```

Acrescentar ao fim de `internal/workertest/fake_test.go`:

```go
func TestFakeMessaging(t *testing.T) {
	sp := NewSpawner()
	h, err := sp.Start(context.Background(), "inst_m")
	if err != nil {
		t.Fatal(err)
	}
	f := sp.Last("inst_m")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := h.Client().Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.EmitMessage(&workerv1.Message{Id: "W1", Chat: "a@s.whatsapp.net", Type: "text", Text: "oi"})
	if ev, err := stream.Recv(); err != nil || ev.GetMessage().GetId() != "W1" {
		t.Fatalf("got %v, %v", ev, err)
	}
	resp, err := h.Client().Send(ctx, &workerv1.SendRequest{To: "a@s.whatsapp.net", MessageId: "M1", Type: "text"})
	if err != nil || resp.MessageId != "M1" || len(f.WA.Sent()) != 1 {
		t.Fatalf("send = %v, %v", resp, err)
	}
	f.WA.SetMedia("ref", []byte("bytes"))
	dest := filepath.Join(t.TempDir(), "m")
	dl, err := h.Client().DownloadMedia(ctx, &workerv1.DownloadMediaRequest{Kind: "image", Ref: []byte("ref"), DestPath: dest})
	if b, _ := os.ReadFile(dest); err != nil || dl.Size != 5 || string(b) != "bytes" {
		t.Fatalf("download = %v, %v (%q)", dl, err, b)
	}
	f.WA.SetUnregistered("5511000000000")
	res, err := h.Client().CheckNumbers(ctx, &workerv1.CheckNumbersRequest{Phones: []string{"5511999999999", "5511000000000"}})
	if err != nil || !res.Results[0].Exists || res.Results[0].Jid != "5511999999999@s.whatsapp.net" || res.Results[1].Exists {
		t.Fatalf("check = %v, %v", res, err)
	}
}
```

Run: `go test ./internal/workertest/`
Expected: FAIL — `f.EmitMessage undefined`, `f.WA.Sent undefined`, `f.WA.SetUnregistered undefined`.

- [ ] **Step 2: Implementar**

Em `internal/workertest/fake.go` (imports), trocar:

```go
import (
	"context"
	"net"
```

por:

```go
import (
	"context"
	"net"
	"os"
```

Em `internal/workertest/fake.go` (imports), trocar:

```go
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
```

por:

```go
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
```

Em `internal/workertest/fake.go` (tipo `FakeWA`), trocar:

```go
const testJID = "5511999999999:7@s.whatsapp.net"

// FakeWA is a scriptable worker.WAClient.
type FakeWA struct {
	mu       sync.Mutex
	sink     worker.Sink
	PairCode string
	Logouts  atomic.Int32
	// SilentLogout makes Logout succeed without reporting logged_out (a missed event).
	SilentLogout atomic.Bool
}
```

por:

```go
const testJID = "5511999999999:7@s.whatsapp.net"

// The fake must satisfy the messaging interface, or Server would answer Unimplemented.
var _ worker.Messenger = (*FakeWA)(nil)

// FakeWA is a scriptable worker.WAClient and worker.Messenger.
type FakeWA struct {
	mu       sync.Mutex
	sink     worker.Sink
	PairCode string
	Logouts  atomic.Int32
	// SilentLogout makes Logout succeed without reporting logged_out (a missed event).
	SilentLogout atomic.Bool

	msgMu        sync.Mutex
	sent         []*workerv1.SendRequest
	sentMedia    map[string][]byte // message id -> attachment read at Send time
	presences    []*workerv1.SendPresenceRequest
	reads        []*workerv1.MarkReadRequest
	sendFn       func(*workerv1.SendRequest) (*workerv1.SendResponse, error)
	media        map[string][]byte
	contacts     []*workerv1.Contact
	registered   map[string]string // phone -> JID, overriding the default
	unregistered map[string]bool
	checks       int   // phones looked up by CheckNumbers
	checkErr     error // returned by CheckNumbers when set
	downloads    int
}
```

Em `internal/workertest/fake.go` (métodos de `Messenger` depois de `Disconnect`), trocar:

```go
func (f *FakeWA) Disconnect() {}
```

por:

```go
func (f *FakeWA) Disconnect() {}

// SetSendFunc scripts Send; fn sees every request (nil restores the default success).
func (f *FakeWA) SetSendFunc(fn func(*workerv1.SendRequest) (*workerv1.SendResponse, error)) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.sendFn = fn
}

func (f *FakeWA) Send(_ context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	var data []byte
	if req.MediaPath != "" {
		data, _ = os.ReadFile(req.MediaPath) // the queue deletes the file once the item is final
	}
	f.msgMu.Lock()
	f.sent = append(f.sent, req)
	if data != nil {
		if f.sentMedia == nil {
			f.sentMedia = map[string][]byte{}
		}
		f.sentMedia[req.MessageId] = data
	}
	fn := f.sendFn
	f.msgMu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return &workerv1.SendResponse{MessageId: req.MessageId, TimestampMs: time.Now().UnixMilli()}, nil
}

// Sent returns every Send request received so far (including failed ones).
func (f *FakeWA) Sent() []*workerv1.SendRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.SendRequest(nil), f.sent...)
}

// SentMedia returns the attachment of a sent message, as read from its media_path.
func (f *FakeWA) SentMedia(messageID string) []byte {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.sentMedia[messageID]
}

func (f *FakeWA) SendPresence(_ context.Context, req *workerv1.SendPresenceRequest) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.presences = append(f.presences, req)
	return nil
}

func (f *FakeWA) Presences() []*workerv1.SendPresenceRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.SendPresenceRequest(nil), f.presences...)
}

func (f *FakeWA) MarkRead(_ context.Context, req *workerv1.MarkReadRequest) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.reads = append(f.reads, req)
	return nil
}

func (f *FakeWA) Reads() []*workerv1.MarkReadRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.MarkReadRequest(nil), f.reads...)
}

// SetMedia makes DownloadMedia write data for ref.
func (f *FakeWA) SetMedia(ref string, data []byte) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.media == nil {
		f.media = map[string][]byte{}
	}
	f.media[ref] = data
}

// Downloads counts DownloadMedia calls.
func (f *FakeWA) Downloads() int {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.downloads
}

func (f *FakeWA) DownloadMedia(_ context.Context, _ string, ref []byte, dest string) (int64, error) {
	time.Sleep(20 * time.Millisecond) // a download takes a while: concurrent callers overlap
	f.msgMu.Lock()
	f.downloads++
	data, ok := f.media[string(ref)]
	f.msgMu.Unlock()
	if !ok {
		return 0, status.Error(codes.NotFound, "media_unavailable: gone")
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return 0, status.Error(codes.Unknown, "worker_error: "+err.Error())
	}
	return int64(len(data)), nil
}

func (f *FakeWA) SetContacts(cs ...*workerv1.Contact) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.contacts = cs
}

func (f *FakeWA) Contacts(context.Context) ([]*workerv1.Contact, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.Contact(nil), f.contacts...), nil
}

func (f *FakeWA) Contact(_ context.Context, jid string) (*workerv1.Contact, bool, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	for _, c := range f.contacts {
		if c.Jid == jid {
			return c, true, nil
		}
	}
	return &workerv1.Contact{Jid: jid}, false, nil
}

// SetRegistered maps phone to a different JID (e.g. with the Brazilian 9th digit). By
// default every number exists as <phone>@s.whatsapp.net.
func (f *FakeWA) SetRegistered(phone, jid string) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.registered == nil {
		f.registered = map[string]string{}
	}
	f.registered[phone] = jid
}

// SetUnregistered makes phone a number without WhatsApp.
func (f *FakeWA) SetUnregistered(phone string) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.unregistered == nil {
		f.unregistered = map[string]bool{}
	}
	f.unregistered[phone] = true
}

// SetCheckErr makes CheckNumbers fail with err (nil restores normal answers).
func (f *FakeWA) SetCheckErr(err error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.checkErr = err
}

// Checks counts the phones looked up so far.
func (f *FakeWA) Checks() int {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.checks
}

func (f *FakeWA) CheckNumbers(_ context.Context, phones []string) ([]*workerv1.NumberStatus, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.checks += len(phones)
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	out := make([]*workerv1.NumberStatus, 0, len(phones))
	for _, p := range phones {
		ns := &workerv1.NumberStatus{Phone: p}
		if !f.unregistered[p] {
			ns.Exists, ns.Jid = true, p+"@s.whatsapp.net"
			if jid, ok := f.registered[p]; ok {
				ns.Jid = jid
			}
		}
		out = append(out, ns)
	}
	return out, nil
}
```

Em `internal/workertest/fake.go` (depois de `EmitQR`), trocar:

```go
func (f *Fake) EmitQR(code string) { f.WA.getSink().QR(code, time.Minute) }
```

por:

```go
func (f *Fake) EmitQR(code string) { f.WA.getSink().QR(code, time.Minute) }

// EmitMessage, EmitReceipt and EmitHistory simulate data events from WhatsApp.
func (f *Fake) EmitMessage(m *workerv1.Message) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Message{Message: m}})
}

func (f *Fake) EmitReceipt(r *workerv1.Receipt) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: r}})
}

func (f *Fake) EmitHistory(h *workerv1.HistoryChunk) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_History{History: h}})
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race ./internal/workertest/ ./internal/manager/ ./internal/api/`
Expected: `ok` em todos.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/workertest
git commit -m "test(workertest): fake implementa Messenger e emite eventos de dados" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 8: Worker whatsmeow

### Task 8: Mensagens no adaptador whatsmeow

**Files:**
- Create: `internal/worker/messaging.go`
- Test: `internal/worker/messaging_test.go`
- Create: `internal/worker/e2e_dup_test.go` (tag `e2e`)
- Modify: `internal/worker/whatsmeow.go`
- Modify: `internal/worker/run.go`

APIs do whatsmeow usadas (conferidas no módulo fixado em `go.mod`, `go list -m -f '{{.Dir}}' go.mau.fi/whatsmeow`):

- `send.go`: `(*Client).SendMessage(ctx, to, *waE2E.Message, SendRequestExtra{ID})` → `SendResponse{ID, Timestamp}`; `BuildReaction(chat, sender, id, emoji)` (sender vazio = mensagem nossa). Destinatário sem WhatsApp falha com `"no LID found for ... from server"` (só string: mapeado para `invalid_number`; a fila já evita esse caso consultando o número antes).
- `upload.go`: `UploadReader(ctx, io.Reader, tempFile, MediaImage|MediaVideo|MediaAudio|MediaDocument)` (tempFile `nil` = arquivo temporário criado e apagado pelo whatsmeow) → `UploadResponse{URL, DirectPath, MediaKey, FileEncSHA256, FileSHA256, FileLength}`; os campos vão para `waE2E.ImageMessage`/`VideoMessage`/`AudioMessage{PTT}`/`DocumentMessage{FileName}`.
- `download-to-file.go`: `DownloadToFile(ctx, DownloadableMessage, File)` (um `*os.File` aberto para leitura e escrita); erros HTTP são `DownloadHTTPError` (404/410 = mídia expirada).
- `presence.go`: `SendChatPresence(ctx, jid, ChatPresenceComposing|Paused, ChatPresenceMediaText|Audio)`. `receipt.go`: `MarkRead(ctx, ids, ts, chat, sender)` (sender obrigatório em grupos).
- `user.go`: `IsOnWhatsApp(ctx, ["+5511..."])` (exige `+`) → `IsOnWhatsAppResponse{Query, JID, IsIn, PhoneNumber}`. `store.ContactStore`: `GetAllContacts`, `GetContact` → `types.ContactInfo{Found, FullName, FirstName, PushName, BusinessName}`.
- `types/events`: `Message{Info types.MessageInfo, Message}`, `Receipt{MessageSource, MessageIDs, Timestamp, Type}`, `HistorySync{Data *waHistorySync.HistorySync}`; `client.go`: `ParseWebMessage(chatJID, webMsg)` para mensagens do histórico. LID→telefone: `MessageSource.SenderAlt`/`RecipientAlt` ou `Store.LIDs.GetPNForLID`.

Caminhos de mídia vindos do supervisor são restritos (`dataPath`): `Send` só lê arquivos diretamente em `outbox/<instância>/` e `DownloadMedia` só escreve diretamente em `media/<instância>/` (nada de outras instâncias, `litezap.db`, `media_key` ou da sessão). `handle()` repassa `Message`, `Receipt` e `HistorySync` para `onData` antes do switch de conexão; com a fila de histórico cheia, `onHistory` bloqueia o handler (contrapressão). Histórico sai em `HistoryChunk` de até 250 mensagens por chat; status, listas de transmissão e canais são ignorados; mensagens de protocolo (revogação, edição, distribuição de chave) não entram no log neste marco. O teste com tag `e2e` é usado no roteiro manual (envio duplicado com o mesmo id direto no worker).

- [ ] **Step 1: Escrever os testes**

Criar `internal/worker/messaging_test.go`:

```go
package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestContentTypes(t *testing.T) {
	img := &waE2E.ImageMessage{Caption: proto.String("foto"), Mimetype: proto.String("image/jpeg"),
		FileLength: proto.Uint64(42), DirectPath: proto.String("/p"), MediaKey: []byte{1}}
	cases := []struct {
		msg        *waE2E.Message
		typ, text  string
		media      string
		reactionTo string
		ok         bool
	}{
		{&waE2E.Message{Conversation: proto.String("oi")}, "text", "oi", "", "", true},
		{&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("link")}}, "text", "link", "", "", true},
		{&waE2E.Message{ImageMessage: img}, "image", "foto", "image", "", true},
		{&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "audio", "", "audio", "", true},
		{&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf")}}, "document", "", "document", "", true},
		{&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "", "sticker", "", true},
		{&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍"), Key: &waCommon.MessageKey{ID: proto.String("T1")}}}, "reaction", "👍", "", "T1", true},
		{&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana")}}, "contact", "Ana", "", "", true},
		{&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}, "", "", "", "", false},
		{nil, "", "", "", "", false},
	}
	for i, c := range cases {
		typ, text, media, reactionTo, ok := content(c.msg)
		if typ != c.typ || text != c.text || reactionTo != c.reactionTo || ok != c.ok || media.GetKind() != c.media {
			t.Errorf("case %d: got %q %q %v %q %v", i, typ, text, media, reactionTo, ok)
		}
	}
	_, _, media, _, _ := content(&waE2E.Message{ImageMessage: img})
	if media.Mime != "image/jpeg" || media.Size != 42 {
		t.Fatalf("media = %+v", media)
	}
	back, err := downloadable("image", media.Ref)
	if err != nil || back.GetDirectPath() != "/p" || string(back.GetMediaKey()) != "\x01" {
		t.Fatalf("ref round trip = %v, %v", back, err)
	}
	if _, err := downloadable("hologram", media.Ref); err == nil {
		t.Fatal("unknown kind must fail")
	}
}

func TestSendErrCodes(t *testing.T) {
	cases := []struct {
		err  error
		code codes.Code
	}{
		{whatsmeow.ErrNotConnected, codes.Unavailable},
		{whatsmeow.ErrNotLoggedIn, codes.FailedPrecondition},
		{whatsmeow.ErrMessageTimedOut, codes.DeadlineExceeded},
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{errors.New("no LID found for 5511@s.whatsapp.net from server"), codes.InvalidArgument},
		{whatsmeow.ErrNoPermissionInGroup, codes.PermissionDenied},
		{errors.New("boom"), codes.Unknown},
	}
	for _, c := range cases {
		if got := status.Code(sendErr(c.err)); got != c.code {
			t.Errorf("%v -> %v, want %v", c.err, got, c.code)
		}
	}
	if msg := status.Convert(sendErr(errors.New("no LID found for x"))).Message(); msg != "invalid_number: recipient is not on WhatsApp" {
		t.Fatalf("message = %q", msg)
	}
}

func TestBuildMedia(t *testing.T) {
	up := whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", MediaKey: []byte{9}, FileLength: 3}
	m := buildMedia(&workerv1.SendRequest{Type: "audio", Mime: "audio/ogg", Ptt: true}, up)
	if !m.GetAudioMessage().GetPTT() || m.GetAudioMessage().GetDirectPath() != "/d" {
		t.Fatalf("audio = %v", m)
	}
	d := buildMedia(&workerv1.SendRequest{Type: "document", Mime: "application/pdf", Text: "veja"}, up)
	if d.GetDocumentMessage().GetFileName() != "file" || d.GetDocumentMessage().GetCaption() != "veja" {
		t.Fatalf("document = %v", d)
	}
	i := buildMedia(&workerv1.SendRequest{Type: "image", Mime: "image/png"}, up)
	if i.GetImageMessage().Caption != nil || i.GetImageMessage().GetFileLength() != 3 {
		t.Fatalf("image = %v", i)
	}
}

type dataRec struct {
	stateRec
	mu  sync.Mutex
	evs []*workerv1.Event
}

func (r *dataRec) Data(ev *workerv1.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func TestHandleForwardsMessagesAndReceipts(t *testing.T) {
	own := types.NewJID("5511999999999", types.DefaultUserServer)
	w := NewWhatsmeow(whatsmeow.NewClient(&store.Device{ID: &own}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := &dataRec{}
	w.sink = r
	lid := types.NewJID("123456", types.HiddenUserServer)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	ts := time.Unix(1_800_000_000, 0)
	w.handle(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn},
			ID:            "WA1", PushName: "João", Timestamp: ts,
		},
		Message: &waE2E.Message{Conversation: proto.String("oi")},
	})
	w.handle(&events.Message{ // status broadcast: ignored
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: pn}, ID: "S1"},
		Message: &waE2E.Message{Conversation: proto.String("story")},
	})
	w.handle(&events.Receipt{
		MessageSource: types.MessageSource{Chat: pn, Sender: pn},
		MessageIDs:    []types.MessageID{"OUT1"}, Type: types.ReceiptTypeRead, Timestamp: ts,
	})
	w.handle(&events.Receipt{ // retry receipts are not delivery reports
		MessageSource: types.MessageSource{Chat: pn, Sender: pn},
		MessageIDs:    []types.MessageID{"OUT1"}, Type: types.ReceiptTypeRetry,
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != 2 {
		t.Fatalf("events = %v", r.evs)
	}
	m := r.evs[0].GetMessage()
	if m.GetChat() != pn.String() || m.GetSender() != pn.String() || m.GetText() != "oi" ||
		m.GetPushName() != "João" || m.GetTimestampMs() != ts.UnixMilli() {
		t.Fatalf("message = %+v", m)
	}
	rc := r.evs[1].GetReceipt()
	if rc.GetType() != "read" || len(rc.GetMessageIds()) != 1 || rc.GetMessageIds()[0] != "OUT1" {
		t.Fatalf("receipt = %+v", rc)
	}
}

func TestDataPathOnlyInstanceAreas(t *testing.T) {
	w := &Whatsmeow{dataDir: "/srv/litezap/data", instanceID: "i1"}
	if p, err := w.dataPath("/srv/litezap/data/outbox/i1/msg_1", "outbox"); err != nil || p != "/srv/litezap/data/outbox/i1/msg_1" {
		t.Fatalf("outbox = %q, %v", p, err)
	}
	if _, err := w.dataPath("/srv/litezap/data/media/i1/med_1-123.part", "media"); err != nil {
		t.Fatalf("media = %v", err)
	}
	bad := []struct{ path, area string }{
		{"", "outbox"}, {"relative/file", "outbox"}, {"/etc/hosts", "outbox"},
		{"/srv/litezap/data/litezap.db", "outbox"}, {"/srv/litezap/data/media_key", "media"},
		{"/srv/litezap/data/outbox/i2/msg_1", "outbox"},        // another instance
		{"/srv/litezap/data/media/i1/med_1", "outbox"},         // reading from media
		{"/srv/litezap/data/outbox/i1/msg_1", "media"},         // writing to outbox
		{"/srv/litezap/data/instances/i1/session.db", "media"}, // own session
		{"/srv/litezap/data/outbox/i1/../../litezap.db", "outbox"},
		{"/srv/litezap/data/outbox/i1/sub/x", "outbox"},
	}
	for _, b := range bad {
		if _, err := w.dataPath(b.path, b.area); status.Code(err) != codes.InvalidArgument {
			t.Errorf("dataPath(%q, %s) = %v, want InvalidArgument", b.path, b.area, err)
		}
	}
}

func newMsgAdapter(t *testing.T) *Whatsmeow {
	t.Helper()
	own := types.NewJID("5511999999999", types.DefaultUserServer)
	w := NewWhatsmeow(whatsmeow.NewClient(&store.Device{ID: &own}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.sink = &dataRec{}
	return w
}

// The mappings shipped inside the history blob are used directly: whatsmeow stores them
// from a goroutine that may not have finished when the event is handled.
func TestHistoryResolvesLIDFromBlob(t *testing.T) {
	w := newMsgAdapter(t)
	r := w.sink.(*dataRec)
	web := func(chat, participant, id string) *waWeb.WebMessageInfo {
		key := &waCommon.MessageKey{RemoteJID: proto.String(chat), ID: proto.String(id)}
		if participant != "" {
			key.Participant = proto.String(participant)
		}
		return &waWeb.WebMessageInfo{Key: key, MessageTimestamp: proto.Uint64(1_800_000_000),
			Message: &waE2E.Message{Conversation: proto.String("hi " + id)}}
	}
	hs := &waHistorySync.HistorySync{
		PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
			{PnJID: proto.String("5511888887777@s.whatsapp.net"), LidJID: proto.String("111@lid")},
			{PnJID: proto.String("5511777776666"), LidJID: proto.String("222")}, // bare user parts
		},
		Conversations: []*waHistorySync.Conversation{
			{ID: proto.String("111@lid"), Messages: []*waHistorySync.HistorySyncMsg{{Message: web("111@lid", "", "M1")}}},
			// Conversation-level mapping only.
			{ID: proto.String("333@lid"), PnJID: proto.String("5511666665555@s.whatsapp.net"),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: web("333@lid", "", "M2")}}},
			// Group whose participant is a LID.
			{ID: proto.String("120363@g.us"), Name: proto.String("G"),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: web("120363@g.us", "222@lid", "M3")}}},
		},
	}
	w.onHistory(&events.HistorySync{Data: hs})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != 3 {
		t.Fatalf("events = %d", len(r.evs))
	}
	got := func(i int) *workerv1.HistoryChunk { return r.evs[i].GetHistory() }
	if c := got(0); c.Chat != "5511888887777@s.whatsapp.net" || c.Messages[0].Sender != c.Chat {
		t.Fatalf("lid dm = %+v", c)
	}
	if c := got(1); c.Chat != "5511666665555@s.whatsapp.net" {
		t.Fatalf("conversation mapping = %+v", c)
	}
	if c := got(2); c.Chat != "120363@g.us" || c.Messages[0].Sender != "5511777776666@s.whatsapp.net" {
		t.Fatalf("group participant = %+v", c.Messages[0])
	}
}

func TestGroupParticipantAddressing(t *testing.T) {
	w := newMsgAdapter(t)
	group := types.NewJID("120363", types.GroupServer)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	lid := types.NewJID("555", types.HiddenUserServer)
	calls := 0
	mode := types.AddressingModeLID
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		calls++
		return &types.GroupInfo{AddressingMode: mode}, nil
	}
	w.lidFor = func(_ context.Context, p types.JID) (types.JID, error) { return lid, nil }
	ctx := context.Background()
	if got := w.groupParticipant(ctx, group, pn); got != lid {
		t.Fatalf("lid group: %v", got)
	}
	w.groupParticipant(ctx, group, pn)
	if calls != 1 {
		t.Fatalf("group info not cached: %d calls", calls)
	}
	// Phone-addressed group keeps the phone participant.
	pnGroup := types.NewJID("999", types.GroupServer)
	mode = types.AddressingModePN
	if got := w.groupParticipant(ctx, pnGroup, pn); got != pn {
		t.Fatalf("pn group: %v", got)
	}
	// Lookup failures fall back to the participant as given (and are not cached).
	errGroup := types.NewJID("777", types.GroupServer)
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) { return nil, errors.New("boom") }
	if got := w.groupParticipant(ctx, errGroup, pn); got != pn {
		t.Fatalf("error fallback: %v", got)
	}
	// A LID participant, an empty one or a direct chat are left alone.
	if got := w.groupParticipant(ctx, group, lid); got != lid {
		t.Fatalf("lid participant: %v", got)
	}
	if got := w.groupParticipant(ctx, pn, pn); got != pn {
		t.Fatalf("direct chat: %v", got)
	}
}

func TestGroupReactionNeedsSender(t *testing.T) {
	w := newMsgAdapter(t)
	_, err := w.Send(context.Background(), &workerv1.SendRequest{
		To: "120363@g.us", MessageId: "X", Type: "reaction", Text: "👍", ReactionMessageId: "T1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadErrCodes(t *testing.T) {
	http := func(code int) error {
		return fmt.Errorf("wrap: %w", whatsmeow.DownloadHTTPError{Response: &nethttp.Response{StatusCode: code}})
	}
	cases := []struct {
		err  error
		code codes.Code
	}{
		{http(403), codes.NotFound}, {http(404), codes.NotFound}, {http(410), codes.NotFound},
		{http(500), codes.Unavailable}, {http(429), codes.Unavailable}, {http(503), codes.Unavailable},
		{whatsmeow.ErrNoURLPresent, codes.NotFound},
		{errors.New("boom"), codes.Unknown},
	}
	for _, c := range cases {
		if got := status.Code(downloadErr(c.err)); got != c.code {
			t.Errorf("%v -> %v, want %v", c.err, got, c.code)
		}
	}
}

func TestReceiptTypes(t *testing.T) {
	for in, want := range map[types.ReceiptType]string{
		types.ReceiptTypeDelivered: "delivered", types.ReceiptTypeInactive: "delivered",
		types.ReceiptTypeRead: "read", types.ReceiptTypePlayed: "played",
	} {
		if got, ok := receiptType(in); !ok || got != want {
			t.Errorf("%q -> %q, %v", in, got, ok)
		}
	}
	if _, ok := receiptType(types.ReceiptTypeRetry); ok {
		t.Error("retry must not be a delivery report")
	}
}
```

Criar `internal/worker/e2e_dup_test.go`:

```go
//go:build e2e

package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestE2EDuplicateMessageID sends the same text twice, with the same WhatsApp message id,
// straight to a running worker (manual verification, real number). Expected on the phone:
// one message. Run with:
//
//	LITEZAP_E2E_DATA_DIR=$PWD/data LITEZAP_E2E_INSTANCE=$ID LITEZAP_E2E_TO=5511...@s.whatsapp.net \
//	  go test -tags=e2e -run TestE2EDuplicateMessageID -v ./internal/worker/
func TestE2EDuplicateMessageID(t *testing.T) {
	dataDir, id, to := os.Getenv("LITEZAP_E2E_DATA_DIR"), os.Getenv("LITEZAP_E2E_INSTANCE"), os.Getenv("LITEZAP_E2E_TO")
	if dataDir == "" || id == "" || to == "" {
		t.Skip("set LITEZAP_E2E_DATA_DIR, LITEZAP_E2E_INSTANCE and LITEZAP_E2E_TO")
	}
	sock := spawner.SocketPath(dataDir, id)
	cc, err := grpc.NewClient("passthrough:///worker",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()), wire.DialOption())
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	cli := workerv1.NewWorkerClient(cc)
	b := make([]byte, 9)
	rand.Read(b)
	msgID := "3EB0" + strings.ToUpper(hex.EncodeToString(b))
	for i := 1; i <= 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := cli.Send(ctx, &workerv1.SendRequest{To: to, MessageId: msgID, Type: "text", Text: "teste de id duplicado " + msgID[4:10]})
		cancel()
		t.Logf("send #%d: %v, %v", i, resp, err)
	}
	t.Logf("check the phone: exactly one message %q", "teste de id duplicado "+msgID[4:10])
}
```

Run: `go test ./internal/worker/`
Expected: FAIL — `undefined: content`, `undefined: sendErr`, `undefined: buildMedia`, `w.dataPath undefined`.

- [ ] **Step 2: Implementar**

Criar `internal/worker/messaging.go`:

```go
package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// historyChunkSize bounds the messages of one HistoryChunk event (and so the supervisor's
// write transaction for it).
const historyChunkSize = 250

var _ Messenger = (*Whatsmeow)(nil)

// grpcErr wraps a failure as a status whose message starts with a stable code.
func grpcErr(c codes.Code, code, msg string) error {
	return status.Error(c, code+": "+msg)
}

// sendErr maps whatsmeow send/upload errors onto the codes documented in worker.proto.
func sendErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrNotConnected):
		return grpcErr(codes.Unavailable, "instance_not_connected", "websocket not connected")
	case errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	case errors.Is(err, whatsmeow.ErrMessageTimedOut), errors.Is(err, context.DeadlineExceeded):
		return grpcErr(codes.DeadlineExceeded, "worker_timeout", err.Error())
	case errors.Is(err, whatsmeow.ErrRecipientADJID), errors.Is(err, whatsmeow.ErrUnknownServer):
		return grpcErr(codes.InvalidArgument, "invalid_number", err.Error())
	case errors.Is(err, whatsmeow.ErrNoPermissionInGroup):
		return grpcErr(codes.PermissionDenied, "forbidden", err.Error())
	case strings.Contains(err.Error(), "no LID found"):
		// whatsmeow cannot resolve a phone number that has no WhatsApp account.
		return grpcErr(codes.InvalidArgument, "invalid_number", "recipient is not on WhatsApp")
	}
	return grpcErr(codes.Unknown, "worker_error", err.Error())
}

func mediaType(kind string) (whatsmeow.MediaType, bool) {
	switch kind {
	case "image":
		return whatsmeow.MediaImage, true
	case "video":
		return whatsmeow.MediaVideo, true
	case "audio":
		return whatsmeow.MediaAudio, true
	case "document":
		return whatsmeow.MediaDocument, true
	}
	return "", false
}

// buildMedia builds the message for an uploaded attachment.
func buildMedia(req *workerv1.SendRequest, up whatsmeow.UploadResponse) *waE2E.Message {
	var caption *string
	if req.Text != "" {
		caption = proto.String(req.Text)
	}
	switch req.Type {
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption: caption, Mimetype: proto.String(req.Mime),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	case "video":
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption: caption, Mimetype: proto.String(req.Mime),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	case "audio":
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			Mimetype: proto.String(req.Mime), PTT: proto.Bool(req.Ptt),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	default: // document
		name := req.FileName
		if name == "" {
			name = "file"
		}
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Caption: caption, Mimetype: proto.String(req.Mime), FileName: proto.String(name), Title: proto.String(name),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	}
}

// dataPath checks that p is a file directly inside <dataDir>/<area>/<instance>: Send only
// reads from "outbox" and DownloadMedia only writes to "media", each for this instance.
// The supervisor passes paths; the worker must not touch anything else (other instances,
// litezap.db, media_key, its own session).
func (w *Whatsmeow) dataPath(p, area string) (string, error) {
	if w.dataDir == "" || w.instanceID == "" || p == "" || !filepath.IsAbs(p) {
		return "", grpcErr(codes.InvalidArgument, "validation_failed", "media path must be absolute")
	}
	p = filepath.Clean(p)
	if filepath.Dir(p) != filepath.Join(w.dataDir, area, w.instanceID) {
		return "", grpcErr(codes.InvalidArgument, "validation_failed", "media path outside the instance's "+area+" directory")
	}
	return p, nil
}

func (w *Whatsmeow) Send(ctx context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	to, err := types.ParseJID(req.To)
	if err != nil || to.User == "" {
		return nil, grpcErr(codes.InvalidArgument, "invalid_number", "invalid recipient JID")
	}
	var msg *waE2E.Message
	switch req.Type {
	case "text":
		msg = &waE2E.Message{Conversation: proto.String(req.Text)}
	case "reaction":
		sender := to // reacting to a message the contact sent in a direct chat
		if req.ReactionFromMe {
			sender = types.EmptyJID
		} else if req.ReactionSender != "" {
			if sender, err = types.ParseJID(req.ReactionSender); err != nil {
				return nil, grpcErr(codes.InvalidArgument, "validation_failed", "invalid reaction sender")
			}
		} else if to.Server == types.GroupServer {
			// The key of a group reaction names the author of the target message.
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", "reaction_sender is required in groups")
		}
		sender = w.groupParticipant(ctx, to, sender)
		msg = w.cli.BuildReaction(to, sender, types.MessageID(req.ReactionMessageId), req.Text)
	default:
		mt, ok := mediaType(req.Type)
		if !ok {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", "unsupported message type "+req.Type)
		}
		path, err := w.dataPath(req.MediaPath, "outbox")
		if err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", "media file is missing")
		}
		// Streams the file (encrypted into a temporary file) instead of loading it in memory.
		up, err := w.cli.UploadReader(ctx, f, nil, mt)
		f.Close()
		if err != nil {
			return nil, grpcErr(codes.Unavailable, "media_upload_failed", err.Error())
		}
		msg = buildMedia(req, up)
	}
	resp, err := w.cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: types.MessageID(req.MessageId)})
	if err != nil {
		return nil, sendErr(err)
	}
	return &workerv1.SendResponse{MessageId: string(resp.ID), TimestampMs: resp.Timestamp.UnixMilli()}, nil
}

func (w *Whatsmeow) SendPresence(ctx context.Context, req *workerv1.SendPresenceRequest) error {
	to, err := types.ParseJID(req.To)
	if err != nil {
		return grpcErr(codes.InvalidArgument, "validation_failed", "invalid JID")
	}
	state, media := types.ChatPresenceComposing, types.ChatPresenceMediaText
	switch req.State {
	case "recording":
		media = types.ChatPresenceMediaAudio
	case "paused":
		state = types.ChatPresencePaused
	}
	if err := w.cli.SendChatPresence(ctx, to, state, media); err != nil {
		return sendErr(err)
	}
	return nil
}

func (w *Whatsmeow) MarkRead(ctx context.Context, req *workerv1.MarkReadRequest) error {
	chat, err := types.ParseJID(req.Chat)
	if err != nil {
		return grpcErr(codes.InvalidArgument, "validation_failed", "invalid chat JID")
	}
	sender := types.EmptyJID
	if req.Sender != "" {
		if sender, err = types.ParseJID(req.Sender); err != nil {
			return grpcErr(codes.InvalidArgument, "validation_failed", "invalid sender JID")
		}
	}
	ids := make([]types.MessageID, len(req.MessageIds))
	for i, id := range req.MessageIds {
		ids[i] = types.MessageID(id)
	}
	sender = w.groupParticipant(ctx, chat, sender)
	if err := w.cli.MarkRead(ctx, ids, time.Now(), chat, sender); err != nil {
		return sendErr(err)
	}
	return nil
}

// groupParticipant converts a phone-number participant to its LID when chat is a group
// that addresses members by LID: the key of a reaction and the read receipt must name the
// participant the way the group does. Anything it cannot determine (lookup failure, no
// mapping) leaves the participant as given.
func (w *Whatsmeow) groupParticipant(ctx context.Context, chat, p types.JID) types.JID {
	if chat.Server != types.GroupServer || p.Server != types.DefaultUserServer {
		return p
	}
	if !w.groupUsesLID(ctx, chat) {
		return p
	}
	lidFor := w.lidFor
	if lidFor == nil {
		if w.cli.Store.LIDs == nil {
			return p
		}
		lidFor = w.cli.Store.LIDs.GetLIDForPN
	}
	if lid, err := lidFor(ctx, p.ToNonAD()); err == nil && !lid.IsEmpty() {
		return lid.ToNonAD()
	}
	return p
}

// groupUsesLID reports whether the group addresses its members by LID. The answer is
// cached for the life of the worker (a group's addressing mode does not change); failed
// lookups are not cached.
func (w *Whatsmeow) groupUsesLID(ctx context.Context, group types.JID) bool {
	w.groupMu.Lock()
	lid, ok := w.groupLID[group]
	w.groupMu.Unlock()
	if ok {
		return lid
	}
	info := w.groupInfo
	if info == nil {
		info = w.cli.GetGroupInfo
	}
	gi, err := info(ctx, group)
	if err != nil || gi == nil {
		return false
	}
	lid = gi.AddressingMode == types.AddressingModeLID
	w.groupMu.Lock()
	if w.groupLID == nil {
		w.groupLID = map[types.JID]bool{}
	}
	w.groupLID[group] = lid
	w.groupMu.Unlock()
	return lid
}

// mediaRef serializes the attachment sub-message: it carries everything needed to download it later.
func mediaRef(m proto.Message) []byte {
	b, _ := proto.Marshal(m)
	return b
}

// downloadable rebuilds the attachment sub-message from a mediaRef.
func downloadable(kind string, ref []byte) (whatsmeow.DownloadableMessage, error) {
	var m interface {
		proto.Message
		whatsmeow.DownloadableMessage
	}
	switch kind {
	case "image":
		m = &waE2E.ImageMessage{}
	case "video":
		m = &waE2E.VideoMessage{}
	case "audio":
		m = &waE2E.AudioMessage{}
	case "document":
		m = &waE2E.DocumentMessage{}
	case "sticker":
		m = &waE2E.StickerMessage{}
	default:
		return nil, fmt.Errorf("unknown media kind %q", kind)
	}
	if err := proto.Unmarshal(ref, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (w *Whatsmeow) DownloadMedia(ctx context.Context, kind string, ref []byte, dest string) (int64, error) {
	msg, err := downloadable(kind, ref)
	if err != nil {
		return 0, grpcErr(codes.InvalidArgument, "validation_failed", err.Error())
	}
	path, err := w.dataPath(dest, "media")
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, grpcErr(codes.Unknown, "worker_error", "cannot create media file")
	}
	err = w.cli.DownloadToFile(ctx, msg, f)
	var size int64
	if fi, serr := f.Stat(); serr == nil {
		size = fi.Size()
	}
	f.Close()
	if err != nil {
		os.Remove(path)
		return 0, downloadErr(err)
	}
	return size, nil
}

// downloadErr maps a download failure: only 403/404/410 (and a missing URL) mean the
// media is gone for good; other HTTP statuses (5xx, 429) are worth retrying later.
func downloadErr(err error) error {
	var httpErr whatsmeow.DownloadHTTPError
	switch {
	case errors.As(err, &httpErr):
		switch httpErr.StatusCode {
		case 403, 404, 410:
			return grpcErr(codes.NotFound, "media_unavailable", err.Error())
		}
		return grpcErr(codes.Unavailable, "media_download_failed", err.Error())
	case errors.Is(err, whatsmeow.ErrNoURLPresent):
		return grpcErr(codes.NotFound, "media_unavailable", err.Error())
	}
	return grpcErr(codes.Unknown, "media_download_failed", err.Error())
}

func contactOf(jid types.JID, c types.ContactInfo) *workerv1.Contact {
	name := c.FullName
	if name == "" {
		name = c.FirstName
	}
	return &workerv1.Contact{Jid: jid.String(), Name: name, PushName: c.PushName, BusinessName: c.BusinessName}
}

func (w *Whatsmeow) Contacts(ctx context.Context) ([]*workerv1.Contact, error) {
	all, err := w.cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, grpcErr(codes.Unknown, "worker_error", err.Error())
	}
	out := make([]*workerv1.Contact, 0, len(all))
	for jid, c := range all {
		out = append(out, contactOf(jid, c))
	}
	return out, nil
}

func (w *Whatsmeow) Contact(ctx context.Context, raw string) (*workerv1.Contact, bool, error) {
	jid, err := types.ParseJID(raw)
	if err != nil {
		return nil, false, grpcErr(codes.InvalidArgument, "validation_failed", "invalid JID")
	}
	c, err := w.cli.Store.Contacts.GetContact(ctx, jid)
	if err != nil {
		return nil, false, grpcErr(codes.Unknown, "worker_error", err.Error())
	}
	return contactOf(jid, c), c.Found, nil
}

func (w *Whatsmeow) CheckNumbers(ctx context.Context, phones []string) ([]*workerv1.NumberStatus, error) {
	query := make([]string, len(phones))
	for i, p := range phones {
		query[i] = "+" + p // IsOnWhatsApp wants international format with "+"
	}
	res, err := w.cli.IsOnWhatsApp(ctx, query)
	if err != nil {
		return nil, sendErr(err)
	}
	byPhone := map[string]types.IsOnWhatsAppResponse{}
	for _, r := range res {
		byPhone[strings.TrimPrefix(r.Query, "+")] = r
	}
	out := make([]*workerv1.NumberStatus, 0, len(phones))
	for _, p := range phones {
		ns := &workerv1.NumberStatus{Phone: p}
		if r, ok := byPhone[p]; ok && r.IsIn {
			ns.Exists = true
			jid := r.JID
			if r.PhoneNumber.Server == types.DefaultUserServer {
				jid = r.PhoneNumber // prefer the phone JID over a LID
			}
			ns.Jid = jid.ToNonAD().String()
		}
		out = append(out, ns)
	}
	return out, nil
}

// content extracts the LiteZap type, text, media and reaction target of a message.
// ok=false means the message carries nothing to log (protocol messages, revokes, edits...).
func content(m *waE2E.Message) (typ, text string, media *workerv1.Media, reactionTo string, ok bool) {
	switch {
	case m == nil:
		return "", "", nil, "", false
	case m.GetConversation() != "":
		return "text", m.GetConversation(), nil, "", true
	case m.GetExtendedTextMessage() != nil:
		return "text", m.GetExtendedTextMessage().GetText(), nil, "", true
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return "image", x.GetCaption(), &workerv1.Media{Kind: "image", Mime: x.GetMimetype(), Size: x.GetFileLength(), Ref: mediaRef(x)}, "", true
	case m.GetVideoMessage() != nil:
		x := m.GetVideoMessage()
		return "video", x.GetCaption(), &workerv1.Media{Kind: "video", Mime: x.GetMimetype(), Size: x.GetFileLength(), Seconds: x.GetSeconds(), Ref: mediaRef(x)}, "", true
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		return "audio", "", &workerv1.Media{Kind: "audio", Mime: x.GetMimetype(), Size: x.GetFileLength(), Seconds: x.GetSeconds(), Ptt: x.GetPTT(), Ref: mediaRef(x)}, "", true
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		return "document", x.GetCaption(), &workerv1.Media{Kind: "document", Mime: x.GetMimetype(), Size: x.GetFileLength(), FileName: x.GetFileName(), Ref: mediaRef(x)}, "", true
	case m.GetStickerMessage() != nil:
		x := m.GetStickerMessage()
		return "sticker", "", &workerv1.Media{Kind: "sticker", Mime: x.GetMimetype(), Size: x.GetFileLength(), Ref: mediaRef(x)}, "", true
	case m.GetLocationMessage() != nil:
		x := m.GetLocationMessage()
		return "location", strings.TrimSpace(fmt.Sprintf("%f,%f %s", x.GetDegreesLatitude(), x.GetDegreesLongitude(), x.GetName())), nil, "", true
	case m.GetContactMessage() != nil:
		return "contact", m.GetContactMessage().GetDisplayName(), nil, "", true
	case m.GetReactionMessage() != nil:
		x := m.GetReactionMessage()
		return "reaction", x.GetText(), nil, x.GetKey().GetID(), true
	case m.GetProtocolMessage() != nil, m.GetSenderKeyDistributionMessage() != nil:
		return "", "", nil, "", false
	}
	return "unknown", "", nil, "", true
}

// lidLookupTimeout bounds each LID store lookup. History sync can block for a long time on
// backpressure, so no overall deadline covers a whole blob.
const lidLookupTimeout = 2 * time.Second

// lidMap maps LID users to phone-number JIDs learned from a history sync blob.
type lidMap map[string]types.JID

// add records lid -> pn; both may be full JIDs or bare user parts.
func (m lidMap) add(lid, pn string) {
	if lid == "" || pn == "" {
		return
	}
	l, err1 := parseJIDOr(lid, types.HiddenUserServer)
	p, err2 := parseJIDOr(pn, types.DefaultUserServer)
	if err1 != nil || err2 != nil || l.Server != types.HiddenUserServer || p.Server != types.DefaultUserServer {
		return
	}
	m[l.User] = p.ToNonAD()
}

func parseJIDOr(s, server string) (types.JID, error) {
	if !strings.Contains(s, "@") {
		return types.NewJID(s, server), nil
	}
	return types.ParseJID(s)
}

// phoneJID returns the phone-number JID for a LID when it is known (the blob's mappings,
// alt, or the LID store), so a contact keeps one chat whatever addressing WhatsApp uses.
func (w *Whatsmeow) phoneJID(jid, alt types.JID, known lidMap) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid.ToNonAD()
	}
	if pn, ok := known[jid.User]; ok {
		return pn
	}
	if alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if w.cli.Store.LIDs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), lidLookupTimeout)
		defer cancel()
		if pn, err := w.cli.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid.ToNonAD()
}

// toMessage normalizes a whatsmeow message; nil means it is not logged.
func (w *Whatsmeow) toMessage(info types.MessageInfo, m *waE2E.Message, known lidMap) *workerv1.Message {
	if info.Chat.Server == types.BroadcastServer || info.Chat.Server == types.NewsletterServer {
		return nil // status updates, broadcast lists and channels are out of scope
	}
	typ, text, media, reactionTo, ok := content(m)
	if !ok {
		return nil
	}
	chat := info.Chat.ToNonAD()
	if !info.IsGroup {
		alt := info.SenderAlt
		if info.IsFromMe {
			alt = info.RecipientAlt
		}
		chat = w.phoneJID(info.Chat, alt, known)
	}
	return &workerv1.Message{
		Id: string(info.ID), Chat: chat.String(), Sender: w.phoneJID(info.Sender, info.SenderAlt, known).String(),
		FromMe: info.IsFromMe, IsGroup: info.IsGroup, TimestampMs: info.Timestamp.UnixMilli(),
		PushName: info.PushName, Type: typ, Text: text, Media: media, ReactionTo: reactionTo,
	}
}

func receiptType(t types.ReceiptType) (string, bool) {
	switch t {
	case types.ReceiptTypeDelivered, types.ReceiptTypeInactive:
		return "delivered", true
	case types.ReceiptTypeRead:
		return "read", true
	case types.ReceiptTypePlayed:
		return "played", true
	}
	return "", false
}

// onData handles the message-related whatsmeow events; it reports whether evt was one.
func (w *Whatsmeow) onData(evt any) bool {
	switch e := evt.(type) {
	case *events.Message:
		if m := w.toMessage(e.Info, e.Message, nil); m != nil {
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Message{Message: m}})
		}
	case *events.Receipt:
		typ, ok := receiptType(e.Type)
		if !ok || e.IsFromMe { // our own devices' read receipts are not delivery reports
			return true
		}
		ids := make([]string, len(e.MessageIDs))
		for i, id := range e.MessageIDs {
			ids[i] = string(id)
		}
		w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: &workerv1.Receipt{
			MessageIds: ids, Chat: e.Chat.ToNonAD().String(), Type: typ, TimestampMs: e.Timestamp.UnixMilli(),
		}}})
	case *events.HistorySync:
		w.onHistory(e)
	default:
		return false
	}
	return true
}

// onHistory turns a history blob into HistoryChunk events. Sink.Data may block for a long
// time (backpressure), so nothing here has an overall deadline. LIDs are resolved from the
// blob's own mappings first: whatsmeow stores them from a goroutine that can still be
// running, so the LID store alone is not reliable yet.
func (w *Whatsmeow) onHistory(e *events.HistorySync) {
	known := lidMap{}
	for _, m := range e.Data.GetPhoneNumberToLidMappings() {
		known.add(m.GetLidJID(), m.GetPnJID())
	}
	for _, conv := range e.Data.GetConversations() {
		known.add(conv.GetLidJID(), conv.GetPnJID())
		if id := conv.GetID(); strings.HasSuffix(id, "@"+types.HiddenUserServer) {
			known.add(id, conv.GetPnJID())
		}
	}
	for _, conv := range e.Data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		name := conv.GetName()
		if name == "" {
			name = conv.GetDisplayName()
		}
		var msgs []*workerv1.Message
		flush := func() {
			if len(msgs) == 0 {
				return
			}
			chat := msgs[0].Chat
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{
				Chat: chat, Name: name, IsGroup: chatJID.Server == types.GroupServer, Messages: msgs,
			}}})
			msgs = nil
		}
		for _, hm := range conv.GetMessages() {
			evt, err := w.cli.ParseWebMessage(chatJID, hm.GetMessage())
			if err != nil {
				continue
			}
			if m := w.toMessage(evt.Info, evt.Message, known); m != nil {
				msgs = append(msgs, m)
			}
			if len(msgs) >= historyChunkSize {
				flush()
			}
		}
		flush()
	}
}
```

Em `internal/worker/whatsmeow.go` (struct `Whatsmeow`), trocar:

```go
	// proxied is set when the client goes through a proxy (spec 4.6): connection errors
	// are then reported as proxy_unreachable. There is never a fallback to direct.
	proxied bool
```

por:

```go
	// proxied is set when the client goes through a proxy (spec 4.6): connection errors
	// are then reported as proxy_unreachable. There is never a fallback to direct.
	proxied bool
	// dataDir and instanceID bound the files Send reads (outbox/<instance>/) and
	// DownloadMedia writes (media/<instance>/).
	dataDir    string
	instanceID string

	// Group addressing (reactions and read receipts must name participants the way the
	// group does). groupInfo and lidFor default to the client and its LID store; tests
	// replace them.
	groupMu   sync.Mutex
	groupLID  map[types.JID]bool // group -> addresses members by LID (successful lookups only)
	groupInfo func(context.Context, types.JID) (*types.GroupInfo, error)
	lidFor    func(context.Context, types.JID) (types.JID, error)
```

Em `internal/worker/whatsmeow.go` (imports), trocar:

```go
	"go.mau.fi/whatsmeow/types/events"
```

por:

```go
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
```

> Revisão pós-replay: os blocos de `messaging.go`/`messaging_test.go` abaixo já incluem as correções do histórico (sem prazo geral, mapeamentos LID do próprio blob, consultas ao store com 2s cada), endereçamento LID em reações/`MarkRead` de grupos, mapeamento dos erros de download e `inactive` como `delivered` (commits `a790ab9` e `e9e784a`).


Em `internal/worker/whatsmeow.go`, trocar:

```go
func (w *Whatsmeow) handle(evt any) {
	loggedIn := w.cli.Store.ID != nil
```

por:

```go
func (w *Whatsmeow) handle(evt any) {
	if w.onData(evt) {
		return
	}
	loggedIn := w.cli.Store.ID != nil
```

Em `internal/worker/run.go`, trocar:

```go
	wa.proxied = opt.ProxyURL != ""
```

por:

```go
	wa.proxied = opt.ProxyURL != ""
	wa.dataDir, wa.instanceID = opt.DataDir, opt.InstanceID
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race ./internal/worker/ && go vet -tags=e2e ./internal/worker/`
Expected: `ok`; o teste `e2e` compila (sem as variáveis de ambiente ele é pulado).

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/worker
git commit -m "feat(worker): envio e download por arquivo, contatos, números e eventos de mensagem via whatsmeow" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 9: Manager e ingestão

### Task 9: Manager: observers, acesso ao worker, primeira conexão e histórico em paralelo

**Files:**
- Modify: `internal/manager/manager.go`
- Test: `internal/manager/manager_test.go`

O manager continua sem lógica de mensagens: repassa eventos que não são de conexão a um `Observer` e avisa cada estado persistido. Blocos de histórico vão para uma goroutine própria (um por vez), então gravar um history sync grande não atrasa estados, heartbeats nem mensagens ao vivo além do bloco em gravação. `Client(id)` expõe o cliente gRPC do worker vivo para a fila e o service. Ao chegar em `connected`, grava `first_connected_at` (início da rampa).

- [ ] **Step 1: Escrever os testes**

Em `internal/manager/manager_test.go` (imports), trocar:

```go
	"github.com/douglasdf99/litezap/internal/instance"
```

por:

```go
	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
```

Acrescentar ao fim de `internal/manager/manager_test.go`:

```go
type recObserver struct {
	mu     sync.Mutex
	events []*workerv1.Event
	states []instance.State
}

func (r *recObserver) WorkerEvent(_ string, ev *workerv1.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recObserver) StateChanged(_ string, st instance.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, st)
}

func (r *recObserver) snapshot() ([]*workerv1.Event, []instance.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*workerv1.Event(nil), r.events...), append([]instance.State(nil), r.states...)
}

func TestObserverGetsDataEventsAndStates(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &recObserver{}
	m.SetObserver(Observers{obs})
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	sp.Last("i1").EmitMessage(&workerv1.Message{Id: "W1", Chat: "a@s.whatsapp.net", Type: "text"})
	eventually(t, "message observed", func() bool {
		evs, _ := obs.snapshot()
		return len(evs) == 1 && evs[0].GetMessage().GetId() == "W1"
	})
	_, states := obs.snapshot()
	if len(states) < 2 || states[0] != instance.Starting || states[len(states)-1] != instance.Connected {
		t.Fatalf("states = %v", states)
	}
	in, _ := st.GetInstance(context.Background(), "i1")
	if in.FirstConnectedAt.IsZero() {
		t.Fatal("first connection not recorded")
	}
	if cli, err := m.Client("i1"); err != nil || cli == nil {
		t.Fatalf("client = %v, %v", cli, err)
	}
	if _, err := m.Client("nope"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("client of unknown = %v", err)
	}
}

type blockingHistory struct {
	recObserver
	release chan struct{}
}

func (b *blockingHistory) WorkerEvent(id string, ev *workerv1.Event) {
	if ev.GetHistory() != nil {
		<-b.release
	}
	b.recObserver.WorkerEvent(id, ev)
}

// A slow history write must not hold up state changes or live messages.
func TestHistoryDoesNotBlockStateAndLiveEvents(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &blockingHistory{release: make(chan struct{})}
	defer close(obs.release)
	m.SetObserver(obs)
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	f := sp.Last("i1")
	f.EmitHistory(&workerv1.HistoryChunk{Chat: "a@s.whatsapp.net"})
	f.EmitHistory(&workerv1.HistoryChunk{Chat: "b@s.whatsapp.net"})
	f.EmitMessage(&workerv1.Message{Id: "LIVE", Chat: "c@s.whatsapp.net"})
	f.Emit("reconnecting")
	eventually(t, "reconnecting while history is stuck", stateIs(t, st, "i1", instance.Reconnecting))
	eventually(t, "live message while history is stuck", func() bool {
		evs, _ := obs.snapshot()
		return len(evs) == 1 && evs[0].GetMessage().GetId() == "LIVE"
	})
}

// first_connected_at is only recorded when the connected state was actually persisted.
func TestFirstConnectedNeedsPersistedConnected(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &recObserver{}
	m.SetObserver(obs)
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "starting", stateIs(t, st, "i1", instance.Starting))
	// stopped -> connected is not an allowed transition: setState will refuse it.
	if err := st.UpdateInstanceState(context.Background(), "i1", instance.Stopped, ""); err != nil {
		t.Fatal(err)
	}
	f := sp.Last("i1")
	f.Emit("connected")
	f.EmitMessage(&workerv1.Message{Id: "AFTER", Chat: "a@s.whatsapp.net", Type: "text"})
	eventually(t, "message after connected", func() bool {
		evs, _ := obs.snapshot()
		return len(evs) == 1
	})
	in, _ := st.GetInstance(context.Background(), "i1")
	if in.State != instance.Stopped || !in.FirstConnectedAt.IsZero() {
		t.Fatalf("state=%s first=%v", in.State, in.FirstConnectedAt)
	}
}

// After Stop returns, the history consumer is done: nothing more is written for the worker.
func TestStopWaitsForHistoryWriter(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &slowHistory{}
	m.SetObserver(obs)
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	f := sp.Last("i1")
	for i := 0; i < 5; i++ {
		f.EmitHistory(&workerv1.HistoryChunk{Chat: "a@s.whatsapp.net"})
	}
	eventually(t, "first chunk being written", func() bool { return obs.count() >= 1 })
	if err := m.Stop(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	n := obs.count()
	time.Sleep(300 * time.Millisecond)
	if got := obs.count(); got != n {
		t.Fatalf("history written after Stop returned: %d -> %d", n, got)
	}
}

type slowHistory struct {
	mu sync.Mutex
	n  int
}

func (s *slowHistory) WorkerEvent(_ string, ev *workerv1.Event) {
	if ev.GetHistory() != nil {
		time.Sleep(100 * time.Millisecond)
		s.mu.Lock()
		s.n++
		s.mu.Unlock()
	}
}
func (s *slowHistory) StateChanged(string, instance.State) {}
func (s *slowHistory) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
```

Run: `go test ./internal/manager/`
Expected: FAIL — `m.SetObserver undefined`, `undefined: Observers`, `m.Client undefined`.

- [ ] **Step 2: Implementar**

Em `internal/manager/manager.go`, trocar:

```go
type Options struct {
```

por:

```go
// Observer receives worker data events (messages, receipts, history) and every persisted
// state change. Calls are synchronous, sometimes with the Manager's lock held: they must
// be quick and must never call into the Manager. History chunks arrive on a separate
// goroutine, concurrently with the other calls, so observers must be safe for concurrent use.
type Observer interface {
	WorkerEvent(instanceID string, ev *workerv1.Event)
	StateChanged(instanceID string, st instance.State)
}

// Observers fans out to several observers, in order.
type Observers []Observer

func (obs Observers) WorkerEvent(id string, ev *workerv1.Event) {
	for _, o := range obs {
		o.WorkerEvent(id, ev)
	}
}

func (obs Observers) StateChanged(id string, st instance.State) {
	for _, o := range obs {
		o.StateChanged(id, st)
	}
}

type nopObserver struct{}

func (nopObserver) WorkerEvent(string, *workerv1.Event) {}
func (nopObserver) StateChanged(string, instance.State) {}

type Options struct {
```

Em `internal/manager/manager.go` (struct `Manager`), trocar:

```go
	store   *store.Store
	spawner spawner.Spawner
	opt     Options
	log     *slog.Logger
```

por:

```go
	store    *store.Store
	spawner  spawner.Spawner
	opt      Options
	log      *slog.Logger
	observer Observer
```

Em `internal/manager/manager.go` (`New`), trocar:

```go
	return &Manager{store: st, spawner: sp, opt: opt, log: log, running: map[string]*supervised{}, retired: map[string]bool{}}
}
```

por:

```go
	return &Manager{store: st, spawner: sp, opt: opt, log: log, observer: nopObserver{},
		running: map[string]*supervised{}, retired: map[string]bool{}}
}

// SetObserver registers the observer of worker events and state changes. Call it before
// Boot or Start; it is not safe to change while instances run.
func (m *Manager) SetObserver(o Observer) { m.observer = o }
```

Em `internal/manager/manager.go`, trocar:

```go
// QR returns the latest QR code while the instance is waiting to be paired.
```

por:

```go
// Client returns the gRPC client of the instance's running worker (ErrNotRunning otherwise).
func (m *Manager) Client(id string) (workerv1.WorkerClient, error) {
	h, err := m.handle(id)
	if err != nil {
		return nil, err
	}
	return h.Client(), nil
}

// QR returns the latest QR code while the instance is waiting to be paired.
```

Em `internal/manager/manager.go` (`runWorker`: leitura do stream), trocar:

```go
	evs := make(chan *workerv1.Event)
	errc := make(chan error, 1)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			select {
			case evs <- ev:
			case <-sctx.Done():
				return
			}
		}
	}()

	tick := time.NewTicker(m.opt.HeartbeatInterval)
```

por:

```go
	evs := make(chan *workerv1.Event)
	errc := make(chan error, 1)
	// History chunks are written by their own goroutine, one at a time, so a large history
	// sync never holds up state changes and heartbeats; live events wait at most for the
	// chunk being written (the worker already sends live events before queued chunks).
	hist := make(chan *workerv1.Event, 1)
	var histDone sync.WaitGroup
	histDone.Add(1)
	go func() {
		defer histDone.Done()
		for ev := range hist {
			m.observer.WorkerEvent(s.id, ev)
		}
	}()
	// Once runWorker returns nothing more is written for this worker: cancel ends the
	// reader, which closes hist, and we wait for the writer to finish its current chunk.
	defer func() {
		cancel()
		histDone.Wait()
	}()
	go func() {
		defer close(hist)
		for {
			ev, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			out := evs
			if ev.GetHistory() != nil {
				out = hist
			}
			select {
			case out <- ev:
			case <-sctx.Done():
				return
			}
		}
	}()

	tick := time.NewTicker(m.opt.HeartbeatInterval)
```

Em `internal/manager/manager.go` (`runWorker`), trocar:

```go
			c := ev.GetConn()
			if c == nil {
				continue
			}
```

por:

```go
			c := ev.GetConn()
			if c == nil {
				m.observer.WorkerEvent(s.id, ev) // message or receipt
				continue
			}
```

Em `internal/manager/manager.go` (`runWorker`), trocar:

```go
			m.setState(s.id, st, c.Reason)
			if st.Terminal() {
```

por:

```go
			persisted := m.setState(s.id, st, c.Reason)
			if persisted && st == instance.Connected {
				if err := m.store.MarkFirstConnected(context.Background(), s.id, time.Now()); err != nil {
					m.log.Error("save first connection", "instance_id", s.id, "err", err)
				}
			}
			if st.Terminal() {
```

Em `internal/manager/manager.go` (`setState`), trocar o cabeçalho (comentário e assinatura):

```go
// setState persists a transition if the state machine allows it. Persisting logged_out
// also clears the instance's JID.
func (m *Manager) setState(id string, to instance.State, reason string) {
```

por:

```go
// setState persists a transition if the state machine allows it and reports whether it
// wrote one. Persisting logged_out also clears the instance's JID.
func (m *Manager) setState(id string, to instance.State, reason string) bool {
```

e trocar cada `return` sem valor dentro de `setState` (são quatro) por `return false`. Depois, trocar o fim:

```go
	m.log.Info("instance state", "instance_id", id, "from", in.State, "to", to, "reason", reason)
}
```

por:

```go
	m.log.Info("instance state", "instance_id", id, "from", in.State, "to", to, "reason", reason)
	m.observer.StateChanged(id, to)
	return true
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race ./internal/manager/`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/manager
git commit -m "feat(manager): observers, Client(id), first_connected_at e histórico fora do laço de supervisão" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

### Task 10: Ingestão do log

**Files:**
- Create: `internal/ingest/ingest.go`
- Test: `internal/ingest/ingest_test.go`

Regras: mensagem recebida → `source: contact`, `status: received`, soma 1 em `unread_count` (exceto reações) e marca `has_inbound`; o nome do chat direto vem do `push_name` (em grupo o `push_name` é de quem enviou, não do grupo). `from_me` vindo de outro aparelho → `source: device`, a menos que o `wa_message_id` seja de um envio da API (`api`): é a base da pausa por humano do hook de agente (spec 4.3). Recibos sobem o status (`played` conta como `read`). History sync → `source: history`, sem `unread`, com o nome da conversa. Reentrega do mesmo evento não duplica (chave `instance_id + chat_jid + wa_message_id`). O `Ingestor` não guarda estado: é chamado em paralelo para histórico e eventos ao vivo.

- [ ] **Step 1: Escrever os testes**

Criar `internal/ingest/ingest_test.go`:

```go
package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

const chat = "5511988887777@s.whatsapp.net"

func setup(t *testing.T) (*Ingestor, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	return &Ingestor{Store: st, Log: slog.New(slog.DiscardHandler)}, st
}

func msgEv(m *workerv1.Message) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Message{Message: m}}
}

var ts = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).UnixMilli()

func TestReceivedMessageIsLoggedOnce(t *testing.T) {
	in, st := setup(t)
	ctx := context.Background()
	m := &workerv1.Message{Id: "WA1", Chat: chat, Sender: chat, PushName: "João", TimestampMs: ts,
		Type: "image", Text: "foto", Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 9, Ref: []byte("r")}}
	in.WorkerEvent("i1", msgEv(m))
	in.WorkerEvent("i1", msgEv(m)) // redelivered after a worker restart
	msgs, _, err := st.ListMessages(ctx, "i1", chat, 10, nil)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	got := msgs[0]
	if got.Source != "contact" || got.Status != "received" || got.Text != "foto" || got.Timestamp.UnixMilli() != ts {
		t.Fatalf("message = %+v", got)
	}
	var info store.MediaInfo
	if err := json.Unmarshal([]byte(got.MediaJSON), &info); err != nil || info.ID == "" || info.Mime != "image/jpeg" {
		t.Fatalf("media json = %q, %v", got.MediaJSON, err)
	}
	if md, err := st.GetMedia(ctx, "i1", info.ID); err != nil || string(md.Ref) != "r" {
		t.Fatalf("media row = %+v, %v", md, err)
	}
	c, _ := st.GetChat(ctx, "i1", chat)
	if c.Name != "João" || c.UnreadCount != 1 || !c.HasInbound {
		t.Fatalf("chat = %+v", c)
	}
}

func TestFromMeSourceAndReceipts(t *testing.T) {
	in, st := setup(t)
	ctx := context.Background()
	if _, _, err := st.Enqueue(ctx, store.Outbound{ID: "q1", InstanceID: "i1", ChatJID: chat, WAMessageID: "OWN1", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "PHONE1", Chat: chat, FromMe: true, TimestampMs: ts, Type: "text", Text: "eu"}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "OWN1", Chat: chat, FromMe: true, TimestampMs: ts, Type: "text", Text: "api"}))
	phone, _ := st.MessageByWAID(ctx, "i1", chat, "PHONE1")
	own, _ := st.MessageByWAID(ctx, "i1", chat, "OWN1")
	if phone.Source != "device" || own.Source != "api" || phone.Status != "sent" {
		t.Fatalf("phone=%+v own=%+v", phone, own)
	}
	c, _ := st.GetChat(ctx, "i1", chat)
	if c.UnreadCount != 0 || c.HasInbound {
		t.Fatalf("our own messages must not count as inbound: %+v", c)
	}
	in.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: &workerv1.Receipt{
		MessageIds: []string{"PHONE1"}, Chat: chat, Type: "played"}}})
	if m, _ := st.MessageByWAID(ctx, "i1", chat, "PHONE1"); m.Status != "read" {
		t.Fatalf("status = %q", m.Status)
	}
}

func TestHistoryIsLoggedWithoutUnread(t *testing.T) {
	in, st := setup(t)
	ctx := context.Background()
	group := "1203630@g.us"
	in.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{
		Chat: group, Name: "Família", IsGroup: true, Messages: []*workerv1.Message{
			{Id: "H1", Chat: group, Sender: chat, IsGroup: true, PushName: "João", TimestampMs: ts, Type: "text", Text: "a"},
			{Id: "H2", Chat: group, FromMe: true, IsGroup: true, TimestampMs: ts + 1000, Type: "text", Text: "b"},
		}}}})
	msgs, _, _ := st.ListMessages(ctx, "i1", group, 10, nil)
	if len(msgs) != 2 || msgs[0].Source != "history" {
		t.Fatalf("messages = %+v", msgs)
	}
	c, _ := st.GetChat(ctx, "i1", group)
	if c.Name != "Família" || !c.IsGroup || c.UnreadCount != 0 || !c.HasInbound {
		t.Fatalf("chat = %+v", c)
	}
}
```

Run: `go test ./internal/ingest/`
Expected: FAIL — `undefined: Ingestor`.

- [ ] **Step 2: Implementar**

Criar `internal/ingest/ingest.go`:

```go
// Package ingest writes worker events (messages, receipts, history sync) into the message
// log (spec 4.2). Webhooks and SSE consume the same events from marco 3 on.
package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

// Ingestor is a manager.Observer.
type Ingestor struct {
	Store *store.Store
	Log   *slog.Logger
}

func (in *Ingestor) StateChanged(string, instance.State) {}

func (in *Ingestor) WorkerEvent(instanceID string, ev *workerv1.Event) {
	ctx := context.Background()
	var err error
	switch {
	case ev.GetMessage() != nil:
		err = in.message(ctx, instanceID, ev.GetMessage())
	case ev.GetReceipt() != nil:
		r := ev.GetReceipt()
		st := r.Type
		if st == "played" {
			st = "read"
		}
		err = in.Store.ApplyReceipt(ctx, instanceID, r.MessageIds, st)
	case ev.GetHistory() != nil:
		err = in.history(ctx, instanceID, ev.GetHistory())
	}
	if err != nil {
		in.Log.Error("ingest worker event", "instance_id", instanceID, "err", err)
	}
}

// record converts a worker message into a log entry.
func record(m *workerv1.Message, source string) store.Message {
	rec := store.Message{
		ID: secure.NewID("msg_"), WAMessageID: m.Id, ChatJID: m.Chat, SenderJID: m.Sender,
		FromMe: m.FromMe, Source: source, Type: m.Type, Text: m.Text, ReactionTo: m.ReactionTo,
		Status: "received", Timestamp: time.UnixMilli(m.TimestampMs).UTC(),
	}
	if m.FromMe {
		rec.Status = "sent"
	}
	if md := m.Media; md != nil {
		info := store.MediaInfo{ID: secure.NewID("med_"), Kind: md.Kind, Mime: md.Mime, Size: int64(md.Size),
			FileName: md.FileName, Seconds: int(md.Seconds), PTT: md.Ptt}
		b, _ := json.Marshal(info)
		rec.MediaJSON = string(b)
		rec.Media = &store.Media{ID: info.ID, Kind: md.Kind, Mime: md.Mime, Size: info.Size, FileName: md.FileName, Ref: md.Ref}
	}
	return rec
}

func chatUpdate(m *workerv1.Message, live bool) store.ChatUpdate {
	cu := store.ChatUpdate{IsGroup: m.IsGroup, Inbound: !m.FromMe}
	if !m.IsGroup && !m.FromMe {
		cu.Name = m.PushName // in groups the push name is the sender's, not the group's
	}
	cu.CountUnread = live && !m.FromMe && m.Type != "reaction"
	return cu
}

func (in *Ingestor) message(ctx context.Context, instanceID string, m *workerv1.Message) error {
	if m.Chat == "" || m.Id == "" {
		return nil
	}
	source := "contact"
	if m.FromMe {
		// from_me events come from the account's other devices. Our own API sends are not
		// echoed, but check anyway: a from_me message the API did not send means a human
		// answered from the phone (the agent hook pauses on it, marco 5).
		own, err := in.Store.IsOwnSent(ctx, instanceID, m.Id)
		if err != nil {
			return err
		}
		source = "device"
		if own {
			source = "api"
		}
	}
	_, err := in.Store.InsertMessage(ctx, instanceID, record(m, source), chatUpdate(m, true))
	return err
}

func (in *Ingestor) history(ctx context.Context, instanceID string, h *workerv1.HistoryChunk) error {
	msgs := make([]store.Message, 0, len(h.Messages))
	ups := make([]store.ChatUpdate, 0, len(h.Messages))
	for _, m := range h.Messages {
		if m.Chat == "" || m.Id == "" {
			continue
		}
		msgs = append(msgs, record(m, "history"))
		cu := chatUpdate(m, false)
		cu.Name, cu.IsGroup = h.Name, h.IsGroup // the conversation name is the chat's name
		ups = append(ups, cu)
	}
	n, err := in.Store.InsertMessages(ctx, instanceID, msgs, ups)
	if err == nil {
		in.Log.Info("history synced", "instance_id", instanceID, "chat_messages", len(msgs), "new", n)
	}
	return err
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test ./internal/ingest/`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/ingest
git commit -m "feat(ingest): grava mensagens, recibos e history sync no log" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 10: Relógio, ritmo e números

### Task 11: Relógio injetável e ritmo de envio

**Files:**
- Create: `internal/clock/clock.go`
- Test: `internal/clock/clock_test.go`
- Create: `internal/queue/pacing.go`
- Test: `internal/queue/pacing_test.go`

O relógio usa timers com `Stop` (não `time.After`): o despachante para o timer quando é acordado antes, e `Fake.Waiters()` conta só timers vivos, o que permite ao teste esperar o despachante dormir antes de avançar o tempo sem corrida.

- [ ] **Step 1: Escrever os testes**

Criar `internal/clock/clock_test.go`:

```go
package clock

import (
	"testing"
	"time"
)

func TestFakeTimers(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	f := NewFake(t0)
	a, b := f.NewTimer(time.Second), f.NewTimer(time.Minute)
	if f.Waiters() != 2 {
		t.Fatalf("waiters = %d", f.Waiters())
	}
	f.Advance(2 * time.Second)
	select {
	case got := <-a.C():
		if !got.Equal(t0.Add(2 * time.Second)) {
			t.Fatalf("fired at %v", got)
		}
	default:
		t.Fatal("1s timer did not fire")
	}
	select {
	case <-b.C():
		t.Fatal("1m timer fired early")
	default:
	}
	b.Stop()
	if f.Waiters() != 0 {
		t.Fatalf("waiters after stop = %d", f.Waiters())
	}
	if z := f.NewTimer(0); f.Waiters() != 0 || len(z.C()) != 1 {
		t.Fatal("a zero timer fires immediately")
	}
	r := Real{}.NewTimer(time.Millisecond)
	<-r.C()
	r.Stop()
}
```

Criar `internal/queue/pacing_test.go`:

```go
package queue

import (
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/settings"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestPacerInterval(t *testing.T) {
	s := settings.Defaults() // 3s + up to 2s jitter
	p := &pacer{rand: func() float64 { return 0.5 }}
	if d := p.wait(t0, s, false); d != 0 {
		t.Fatalf("first send waits %v", d)
	}
	p.record(t0, s)
	if d := p.wait(t0.Add(time.Second), s, false); d != 3*time.Second { // 3s + 1s jitter - 1s elapsed
		t.Fatalf("wait = %v", d)
	}
	if d := p.wait(t0.Add(time.Second), s, true); d != 6*time.Second { // doubled interval
		t.Fatalf("double wait = %v", d)
	}
	if d := p.wait(t0.Add(time.Minute), s, false); d != 0 {
		t.Fatalf("late wait = %v", d)
	}
}

func TestPacerMaxPerMinute(t *testing.T) {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.MaxPerMinute = 0, 0, 3
	p := &pacer{rand: func() float64 { return 0 }}
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		if d := p.wait(at, s, false); d != 0 {
			t.Fatalf("send %d waits %v", i, d)
		}
		p.record(at, s)
	}
	if d := p.wait(t0.Add(3*time.Second), s, false); d != 57*time.Second {
		t.Fatalf("4th send waits %v, want 57s", d)
	}
}

func TestPacerNoPerMinuteLimitWhenZero(t *testing.T) {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.MaxPerMinute = 0, 0, 0
	p := &pacer{rand: func() float64 { return 0 }}
	p.record(t0, s)
	if d := p.wait(t0, s, false); d != 0 {
		t.Fatalf("wait = %v", d)
	}
}

func TestDelays(t *testing.T) {
	if typingDelay("oi") != 100*time.Millisecond || typingDelay(string(make([]rune, 1000))) != 5*time.Second {
		t.Fatal("typing delay")
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	for i, w := range want {
		if got := retryDelay(i + 1); got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
	if retryDelay(30) != 5*time.Minute {
		t.Fatal("retry delay cap")
	}
}
```

Run: `go test ./internal/clock/ ./internal/queue/`
Expected: FAIL — `undefined: NewFake`, `undefined: pacer`.

- [ ] **Step 2: Implementar**

Criar `internal/clock/clock.go`:

```go
// Package clock abstracts time so the send queue's pacing can be tested without sleeping.
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	// Stop releases the timer; always call it (a Fake counts unstopped timers as waiters).
	Stop()
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop()               { r.t.Stop() }

// Fake is a manual clock: time only moves with Advance.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

type fakeTimer struct {
	f  *Fake
	at time.Time
	ch chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	delete(t.f.timers, t)
}

func NewFake(t time.Time) *Fake { return &Fake{now: t, timers: map[*fakeTimer]struct{}{}} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{f: f, at: f.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- f.now
		return t
	}
	f.timers[t] = struct{}{}
	return t
}

// Advance moves time forward and fires every timer that became due.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	for t := range f.timers {
		if !t.at.After(f.now) {
			t.ch <- f.now
			delete(f.timers, t)
		}
	}
}

// Waiters is the number of pending, unstopped timers: tests use it to know that a
// goroutine is blocked waiting for time to pass.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}
```

Criar `internal/queue/pacing.go`:

```go
package queue

import (
	"time"

	"github.com/douglasdf99/litezap/internal/settings"
)

// pacer spaces sends of one instance (spec 4.1): min_interval + random jitter between
// sends (twice the interval for an identical text to another new recipient) and at most
// max_per_minute sends in any minute.
type pacer struct {
	rand   func() float64
	last   time.Time
	jitter time.Duration // drawn when last was recorded
	recent []time.Time   // sends of the last minute
}

// record notes a send attempt at t.
func (p *pacer) record(t time.Time, s settings.Settings) {
	p.last = t
	p.jitter = time.Duration(p.rand() * float64(s.Jitter()))
	p.recent = append(p.recent, t)
}

// wait is how long to wait at now before the next send.
func (p *pacer) wait(now time.Time, s settings.Settings, double bool) time.Duration {
	var d time.Duration
	if !p.last.IsZero() {
		gap := s.MinInterval()
		if double {
			gap *= 2
		}
		d = p.last.Add(gap + p.jitter).Sub(now)
	}
	cut := now.Add(-time.Minute)
	keep := p.recent[:0]
	for _, t := range p.recent {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	p.recent = keep
	if s.MaxPerMinute > 0 && len(p.recent) >= s.MaxPerMinute {
		if w := p.recent[len(p.recent)-s.MaxPerMinute].Add(time.Minute).Sub(now); w > d {
			d = w
		}
	}
	return max(d, 0)
}

// typingDelay simulates typing: ~50ms per character, at most 5s.
func typingDelay(text string) time.Duration {
	return min(time.Duration(len([]rune(text)))*50*time.Millisecond, 5*time.Second)
}

// retryDelay is the backoff before attempt n+1 after n failed attempts: 5s, 10s, 20s, 40s...
func retryDelay(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts && d < 5*time.Minute; i++ {
		d *= 2
	}
	return min(d, 5*time.Minute)
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test ./internal/clock/ ./internal/queue/`
Expected: `ok` nos dois.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/clock internal/queue
git commit -m "feat(queue): relógio injetável, ritmo, digitação e backoff" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

### Task 12: Resolução de números com cache e limite

**Files:**
- Create: `internal/lookup/lookup.go`
- Test: `internal/lookup/lookup_test.go`

`Resolver.Resolve` devolve um resultado por telefone: usa o `number_cache` enquanto fresco (encontrado: 30 dias; não encontrado: 24h) e só consulta o worker (`CheckNumbers` → `IsOnWhatsApp`) para o resto, com cotas separadas por finalidade: 30 consultas novas por instância por minuto para a fila (`ForSend`) e 10 para `check-numbers` (`ForCheck`), de modo que a API nunca esgota a cota dos envios (`ErrRateLimited` acima disso; consultas em massa parecem raspagem de contatos; um pedido com mais números não cacheados que a cota por minuto nunca poderia ser atendido e devolve `ErrTooManyNumbers`, não retentável, sem gastar cota). Quando o WhatsApp devolve outro JID (9º dígito), o número canônico também entra no cache.

- [ ] **Step 1: Escrever os testes**

Criar `internal/lookup/lookup_test.go`:

```go
package lookup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

type workers map[string]workerv1.WorkerClient

func (w workers) Client(id string) (workerv1.WorkerClient, error) { return w[id], nil }

func setup(t *testing.T) (*Resolver, *workertest.FakeWA, *clock.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	sp := workertest.NewSpawner()
	h, err := sp.Start(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	return &Resolver{Store: st, Workers: workers{"i1": h.Client()}, Clock: fc, PerMinute: 3, CheckPerMinute: 1}, sp.Last("i1").WA, fc
}

func TestResolveCachesAndHandlesNinthDigit(t *testing.T) {
	r, wa, fc := setup(t)
	ctx := context.Background()
	wa.SetRegistered("551188887777", "5511988887777@s.whatsapp.net")
	wa.SetUnregistered("5511000000000")
	res, err := r.Resolve(ctx, "i1", []string{"551188887777", "5511000000000", "551188887777"}, ForSend)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Found || res[0].JID != "5511988887777@s.whatsapp.net" || res[1].Found || res[2].JID != res[0].JID {
		t.Fatalf("results = %+v", res)
	}
	if wa.Checks() != 2 {
		t.Fatalf("worker lookups = %d, want 2 (duplicates collapsed)", wa.Checks())
	}
	if _, err := r.Resolve(ctx, "i1", []string{"551188887777", "5511000000000", "5511988887777"}, ForSend); err != nil || wa.Checks() != 2 {
		t.Fatalf("cached lookup (including the canonical number) hit the worker: %d, %v", wa.Checks(), err)
	}
	fc.Advance(25 * time.Hour) // the negative result expired, the positive one did not
	if _, err := r.Resolve(ctx, "i1", []string{"551188887777", "5511000000000"}, ForSend); err != nil || wa.Checks() != 3 {
		t.Fatalf("after 25h: checks = %d, %v", wa.Checks(), err)
	}
}

func TestResolveRateLimit(t *testing.T) {
	r, wa, fc := setup(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000001", "5511900000002", "5511900000003"}, ForSend); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000004"}, ForSend); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("4th lookup in a minute = %v", err)
	}
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000001"}, ForSend); err != nil {
		t.Fatalf("cached numbers are not limited: %v", err)
	}
	// check-numbers has its own bucket: exhausting it never blocks the send queue.
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000005"}, ForCheck); err != nil {
		t.Fatalf("check bucket = %v", err)
	}
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000006"}, ForCheck); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("2nd check lookup = %v", err)
	}
	fc.Advance(time.Minute)
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000004"}, ForSend); err != nil || wa.Checks() != 5 {
		t.Fatalf("after a minute: %v (checks %d)", err, wa.Checks())
	}
}

func TestResolveBatchLargerThanBucket(t *testing.T) {
	r, wa, _ := setup(t)
	ctx := context.Background()
	// check bucket is 1/min: two uncached numbers can never be served, so it must not be retryable.
	_, err := r.Resolve(ctx, "i1", []string{"5511900000001", "5511900000002"}, ForCheck)
	if !errors.Is(err, ErrTooManyNumbers) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if wa.Checks() != 0 {
		t.Fatalf("worker was called: %d", wa.Checks())
	}
	// no quota was consumed
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000003"}, ForCheck); err != nil {
		t.Fatalf("quota consumed by the rejected batch: %v", err)
	}
}
```

Run: `go test ./internal/lookup/`
Expected: FAIL — `undefined: Resolver`.

- [ ] **Step 2: Implementar**

Criar `internal/lookup/lookup.go`:

```go
// Package lookup resolves phone numbers to WhatsApp JIDs (IsOnWhatsApp) with a cache and a
// per-instance rate limit. The send queue uses it before the first message to a number (a
// number without WhatsApp fails as invalid_number, and Brazilian numbers get the JID with
// or without the 9th digit that WhatsApp actually uses); check-numbers uses it too.
package lookup

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/store"
)

// ErrRateLimited means the instance looked up too many uncached numbers in the last minute.
var ErrRateLimited = errors.New("too many number lookups; try again in a minute")

// ErrTooManyNumbers means one request has more uncached numbers than the purpose's
// per-minute limit, so it could never be served (not retryable).
var ErrTooManyNumbers = errors.New("too many uncached numbers in one request")

// Workers gives access to running workers (implemented by *manager.Manager).
type Workers interface {
	Client(instanceID string) (workerv1.WorkerClient, error)
}

// Purpose selects the rate-limit bucket: check-numbers has its own, smaller quota, so API
// lookups can never starve the send queue.
type Purpose string

const (
	ForSend  Purpose = "send"
	ForCheck Purpose = "check"
)

type Result struct {
	Phone string
	Found bool
	JID   string
}

// Resolver must be created with its exported fields set; the zero limits take defaults.
type Resolver struct {
	Store   *store.Store
	Workers Workers
	Clock   clock.Clock
	// PerMinute bounds uncached lookups per instance for the send queue (default 30) and
	// CheckPerMinute those for check-numbers (default 10): bulk lookups look like contact
	// scraping to WhatsApp.
	PerMinute, CheckPerMinute int
	// FoundTTL and MissingTTL say how long a result is trusted (defaults 30 days and 24h).
	FoundTTL, MissingTTL time.Duration

	mu     sync.Mutex
	recent map[string][]time.Time
}

func (r *Resolver) now() time.Time {
	if r.Clock == nil {
		return time.Now()
	}
	return r.Clock.Now()
}

func (r *Resolver) fresh(c store.CachedNumber, now time.Time) bool {
	ttl := r.FoundTTL
	if ttl == 0 {
		ttl = 30 * 24 * time.Hour
	}
	if !c.Found {
		if ttl = r.MissingTTL; ttl == 0 {
			ttl = 24 * time.Hour
		}
	}
	return now.Sub(c.CheckedAt) < ttl
}

// limit is the per-minute quota of purpose.
func (r *Resolver) limit(purpose Purpose) int {
	limit := r.PerMinute
	if limit == 0 {
		limit = 30
	}
	if purpose == ForCheck {
		if limit = r.CheckPerMinute; limit == 0 {
			limit = 10
		}
	}
	return limit
}

// take reserves n lookups in the instance's one-minute window for purpose.
func (r *Resolver) take(instanceID string, purpose Purpose, n int, now time.Time) bool {
	limit := r.limit(purpose)
	key := instanceID + "/" + string(purpose)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recent == nil {
		r.recent = map[string][]time.Time{}
	}
	keep := r.recent[key][:0]
	for _, t := range r.recent[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep)+n > limit {
		r.recent[key] = keep
		return false
	}
	for i := 0; i < n; i++ {
		keep = append(keep, now)
	}
	r.recent[key] = keep
	return true
}

// Resolve returns one result per phone (digits with country code), in order. Cached
// results are used while fresh; the rest cost one worker call and count against the
// purpose's limit.
func (r *Resolver) Resolve(ctx context.Context, instanceID string, phones []string, purpose Purpose) ([]Result, error) {
	now := r.now()
	out := make([]Result, len(phones))
	var missing []string
	idx := map[string][]int{}
	for i, p := range phones {
		out[i].Phone = p
		c, err := r.Store.GetCachedNumber(ctx, instanceID, p)
		if err == nil && r.fresh(c, now) {
			out[i].Found, out[i].JID = c.Found, c.JID
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if _, dup := idx[p]; !dup {
			missing = append(missing, p)
		}
		idx[p] = append(idx[p], i)
	}
	if len(missing) == 0 {
		return out, nil
	}
	if len(missing) > r.limit(purpose) {
		return nil, ErrTooManyNumbers
	}
	if !r.take(instanceID, purpose, len(missing), now) {
		return nil, ErrRateLimited
	}
	cli, err := r.Workers.Client(instanceID)
	if err != nil {
		return nil, err
	}
	resp, err := cli.CheckNumbers(ctx, &workerv1.CheckNumbersRequest{Phones: missing})
	if err != nil {
		return nil, err
	}
	for _, ns := range resp.Results {
		c := store.CachedNumber{Phone: ns.Phone, Found: ns.Exists, JID: ns.Jid, CheckedAt: now}
		if err := r.Store.PutCachedNumber(ctx, instanceID, c); err != nil {
			return nil, err
		}
		// The canonical number (e.g. with the 9th digit) resolves to itself: cache it too,
		// so the retargeted send does not cost a second lookup.
		if user, ok := strings.CutSuffix(ns.Jid, "@s.whatsapp.net"); ns.Exists && ok && user != ns.Phone {
			c.Phone = user
			if err := r.Store.PutCachedNumber(ctx, instanceID, c); err != nil {
				return nil, err
			}
		}
		for _, i := range idx[ns.Phone] {
			out[i].Found, out[i].JID = ns.Exists, ns.Jid
		}
	}
	return out, nil
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race ./internal/lookup/`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/lookup
git commit -m "feat(lookup): resolução de números com cache e limite por minuto" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 11: Despachante — núcleo

### Task 13: Despachante da fila por instância: núcleo

**Files:**
- Create: `internal/queue/queue.go`
- Test: `internal/queue/queue_test.go`

Um passo do despachante (`step`), em ordem:

1. Instância sumiu → encerra; fora de `connected` → dorme até ser acordado (sem gastar tentativas).
2. Carência depois do aviso de `connected` (`ConnectGrace` + até mais `ConnectGrace` aleatório; `serve` usa 15s). Com carência ligada, um aviso de outro estado segura os envios até o próximo aviso de `connected` (o estado gravado pode ler `connected` antes do aviso chegar).
3. Próximo item vencido; sem item → dorme até o próximo vencimento.
4. TTL estourado → `failed` com `expired`.
5. Janela de envio, só para destinatários sem mensagem recebida nas últimas 24h (`chats.last_inbound_at`): fora dela (ou antes de início + variação de até 5 min) → o item é adiado até a abertura; respostas a conversas ativas seguem.
6. Rampa (destinatário novo = chat, grupos inclusive, sem nada recebido) → adia para o início da janela do dia seguinte + variação (nunca meia-noite), sem gastar tentativa e **sem consultar o número**.
7. Primeiro envio a um número sem chat → `lookup` (cota da fila): inexistente → `failed` `invalid_number` sem enviar; outro JID de telefone → redireciona os itens do chat e recomeça (o passo seguinte confere a rampa para o JID canônico; um JID `@lid` nunca vira chat: segue o JID de telefone); erro → adia só este item (1, 2, 5, 15 min) e, na 5ª falha seguida, `failed` `worker_error`; limite de consultas → adia este item 1 min sem contar falha; consulta interrompida pelo shutdown → não conta falha.
8. Ritmo (`pacer.wait`, intervalo dobrado para texto idêntico a outro destinatário novo em sequência, com ou sem rampa; o retry do mesmo item não dobra).
9. `MarkInFlight` (com a linha `pending` no log), digitação (`composing` ~50ms/caractere até 5s; `recording` 2s para voz). Se o shutdown chegou durante a digitação → volta para a fila sem gastar tentativa.
10. `Send` com timeout próprio (o shutdown espera até 10s por um envio já iniciado) e o resultado:

| Resultado | Ação |
|---|---|
| sucesso | `sent` (linha do log promovida, chat atualizado) |
| erro, mas um recibo já elevou a linha do log a `delivered`/`read` | `sent` (a resposta do worker se perdeu ou atrasou) |
| `InvalidArgument`, `PermissionDenied`, `FailedPrecondition` | `failed` imediato |
| mensagem do worker começando com `instance_not_connected:` | volta para a fila em 5s sem gastar tentativa |
| `Unavailable`/`Canceled` com a instância fora de `connected` | volta para a fila sem gastar tentativa |
| demais | tentativa +1; backoff 5s, 10s, 20s, 40s; na 5ª, `failed` |

`Wait` acorda quando o item termina (`sent`/`failed`) ou o contexto acaba, devolvendo o estado atual (para o `?wait=true` responder 202 ainda `queued`). O arquivo de mídia em `outbox` é apagado quando o item termina.

Uma escrita no store que falha (adiar, `failed`, `sent`, voltar para a fila) pausa o despachante por `errPause`, sem laço apertado na conexão única do SQLite; `sent`/`failed` só apagam a mídia em `outbox` quando a escrita deu certo.

Esta task entrega o núcleo (passos 1, 3, 6 e 8 a 10); a próxima acrescenta carência, TTL, janela e verificação de número (passos 2, 4, 5 e 7).

- [ ] **Step 1: Escrever os testes**

Criar `internal/queue/queue_test.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type workers map[string]workerv1.WorkerClient

func (w workers) Client(id string) (workerv1.WorkerClient, error) {
	if c, ok := w[id]; ok {
		return c, nil
	}
	return nil, manager.ErrNotRunning
}

type env struct {
	t     *testing.T
	st    *store.Store
	clock *clock.Fake
	q     *Queues
	fake  *workertest.Fake
	seq   int
}

// fastSettings sends back to back: no interval, jitter, typing, ramp or send window.
func fastSettings() settings.Settings {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.TypingSimulation, s.Ramp.Enabled = 0, 0, false, false
	s.SendWindow, s.Timezone, s.UnsafePacing = settings.Window{}, "UTC", true
	return s
}

func newEnv(t *testing.T, defaults settings.Settings, state instance.State) *env {
	t.Helper()
	return newEnvAt(t, t0, defaults, state, nil)
}

// newEnvAt starts the fake clock at start; tweak adjusts the queue options.
func newEnvAt(t *testing.T, start time.Time, defaults settings.Settings, state instance.State, tweak func(*Options)) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: state, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	sp := workertest.NewSpawner()
	h, err := sp.Start(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(start)
	ws := workers{"i1": h.Client()}
	opt := Options{Clock: fc, Rand: func() float64 { return 0.5 }, Defaults: defaults, Log: slog.New(slog.DiscardHandler)}
	if tweak != nil {
		tweak(&opt)
	}
	q := New(st, ws, opt)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
	})
	return &env{t: t, st: st, clock: fc, q: q, fake: sp.Last("i1")}
}

func (e *env) enqueue(chat string, p Payload) string {
	e.t.Helper()
	e.seq++
	id := "q" + string(rune('0'+e.seq))
	b, _ := json.Marshal(p)
	if _, _, err := e.st.Enqueue(context.Background(), store.Outbound{ID: id, InstanceID: "i1", ChatJID: chat,
		WAMessageID: "WA-" + id, Payload: string(b), NextAttemptAt: e.clock.Now()}); err != nil {
		e.t.Fatal(err)
	}
	e.q.Wake("i1")
	return id
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (e *env) status(id string) store.Outbound {
	e.t.Helper()
	o, err := e.st.GetOutbound(context.Background(), "i1", id)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *env) sentTo() []string {
	var out []string
	for _, r := range e.fake.WA.Sent() {
		out = append(out, r.MessageId)
	}
	return out
}

const (
	chatA = "5511900000001@s.whatsapp.net"
	chatB = "5511900000002@s.whatsapp.net"
	chatC = "5511900000003@s.whatsapp.net"
)

func TestSendsInOrderAndLogs(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	a1 := e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	e.enqueue(chatA, Payload{Type: "text", Text: "dois"})
	e.enqueue(chatB, Payload{Type: "text", Text: "três"})
	e.eventually("3 sends", func() bool { return len(e.fake.WA.Sent()) == 3 })
	got := e.sentTo()
	if got[0] != "WA-q1" || got[1] != "WA-q2" || got[2] != "WA-q3" {
		t.Fatalf("send order = %v", got)
	}
	e.eventually("a1 sent", func() bool { return e.status(a1).Status == store.OutSent })
	m, err := e.st.GetMessage(context.Background(), "i1", a1)
	if err != nil || m.Source != "api" || m.Text != "um" || m.WAMessageID != "WA-q1" {
		t.Fatalf("logged = %+v, %v", m, err)
	}
}

func TestPausedWhileNotConnected(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Reconnecting)
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	time.Sleep(50 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 0 {
		t.Fatalf("sent %d while reconnecting", n)
	}
	if err := e.st.UpdateInstanceState(context.Background(), "i1", instance.Connected, ""); err != nil {
		t.Fatal(err)
	}
	e.q.StateChanged("i1", instance.Connected)
	e.eventually("sent after connect", func() bool { return e.status(id).Status == store.OutSent })
	if a := e.status(id).Attempts; a != 0 {
		t.Fatalf("attempts = %d", a)
	}
}

func TestRetriesThenFails(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.Unknown, "worker_error: server returned error 500")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	for i := 1; i < 5; i++ {
		e.eventually("attempt", func() bool { return len(e.fake.WA.Sent()) == i && e.clock.Waiters() > 0 })
		if o := e.status(id); o.Status != store.OutQueued || o.Attempts != i {
			t.Fatalf("after attempt %d: %+v", i, o)
		}
		e.clock.Advance(retryDelay(i))
	}
	e.eventually("failed", func() bool { return e.status(id).Status == store.OutFailed })
	if o := e.status(id); o.Attempts != 5 || o.LastError != "worker_error: server returned error 500" {
		t.Fatalf("final = %+v", o)
	}
}

func TestPermanentFailureAndWait(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid_number: recipient is not on WhatsApp")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o, err := e.q.Wait(ctx, "i1", id)
	if err != nil || o.Status != store.OutFailed || o.Attempts != 1 || o.LastError != "invalid_number: recipient is not on WhatsApp" {
		t.Fatalf("wait = %+v, %v", o, err)
	}
}

func TestDisconnectDuringSendKeepsAttempts(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		// The connection drops mid-send: the manager records reconnecting first.
		_ = e.st.UpdateInstanceState(context.Background(), "i1", instance.Reconnecting, "disconnected")
		return nil, status.Error(codes.Unavailable, "instance_not_connected: websocket not connected")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("requeued", func() bool {
		o := e.status(id)
		return len(e.fake.WA.Sent()) == 1 && o.Status == store.OutQueued
	})
	if o := e.status(id); o.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", o.Attempts)
	}
}

func TestPacingAndTyping(t *testing.T) {
	s := fastSettings()
	s.MinIntervalMS, s.TypingSimulation = 3000, true
	e := newEnv(t, s, instance.Connected)
	e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.enqueue(chatB, Payload{Type: "text", Text: "olá"})
	// Typing first: "composing", then 2 chars x 50ms.
	e.eventually("typing", func() bool { return len(e.fake.WA.Presences()) == 1 && e.clock.Waiters() > 0 })
	if p := e.fake.WA.Presences()[0]; p.State != "composing" || p.To != chatA {
		t.Fatalf("presence = %+v", p)
	}
	e.clock.Advance(100 * time.Millisecond)
	e.eventually("first send", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	e.clock.Advance(2 * time.Second) // still inside the 3s interval
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Presences()) != 1 {
		t.Fatal("second message started before min_interval")
	}
	e.clock.Advance(time.Second)
	e.eventually("second typing", func() bool { return len(e.fake.WA.Presences()) == 2 && e.clock.Waiters() > 0 })
	e.clock.Advance(150 * time.Millisecond)
	e.eventually("second send", func() bool { return len(e.fake.WA.Sent()) == 2 })
}

func TestRampDefersNewRecipientsToNextWindow(t *testing.T) {
	s := fastSettings()
	s.Ramp = settings.Ramp{Enabled: true, StartPerDay: 1, EndPerDay: 1, Days: 1}
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	e := newEnv(t, s, instance.Connected)
	ctx := context.Background()
	// chatC already wrote to us: replies never count against the ramp.
	if _, err := e.st.InsertMessage(ctx, "i1", store.Message{ID: "in1", WAMessageID: "IN1", ChatJID: chatC,
		Source: "contact", Type: "text", Status: "received", Timestamp: t0}, store.ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	g := e.enqueue("120363000000000001@g.us", Payload{Type: "text", Text: "oi grupo"}) // a group with no inbound counts
	a := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "oi"})
	g2 := e.enqueue("120363000000000001@g.us", Payload{Type: "text", Text: "de novo"}) // same recipient: counted once
	e.eventually("group, c and g2 sent", func() bool {
		return e.status(g).Status == store.OutSent && e.status(c).Status == store.OutSent && e.status(g2).Status == store.OutSent
	})
	o := e.status(a)
	// Next day's window start (08:00) plus jitter (0.5 x 5min), not midnight.
	want := time.Date(2026, 10, 18, 8, 2, 30, 0, time.UTC)
	if o.Status != store.OutQueued || o.Attempts != 0 || !o.NextAttemptAt.Equal(want) || o.LastError == "" {
		t.Fatalf("a = %+v, want due at %v", o, want)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(12 * time.Hour) // midnight: nothing happens
	time.Sleep(20 * time.Millisecond)
	if e.status(a).Status != store.OutQueued {
		t.Fatal("deferred item sent at midnight")
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(8*time.Hour + 150*time.Second)
	e.eventually("a sent in the next window", func() bool { return e.status(a).Status == store.OutSent })
}

func TestWorkerNotConnectedKeepsAttempts(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	calls := 0
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		if calls++; calls == 1 { // the stored state still says connected
			return nil, status.Error(codes.Unknown, "instance_not_connected: websocket not connected")
		}
		return &workerv1.SendResponse{MessageId: req.MessageId}, nil
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("requeued", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	if o := e.status(id); o.Status != store.OutQueued || o.Attempts != 0 {
		t.Fatalf("after worker said not connected: %+v", o)
	}
	e.clock.Advance(errPause)
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
}

// A delivery receipt that beat a failed Send answer proves the message went out.
func TestReceiptBeatsFailedAnswer(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		_ = e.st.ApplyReceipt(context.Background(), "i1", []string{req.MessageId}, "delivered")
		return nil, status.Error(codes.Unknown, "worker_error: answer lost")
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	if o := e.status(id); o.Attempts != 0 || len(e.fake.WA.Sent()) != 1 {
		t.Fatalf("item = %+v, sends %d", o, len(e.fake.WA.Sent()))
	}
	if m, _ := e.st.GetMessage(context.Background(), "i1", id); m.Status != "delivered" {
		t.Fatalf("log status = %q", m.Status)
	}
}

func TestShutdownDuringTypingRequeues(t *testing.T) {
	s := fastSettings()
	s.TypingSimulation = true
	e := newEnv(t, s, instance.Connected)
	id := e.enqueue(chatA, Payload{Type: "text", Text: "uma mensagem longa"})
	e.eventually("typing", func() bool { return len(e.fake.WA.Presences()) == 1 && e.clock.Waiters() > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.q.Shutdown(ctx)
	if o := e.status(id); o.Status != store.OutQueued || o.Attempts != 0 || len(e.fake.WA.Sent()) != 0 {
		t.Fatalf("after shutdown: %+v, sent %d", o, len(e.fake.WA.Sent()))
	}
}

func TestMediaFileIsSentAndRemoved(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	f := filepath.Join(t.TempDir(), "img")
	if err := os.WriteFile(f, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := e.enqueue(chatA, Payload{Type: "image", Text: "legenda", MediaFile: f, Mime: "image/png", Size: 9})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	req := e.fake.WA.Sent()[0]
	if req.MediaPath != f || string(e.fake.WA.SentMedia(req.MessageId)) != "png-bytes" || req.Mime != "image/png" || req.Text != "legenda" {
		t.Fatalf("request = %+v", req)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("staged file not removed: %v", err)
	}
	m, _ := e.st.GetMessage(context.Background(), "i1", id)
	if m.MediaJSON == "" {
		t.Fatal("sent media not described in the log")
	}
}

func TestDeletedInstanceStopsDispatcher(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	if err := e.st.DeleteInstance(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	e.q.Wake("i1")
	e.eventually("dispatcher gone", func() bool {
		e.q.mu.Lock()
		defer e.q.mu.Unlock()
		return len(e.q.disp) == 0
	})
}

// An identical text to new recipients in a row doubles the interval, ramp or not.
func TestDoubledIntervalForIdenticalTextToNewRecipients(t *testing.T) {
	for _, ramp := range []bool{false, true} {
		t.Run(map[bool]string{false: "ramp off", true: "ramp on"}[ramp], func(t *testing.T) {
			s := fastSettings()
			s.MinIntervalMS = 3000
			s.Ramp = settings.Ramp{Enabled: ramp, StartPerDay: 10, EndPerDay: 10, Days: 1}
			e := newEnv(t, s, instance.Connected)
			e.enqueue(chatA, Payload{Type: "text", Text: "promo"})
			e.enqueue(chatB, Payload{Type: "text", Text: "promo"})
			e.eventually("first send", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
			e.clock.Advance(3 * time.Second)
			time.Sleep(20 * time.Millisecond)
			if n := len(e.fake.WA.Sent()); n != 1 {
				t.Fatalf("sent %d after 3s, want the doubled 6s interval", n)
			}
			e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
			e.clock.Advance(3 * time.Second)
			e.eventually("second send", func() bool { return len(e.fake.WA.Sent()) == 2 })
		})
	}
}

func TestMaxPerMinuteAcrossSends(t *testing.T) {
	s := fastSettings()
	s.MaxPerMinute = 2
	e := newEnv(t, s, instance.Connected)
	e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "três"})
	e.eventually("two sends", func() bool { return len(e.fake.WA.Sent()) == 2 && e.clock.Waiters() > 0 })
	e.clock.Advance(59 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 2 {
		t.Fatalf("sent %d inside the minute, max_per_minute is 2", n)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Second)
	e.eventually("third send after a minute", func() bool { return e.status(c).Status == store.OutSent })
}

// A retry waits for its backoff and for min_interval since the previous attempt.
func TestPacingAppliesBetweenRetries(t *testing.T) {
	s := fastSettings()
	s.MinIntervalMS = 10000
	e := newEnv(t, s, instance.Connected)
	calls := 0
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		if calls++; calls == 1 {
			return nil, status.Error(codes.Unknown, "worker_error: transient")
		}
		return &workerv1.SendResponse{MessageId: req.MessageId}, nil
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("first attempt", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	e.clock.Advance(retryDelay(1)) // 5s: the backoff is over, min_interval (10s) is not
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(4 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 1 {
		t.Fatalf("retried after 9s, min_interval is 10s (%d attempts)", n)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Second)
	e.eventually("sent at 10s", func() bool { return e.status(id).Status == store.OutSent })
}
```

Run: `go test ./internal/queue/`
Expected: FAIL — `undefined: New`, `undefined: Payload`, `undefined: Options`.

- [ ] **Step 2: Implementar**

Criar `internal/queue/queue.go`:

```go
// Package queue dispatches the persistent send queue (spec 4.1): one goroutine per
// instance, strict per-recipient order, pacing, the new-number ramp and retries.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Payload is what the service stores in outbound_queue.payload_json.
type Payload struct {
	Type string `json:"type"` // text | image | video | audio | document | reaction
	Text string `json:"text,omitempty"`
	// MediaFile is the staged attachment; it is deleted once the item is final.
	MediaFile string          `json:"media_file,omitempty"`
	Mime      string          `json:"mime,omitempty"`
	FileName  string          `json:"file_name,omitempty"`
	Size      int64           `json:"size,omitempty"`
	PTT       bool            `json:"ptt,omitempty"`
	Reaction  *ReactionTarget `json:"reaction,omitempty"`
}

// ReactionTarget identifies the message a reaction refers to.
type ReactionTarget struct {
	WAMessageID string `json:"wa_message_id"`
	FromMe      bool   `json:"from_me"`
	Sender      string `json:"sender,omitempty"`
}

// Workers gives access to running workers (implemented by *manager.Manager).
type Workers interface {
	Client(instanceID string) (workerv1.WorkerClient, error)
}

type Options struct {
	Clock    clock.Clock
	Rand     func() float64 // jitter source, [0,1)
	Defaults settings.Settings
	// MaxAttempts bounds retryable failures (spec: 5).
	MaxAttempts int
	// SendTimeout bounds one Send RPC; whatsmeow itself waits up to 75s for the server ack.
	SendTimeout time.Duration
	// WindowJitter spreads ramp-deferred sends over [0, WindowJitter) after the next
	// window start (default 5 minutes).
	WindowJitter time.Duration
	Log          *slog.Logger
}

// Queues owns the per-instance dispatchers. It is a manager.Observer: reaching connected
// wakes the instance's dispatcher.
type Queues struct {
	store   *store.Store
	workers Workers
	opt     Options

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	disp    map[string]*dispatcher
	waiters map[string][]chan struct{}
}

func New(st *store.Store, workers Workers, opt Options) *Queues {
	if opt.Clock == nil {
		opt.Clock = clock.Real{}
	}
	if opt.Rand == nil {
		opt.Rand = rand.Float64
	}
	if opt.MaxAttempts == 0 {
		opt.MaxAttempts = 5
	}
	if opt.SendTimeout == 0 {
		opt.SendTimeout = 90 * time.Second
	}
	if opt.WindowJitter == 0 {
		opt.WindowJitter = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Queues{store: st, workers: workers, opt: opt, ctx: ctx, cancel: cancel,
		disp: map[string]*dispatcher{}, waiters: map[string][]chan struct{}{}}
}

// Recover runs at boot, before any worker starts: in_flight items go back to queued.
func (q *Queues) Recover(ctx context.Context) (int, error) { return q.store.RecoverInFlight(ctx) }

func (q *Queues) WorkerEvent(string, *workerv1.Event) {}

func (q *Queues) StateChanged(id string, st instance.State) {
	if st == instance.Connected {
		q.Wake(id)
	}
}

// Wake makes the instance's dispatcher look at the queue now (new item, new settings,
// connected), starting it if needed.
func (q *Queues) Wake(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	d := q.disp[id]
	if d == nil {
		d = &dispatcher{q: q, id: id, wake: make(chan struct{}, 1), pacer: pacer{rand: q.opt.Rand}}
		q.disp[id] = d
		q.wg.Add(1)
		go d.run()
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Shutdown stops the dispatchers; a send in progress is allowed to finish until ctx ends.
func (q *Queues) Shutdown(ctx context.Context) {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func final(st string) bool { return st == store.OutSent || st == store.OutFailed }

// Wait blocks until the item is sent or failed, or ctx ends, and returns its latest state
// (still queued or in_flight when ctx ended first).
func (q *Queues) Wait(ctx context.Context, instanceID, id string) (store.Outbound, error) {
	for {
		ch := make(chan struct{})
		q.mu.Lock()
		q.waiters[id] = append(q.waiters[id], ch)
		q.mu.Unlock()
		o, err := q.store.GetOutbound(ctx, instanceID, id)
		if err != nil || final(o.Status) {
			q.dropWaiter(id, ch)
			return o, err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			q.dropWaiter(id, ch)
			// The request context is over, not the store: read with a fresh one.
			o, err = q.store.GetOutbound(context.WithoutCancel(ctx), instanceID, id)
			return o, err
		}
	}
}

func (q *Queues) dropWaiter(id string, ch chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.waiters[id]
	for i, c := range l {
		if c == ch {
			q.waiters[id] = append(l[:i], l[i+1:]...)
			break
		}
	}
	if len(q.waiters[id]) == 0 {
		delete(q.waiters, id)
	}
}

// notify wakes the Wait calls of an item whose status changed.
func (q *Queues) notify(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ch := range q.waiters[id] {
		close(ch)
	}
	delete(q.waiters, id)
}

type dispatcher struct {
	q     *Queues
	id    string
	wake  chan struct{}
	pacer pacer
	// lastNewText/lastNewChat: the text and chat of the previous send when it went to a new
	// recipient.
	lastNewText, lastNewChat string
}

func (d *dispatcher) log() *slog.Logger { return d.q.opt.Log.With("instance_id", d.id) }

func (d *dispatcher) run() {
	defer func() {
		d.q.mu.Lock()
		if d.q.disp[d.id] == d {
			delete(d.q.disp, d.id)
		}
		d.q.mu.Unlock()
		d.q.wg.Done()
	}()
	ctx := d.q.ctx
	for ctx.Err() == nil {
		next, alive := d.step(ctx)
		if !alive {
			return
		}
		d.sleep(ctx, next)
	}
}

// sleep waits for d, a wake-up or the end of ctx; d < 0 waits for a wake-up only.
func (d *dispatcher) sleep(ctx context.Context, dur time.Duration) {
	if dur == 0 {
		return
	}
	var timer <-chan time.Time
	if dur > 0 {
		t := d.q.opt.Clock.NewTimer(dur)
		defer t.Stop()
		timer = t.C()
	}
	select {
	case <-ctx.Done():
	case <-d.wake:
	case <-timer:
	}
}

const errPause = 5 * time.Second // pause after an unexpected store error

// step does at most one send and returns how long to sleep before the next step
// (-1: until woken). alive=false means the instance no longer exists.
func (d *dispatcher) step(ctx context.Context) (next time.Duration, alive bool) {
	st, opt := d.q.store, d.q.opt
	in, err := st.GetInstance(ctx, d.id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false
	}
	if err != nil {
		d.log().Error("queue: load instance", "err", err)
		return errPause, true
	}
	if in.State != instance.Connected {
		return -1, true // paused: no attempt is consumed while disconnected
	}
	now := opt.Clock.Now()
	set, err := settings.Effective(opt.Defaults, "", in.SettingsJSON)
	if err != nil {
		d.log().Error("queue: bad settings; using defaults", "err", err)
		set = opt.Defaults
	}
	item, err := st.NextOutbound(ctx, d.id, now)
	if errors.Is(err, store.ErrNotFound) {
		due, ok, err := st.NextDue(ctx, d.id)
		switch {
		case err != nil:
			d.log().Error("queue: next due", "err", err)
			return errPause, true
		case !ok:
			return -1, true
		}
		return max(due.Sub(now), time.Millisecond), true
	}
	if err != nil {
		d.log().Error("queue: next item", "err", err)
		return errPause, true
	}
	var p Payload
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		return pauseOn(d.fail(item, item.Attempts, "validation_failed: unreadable payload")), true
	}
	isNew, err := d.newRecipient(ctx, item)
	if err != nil {
		d.log().Error("queue: load chat", "err", err)
		return errPause, true
	}
	if until, err := d.ramp(ctx, in, set, item, isNew, now); err != nil {
		d.log().Error("queue: ramp", "err", err)
		return errPause, true
	} else if !until.IsZero() {
		if err := st.Defer(ctx, item.ID, until, "ramp_limit: daily limit of new recipients reached"); err != nil {
			d.log().Error("queue: defer", "err", err)
			return errPause, true
		}
		return 0, true
	}
	double := isNew && p.Type == "text" && p.Text != "" && p.Text == d.lastNewText && item.ChatJID != d.lastNewChat
	if w := d.pacer.wait(now, set, double); w > 0 {
		return w, true
	}
	cli, err := d.q.workers.Client(d.id)
	if err != nil {
		return time.Second, true // connected but the worker handle is not ready yet
	}
	req, err := buildRequest(item, p)
	if err != nil {
		return pauseOn(d.fail(item, item.Attempts, "validation_failed: "+err.Error())), true
	}
	if err := st.MarkInFlight(ctx, item.ID, isNew, pendingMessage(item, p, now)); err != nil {
		d.log().Error("queue: mark in flight", "err", err)
		return errPause, true
	}
	d.typing(ctx, cli, item.ChatJID, p, set)
	if ctx.Err() != nil {
		// Shutting down before the send started: back to the queue, no attempt spent.
		if err := st.Requeue(context.Background(), item.ID, now, item.Attempts, item.LastError); err != nil {
			d.log().Error("queue: requeue", "err", err)
		}
		return 0, true
	}

	sctx, cancel := context.WithTimeout(context.Background(), opt.SendTimeout)
	resp, sendErr := cli.Send(sctx, req)
	cancel()
	d.pacer.record(opt.Clock.Now(), set)
	if isNew && p.Type == "text" {
		d.lastNewText, d.lastNewChat = p.Text, item.ChatJID
	} else {
		d.lastNewText, d.lastNewChat = "", ""
	}
	return pauseOn(d.finish(item, p, resp, sendErr)), true
}

// pauseOn is the pause after a store write: errPause when it failed (no tight loop on the
// store's single connection), else none.
func pauseOn(err error) time.Duration {
	if err != nil {
		return errPause
	}
	return 0
}

// newRecipient reports whether item goes to a chat (groups included) that never sent us
// anything. Such sends count against the ramp, and an identical text to new recipients in
// a row doubles the interval, whether or not the ramp is enabled.
func (d *dispatcher) newRecipient(ctx context.Context, item store.Outbound) (bool, error) {
	chat, err := d.q.store.GetChat(ctx, d.id, item.ChatJID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	return !chat.HasInbound, nil
}

// ramp returns when item must be deferred to because today's limit of new recipients is
// reached (zero: it may go now). Each new chat counts once a day.
func (d *dispatcher) ramp(ctx context.Context, in store.Instance, set settings.Settings, item store.Outbound, isNew bool, now time.Time) (time.Time, error) {
	if !set.Ramp.Enabled || !isNew {
		return time.Time{}, nil
	}
	day := set.DayStart(now)
	count, counted, err := d.q.store.NewRecipientsSince(ctx, d.id, item.ChatJID, day)
	if err != nil || counted || count < set.Ramp.DailyCap(in.FirstConnectedAt, now) {
		return time.Time{}, err
	}
	// Not at midnight: at the next day's window start plus jitter (a burst of deferred
	// messages at night looks like spam).
	tomorrow := day.AddDate(0, 0, 1)
	if start, _, _ := set.WindowAt(tomorrow); !start.IsZero() {
		tomorrow = start
	}
	return tomorrow.Add(time.Duration(d.q.opt.Rand() * float64(d.q.opt.WindowJitter))), nil
}

func buildRequest(item store.Outbound, p Payload) (*workerv1.SendRequest, error) {
	req := &workerv1.SendRequest{
		To: item.ChatJID, MessageId: item.WAMessageID, Type: p.Type, Text: p.Text,
		Mime: p.Mime, FileName: p.FileName, Ptt: p.PTT,
	}
	if p.MediaFile != "" {
		if _, err := os.Stat(p.MediaFile); err != nil {
			return nil, errors.New("staged media is missing")
		}
		req.MediaPath = p.MediaFile // the worker streams it from disk
	}
	if r := p.Reaction; r != nil {
		req.ReactionMessageId, req.ReactionFromMe, req.ReactionSender = r.WAMessageID, r.FromMe, r.Sender
	}
	return req, nil
}

// typing shows "typing..." (or "recording..." for voice notes) before a send.
func (d *dispatcher) typing(ctx context.Context, cli workerv1.WorkerClient, chat string, p Payload, set settings.Settings) {
	if !set.TypingSimulation {
		return
	}
	var state string
	var delay time.Duration
	switch {
	case p.Type == "text":
		state, delay = "composing", typingDelay(p.Text)
	case p.Type == "audio" && p.PTT:
		state, delay = "recording", 2*time.Second
	default:
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = cli.SendPresence(pctx, &workerv1.SendPresenceRequest{To: chat, State: state})
	cancel()
	t := d.q.opt.Clock.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C():
	case <-ctx.Done():
	}
}

// finish records the outcome of a send attempt; an error means the store write failed.
func (d *dispatcher) finish(item store.Outbound, p Payload, resp *workerv1.SendResponse, sendErr error) error {
	ctx := context.Background()
	st, opt := d.q.store, d.q.opt
	if sendErr == nil {
		at := opt.Clock.Now()
		if resp.GetTimestampMs() > 0 {
			at = time.UnixMilli(resp.GetTimestampMs())
		}
		return d.sent(item, p, at)
	}
	// A receipt already proved delivery (the worker's answer was lost or late): it was sent.
	if m, err := st.GetMessage(ctx, d.id, item.ID); err == nil && (m.Status == "delivered" || m.Status == "read") {
		return d.sent(item, p, opt.Clock.Now())
	}
	s := status.Convert(sendErr)
	msg := s.Message()
	if !strings.Contains(msg, ": ") {
		msg = "worker_error: " + msg
	}
	attempts := item.Attempts + 1
	if strings.HasPrefix(msg, "instance_not_connected:") {
		// The worker itself says it is not connected: wait without spending an attempt,
		// whatever the stored state says (it may lag behind).
		return d.requeue(item, opt.Clock.Now().Add(errPause), item.Attempts, msg)
	}
	switch s.Code() {
	case codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition:
		return d.fail(item, attempts, msg)
	case codes.Unavailable, codes.Canceled:
		// Not connected: unless the instance still claims to be connected, wait for the
		// reconnection without consuming an attempt.
		if in, err := st.GetInstance(ctx, d.id); err == nil && in.State != instance.Connected {
			return d.requeue(item, opt.Clock.Now(), item.Attempts, msg)
		}
	case codes.DeadlineExceeded:
		msg = "worker_timeout: " + strings.TrimPrefix(msg, "worker_timeout: ")
	}
	if attempts >= opt.MaxAttempts {
		return d.fail(item, attempts, msg)
	}
	return d.requeue(item, opt.Clock.Now().Add(retryDelay(attempts)), attempts, msg)
}

func (d *dispatcher) requeue(item store.Outbound, next time.Time, attempts int, msg string) error {
	err := d.q.store.Requeue(context.Background(), item.ID, next, attempts, msg)
	if err != nil {
		d.log().Error("queue: requeue", "err", err)
	}
	return err
}

// sent finishes a delivered item. If the write fails the item stays in flight (boot
// recovery requeues it; the reused wa_message_id keeps the resend idempotent), so the
// staged media is kept.
func (d *dispatcher) sent(item store.Outbound, p Payload, at time.Time) error {
	if err := d.q.store.MarkSent(context.Background(), item.ID, at); err != nil {
		d.log().Error("queue: mark sent", "err", err)
		return err
	}
	d.done(item, p)
	return nil
}

// fail finishes an item for good. If the write fails the item keeps its status (queued,
// or in_flight when it fails after a send attempt) and its staged media.
func (d *dispatcher) fail(item store.Outbound, attempts int, msg string) error {
	if err := d.q.store.MarkFailed(context.Background(), item.ID, attempts, msg); err != nil {
		d.log().Error("queue: mark failed", "err", err)
		return err
	}
	var p Payload
	_ = json.Unmarshal([]byte(item.Payload), &p)
	d.done(item, p)
	return nil
}

// done cleans up a final item and wakes its waiters.
func (d *dispatcher) done(item store.Outbound, p Payload) {
	if p.MediaFile != "" {
		_ = os.Remove(p.MediaFile)
	}
	d.q.notify(item.ID)
}

// pendingMessage is the log row written when the item goes in flight.
func pendingMessage(item store.Outbound, p Payload, at time.Time) store.Message {
	m := store.Message{
		ID: item.ID, WAMessageID: item.WAMessageID, ChatJID: item.ChatJID, FromMe: true, Source: "api",
		Type: p.Type, Text: p.Text, Status: "pending", Timestamp: at,
	}
	if p.Reaction != nil {
		m.ReactionTo = p.Reaction.WAMessageID
	}
	if p.MediaFile != "" {
		b, _ := json.Marshal(store.MediaInfo{Kind: p.Type, Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT})
		m.MediaJSON = string(b)
	}
	return m
}
```

- [ ] **Step 3: Rodar e ver passar (inclusive repetido, para pegar corridas)**

Run: `go test -race -count=20 ./internal/queue/`
Expected: `ok` (cerca de 15s).

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/queue
git commit -m "feat(queue): despachante por instância com ordem estrita, rampa, retry e wait" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 12: Despachante — políticas

### Task 14: Despachante: carência, janela de envio, TTL e verificação de número

**Files:**
- Modify: `internal/queue/queue.go`
- Test: `internal/queue/queue_test.go`

Passos 2, 4, 5 e 7 da task anterior. A janela é decidida por item: só quem não mandou nada nas últimas 24h espera; a variação é sorteada uma vez por janela. A verificação usa o `lookup.Resolver` opcional (`Options.Resolver`; `serve` liga) só para JIDs de telefone sem chat e só depois da rampa (acima do limite diário não há consulta); falhas e limite adiam apenas o item afetado (os outros destinatários seguem), uma consulta cortada pelo shutdown não conta falha, e depois de redirecionar para o JID com 9º dígito o número canônico já está no cache. A carência começa no aviso de `connected`; outro estado a reinicia.

- [ ] **Step 1: Escrever os testes**

Em `internal/queue/queue_test.go`, trocar:

```go
	"os"
	"path/filepath"
	"testing"
	"time"
```

por:

```go
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
```

Em `internal/queue/queue_test.go`, trocar:

```go
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/settings"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/settings"
```

Em `internal/queue/queue_test.go`, trocar:

```go
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
```

Em `internal/queue/queue_test.go`, trocar:

```go
	fc := clock.NewFake(start)
	ws := workers{"i1": h.Client()}
	opt := Options{Clock: fc, Rand: func() float64 { return 0.5 }, Defaults: defaults, Log: slog.New(slog.DiscardHandler)}
	if tweak != nil {
		tweak(&opt)
```

por:

```go
	fc := clock.NewFake(start)
	ws := workers{"i1": h.Client()}
	opt := Options{Clock: fc, Rand: func() float64 { return 0.5 }, Defaults: defaults, Log: slog.New(slog.DiscardHandler),
		Resolver: &lookup.Resolver{Store: st, Workers: ws, Clock: fc}}
	if tweak != nil {
		tweak(&opt)
```

Em `internal/queue/queue_test.go`, trocar:

```go
func (e *env) enqueue(chat string, p Payload) string {
	e.t.Helper()
	e.seq++
	id := "q" + string(rune('0'+e.seq))
	b, _ := json.Marshal(p)
	if _, _, err := e.st.Enqueue(context.Background(), store.Outbound{ID: id, InstanceID: "i1", ChatJID: chat,
		WAMessageID: "WA-" + id, Payload: string(b), NextAttemptAt: e.clock.Now()}); err != nil {
		e.t.Fatal(err)
	}
	e.q.Wake("i1")
	return id
}

```

por:

```go
func (e *env) enqueue(chat string, p Payload) string {
	e.t.Helper()
	return e.enqueueExpiring(chat, p, time.Time{})
}

func (e *env) enqueueExpiring(chat string, p Payload, expires time.Time) string {
	e.t.Helper()
	e.seq++
	id := "q" + string(rune('0'+e.seq))
	b, _ := json.Marshal(p)
	if _, _, err := e.st.Enqueue(context.Background(), store.Outbound{ID: id, InstanceID: "i1", ChatJID: chat,
		WAMessageID: "WA-" + id, Payload: string(b), NextAttemptAt: e.clock.Now(), ExpiresAt: expires}); err != nil {
		e.t.Fatal(err)
	}
	e.q.Wake("i1")
	return id
}

func (e *env) connect() {
	e.t.Helper()
	if err := e.st.UpdateInstanceState(context.Background(), "i1", instance.Connected, ""); err != nil {
		e.t.Fatal(err)
	}
	e.q.StateChanged("i1", instance.Connected)
}

```

Em `internal/queue/queue_test.go`, trocar:

```go
}

func TestWorkerNotConnectedKeepsAttempts(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
```

por:

```go
}

func TestOutsideWindowWaitsForNextWindow(t *testing.T) {
	s := fastSettings()
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	e := newEnvAt(t, time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC), s, instance.Connected, nil)
	id := e.enqueue(chatA, Payload{Type: "text", Text: "boa noite"})
	e.eventually("waiting for the window", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(11 * time.Hour) // 08:00: still inside the jitter
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("sent before window start + jitter")
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(150 * time.Second)
	e.eventually("sent at 08:02:30", func() bool { return e.status(id).Status == store.OutSent })
}

// Replies to an active conversation (something received in the last 24h) are not held
// by the send window; cold recipients wait for the next window.
func TestWindowLetsActiveConversationsThrough(t *testing.T) {
	s := fastSettings()
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	start := time.Date(2026, 10, 17, 20, 5, 0, 0, time.UTC)
	e := newEnvAt(t, start, s, instance.Connected, nil)
	if _, err := e.st.InsertMessage(context.Background(), "i1", store.Message{ID: "in1", WAMessageID: "IN1", ChatJID: chatA,
		Source: "contact", Type: "text", Status: "received", Timestamp: start.Add(-10 * time.Minute)},
		store.ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	cold := e.enqueue(chatB, Payload{Type: "text", Text: "oi"})
	reply := e.enqueue(chatA, Payload{Type: "text", Text: "respondendo"})
	e.eventually("reply sent at 20:05", func() bool { return e.status(reply).Status == store.OutSent })
	o := e.status(cold)
	if want := time.Date(2026, 10, 18, 8, 2, 30, 0, time.UTC); o.Status != store.OutQueued || !o.NextAttemptAt.Equal(want) || o.Attempts != 0 {
		t.Fatalf("cold recipient = %+v, want due at %v", o, want)
	}
}

func TestGraceAfterConnect(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.ConnectGrace = 20 * time.Second })
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.connect()
	e.eventually("grace timer", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(29 * time.Second) // grace is 20s + 0.5 x 20s
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("sent during the grace period")
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Second)
	e.eventually("sent after grace", func() bool { return e.status(id).Status == store.OutSent })
}

func TestExpiredItemFails(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Reconnecting)
	id := e.enqueueExpiring(chatA, Payload{Type: "text", Text: "oi"}, t0.Add(time.Hour))
	e.clock.Advance(2 * time.Hour)
	e.connect()
	e.eventually("expired", func() bool { return e.status(id).Status == store.OutFailed })
	if o := e.status(id); !strings.HasPrefix(o.LastError, "expired:") || len(e.fake.WA.Sent()) != 0 {
		t.Fatalf("expired item = %+v, sent %d", o, len(e.fake.WA.Sent()))
	}
}

func TestRecipientResolution(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetRegistered("551188887777", "5511988887777@s.whatsapp.net")
	e.fake.WA.SetUnregistered("5511000000000")
	a := e.enqueue("551188887777@s.whatsapp.net", Payload{Type: "text", Text: "um"})
	b := e.enqueue("551188887777@s.whatsapp.net", Payload{Type: "text", Text: "dois"})
	x := e.enqueue("5511000000000@s.whatsapp.net", Payload{Type: "text", Text: "ninguém"})
	e.eventually("resolved sends", func() bool {
		return e.status(a).Status == store.OutSent && e.status(b).Status == store.OutSent && e.status(x).Status == store.OutFailed
	})
	sent := e.fake.WA.Sent()
	if len(sent) != 2 || sent[0].To != "5511988887777@s.whatsapp.net" || sent[1].To != "5511988887777@s.whatsapp.net" || sent[0].MessageId != "WA-q1" {
		t.Fatalf("sent = %v", sent)
	}
	if o := e.status(x); !strings.HasPrefix(o.LastError, "invalid_number:") {
		t.Fatalf("x = %+v", o)
	}
	if e.fake.WA.Checks() != 2 {
		t.Fatalf("lookups = %d, want 2 (the second item reused the cache/chat)", e.fake.WA.Checks())
	}
}

// A LID answer never becomes the chat: the item goes to the phone JID.
func TestResolutionNeverRetargetsToLID(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetRegistered("5511777766665", "123456789@lid")
	id := e.enqueue("5511777766665@s.whatsapp.net", Payload{Type: "text", Text: "oi"})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	if to := e.fake.WA.Sent()[0].To; to != "5511777766665@s.whatsapp.net" {
		t.Fatalf("sent to %q", to)
	}
}

// A failing lookup only postpones its own item, with growing backoff, and never re-queries
// before the backoff; a known chat's item is not held.
func TestLookupFailureDefersOnlyThatItem(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	ctx := context.Background()
	if _, err := e.st.InsertMessage(ctx, "i1", store.Message{ID: "in1", WAMessageID: "IN1", ChatJID: chatC,
		Source: "contact", Type: "text", Status: "received", Timestamp: t0}, store.ChatUpdate{Inbound: true}); err != nil {
		t.Fatal(err)
	}
	e.fake.WA.SetCheckErr(status.Error(codes.Unknown, "worker_error: usync failed"))
	x := e.enqueue(chatA, Payload{Type: "text", Text: "novo"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "conhecido"})
	e.eventually("known chat sent", func() bool { return e.status(c).Status == store.OutSent })
	o := e.status(x)
	if o.Status != store.OutQueued || o.LookupFailures != 1 || o.Attempts != 0 || !o.NextAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after 1st lookup failure: %+v", o)
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(30 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if e.fake.WA.Checks() != 1 {
		t.Fatalf("looked up again before the backoff: %d", e.fake.WA.Checks())
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(30 * time.Second)
	e.eventually("2nd lookup failure", func() bool { return e.status(x).LookupFailures == 2 })
	if o := e.status(x); !o.NextAttemptAt.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("2nd backoff = %v", o.NextAttemptAt)
	}
	e.fake.WA.SetCheckErr(nil)
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(2 * time.Minute)
	e.eventually("sent once the lookup works", func() bool { return e.status(x).Status == store.OutSent })
}

func TestLookupGivesUpAfterFiveFailures(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
	e.fake.WA.SetCheckErr(status.Error(codes.Unknown, "worker_error: usync failed"))
	x := e.enqueue(chatA, Payload{Type: "text", Text: "novo"})
	for i := 1; i < maxLookupFailures; i++ {
		e.eventually("deferred", func() bool { return e.status(x).LookupFailures == i && e.clock.Waiters() > 0 })
		e.clock.Advance(lookupBackoff(i))
	}
	e.eventually("failed", func() bool { return e.status(x).Status == store.OutFailed })
	if o := e.status(x); !strings.HasPrefix(o.LastError, "worker_error: recipient lookup failed") || len(e.fake.WA.Sent()) != 0 {
		t.Fatalf("final = %+v", o)
	}
}

// Past today's ramp limit, new numbers are deferred without a lookup: a stream of lookups
// looks like contact scraping.
func TestRampLimitSkipsLookups(t *testing.T) {
	s := fastSettings()
	s.Ramp = settings.Ramp{Enabled: true, StartPerDay: 1, EndPerDay: 1, Days: 1}
	s.SendWindow = settings.Window{Start: "08:00", End: "20:00"}
	e := newEnv(t, s, instance.Connected)
	a := e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	b := e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	c := e.enqueue(chatC, Payload{Type: "text", Text: "três"})
	e.eventually("a sent, b and c deferred", func() bool {
		return e.status(a).Status == store.OutSent && strings.HasPrefix(e.status(b).LastError, "ramp_limit:") &&
			strings.HasPrefix(e.status(c).LastError, "ramp_limit:")
	})
	if n := e.fake.WA.Checks(); n != 1 {
		t.Fatalf("lookups = %d, want 1 (only the recipient that went out)", n)
	}
}

func TestLookupRateLimitDefersWithoutCountingFailure(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) { o.Resolver.PerMinute = 1 })
	a := e.enqueue(chatA, Payload{Type: "text", Text: "um"})
	b := e.enqueue(chatB, Payload{Type: "text", Text: "dois"})
	e.eventually("a sent, b deferred", func() bool {
		return e.status(a).Status == store.OutSent && strings.HasPrefix(e.status(b).LastError, "lookup_rate_limited:")
	})
	if o := e.status(b); o.Status != store.OutQueued || o.LookupFailures != 0 || o.Attempts != 0 ||
		!o.NextAttemptAt.Equal(t0.Add(time.Minute)) || e.fake.WA.Checks() != 1 {
		t.Fatalf("b = %+v, lookups %d", o, e.fake.WA.Checks())
	}
	e.eventually("waiting", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(time.Minute)
	e.eventually("b sent after a minute", func() bool { return e.status(b).Status == store.OutSent })
}

// blockingCheck holds CheckNumbers until the caller's context ends.
type blockingCheck struct {
	workerv1.WorkerClient
	started chan struct{}
}

func (b blockingCheck) CheckNumbers(ctx context.Context, _ *workerv1.CheckNumbersRequest, _ ...grpc.CallOption) (*workerv1.CheckNumbersResponse, error) {
	close(b.started)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

// A lookup cut short by shutdown is not a lookup failure (not even the fifth one).
func TestLookupInterruptedByShutdownIsNotAFailure(t *testing.T) {
	started := make(chan struct{})
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) {
		base, _ := o.Resolver.Workers.Client("i1")
		o.Resolver.Workers = workers{"i1": blockingCheck{WorkerClient: base, started: started}}
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	if err := e.st.DeferLookup(context.Background(), id, t0, maxLookupFailures-1, "worker_error: recipient lookup failed"); err != nil {
		t.Fatal(err)
	}
	e.connect()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup never started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.q.Shutdown(ctx)
	if o := e.status(id); o.Status != store.OutQueued || o.LookupFailures != maxLookupFailures-1 {
		t.Fatalf("after shutdown: %+v", o)
	}
}

// The stored state may read connected before the connected notice arrives: nothing goes
// out until the notice starts the grace period.
func TestGraceStartsWithTheConnectedNotice(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.ConnectGrace = 20 * time.Second })
	e.q.StateChanged("i1", instance.Reconnecting)
	if err := e.st.UpdateInstanceState(context.Background(), "i1", instance.Connected, ""); err != nil {
		t.Fatal(err)
	}
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"}) // wakes the dispatcher before the notice
	time.Sleep(50 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 0 {
		t.Fatalf("sent %d before the connected notice", n)
	}
	e.q.StateChanged("i1", instance.Connected)
	e.eventually("grace timer", func() bool { return e.clock.Waiters() > 0 })
	e.clock.Advance(30 * time.Second)
	e.eventually("sent after grace", func() bool { return e.status(id).Status == store.OutSent })
}

func TestWorkerNotConnectedKeepsAttempts(t *testing.T) {
	e := newEnv(t, fastSettings(), instance.Connected)
```

Run: `go test ./internal/queue/`
Expected: FAIL — `unknown field Resolver in struct literal of type Options`, `unknown field ConnectGrace`.

- [ ] **Step 2: Implementar**

Em `internal/queue/queue.go`, trocar:

```go
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
```

Em `internal/queue/queue.go`, trocar:

```go
	// SendTimeout bounds one Send RPC; whatsmeow itself waits up to 75s for the server ack.
	SendTimeout time.Duration
	// WindowJitter spreads ramp-deferred sends over [0, WindowJitter) after the next
	// window start (default 5 minutes).
	WindowJitter time.Duration
	Log          *slog.Logger
}

```

por:

```go
	// SendTimeout bounds one Send RPC; whatsmeow itself waits up to 75s for the server ack.
	SendTimeout time.Duration
	// ConnectGrace delays the first send after reaching connected by ConnectGrace plus a
	// random part of up to ConnectGrace (0 disables; serve uses 15s).
	ConnectGrace time.Duration
	// WindowJitter spreads the first send of each send window over [0, WindowJitter)
	// (default 5 minutes), so queued work does not start exactly on the hour.
	WindowJitter time.Duration
	// Resolver, when set, resolves a phone number before the first send to it.
	Resolver *lookup.Resolver
	Log      *slog.Logger
}

```

Em `internal/queue/queue.go`, trocar:

```go
	disp    map[string]*dispatcher
	waiters map[string][]chan struct{}
}

```

por:

```go
	disp    map[string]*dispatcher
	waiters map[string][]chan struct{}
	readyAt map[string]time.Time // end of the grace period after connecting
}

```

Em `internal/queue/queue.go`, trocar:

```go
	ctx, cancel := context.WithCancel(context.Background())
	return &Queues{store: st, workers: workers, opt: opt, ctx: ctx, cancel: cancel,
		disp: map[string]*dispatcher{}, waiters: map[string][]chan struct{}{}}
}

```

por:

```go
	ctx, cancel := context.WithCancel(context.Background())
	return &Queues{store: st, workers: workers, opt: opt, ctx: ctx, cancel: cancel,
		disp: map[string]*dispatcher{}, waiters: map[string][]chan struct{}{}, readyAt: map[string]time.Time{}}
}

```

Em `internal/queue/queue.go`, trocar:

```go

func (q *Queues) StateChanged(id string, st instance.State) {
	if st == instance.Connected {
		q.Wake(id)
	}
}

```

por:

```go

// notReady marks an instance whose connected notice has not arrived yet.
var notReady = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

func (q *Queues) StateChanged(id string, st instance.State) {
	g := q.opt.ConnectGrace
	if st != instance.Connected {
		if g > 0 {
			// The stored state may read connected before this notice arrives: hold sends
			// until the connected notice starts the grace period.
			q.mu.Lock()
			q.readyAt[id] = notReady
			q.mu.Unlock()
		}
		return
	}
	if g > 0 {
		ready := q.opt.Clock.Now().Add(g + time.Duration(q.opt.Rand()*float64(g)))
		q.mu.Lock()
		q.readyAt[id] = ready
		q.mu.Unlock()
	}
	q.Wake(id)
}

func (q *Queues) ready(id string) time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.readyAt[id]
}

```

Em `internal/queue/queue.go`, trocar:

```go
	// lastNewText/lastNewChat: the text and chat of the previous send when it went to a new
	// recipient.
	lastNewText, lastNewChat string
}

```

por:

```go
	// lastNewText/lastNewChat: the text and chat of the previous send when it went to a new
	// recipient.
	lastNewText, lastNewChat string
	// winStart/winJitter: the jitter drawn for the send window starting at winStart.
	winStart  time.Time
	winJitter time.Duration
}

// windowOpensAt is when sending may start in the window beginning at start (start + jitter).
func (d *dispatcher) windowOpensAt(start time.Time) time.Time {
	if !start.Equal(d.winStart) {
		d.winStart = start
		d.winJitter = time.Duration(d.q.opt.Rand() * float64(d.q.opt.WindowJitter))
	}
	return start.Add(d.winJitter)
}

```

Em `internal/queue/queue.go`, trocar:

```go
	}
	now := opt.Clock.Now()
	set, err := settings.Effective(opt.Defaults, "", in.SettingsJSON)
	if err != nil {
```

por:

```go
	}
	now := opt.Clock.Now()
	if ready := d.q.ready(d.id); ready.Equal(notReady) {
		return -1, true // the connected notice will wake us
	} else if now.Before(ready) {
		return ready.Sub(now), true // grace period after connecting
	}
	set, err := settings.Effective(opt.Defaults, "", in.SettingsJSON)
	if err != nil {
```

Em `internal/queue/queue.go`, trocar:

```go
		return errPause, true
	}
	var p Payload
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		return pauseOn(d.fail(item, item.Attempts, "validation_failed: unreadable payload")), true
	}
```

por:

```go
		return errPause, true
	}
	if !item.ExpiresAt.IsZero() && now.After(item.ExpiresAt) {
		return pauseOn(d.fail(item, item.Attempts, "expired: not sent before expires_after")), true
	}
	var p Payload
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		return pauseOn(d.fail(item, item.Attempts, "validation_failed: unreadable payload")), true
	}
	if opens, wait, err := d.window(ctx, set, item, now); err != nil {
		d.log().Error("queue: send window", "err", err)
		return errPause, true
	} else if wait {
		if err := st.Defer(ctx, item.ID, opens, "send_window: waiting for the send window"); err != nil {
			d.log().Error("queue: defer", "err", err)
			return errPause, true
		}
		return 0, true
	}
```

Em `internal/queue/queue.go`, trocar:

```go
		if err := st.Defer(ctx, item.ID, until, "ramp_limit: daily limit of new recipients reached"); err != nil {
			d.log().Error("queue: defer", "err", err)
			return errPause, true
		}
		return 0, true
	}
```

por:

```go
		if err := st.Defer(ctx, item.ID, until, "ramp_limit: daily limit of new recipients reached"); err != nil {
			d.log().Error("queue: defer", "err", err)
			return errPause, true
		}
		return 0, true
	}
	// The number lookup comes after the ramp: past today's limit new numbers are not even
	// looked up (a stream of lookups looks like contact scraping). An item moved to the
	// canonical JID is picked again, so the ramp is checked for that JID too.
	if next, done := d.resolve(ctx, item, now); done {
		return next, true
	}
	// Doubled only towards another chat: a retry of the same item is not a repeated text.
```

Em `internal/queue/queue.go`, trocar:

```go
func pauseOn(err error) time.Duration {
	if err != nil {
		return errPause
	}
	return 0
}

```

por:

```go
func pauseOn(err error) time.Duration {
	if err != nil {
		return errPause
	}
	return 0
}

// activeWindow is how recent a received message must be for a chat to count as an active
// conversation, which the send window does not hold back.
const activeWindow = 24 * time.Hour

// window decides whether item must wait for the send window: only recipients without a
// message received in the last 24h wait (replies to active conversations go out at any
// hour). opens is when the next window opens, plus its jitter.
func (d *dispatcher) window(ctx context.Context, set settings.Settings, item store.Outbound, now time.Time) (opens time.Time, wait bool, err error) {
	start, _, open := set.WindowAt(now)
	if start.IsZero() {
		return time.Time{}, false, nil
	}
	opens = d.windowOpensAt(start)
	if open && !now.Before(opens) {
		return time.Time{}, false, nil
	}
	chat, err := d.q.store.GetChat(ctx, d.id, item.ChatJID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return time.Time{}, false, err
	}
	if !chat.LastInboundAt.IsZero() && now.Sub(chat.LastInboundAt) < activeWindow {
		return time.Time{}, false, nil
	}
	return opens, true, nil
}

// lookupBackoff is the wait after n consecutive failed lookups of an item's recipient.
func lookupBackoff(n int) time.Duration {
	switch n {
	case 1:
		return time.Minute
	case 2:
		return 2 * time.Minute
	case 3:
		return 5 * time.Minute
	}
	return 15 * time.Minute
}

const maxLookupFailures = 5

// resolve checks a phone-number recipient the first time it is used (no chat yet): a
// number without WhatsApp fails without sending, and a number WhatsApp knows under another
// phone JID (Brazilian 9th digit) has its queued items moved there. A failed or
// rate-limited lookup only postpones this item; other recipients keep going. done=true
// means the item was handled (failed, deferred or moved) and the step should sleep next
// (errPause after a failed store write) and pick again.
func (d *dispatcher) resolve(ctx context.Context, item store.Outbound, now time.Time) (next time.Duration, done bool) {
	r := d.q.opt.Resolver
	user, ok := strings.CutSuffix(item.ChatJID, "@s.whatsapp.net")
	if r == nil || !ok {
		return 0, false
	}
	st := d.q.store
	if _, err := st.GetChat(ctx, d.id, item.ChatJID); err == nil {
		return 0, false // known chat: we already talked to this JID
	} else if !errors.Is(err, store.ErrNotFound) {
		d.log().Error("queue: load chat", "err", err)
		return errPause, true
	}
	res, err := r.Resolve(ctx, d.id, []string{user}, lookup.ForSend)
	switch {
	case ctx.Err() != nil:
		return 0, true // shutting down: an interrupted lookup is not a lookup failure
	case errors.Is(err, lookup.ErrRateLimited):
		err := st.Defer(ctx, item.ID, now.Add(time.Minute), "lookup_rate_limited: retrying in a minute")
		if err != nil {
			d.log().Error("queue: defer", "err", err)
		}
		return pauseOn(err), true
	case err != nil:
		n := item.LookupFailures + 1
		msg := "worker_error: recipient lookup failed: " + status.Convert(err).Message()
		if n >= maxLookupFailures {
			return pauseOn(d.fail(item, item.Attempts, msg)), true
		}
		err := st.DeferLookup(ctx, item.ID, now.Add(lookupBackoff(n)), n, msg)
		if err != nil {
			d.log().Error("queue: defer", "err", err)
		}
		return pauseOn(err), true
	case !res[0].Found:
		return pauseOn(d.fail(item, item.Attempts, "invalid_number: recipient is not on WhatsApp")), true
	case res[0].JID != item.ChatJID && strings.HasSuffix(res[0].JID, "@s.whatsapp.net"):
		// Only to another phone JID: a LID answer keeps the phone JID (chats are by phone).
		if err := st.RetargetChat(ctx, d.id, item.ChatJID, res[0].JID); err != nil {
			d.log().Error("queue: retarget", "err", err)
			return errPause, true
		}
		return 0, true
	}
	return 0, false
}

```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race -count=20 ./internal/queue/`
Expected: `ok` (cerca de 25s).

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/queue
git commit -m "feat(queue): carência após conectar, janela de envio, TTL e verificação de número antes do primeiro envio" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 13: Mídia

### Task 15: Download de mídia com proteção SSRF, tipo MIME e URLs assinadas

**Files:**
- Create: `internal/media/media.go`
- Test: `internal/media/media_test.go`

Spec 4.1/4.5: `media.url` só `http(s)`, timeout 30s, limite do tipo (imagem 16MB, vídeo/áudio 64MB, documento 100MB), sem proxy de ambiente. O `Fetcher` resolve o nome ele mesmo e confere **todos** os endereços antes de conectar, em toda conexão (inclusive redirecionamentos): só passa unicast global (`IsGlobalUnicast`), menos privados, `0.0.0.0/8`, `100.64.0.0/10`, `198.18.0.0/15`, `240.0.0.0/4`, `64:ff9b::/96` (NAT64) e `2002::/16` (6to4). Isso fecha DNS rebinding e redirecionamento para endereço interno; o resolvedor é injetável para testar. `media_allow_private_urls: true` desliga a checagem (uso local). `ResolveMime` sempre confere os bytes: imagem precisa ser imagem pelos bytes (SVG nunca vale) e leva o tipo detectado, e áudio/vídeo é recusado quando os bytes são texto/HTML, PDF, zip ou `application/x-*`, qualquer que seja o tipo declarado ou remoto; um `Content-Type` remoto que contradiz o tipo da mensagem é ignorado. URLs assinadas usam HMAC-SHA256 (cada parte prefixada pelo tamanho) com um segredo próprio em `data/media_key` (0600, criado num arquivo temporário e publicado com hard link: atômico, e partidas simultâneas ficam com a mesma chave; sem hard link no sistema de arquivos — FAT/exFAT, alguns SMB/FUSE — cai para `O_EXCL`) e valem 1h.

- [ ] **Step 1: Escrever os testes**

Criar `internal/media/media_test.go`:

```go
package media

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.0.10", "172.16.0.1", "169.254.169.254",
		"::1", "fe80::1", "fd00::1", "0.0.0.0", "0.1.2.3", "100.64.0.1", "::ffff:127.0.0.1", "198.18.0.1",
		"240.0.0.1", "255.255.255.255", "224.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1"} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "157.240.1.1", "2a03:2880::1"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestFetchBlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("x")) }))
	defer srv.Close()
	if _, _, err := (&Fetcher{}).Fetch(context.Background(), srv.URL, 100); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

// A URL on an allowed host that redirects to an internal one is blocked on the second hop.
func TestFetchBlocksRedirectToInternalHost(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://internal.test:"+srv.URL[strings.LastIndex(srv.URL, ":")+1:]+"/secret", http.StatusFound)
			return
		}
		w.Write([]byte("secret"))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	f := &Fetcher{
		LookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil // both names point to the test server
		},
		allow: func(host string, _ netip.Addr) bool { return host != "internal.test" },
	}
	if data, _, err := f.Fetch(context.Background(), "http://public.test:"+port+"/secret", 100); err != nil || string(data) != "secret" {
		t.Fatalf("allowed host = %q, %v", data, err)
	}
	if _, _, err := f.Fetch(context.Background(), "http://public.test:"+port+"/redir", 100); !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect to internal host = %v, want ErrBlocked", err)
	}
}

func TestFetchWhenPrivateAllowed(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img":
			w.Header().Set("Content-Type", "text/html") // lies; ResolveMime ignores it
			w.Write(png)
		case "/big":
			w.Write(make([]byte, 200))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &Fetcher{AllowPrivate: true}
	data, remote, err := f.Fetch(context.Background(), srv.URL+"/img", 100)
	if err != nil || string(data) != string(png) || remote != "text/html" {
		t.Fatalf("fetch = %q %q %v", data, remote, err)
	}
	if _, _, err := f.Fetch(context.Background(), srv.URL+"/big", 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big = %v", err)
	}
	if _, _, err := f.Fetch(context.Background(), srv.URL+"/missing", 100); !errors.Is(err, ErrFetch) {
		t.Fatalf("missing = %v", err)
	}
	if _, _, err := f.Fetch(context.Background(), "ftp://example.com/x", 100); !errors.Is(err, ErrFetch) {
		t.Fatalf("non-http url = %v", err)
	}
}

func TestResolveMime(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	ogg := []byte("OggS\x00\x02rest-of-ogg")
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	cases := []struct {
		kind, declared, remote string
		data                   []byte
		want                   string
		bad                    bool
	}{
		{"image", "", "image/webp", png, "image/png", false},          // images take the sniffed type
		{"image", "", "text/html", png, "image/png", false},           // contradicting remote type ignored
		{"image", "image/jpeg", "image/png", png, "image/png", false}, // even over a declared one
		{"image", "", "image/svg+xml", []byte("<svg/>"), "", true},    // never accept SVG as an image
		{"audio", "", "", ogg, "audio/ogg", false},
		{"audio", "audio/ogg; codecs=opus", "", ogg, "audio/ogg", false},
		{"video", "", "", []byte("plain text"), "", true},
		{"document", "", "application/pdf", []byte("%PDF"), "application/pdf", false},
		{"document", "", "", []byte{0, 1, 2}, "application/octet-stream", false},
		// The bytes are always checked: a declared or remote type does not make HTML media.
		{"image", "image/png", "", html, "", true},
		{"image", "", "image/png", html, "", true},
		{"video", "video/mp4", "", html, "", true},
		{"audio", "", "audio/mpeg", html, "", true},
		{"video", "video/mp4", "", []byte("%PDF-1.4 rest"), "", true},
		{"audio", "audio/ogg", "", []byte("PK\x03\x04rest-of-zip"), "", true},
		{"audio", "audio/mpeg", "", []byte("\x1f\x8b\x08rest-of-gzip"), "", true},
		// Audio/video the sniffer does not know (e.g. AMR) keeps the declared type.
		{"audio", "audio/amr", "", []byte("#!AMR\n\x00\x01"), "audio/amr", false},
	}
	for i, c := range cases {
		got, err := ResolveMime(c.kind, c.declared, c.remote, c.data)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("case %d: %q, %v", i, got, err)
		}
	}
}

func TestDecodeBase64(t *testing.T) {
	raw := []byte("%PDF-1.4 hello")
	enc := base64.StdEncoding.EncodeToString(raw)
	data, mime, err := DecodeBase64(enc, 100)
	if err != nil || string(data) != string(raw) || mime != "" {
		t.Fatalf("plain = %q %q %v", data, mime, err)
	}
	data, mime, err = DecodeBase64("data:image/webp;base64,"+enc, 100)
	if err != nil || mime != "image/webp" || len(data) != len(raw) {
		t.Fatalf("data url = %q %v", mime, err)
	}
	if _, _, err := DecodeBase64(enc, 5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large = %v", err)
	}
	if _, _, err := DecodeBase64("!!!", 100); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}

func TestKeyAndSigner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media_key")
	key, err := LoadOrCreateKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key = %x, %v", key, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", fi.Mode().Perm())
	}
	again, err := LoadOrCreateKey(path)
	if err != nil || string(again) != string(key) {
		t.Fatal("key not reused")
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s := NewSigner(key, "http://127.0.0.1:8787/")
	s.Now = func() time.Time { return now }
	raw, exp := s.URL("inst_1", "med_1")
	if !exp.Equal(now.Add(time.Hour)) || !strings.HasPrefix(raw, "http://127.0.0.1:8787/media/inst_1/med_1?") {
		t.Fatalf("url = %s exp = %v", raw, exp)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if !s.Verify("inst_1", "med_1", q.Get("exp"), q.Get("sig")) {
		t.Fatal("valid signature rejected")
	}
	if s.Verify("inst_1", "med_2", q.Get("exp"), q.Get("sig")) || s.Verify("inst_2", "med_1", q.Get("exp"), q.Get("sig")) {
		t.Fatal("signature not bound to the media")
	}
	s.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if s.Verify("inst_1", "med_1", q.Get("exp"), q.Get("sig")) {
		t.Fatal("expired URL accepted")
	}
	if MaxSize("image") != 16<<20 || MaxSize("document") != 100<<20 || MaxSize("audio") != 64<<20 {
		t.Fatal("limits")
	}
}

// Concurrent first starts agree on one key, and no temporary file is left behind.
func TestKeyConcurrentCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media_key")
	const n = 16
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], errs[i] = LoadOrCreateKey(path)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || string(keys[i]) != string(keys[0]) {
			t.Fatalf("call %d: key %x, %v (first %x)", i, keys[i], errs[i], keys[0])
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files left: %v", entries)
	}
}

// Without hard links (FAT, some SMB/FUSE mounts) the key is created with O_EXCL instead.
func TestKeyWithoutHardLinks(t *testing.T) {
	orig := linkFile
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errors.ErrUnsupported}
	}
	t.Cleanup(func() { linkFile = orig })
	dir := t.TempDir()
	path := filepath.Join(dir, "media_key")
	key, err := LoadOrCreateKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key = %x, %v", key, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", fi, err)
	}
	if again, err := LoadOrCreateKey(path); err != nil || string(again) != string(key) {
		t.Fatalf("key not reused: %x, %v", again, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("files left: %v", entries)
	}
}

// The MAC input cannot be shifted between instance and media ids.
func TestSignatureIsUnambiguous(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://127.0.0.1:8787")
	raw, _ := s.URL("a/b", "c")
	u, _ := url.Parse(raw)
	q := u.Query()
	if !s.Verify("a/b", "c", q.Get("exp"), q.Get("sig")) {
		t.Fatal("valid signature rejected")
	}
	if s.Verify("a", "b/c", q.Get("exp"), q.Get("sig")) {
		t.Fatal("signature of a/b + c accepted for a + b/c")
	}
}
```

Run: `go test ./internal/media/`
Expected: FAIL — `undefined: publicAddr`, `undefined: Fetcher`, `undefined: ResolveMime`.

- [ ] **Step 2: Implementar**

Criar `internal/media/media.go`:

```go
// Package media fetches attachments for sending (media.url with SSRF protection, or
// base64) and signs temporary download URLs for received media (spec 4.1 and 4.5).
package media

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrTooLarge = errors.New("media exceeds the size limit")
	// ErrFetch means media.url could not be downloaded (network, HTTP status, bad URL).
	ErrFetch   = errors.New("media could not be fetched")
	ErrBlocked = errors.New("media URL points to a private or local address")
)

// MaxSize is the send limit per message type (spec 4.5).
func MaxSize(kind string) int64 {
	switch kind {
	case "image":
		return 16 << 20
	case "video", "audio":
		return 64 << 20
	default:
		return 100 << 20
	}
}

// Fetcher downloads media.url attachments.
type Fetcher struct {
	// AllowPrivate disables the address check (local installs).
	AllowPrivate bool
	Timeout      time.Duration // default 30s
	// LookupIP resolves host names (default: net.DefaultResolver). Tests replace it.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// allow decides whether host may be reached at ip (default: publicAddr). Tests replace it.
	allow      func(host string, ip netip.Addr) bool
	clientOnce sync.Once
	client     *http.Client
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64: can map to private IPv4
	netip.MustParsePrefix("2002::/16"),     // 6to4: embeds any IPv4
}

// publicAddr reports whether ip is a public unicast address a user-supplied URL may reach.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// dial resolves the host itself and checks every address before connecting, for every
// connection (redirects included): a name cannot resolve to a blocked address (DNS
// rebinding), and a public URL cannot redirect to an internal one.
func (f *Fetcher) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		lookup := f.LookupIP
		if lookup == nil {
			lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			}
		}
		if ips, err = lookup(ctx, host); err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	allow := f.allow
	if allow == nil {
		allow = func(_ string, ip netip.Addr) bool { return f.AllowPrivate || publicAddr(ip) }
	}
	for _, ip := range ips {
		if !allow(host, ip) {
			return nil, ErrBlocked
		}
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, netip.AddrPortFrom(ips[0], uint16(p)).String())
}

func (f *Fetcher) httpClient() *http.Client {
	f.clientOnce.Do(f.initClient)
	return f.client
}

func (f *Fetcher) initClient() {
	f.client = &http.Client{
		Transport: &http.Transport{
			Proxy:               nil, // never via an environment proxy: dial must see the real target
			DialContext:         f.dial,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http URL")
			}
			return nil
		},
	}
}

// Fetch downloads rawURL, refusing bodies larger than max. It returns the data and the
// content type (from the response, or sniffed).
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, max int64) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("%w: media.url must be an http(s) URL", ErrFetch)
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrFetch, err)
	}
	resp, err := f.httpClient().Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return nil, "", ErrBlocked
		}
		return nil, "", fmt.Errorf("%w: %v", ErrFetch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("%w: HTTP %d", ErrFetch, resp.StatusCode)
	}
	if resp.ContentLength > max {
		return nil, "", ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrFetch, err)
	}
	if int64(len(data)) > max {
		return nil, "", ErrTooLarge
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func baseType(mime string) string {
	m, _, _ := strings.Cut(mime, ";")
	return strings.ToLower(strings.TrimSpace(m))
}

func fits(kind, mime string) bool {
	switch kind {
	case "image":
		return strings.HasPrefix(mime, "image/") && mime != "image/svg+xml"
	case "video":
		return strings.HasPrefix(mime, "video/")
	case "audio":
		return strings.HasPrefix(mime, "audio/")
	}
	return mime != ""
}

// notMedia reports sniffed types that are certainly not audio or video.
func notMedia(sniffed string) bool {
	switch {
	case strings.HasPrefix(sniffed, "text/"), strings.HasPrefix(sniffed, "application/x-"):
		return true // text, HTML, XML/SVG, scripts, archives, executables
	}
	switch sniffed {
	case "application/pdf", "application/zip", "image/svg+xml":
		return true
	}
	return false
}

// ResolveMime picks the attachment's MIME type. The bytes are always checked first: an
// image must sniff as an image (never SVG) and takes the sniffed type, and audio/video must
// not sniff as text, documents, archives or executables, whatever type was declared. Other
// types use the one the client declared if it fits the message type, else the server's
// Content-Type if it fits (one that contradicts the message type is ignored), else what
// the bytes look like.
func ResolveMime(kind, declared, remote string, data []byte) (string, error) {
	sniffed := baseType(http.DetectContentType(data))
	if kind == "audio" && sniffed == "application/ogg" {
		sniffed = "audio/ogg"
	}
	bad := fmt.Errorf("media content is not a valid %s", kind)
	switch kind {
	case "image":
		if !fits(kind, sniffed) {
			return "", bad
		}
		return sniffed, nil
	case "video", "audio":
		if notMedia(sniffed) {
			return "", bad
		}
	}
	for _, m := range []string{declared, remote} {
		if b := baseType(m); b != "" && b != "application/octet-stream" && fits(kind, b) {
			return b, nil
		}
	}
	if fits(kind, sniffed) {
		return sniffed, nil
	}
	if kind == "document" {
		return "application/octet-stream", nil
	}
	return "", bad
}

// DecodeBase64 decodes media.base64 (plain or as a data: URL, whose type is returned),
// refusing more than max bytes.
func DecodeBase64(s string, max int64) (data []byte, mime string, err error) {
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		meta, payload, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(meta, ";base64") {
			return nil, "", errors.New("media.base64: malformed data URL")
		}
		mime, s = strings.TrimSuffix(meta, ";base64"), payload
	}
	if int64(base64.StdEncoding.DecodedLen(len(s))) > max+2 {
		return nil, "", ErrTooLarge
	}
	data, err = base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, "", errors.New("media.base64 is not valid base64")
	}
	if int64(len(data)) > max {
		return nil, "", ErrTooLarge
	}
	return data, mime, nil
}

// Signer makes temporary URLs to received media that work without a token (spec 4.5).
type Signer struct {
	Key     []byte
	BaseURL string // e.g. http://127.0.0.1:8787 or the public_url
	TTL     time.Duration
	Now     func() time.Time
}

func NewSigner(key []byte, baseURL string) *Signer {
	return &Signer{Key: key, BaseURL: strings.TrimRight(baseURL, "/"), TTL: time.Hour, Now: time.Now}
}

// LoadOrCreateKey reads the signing secret at path (hex), creating a random one (0600)
// the first time. Deleting the file invalidates every signed URL.
func LoadOrCreateKey(path string) ([]byte, error) {
	if key, err := readKey(path); !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// Write a temporary file (CreateTemp makes it 0600) and publish it with a hard link:
	// the link is atomic, so nobody reads a half-written key, and it fails if the key
	// already exists, so concurrent first starts all end up with the same key.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".media_key-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(hex.EncodeToString(key) + "\n")
	if err := tmp.Sync(); werr == nil {
		werr = err
	}
	if err := tmp.Close(); werr == nil {
		werr = err
	}
	if werr != nil {
		return nil, werr
	}
	err = linkFile(tmp.Name(), path)
	if err == nil {
		return key, nil
	}
	if errors.Is(err, os.ErrExist) {
		return readKey(path) // another process created it first
	}
	// No hard links here (FAT/exFAT, some SMB/FUSE mounts): create the file exclusively
	// instead. Only one creator wins; it is just not atomic for a concurrent reader.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readKey(path)
	}
	if err != nil {
		return nil, err
	}
	_, werr = f.WriteString(hex.EncodeToString(key) + "\n")
	if err := f.Sync(); werr == nil {
		werr = err
	}
	if err := f.Close(); werr == nil {
		werr = err
	}
	if werr != nil {
		return nil, werr
	}
	return key, nil
}

// linkFile publishes the key file; tests replace it to simulate a filesystem without
// hard links.
var linkFile = os.Link

func readKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) < 32 {
		return nil, fmt.Errorf("media key %s is corrupt", path)
	}
	return key, nil
}

// mac length-prefixes each part, so ids containing separators cannot shift between parts.
func (s *Signer) mac(instanceID, mediaID string, exp int64) string {
	h := hmac.New(sha256.New, s.Key)
	fmt.Fprintf(h, "%d:%s|%d:%s|%d", len(instanceID), instanceID, len(mediaID), mediaID, exp)
	return hex.EncodeToString(h.Sum(nil))
}

// URL returns the signed URL of a media item and when it expires.
func (s *Signer) URL(instanceID, mediaID string) (string, time.Time) {
	exp := s.Now().Add(s.TTL).Truncate(time.Second)
	q := url.Values{"exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {s.mac(instanceID, mediaID, exp.Unix())}}
	return fmt.Sprintf("%s/media/%s/%s?%s", s.BaseURL, url.PathEscape(instanceID), url.PathEscape(mediaID), q.Encode()), exp
}

// Verify checks a signed URL's exp and sig.
func (s *Signer) Verify(instanceID, mediaID, exp, sig string) bool {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || s.Now().Unix() > e {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.mac(instanceID, mediaID, e)))
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race ./internal/media/`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/media
git commit -m "feat(media): download de media.url com bloqueio SSRF, MIME confiável, base64 e URLs assinadas" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 14: Service — envio e settings

### Task 16: Service: envio, consulta de mensagem e settings

**Files:**
- Create: `internal/service/messages.go`
- Test: `internal/service/messages_test.go`
- Modify: `internal/service/instances.go`
- Test: `internal/service/instances_test.go`
- Modify: `internal/queue/queue.go` (`Forget`, `Dispatchers`)
- Test: `internal/queue/queue_test.go`
- Modify: `internal/media/media.go` (`ErrInvalidURL`)
- Test: `internal/media/media_test.go`

Corpo do envio (`SendInput`): `to` (número ou JID), `type` (`text` | `image` | `video` | `audio` | `document` | `reaction`), `text` (texto), `caption` (mídia), `media{url | base64, mime?, file_name?, ptt?}`, `reaction{message_id, emoji}` (id do LiteZap ou `wa_message_id` de uma mensagem do chat), `client_message_id?` (até 128; reuso com outro corpo → `409 conflict`), `expires_after?` (padrão `24h`, de `1m` a `168h`). Instância `banned` ou `logged_out` recusa na hora; em outros estados o item espera na fila. No máximo 3 downloads/decodificações de mídia ao mesmo tempo. Limites: `text` e `caption` até 64KB, `reaction.emoji` até 64 bytes, `media.file_name` até 255 bytes (`400 validation_failed`). `media.url` que não é URL http(s) → `400 validation_failed`; falha ao baixar `media.url` → `422 media_fetch_failed` (retentável); `media_unavailable` (410) fica para mídia recebida que expirou no WhatsApp. Dois envios simultâneos com o mesmo `client_message_id` e corpos diferentes: o que perde a corrida no `Enqueue` também recebe `409 conflict`. `UpdateSettings` é serializado por instância (patches simultâneos não perdem campos).

`mapErr` passa a traduzir os códigos dos RPCs de mensagens: `InvalidArgument` → 400 (`invalid_number` ou `validation_failed`), `NotFound` com `media_unavailable` → 410, `FailedPrecondition` → 409 `instance_not_connected`; mensagens continuam com telefones e tokens redigidos. O `Delete` da instância também apaga `data/media/<id>` e `data/outbox/<id>` e, depois de remover a linha do store, chama `Queues.Forget(id)`: o despachante acorda, não acha a instância e termina; esperas (`?wait=true`) nos itens dela retornam.

- [ ] **Step 1: Escrever os testes**

Criar `internal/service/messages_test.go`:

```go
package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type msgEnv struct {
	t   *testing.T
	st  *store.Store
	sp  *workertest.Spawner
	svc *Messaging
	dir string
}

func fast() settings.Settings {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.TypingSimulation, s.Ramp.Enabled = 0, 0, false, false
	s.SendWindow, s.UnsafePacing = settings.Window{}, true // tests run at any hour, back to back
	return s
}

// newMsgEnv runs instance "i1" connected on a fake worker, with the real queue and ingestor.
func newMsgEnv(t *testing.T) *msgEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.DiscardHandler)
	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	q := queue.New(st, mgr, queue.Options{Defaults: fast(), Log: log, Resolver: resolver})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log}, q})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
	})
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	e := &msgEnv{t: t, st: st, sp: sp, dir: dir, svc: &Messaging{
		Store: st, Manager: mgr, Queues: q, Fetcher: &media.Fetcher{AllowPrivate: true},
		Signer:  media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://127.0.0.1:8787"),
		DataDir: dir, Defaults: fast(), Resolver: resolver, SignedURLs: true,
	}}
	e.eventually("connected", func() bool {
		in, _ := st.GetInstance(context.Background(), "i1")
		return in.State == instance.Connected
	})
	return e
}

func (e *msgEnv) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func apiCode(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestChatJID(t *testing.T) {
	ok := map[string]string{
		"+55 (11) 98888-7777":             "5511988887777@s.whatsapp.net",
		"5511988887777@s.whatsapp.net":    "5511988887777@s.whatsapp.net",
		"5511988887777:12@s.whatsapp.net": "5511988887777@s.whatsapp.net",
		"120363000000000000@g.us":         "120363000000000000@g.us",
	}
	for in, want := range ok {
		if got, err := ChatJID(in); err != nil || got != want {
			t.Errorf("ChatJID(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"123", "x@example.com", "@g.us", ""} {
		if _, err := ChatJID(in); err == nil {
			t.Errorf("ChatJID(%q) accepted", in)
		}
	}
}

func TestSendTextWaitAndIdempotency(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	in := SendInput{To: "5511988887777", Type: "text", Text: "oi", ClientMessageID: "pedido-1"}
	v, err := e.svc.Send(ctx, "i1", in, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "sent" || !strings.HasPrefix(v.WAMessageID, "3EB0") || v.Chat != "5511988887777@s.whatsapp.net" {
		t.Fatalf("view = %+v", v)
	}
	again, err := e.svc.Send(ctx, "i1", in, 0)
	if err != nil || again.ID != v.ID || len(e.sp.Last("i1").WA.Sent()) != 1 {
		t.Fatalf("repeat = %+v, %v (sent %d)", again, err, len(e.sp.Last("i1").WA.Sent()))
	}
	other := in
	other.Text = "outra coisa"
	if _, err := e.svc.Send(ctx, "i1", other, 0); apiCode(err) != "conflict" {
		t.Fatalf("client id reused with another body = %v", err)
	}
	queued, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "text", Text: "depois"}, 0)
	if err != nil || queued.Status != "queued" {
		t.Fatalf("enqueue only = %+v, %v", queued, err)
	}
	if done, err := e.svc.Wait(ctx, "i1", queued.ID, 2*time.Second); err != nil || done.Status != "sent" {
		t.Fatalf("wait = %+v, %v", done, err)
	}
	got, err := e.svc.GetMessage(ctx, "i1", v.ID)
	if err != nil || got.Status != "sent" || got.ClientMessageID != "pedido-1" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// Receipts raise the status shown for the sent item.
	e.sp.Last("i1").EmitReceipt(&workerv1.Receipt{MessageIds: []string{v.WAMessageID}, Type: "read"})
	e.eventually("read status", func() bool {
		got, _ := e.svc.GetMessage(ctx, "i1", v.ID)
		return got.Status == "read"
	})
	if _, err := e.svc.GetMessage(ctx, "i1", "msg_nope"); apiCode(err) != "not_found" {
		t.Fatalf("unknown message = %v", err)
	}
}

func TestSendValidationAndFailures(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	pngB64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	sent, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "text", Text: "alvo"}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	bad := []SendInput{
		{To: "123", Type: "text", Text: "x"},
		{To: "5511988887777", Type: "text"},
		{To: "5511988887777", Type: "sticker"},
		{To: "5511988887777", Type: "image"},
		{To: "5511988887777", Type: "image", Media: &MediaInput{URL: "http://x", Base64: "eA=="}},
		{To: "5511988887777", Type: "reaction", Reaction: &ReactionInput{MessageID: "nope", Emoji: "👍"}},
		{To: "5511988887777", Type: "text", Text: "x", ExpiresAfter: "10s"},
		{To: "5511988887777", Type: "image", Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString([]byte("not an image"))}},
		{To: "5511988887777", Type: "image", Media: &MediaInput{URL: "ftp://example.com/x.png"}},
		{To: "5511988887777", Type: "image", Caption: strings.Repeat("x", maxTextBytes+1), Media: &MediaInput{Base64: pngB64}},
		{To: "5511988887777", Type: "document", Media: &MediaInput{Base64: pngB64, FileName: strings.Repeat("a", 256)}},
		{To: "5511988887777", Type: "reaction", Reaction: &ReactionInput{MessageID: sent.ID, Emoji: strings.Repeat("x", 65)}},
	}
	for i, in := range bad {
		if _, err := e.svc.Send(ctx, "i1", in, 0); apiCode(err) != "validation_failed" {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, media.MaxSize("image")+1))
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "image", Media: &MediaInput{Base64: big}}, 0); apiCode(err) != "media_too_large" {
		t.Fatalf("big image = %v", err)
	}
	e.sp.Last("i1").WA.SetUnregistered("5511900000000")
	_, err = e.svc.Send(ctx, "i1", SendInput{To: "5511900000000", Type: "text", Text: "oi"}, 2*time.Second)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "invalid_number" || ae.Status != http.StatusBadRequest {
		t.Fatalf("failed send = %v", err)
	}
	if n := len(e.sp.Last("i1").WA.Sent()); n != 1 {
		t.Fatalf("a number without WhatsApp was sent to (%d sends, want only the reaction target)", n)
	}
	e.sp.Last("i1").WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "forbidden: no permission to send in this group")
	})
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "120363000000000001@g.us", Type: "text", Text: "oi"}, 2*time.Second); apiCode(err) != "forbidden" {
		t.Fatalf("group without permission = %v", err)
	}
	if err := e.st.UpdateInstanceState(ctx, "i1", instance.LoggedOut, "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511900000000", Type: "text", Text: "oi"}, 0); apiCode(err) != "instance_not_connected" {
		t.Fatalf("logged out = %v", err)
	}
}

func TestSendMediaAndReaction(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	png := []byte("\x89PNG\r\n\x1a\nimage")
	v, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "image", Caption: "foto",
		Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString(png)}}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Media == nil || v.Media.Mime != "image/png" {
		t.Fatalf("image = %+v, %v", v, err)
	}
	req := e.sp.Last("i1").WA.Sent()[0]
	if string(e.sp.Last("i1").WA.SentMedia(req.MessageId)) != string(png) || req.Text != "foto" || req.Type != "image" {
		t.Fatalf("request = %+v", req)
	}
	if left, _ := os.ReadDir(filepath.Join(e.dir, "outbox", "i1")); len(left) != 0 {
		t.Fatalf("outbox not cleaned: %v", left)
	}
	r, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "reaction",
		Reaction: &ReactionInput{MessageID: v.ID, Emoji: "❤️"}}, 2*time.Second)
	if err != nil || r.Status != "sent" {
		t.Fatalf("reaction = %+v, %v", r, err)
	}
	rr := e.sp.Last("i1").WA.Sent()[1]
	if rr.ReactionMessageId != v.WAMessageID || !rr.ReactionFromMe || rr.Text != "❤️" {
		t.Fatalf("reaction request = %+v", rr)
	}
}

func TestSettingsProxyRestartsWorker(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	got, err := e.svc.Settings(ctx, "i1")
	if err != nil || got.MinIntervalMS != 0 {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	jitter := 500
	got, err = e.svc.UpdateSettings(ctx, "i1", settings.Patch{JitterMS: &jitter})
	if err != nil || got.JitterMS != 500 || e.sp.Starts("i1") != 1 {
		t.Fatalf("update = %+v, %v, starts %d", got, err, e.sp.Starts("i1"))
	}
	proxy := "socks5://bob:secret@10.0.0.1:1080"
	got, err = e.svc.UpdateSettings(ctx, "i1", settings.Patch{ProxyURL: &proxy})
	if err != nil || strings.Contains(got.ProxyURL, "secret") || got.JitterMS != 500 {
		t.Fatalf("proxy update = %+v, %v", got, err)
	}
	e.eventually("worker restarted", func() bool { return e.sp.Starts("i1") == 2 })
	bad := "ftp://x"
	if _, err := e.svc.UpdateSettings(ctx, "i1", settings.Patch{ProxyURL: &bad}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad proxy = %v", err)
	}
}

// Concurrent sends with the same client_message_id but different bodies: all pass the
// idempotency check before any is queued (the media server releases them together), so
// the losers learn about the winner from Enqueue and must get a conflict, never its view.
func TestConcurrentClientIDWithDifferentBodies(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	const n = maxMediaJobs // every request downloads at the same time
	var arrived sync.WaitGroup
	arrived.Add(n)
	release := make(chan struct{})
	go func() { arrived.Wait(); close(release) }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Done()
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\n" + r.URL.Path))
	}))
	defer srv.Close()
	type res struct {
		v   MessageView
		err error
	}
	out := make(chan res, n)
	for i := 0; i < n; i++ {
		go func() {
			v, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "image", ClientMessageID: "dup",
				Media: &MediaInput{URL: fmt.Sprintf("%s/%d.png", srv.URL, i)}}, 0)
			out <- res{v, err}
		}()
	}
	ok := 0
	for i := 0; i < n; i++ {
		r := <-out
		switch {
		case r.err == nil:
			ok++
		case apiCode(r.err) != "conflict":
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d requests got the message, want exactly 1 (the others: conflict)", ok)
	}
}

// Deleting an instance stops its send dispatcher.
func TestDeleteStopsTheDispatcher(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "text", Text: "oi"}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := e.svc.Queues.Dispatchers(); n != 1 {
		t.Fatalf("dispatchers = %d", n)
	}
	inst := &Instances{Store: e.st, Manager: e.svc.Manager, DataDir: e.dir, Queues: e.svc.Queues}
	if err := inst.Delete(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	e.eventually("dispatcher stopped", func() bool { return e.svc.Queues.Dispatchers() == 0 })
}

// Concurrent settings updates of different fields all stick (no lost update).
func TestConcurrentSettingsUpdates(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	for round := 1; round <= 20; round++ {
		minI, jit, maxPM, tz := 1000+round, 100+round, 10+round%50, []string{"UTC", "America/Sao_Paulo"}[round%2]
		typing := round%2 == 0
		patches := []settings.Patch{{MinIntervalMS: &minI}, {JitterMS: &jit}, {MaxPerMinute: &maxPM}, {Timezone: &tz}, {TypingSimulation: &typing}}
		var wg sync.WaitGroup
		for _, p := range patches {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := e.svc.UpdateSettings(ctx, "i1", p); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		got, err := e.svc.Settings(ctx, "i1")
		if err != nil {
			t.Fatal(err)
		}
		if got.MinIntervalMS != minI || got.JitterMS != jit || got.MaxPerMinute != maxPM || got.Timezone != tz || got.TypingSimulation != typing {
			t.Fatalf("round %d: lost update: %+v", round, got)
		}
	}
}
```

Acrescentar ao fim de `internal/service/instances_test.go`:

```go
func TestMapErrMessagingCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{status.Error(codes.InvalidArgument, "invalid_number: 5511987654321 is not on WhatsApp"), http.StatusBadRequest, "invalid_number"},
		{status.Error(codes.InvalidArgument, "validation_failed: invalid JID"), http.StatusBadRequest, "validation_failed"},
		{status.Error(codes.NotFound, "media_unavailable: 404"), http.StatusGone, "media_unavailable"},
		{status.Error(codes.FailedPrecondition, "logged_out: session is not logged in"), http.StatusConflict, "instance_not_connected"},
	}
	for _, c := range cases {
		var e *apierr.Error
		if !errors.As(mapErr(c.err), &e) || e.Status != c.status || e.Code != c.code {
			t.Errorf("%v -> %+v, want %d %s", c.err, e, c.status, c.code)
		}
		if e != nil && strings.Contains(e.Message, "5511987654321") {
			t.Errorf("message leaks a phone number: %q", e.Message)
		}
	}
}
```

Em `internal/queue/queue_test.go` (teste do `Forget`), trocar:

```go
	"context"
	"encoding/json"
	"log/slog"
	"os"
```

por:

```go
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
```

Em `internal/queue/queue_test.go` (teste do `Forget`), trocar:

```go
	e.eventually("sent at 10s", func() bool { return e.status(id).Status == store.OutSent })
}
```

por:

```go
	e.eventually("sent at 10s", func() bool { return e.status(id).Status == store.OutSent })
}

// Forget (instance deleted) ends its dispatcher, releases Wait calls on its items and
// drops its per-instance state.
func TestForgetStopsTheDispatcher(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.ConnectGrace = 20 * time.Second })
	ctx := context.Background()
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.q.StateChanged("i1", instance.Reconnecting) // records readyAt
	e.q.Wake("i1")
	e.eventually("dispatcher running", func() bool { return e.q.Dispatchers() == 1 })
	waited := make(chan error, 1)
	go func() { _, err := e.q.Wait(ctx, "i1", id); waited <- err }()
	e.eventually("waiter registered", func() bool {
		e.q.mu.Lock()
		defer e.q.mu.Unlock()
		return len(e.q.waiters) == 1
	})
	if err := e.st.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	e.q.Forget("i1")
	e.eventually("dispatcher gone", func() bool { return e.q.Dispatchers() == 0 })
	select {
	case err := <-waited:
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("wait = %v, want ErrNotFound", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait still blocked after Forget")
	}
	e.q.mu.Lock()
	defer e.q.mu.Unlock()
	if _, ok := e.q.readyAt["i1"]; ok || len(e.q.waiters) != 0 {
		t.Fatalf("state left: readyAt=%v waiters=%v", e.q.readyAt, e.q.waiters)
	}
}
```

Em `internal/media/media_test.go` (URL inválida não é falha de download), trocar:

```go
		t.Fatalf("missing = %v", err)
	}
	if _, _, err := f.Fetch(context.Background(), "ftp://example.com/x", 100); !errors.Is(err, ErrFetch) {
		t.Fatalf("non-http url = %v", err)
	}
}
```

por:

```go
		t.Fatalf("missing = %v", err)
	}
	for _, bad := range []string{"ftp://example.com/x", "http:///nohost", "::nope"} {
		if _, _, err := f.Fetch(context.Background(), bad, 100); !errors.Is(err, ErrInvalidURL) || errors.Is(err, ErrFetch) {
			t.Fatalf("invalid url %q = %v", bad, err)
		}
	}
}
```

Run: `go test ./internal/service/ ./internal/queue/ ./internal/media/`
Expected: FAIL — `undefined: Messaging`, `undefined: ChatJID`, `e.q.Dispatchers undefined`, `undefined: ErrInvalidURL`; `TestMapErrMessagingCodes` ainda não compila junto.

- [ ] **Step 2: Implementar o envio**

Criar `internal/service/messages.go`:

```go
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
)

// Messaging holds the message use cases: send and settings here; the log, contacts and
// media in inbox.go.
type Messaging struct {
	Store        *store.Store
	Manager      *manager.Manager
	Queues       *queue.Queues
	Fetcher      *media.Fetcher
	Signer       *media.Signer
	DataDir      string
	Defaults     settings.Settings
	DefaultProxy string
	// MediaRetention is how long a downloaded attachment is kept (spec 4.5: 7 days).
	MediaRetention time.Duration
	// Resolver caches and rate-limits phone lookups (check-numbers).
	Resolver *lookup.Resolver
	// SignedURLs enables ?signed=1; off when the API is exposed without a public_url
	// (the URLs would point at an address third parties cannot use).
	SignedURLs bool

	mediaOnce  sync.Once
	mediaSlots chan struct{}

	flightMu sync.Mutex
	flights  map[string]*mediaFlight // first downloads in progress (inbox.go)

	settingsMu    sync.Mutex
	settingsLocks map[string]*sync.Mutex // per instance: read-merge-write of the overrides
}

// settingsLock serializes UpdateSettings per instance so concurrent patches never lose
// each other's fields.
func (s *Messaging) settingsLock(instanceID string) *sync.Mutex {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if s.settingsLocks == nil {
		s.settingsLocks = map[string]*sync.Mutex{}
	}
	mu := s.settingsLocks[instanceID]
	if mu == nil {
		mu = &sync.Mutex{}
		s.settingsLocks[instanceID] = mu
	}
	return mu
}

// mediaFlight is one media download in progress; concurrent callers wait for it.
type mediaFlight struct {
	done chan struct{}
	file MediaFile
	err  error
}

// MediaFile is a received attachment available on disk.
type MediaFile struct {
	Path     string
	Mime     string
	FileName string
}

const (
	maxWait          = 2 * time.Minute
	rpcTimeout       = 30 * time.Second
	maxTextBytes     = 65536
	maxEmojiBytes    = 64
	maxFileNameBytes = 255
	// maxMediaJobs bounds concurrent media downloads/decodes (each holds the file in memory).
	maxMediaJobs   = 3
	defaultExpires = 24 * time.Hour
	maxExpires     = 7 * 24 * time.Hour
)

// acquireMedia waits for one of the maxMediaJobs slots.
func (s *Messaging) acquireMedia(ctx context.Context) (func(), error) {
	s.mediaOnce.Do(func() { s.mediaSlots = make(chan struct{}, maxMediaJobs) })
	select {
	case s.mediaSlots <- struct{}{}:
		return func() { <-s.mediaSlots }, nil
	case <-ctx.Done():
		return nil, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many media sends in progress", true)
	}
}

func notFound(what string) *apierr.Error {
	return apierr.New(http.StatusNotFound, "not_found", what+" not found", false)
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// ChatJID normalizes a recipient: a phone number (10 to 15 digits, separators allowed) or a
// user/group JID. The device part of a JID is dropped.
func ChatJID(to string) (string, error) {
	to = strings.TrimSpace(to)
	if user, server, ok := strings.Cut(to, "@"); ok {
		user, _, _ = strings.Cut(user, ":")
		switch server {
		case "s.whatsapp.net", "g.us", "lid":
		default:
			return "", apierr.Validation("unsupported JID server " + server)
		}
		if user == "" {
			return "", apierr.Validation("JID without user part")
		}
		return user + "@" + server, nil
	}
	d := digitsOnly(to)
	if len(d) < 10 || len(d) > 15 {
		return "", apierr.Validation("phone must have 10 to 15 digits including country code")
	}
	return d + "@s.whatsapp.net", nil
}

// newWAMessageID mimics whatsmeow.GenerateMessageID ("3EB0" + 18 hex): the id is chosen
// before the first attempt and reused on every retry.
func newWAMessageID() string {
	b := make([]byte, 9)
	rand.Read(b)
	return "3EB0" + strings.ToUpper(hex.EncodeToString(b))
}

type MediaInput struct {
	URL      string `json:"url,omitempty"`
	Base64   string `json:"base64,omitempty"`
	Mime     string `json:"mime,omitempty"`
	FileName string `json:"file_name,omitempty"`
	PTT      bool   `json:"ptt,omitempty"` // audio as voice note
}

type ReactionInput struct {
	MessageID string `json:"message_id"` // LiteZap id or WhatsApp id of a message in the chat
	Emoji     string `json:"emoji"`      // "" removes the reaction
}

// SendInput is the body of POST /messages.
type SendInput struct {
	To              string         `json:"to"`
	Type            string         `json:"type"`
	Text            string         `json:"text,omitempty"`    // type text
	Caption         string         `json:"caption,omitempty"` // image, video, document
	Media           *MediaInput    `json:"media,omitempty"`
	Reaction        *ReactionInput `json:"reaction,omitempty"`
	ClientMessageID string         `json:"client_message_id,omitempty"`
	// ExpiresAfter is how long the message may wait in the queue ("90m"; default 24h, max
	// 7 days); not sent by then, it fails with "expired".
	ExpiresAfter string `json:"expires_after,omitempty"`
}

// requestHash fingerprints a send request (without its client id) to detect a
// client_message_id reused for a different message.
func requestHash(in SendInput) string {
	in.ClientMessageID = ""
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type MediaView struct {
	store.MediaInfo
	URL string `json:"url,omitempty"` // API path to download a received attachment
}

// MessageView is a message of the log or an item of the send queue.
type MessageView struct {
	ID              string     `json:"id"`
	ClientMessageID string     `json:"client_message_id,omitempty"`
	WAMessageID     string     `json:"wa_message_id,omitempty"`
	Chat            string     `json:"chat"`
	Sender          string     `json:"sender,omitempty"`
	FromMe          bool       `json:"from_me"`
	Source          string     `json:"source,omitempty"`
	Type            string     `json:"type"`
	Text            string     `json:"text,omitempty"`
	Media           *MediaView `json:"media,omitempty"`
	ReactionTo      string     `json:"reaction_to,omitempty"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts,omitempty"`
	Error           string     `json:"error,omitempty"`
	Timestamp       time.Time  `json:"timestamp"`
}

func messageView(instanceID string, m store.Message) MessageView {
	v := MessageView{
		ID: m.ID, WAMessageID: m.WAMessageID, Chat: m.ChatJID, Sender: m.SenderJID, FromMe: m.FromMe,
		Source: m.Source, Type: m.Type, Text: m.Text, ReactionTo: m.ReactionTo, Status: m.Status, Timestamp: m.Timestamp,
	}
	if m.MediaJSON != "" {
		var info store.MediaInfo
		if json.Unmarshal([]byte(m.MediaJSON), &info) == nil {
			v.Media = &MediaView{MediaInfo: info}
			if info.ID != "" {
				v.Media.URL = "/v1/instances/" + instanceID + "/media/" + info.ID
			}
		}
	}
	return v
}

func outboundView(o store.Outbound, logged *store.Message) MessageView {
	var p queue.Payload
	_ = json.Unmarshal([]byte(o.Payload), &p)
	v := MessageView{
		ID: o.ID, ClientMessageID: o.ClientMessageID, Chat: o.ChatJID, FromMe: true, Source: "api",
		Type: p.Type, Text: p.Text, Status: o.Status, Attempts: o.Attempts, Timestamp: o.CreatedAt,
	}
	if p.Reaction != nil {
		v.ReactionTo = p.Reaction.WAMessageID
	}
	if p.Mime != "" {
		v.Media = &MediaView{MediaInfo: store.MediaInfo{Kind: p.Type, Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT}}
	}
	if o.Status != store.OutSent {
		v.Error = o.LastError
	}
	if o.Status == store.OutSent {
		v.WAMessageID, v.Timestamp = o.WAMessageID, o.SentAt
		if logged != nil {
			v.Status = logged.Status // delivered / read from receipts
		}
	}
	return v
}

// failedErr turns the last_error of a failed item ("code: message") into an API error.
func failedErr(lastErr string) error {
	code, msg, _ := strings.Cut(lastErr, ": ")
	switch code {
	case "invalid_number":
		return apierr.New(http.StatusBadRequest, "invalid_number", msg, false)
	case "validation_failed":
		return apierr.Validation(msg)
	case "forbidden":
		return apierr.New(http.StatusForbidden, "forbidden", msg, false)
	case "logged_out":
		return apierr.New(http.StatusConflict, "instance_not_connected", "instance is logged out", false)
	case "worker_timeout":
		return apierr.New(http.StatusGatewayTimeout, "worker_timeout", msg, true)
	case "expired":
		return apierr.New(http.StatusGone, "message_expired", "message not sent before expires_after", false)
	}
	return apierr.New(http.StatusBadGateway, "worker_error", "send failed: "+redact(lastErr), true)
}

// Send validates and enqueues a message (spec 4.1). With wait > 0 it waits for the
// outcome: the returned view says sent, queued/in_flight (timeout: still queued) or the
// error says why it failed.
func (s *Messaging) Send(ctx context.Context, instanceID string, in SendInput, wait time.Duration) (MessageView, error) {
	inst, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return MessageView{}, mapErr(err)
	}
	switch inst.State {
	case instance.Banned:
		return MessageView{}, apierr.New(http.StatusConflict, "instance_banned", "instance is banned; delete it", false)
	case instance.LoggedOut:
		return MessageView{}, apierr.New(http.StatusConflict, "instance_not_connected", "instance is logged out; pair it again", false)
	}
	if len(in.ClientMessageID) > 128 {
		return MessageView{}, apierr.Validation("client_message_id is limited to 128 characters")
	}
	hash := requestHash(in)
	if in.ClientMessageID != "" { // idempotency before any download
		if o, err := s.Store.OutboundByClientID(ctx, instanceID, in.ClientMessageID); err == nil {
			if o.RequestHash != hash {
				return MessageView{}, apierr.New(http.StatusConflict, "conflict",
					"client_message_id was already used for a different message", false)
			}
			return s.finish(ctx, instanceID, o, wait)
		} else if !errors.Is(err, store.ErrNotFound) {
			return MessageView{}, err
		}
	}
	expires := defaultExpires
	if in.ExpiresAfter != "" {
		d, err := time.ParseDuration(in.ExpiresAfter)
		if err != nil || d < time.Minute || d > maxExpires {
			return MessageView{}, apierr.Validation("expires_after must be a duration between 1m and 168h")
		}
		expires = d
	}
	chat, err := ChatJID(in.To)
	if err != nil {
		return MessageView{}, err
	}
	id := secure.NewID("msg_")
	p, err := s.payload(ctx, instanceID, id, chat, in)
	if err != nil {
		return MessageView{}, err
	}
	b, _ := json.Marshal(p)
	o, created, err := s.Store.Enqueue(ctx, store.Outbound{
		ID: id, InstanceID: instanceID, ClientMessageID: in.ClientMessageID, ChatJID: chat,
		WAMessageID: newWAMessageID(), Payload: string(b), RequestHash: hash,
		ExpiresAt: time.Now().Add(expires),
	})
	if err != nil || !created { // !created: a concurrent request with the same client id won
		if p.MediaFile != "" {
			_ = os.Remove(p.MediaFile)
		}
		if err != nil {
			return MessageView{}, err
		}
		if o.RequestHash != hash {
			return MessageView{}, apierr.New(http.StatusConflict, "conflict",
				"client_message_id was already used for a different message", false)
		}
	}
	s.Queues.Wake(instanceID)
	return s.finish(ctx, instanceID, o, wait)
}

// Wait waits up to wait for a queued message to be sent or to fail (the second half of
// ?wait=true, after the request body has been released).
func (s *Messaging) Wait(ctx context.Context, instanceID, id string, wait time.Duration) (MessageView, error) {
	o, err := s.Store.GetOutbound(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("message")
	}
	if err != nil {
		return MessageView{}, err
	}
	return s.finish(ctx, instanceID, o, wait)
}

func (s *Messaging) finish(ctx context.Context, instanceID string, o store.Outbound, wait time.Duration) (MessageView, error) {
	if wait > 0 {
		wctx, cancel := context.WithTimeout(ctx, min(wait, maxWait))
		var err error
		o, err = s.Queues.Wait(wctx, instanceID, o.ID)
		cancel()
		if err != nil {
			return MessageView{}, mapErr(err) // ErrNotFound: the instance was deleted meanwhile
		}
	}
	if o.Status == store.OutFailed {
		return MessageView{}, failedErr(o.LastError)
	}
	var logged *store.Message
	if o.Status == store.OutSent {
		if m, err := s.Store.GetMessage(ctx, instanceID, o.ID); err == nil {
			logged = &m
		}
	}
	return outboundView(o, logged), nil
}

// payload validates the input and stages its attachment under data/outbox.
func (s *Messaging) payload(ctx context.Context, instanceID, id, chat string, in SendInput) (queue.Payload, error) {
	p := queue.Payload{Type: in.Type}
	switch in.Type {
	case "text":
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > maxTextBytes {
			return p, apierr.Validation("text is required (up to 64KB)")
		}
		p.Text = in.Text
		return p, nil
	case "reaction":
		if in.Reaction == nil || in.Reaction.MessageID == "" {
			return p, apierr.Validation("reaction.message_id is required")
		}
		if len(in.Reaction.Emoji) > maxEmojiBytes {
			return p, apierr.Validation("reaction.emoji is limited to 64 bytes")
		}
		target, err := s.Store.GetMessage(ctx, instanceID, in.Reaction.MessageID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && target.ChatJID != chat) {
			target, err = s.Store.MessageByWAID(ctx, instanceID, chat, in.Reaction.MessageID)
		}
		if errors.Is(err, store.ErrNotFound) {
			return p, apierr.Validation("reaction.message_id is not a message of this chat")
		}
		if err != nil {
			return p, err
		}
		p.Text = in.Reaction.Emoji
		p.Reaction = &queue.ReactionTarget{WAMessageID: target.WAMessageID, FromMe: target.FromMe}
		if strings.HasSuffix(chat, "@g.us") {
			p.Reaction.Sender = target.SenderJID
		}
		return p, nil
	case "image", "video", "audio", "document":
	default:
		return p, apierr.Validation("type must be text, image, video, audio, document or reaction")
	}
	m := in.Media
	if m == nil || (m.URL == "") == (m.Base64 == "") {
		return p, apierr.Validation("media needs exactly one of url or base64")
	}
	if len(in.Caption) > maxTextBytes {
		return p, apierr.Validation("caption is limited to 64KB")
	}
	if len(m.FileName) > maxFileNameBytes {
		return p, apierr.Validation("media.file_name is limited to 255 bytes")
	}
	release, err := s.acquireMedia(ctx)
	if err != nil {
		return p, err
	}
	defer release()
	limit := media.MaxSize(in.Type)
	var data []byte
	var remote string
	if m.URL != "" {
		data, remote, err = s.Fetcher.Fetch(ctx, m.URL, limit)
	} else {
		data, remote, err = media.DecodeBase64(m.Base64, limit)
	}
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return p, apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", in.Type+" exceeds the size limit", false)
	case errors.Is(err, media.ErrBlocked):
		return p, apierr.Validation("media.url points to a private or local address")
	case errors.Is(err, media.ErrInvalidURL):
		return p, apierr.Validation(err.Error())
	case errors.Is(err, media.ErrFetch):
		return p, apierr.New(http.StatusUnprocessableEntity, "media_fetch_failed", "media.url could not be downloaded", true)
	case err != nil:
		return p, apierr.Validation(err.Error())
	}
	mime, err := media.ResolveMime(in.Type, m.Mime, remote, data)
	if err != nil {
		return p, apierr.Validation(err.Error())
	}
	name := m.FileName
	if name == "" && in.Type == "document" && m.URL != "" {
		if u, err := url.Parse(m.URL); err == nil {
			if name = path.Base(u.Path); len(name) > maxFileNameBytes {
				name = "" // a name taken from the URL never exceeds the limit either
			}
		}
	}
	dir := filepath.Join(s.DataDir, "outbox", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return p, err
	}
	file := filepath.Join(dir, id)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return p, err
	}
	p.Text, p.MediaFile, p.Mime, p.FileName, p.Size, p.PTT = in.Caption, file, mime, name, int64(len(data)), m.PTT
	return p, nil
}

// GetMessage returns a queued/sent item (by the id returned on send) or a logged message.
func (s *Messaging) GetMessage(ctx context.Context, instanceID, id string) (MessageView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessageView{}, mapErr(err)
	}
	o, err := s.Store.GetOutbound(ctx, instanceID, id)
	if err == nil {
		var logged *store.Message
		if m, err := s.Store.GetMessage(ctx, instanceID, id); err == nil {
			logged = &m
		}
		return outboundView(o, logged), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return MessageView{}, err
	}
	m, err := s.Store.GetMessage(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("message")
	}
	if err != nil {
		return MessageView{}, err
	}
	return messageView(instanceID, m), nil
}

func (s *Messaging) effective(in store.Instance) (settings.Settings, error) {
	return settings.Effective(s.Defaults, s.DefaultProxy, in.SettingsJSON)
}

func redacted(set settings.Settings) settings.Settings {
	set.ProxyURL = settings.RedactProxy(set.ProxyURL)
	return set
}

// Settings returns the instance's effective settings (proxy password hidden).
func (s *Messaging) Settings(ctx context.Context, instanceID string) (settings.Settings, error) {
	in, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return settings.Settings{}, mapErr(err)
	}
	set, err := s.effective(in)
	return redacted(set), err
}

// UpdateSettings merges patch into the instance's overrides. Changing the effective proxy
// restarts a running worker (the proxy is fixed for the whole session, spec 4.6).
func (s *Messaging) UpdateSettings(ctx context.Context, instanceID string, patch settings.Patch) (settings.Settings, error) {
	mu := s.settingsLock(instanceID)
	mu.Lock()
	defer mu.Unlock()
	in, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return settings.Settings{}, mapErr(err)
	}
	before, err := s.effective(in)
	if err != nil {
		return settings.Settings{}, err
	}
	stored, err := settings.ParsePatch(in.SettingsJSON)
	if err != nil {
		return settings.Settings{}, err
	}
	merged := stored.Merge(patch)
	b, _ := json.Marshal(merged)
	after, err := settings.Effective(s.Defaults, s.DefaultProxy, string(b))
	if err != nil {
		return settings.Settings{}, err
	}
	if err := after.Validate(); err != nil {
		return settings.Settings{}, apierr.Validation(err.Error())
	}
	if err := s.Store.SetInstanceSettings(ctx, instanceID, string(b)); err != nil {
		return settings.Settings{}, mapErr(err)
	}
	if after.ProxyURL != before.ProxyURL && s.Manager.Running(instanceID) {
		if err := s.Manager.Restart(context.WithoutCancel(ctx), instanceID); err != nil {
			return settings.Settings{}, mapErr(err)
		}
	}
	s.Queues.Wake(instanceID)
	return redacted(after), nil
}
```

- [ ] **Step 3: Estender `mapErr` e o `Delete`**

Em `internal/service/instances.go` (`mapErr`), trocar:

```go
	// Errors from worker RPCs carry a gRPC status (possibly wrapped).
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		switch st.Code() {
		case codes.Unavailable, codes.Canceled:
			return apierr.NotConnected()
		case codes.DeadlineExceeded:
			return apierr.New(http.StatusGatewayTimeout, "worker_timeout", "instance worker did not answer in time", true)
		}
```

por:

```go
	// Errors from worker RPCs carry a gRPC status (possibly wrapped). Messaging RPCs prefix
	// the message with a stable code ("invalid_number: ...", see worker.proto).
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		code, detail, _ := strings.Cut(st.Message(), ": ")
		switch st.Code() {
		case codes.Unavailable, codes.Canceled:
			return apierr.NotConnected()
		case codes.DeadlineExceeded:
			return apierr.New(http.StatusGatewayTimeout, "worker_timeout", "instance worker did not answer in time", true)
		case codes.InvalidArgument:
			if code == "invalid_number" {
				return apierr.New(http.StatusBadRequest, "invalid_number", redact(detail), false)
			}
			return apierr.Validation(redact(st.Message()))
		case codes.NotFound:
			if code == "media_unavailable" {
				return apierr.New(http.StatusGone, "media_unavailable", "media is no longer available on WhatsApp", false)
			}
		case codes.FailedPrecondition:
			return apierr.New(http.StatusConflict, "instance_not_connected", "instance is not logged in", false)
		}
```

Em `internal/service/instances.go` (`Delete`), trocar:

```go
	if err := os.RemoveAll(filepath.Join(s.DataDir, "instances", id)); err != nil {
		s.Manager.Unretire(id)
		return err
	}
```

por:

```go
	for _, dir := range []string{"instances", "media", "outbox"} {
		if err := os.RemoveAll(filepath.Join(s.DataDir, dir, id)); err != nil {
			s.Manager.Unretire(id)
			return err
		}
	}
```

O `Delete` também encerra o despachante da instância (campo `Queues`, opcional):

Em `internal/service/instances.go` (`Queues` e `Forget` no `Delete`), trocar:

```go
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
```

Em `internal/service/instances.go` (`Queues` e `Forget` no `Delete`), trocar:

```go
	Manager *manager.Manager
	DataDir string
}

```

por:

```go
	Manager *manager.Manager
	DataDir string
	// Queues, when set, has a deleted instance's send dispatcher stopped.
	Queues *queue.Queues
}

```

Em `internal/service/instances.go` (`Queues` e `Forget` no `Delete`), trocar:

```go
		return mapErr(err)
	}
	return nil
}
```

por:

```go
		return mapErr(err)
	}
	if s.Queues != nil {
		s.Queues.Forget(id) // after the row is gone, so the dispatcher sees it and exits
	}
	return nil
}
```

- [ ] **Step 3b: `Queues.Forget` e `ErrInvalidURL`**

As esperas passam a ser indexadas por instância + item, para o `Forget` liberar as de uma instância apagada.

Em `internal/queue/queue.go` (`Forget`, `Dispatchers` e esperas por instância), trocar:

```go
	closed  bool
	disp    map[string]*dispatcher
	waiters map[string][]chan struct{}
	readyAt map[string]time.Time // end of the grace period after connecting
}

```

por:

```go
	closed  bool
	disp    map[string]*dispatcher
	waiters map[string][]chan struct{} // by waiterKey(instance, item)
	readyAt map[string]time.Time       // end of the grace period after connecting
}

```

Em `internal/queue/queue.go` (`Forget`, `Dispatchers` e esperas por instância), trocar:

```go
}

// Shutdown stops the dispatchers; a send in progress is allowed to finish until ctx ends.
func (q *Queues) Shutdown(ctx context.Context) {
```

por:

```go
}

// Forget drops a deleted instance: its dispatcher wakes, finds the instance gone and
// exits; Wait calls on its items return (the items went with the instance).
func (q *Queues) Forget(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.readyAt, id)
	if d := q.disp[id]; d != nil {
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
	prefix := id + "/"
	for k, chans := range q.waiters {
		if strings.HasPrefix(k, prefix) {
			for _, ch := range chans {
				close(ch)
			}
			delete(q.waiters, k)
		}
	}
}

// Dispatchers is the number of running dispatchers (for tests and diagnostics).
func (q *Queues) Dispatchers() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.disp)
}

func waiterKey(instanceID, id string) string { return instanceID + "/" + id }

// Shutdown stops the dispatchers; a send in progress is allowed to finish until ctx ends.
func (q *Queues) Shutdown(ctx context.Context) {
```

Em `internal/queue/queue.go` (`Forget`, `Dispatchers` e esperas por instância), trocar:

```go
// (still queued or in_flight when ctx ended first).
func (q *Queues) Wait(ctx context.Context, instanceID, id string) (store.Outbound, error) {
	for {
		ch := make(chan struct{})
		q.mu.Lock()
		q.waiters[id] = append(q.waiters[id], ch)
		q.mu.Unlock()
		o, err := q.store.GetOutbound(ctx, instanceID, id)
		if err != nil || final(o.Status) {
			q.dropWaiter(id, ch)
			return o, err
		}
```

por:

```go
// (still queued or in_flight when ctx ended first).
func (q *Queues) Wait(ctx context.Context, instanceID, id string) (store.Outbound, error) {
	key := waiterKey(instanceID, id)
	for {
		ch := make(chan struct{})
		q.mu.Lock()
		q.waiters[key] = append(q.waiters[key], ch)
		q.mu.Unlock()
		o, err := q.store.GetOutbound(ctx, instanceID, id)
		if err != nil || final(o.Status) {
			q.dropWaiter(key, ch)
			return o, err
		}
```

Em `internal/queue/queue.go` (`Forget`, `Dispatchers` e esperas por instância), trocar:

```go
		case <-ch:
		case <-ctx.Done():
			q.dropWaiter(id, ch)
			// The request context is over, not the store: read with a fresh one.
			o, err = q.store.GetOutbound(context.WithoutCancel(ctx), instanceID, id)
```

por:

```go
		case <-ch:
		case <-ctx.Done():
			q.dropWaiter(key, ch)
			// The request context is over, not the store: read with a fresh one.
			o, err = q.store.GetOutbound(context.WithoutCancel(ctx), instanceID, id)
```

Em `internal/queue/queue.go` (`Forget`, `Dispatchers` e esperas por instância), trocar:

```go
}

func (q *Queues) dropWaiter(id string, ch chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.waiters[id]
	for i, c := range l {
		if c == ch {
			q.waiters[id] = append(l[:i], l[i+1:]...)
			break
		}
	}
	if len(q.waiters[id]) == 0 {
		delete(q.waiters, id)
	}
}

// notify wakes the Wait calls of an item whose status changed.
func (q *Queues) notify(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ch := range q.waiters[id] {
		close(ch)
	}
	delete(q.waiters, id)
}

```

por:

```go
}

func (q *Queues) dropWaiter(key string, ch chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.waiters[key]
	for i, c := range l {
		if c == ch {
			q.waiters[key] = append(l[:i], l[i+1:]...)
			break
		}
	}
	if len(q.waiters[key]) == 0 {
		delete(q.waiters, key)
	}
}

// notify wakes the Wait calls of an item whose status changed.
func (q *Queues) notify(instanceID, id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := waiterKey(instanceID, id)
	for _, ch := range q.waiters[key] {
		close(ch)
	}
	delete(q.waiters, key)
}

```

Em `internal/queue/queue.go` (`Forget`, `Dispatchers` e esperas por instância), trocar:

```go
		_ = os.Remove(p.MediaFile)
	}
	d.q.notify(item.ID)
}

```

por:

```go
		_ = os.Remove(p.MediaFile)
	}
	d.q.notify(d.id, item.ID)
}

```

Em `internal/media/media.go` (URL inválida é erro do cliente), trocar:

```go
var (
	ErrTooLarge = errors.New("media exceeds the size limit")
	// ErrFetch means media.url could not be downloaded (network, HTTP status, bad URL).
	ErrFetch   = errors.New("media could not be fetched")
	ErrBlocked = errors.New("media URL points to a private or local address")
)

```

por:

```go
var (
	ErrTooLarge = errors.New("media exceeds the size limit")
	// ErrFetch means media.url could not be downloaded (network, HTTP status).
	ErrFetch   = errors.New("media could not be fetched")
	ErrBlocked = errors.New("media URL points to a private or local address")
	// ErrInvalidURL means media.url is not an absolute http(s) URL (a client error).
	ErrInvalidURL = errors.New("media.url must be an http(s) URL")
)

```

Em `internal/media/media.go` (URL inválida é erro do cliente), trocar:

```go
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("%w: media.url must be an http(s) URL", ErrFetch)
	}
	timeout := f.Timeout
```

por:

```go
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", ErrInvalidURL
	}
	timeout := f.Timeout
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go test -race ./internal/service/ ./internal/queue/ ./internal/media/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
go mod tidy
go test ./...
git add internal/service internal/queue internal/media
git commit -m "feat(service): envio com validação, mídia, idempotência, TTL e wait; settings com restart por proxy" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 15: Service — leitura

### Task 17: Service: chats, histórico, marcar como lido, contatos, números e mídia

**Files:**
- Create: `internal/service/inbox.go`
- Test: `internal/service/inbox_test.go`

Paginação por cursor opaco (base64url do último item da página; `limit` padrão 50, máximo 200); `next_cursor` só aparece quando a página veio cheia. `MarkRead` envia recibos das últimas mensagens recebidas (até `max(unread, 1)`, no máximo 100), agrupadas por remetente em grupos, e zera `unread_count`. `CheckNumbers` usa o `lookup` com a cota própria de `check-numbers` (cache + limite; no máximo 10 telefones por chamada, acima disso `400 validation_failed`; acima da cota por minuto → `429 rate_limited`). A mídia recebida é baixada pelo worker direto para um arquivo temporário único (`os.CreateTemp` em `data/media/<instância>/`) e renomeada quando completa; leituras simultâneas da primeira vez compartilham um único download (singleflight por mídia), com `expires_at` = agora + 7 dias (a limpeza é do marco 2b). `?signed=1` é recusado (`409 conflict`) quando as URLs assinadas estão desligadas.

- [ ] **Step 1: Escrever os testes**

Criar `internal/service/inbox_test.go`:

```go
package service

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

func TestChatsMessagesAndMarkRead(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 3; i++ {
		f.EmitMessage(&workerv1.Message{Id: "IN" + string(rune('0'+i)), Chat: "5511988887777@s.whatsapp.net",
			Sender: "5511988887777@s.whatsapp.net", PushName: "João", TimestampMs: base + int64(i)*1000, Type: "text", Text: "oi"})
	}
	f.EmitMessage(&workerv1.Message{Id: "G1", Chat: "1203630@g.us", Sender: "5511977776666@s.whatsapp.net",
		IsGroup: true, TimestampMs: base - 1000, Type: "text", Text: "grupo"})
	e.eventually("ingested", func() bool {
		p, _ := e.svc.Chats(ctx, "i1", 10, "")
		return len(p.Chats) == 2
	})
	page, err := e.svc.Chats(ctx, "i1", 1, "")
	if err != nil || len(page.Chats) != 1 || page.Chats[0].Name != "João" || page.Chats[0].UnreadCount != 3 || page.NextCursor == "" {
		t.Fatalf("chats page 1 = %+v, %v", page, err)
	}
	page2, err := e.svc.Chats(ctx, "i1", 1, page.NextCursor)
	if err != nil || len(page2.Chats) != 1 || page2.Chats[0].JID != "1203630@g.us" {
		t.Fatalf("chats page 2 = %+v, %v", page2, err)
	}
	msgs, err := e.svc.ChatMessages(ctx, "i1", "5511988887777", 2, "")
	if err != nil || len(msgs.Messages) != 2 || msgs.Messages[0].WAMessageID != "IN2" || msgs.NextCursor == "" {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	rest, err := e.svc.ChatMessages(ctx, "i1", "5511988887777", 2, msgs.NextCursor)
	if err != nil || len(rest.Messages) != 1 || rest.Messages[0].WAMessageID != "IN0" {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
	if _, err := e.svc.ChatMessages(ctx, "i1", "5511988887777", 2, "%%%"); apiCode(err) != "validation_failed" {
		t.Fatalf("bad cursor = %v", err)
	}
	if err := e.svc.MarkRead(ctx, "i1", "5511988887777@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	reads := f.WA.Reads()
	if len(reads) != 1 || len(reads[0].MessageIds) != 3 || reads[0].Sender != "" {
		t.Fatalf("reads = %+v", reads)
	}
	if c, _ := e.st.GetChat(ctx, "i1", "5511988887777@s.whatsapp.net"); c.UnreadCount != 0 {
		t.Fatalf("unread = %d", c.UnreadCount)
	}
	if err := e.svc.MarkRead(ctx, "i1", "5511900000000"); apiCode(err) != "not_found" {
		t.Fatalf("unknown chat = %v", err)
	}
}

func TestContactsAndCheckNumbers(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	f.WA.SetContacts(&workerv1.Contact{Jid: "5511988887777@s.whatsapp.net", Name: "João"})
	f.WA.SetUnregistered("5511900000000")
	cs, err := e.svc.Contacts(ctx, "i1")
	if err != nil || len(cs) != 1 || cs[0].Name != "João" {
		t.Fatalf("contacts = %+v, %v", cs, err)
	}
	if c, err := e.svc.Contact(ctx, "i1", "5511988887777"); err != nil || c.Name != "João" {
		t.Fatalf("contact = %+v, %v", c, err)
	}
	if _, err := e.svc.Contact(ctx, "i1", "5511900000000"); apiCode(err) != "not_found" {
		t.Fatalf("unknown contact = %v", err)
	}
	res, err := e.svc.CheckNumbers(ctx, "i1", []string{"+55 11 98888-7777", "5511900000000"})
	if err != nil || len(res) != 2 || !res[0].Exists || res[1].Exists {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if _, err := e.svc.CheckNumbers(ctx, "i1", []string{"12"}); apiCode(err) != "validation_failed" {
		t.Fatalf("short number = %v", err)
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("55119000000%02d", i)
	}
	if _, err := e.svc.CheckNumbers(ctx, "i1", eleven); apiCode(err) != "validation_failed" {
		t.Fatalf("11 numbers = %v", err)
	}
}

func TestMediaDownloadAndSignedURL(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	f.WA.SetMedia("ref-1", []byte("jpeg-bytes"))
	f.EmitMessage(&workerv1.Message{Id: "IMG1", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "image",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 10, Ref: []byte("ref-1")}})
	var mediaID string
	e.eventually("image logged", func() bool {
		p, _ := e.svc.ChatMessages(ctx, "i1", "5511988887777", 10, "")
		if len(p.Messages) == 1 && p.Messages[0].Media != nil {
			mediaID = p.Messages[0].Media.ID
		}
		return mediaID != ""
	})
	mf, err := e.svc.Media(ctx, "i1", mediaID)
	if err != nil || mf.Mime != "image/jpeg" {
		t.Fatalf("media = %+v, %v", mf, err)
	}
	if b, _ := os.ReadFile(mf.Path); string(b) != "jpeg-bytes" {
		t.Fatalf("file = %q", b)
	}
	f.WA.SetMedia("ref-1", nil) // second read comes from disk, not the worker
	if mf2, err := e.svc.Media(ctx, "i1", mediaID); err != nil || mf2.Path != mf.Path {
		t.Fatalf("cached media = %+v, %v", mf2, err)
	}
	raw, _, err := e.svc.SignedMediaURL(ctx, "i1", mediaID)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SignedURLs = false
	if _, _, err := e.svc.SignedMediaURL(ctx, "i1", mediaID); apiCode(err) != "conflict" {
		t.Fatalf("signed URLs disabled = %v", err)
	}
	u, _ := url.Parse(raw)
	if !e.svc.VerifyMediaSignature("i1", mediaID, u.Query().Get("exp"), u.Query().Get("sig")) {
		t.Fatal("signature rejected")
	}
	if _, err := e.svc.Media(ctx, "i1", "med_nope"); apiCode(err) != "not_found" {
		t.Fatalf("unknown media = %v", err)
	}
}

// Two first reads of the same media at once share one download and both get the file.
func TestConcurrentFirstMediaReads(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	f.WA.SetMedia("ref-c", []byte("same-bytes"))
	f.EmitMessage(&workerv1.Message{Id: "IMGC", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "image",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Ref: []byte("ref-c")}})
	var mediaID string
	e.eventually("image logged", func() bool {
		p, _ := e.svc.ChatMessages(ctx, "i1", "5511988887777", 10, "")
		if len(p.Messages) == 1 && p.Messages[0].Media != nil {
			mediaID = p.Messages[0].Media.ID
		}
		return mediaID != ""
	})
	type res struct {
		f   MediaFile
		err error
	}
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() { mf, err := e.svc.Media(ctx, "i1", mediaID); out <- res{mf, err} }()
	}
	a, b := <-out, <-out
	if a.err != nil || b.err != nil || a.f.Path != b.f.Path {
		t.Fatalf("results = %+v / %+v", a, b)
	}
	if data, _ := os.ReadFile(a.f.Path); string(data) != "same-bytes" {
		t.Fatalf("file = %q", data)
	}
	if n := f.WA.Downloads(); n != 1 {
		t.Fatalf("downloads = %d, want 1", n)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "media", "i1", "*.part")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}
```

Run: `go test ./internal/service/`
Expected: FAIL — `e.svc.Chats undefined`, `e.svc.Media undefined` etc.

- [ ] **Step 2: Implementar**

Criar `internal/service/inbox.go`:

```go
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/store"
)

type ChatView struct {
	JID           string    `json:"jid"`
	Name          string    `json:"name,omitempty"`
	IsGroup       bool      `json:"is_group"`
	LastMessageAt time.Time `json:"last_message_at"`
	UnreadCount   int       `json:"unread_count"`
}

type ChatPage struct {
	Chats      []ChatView `json:"chats"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type MessagePage struct {
	Messages   []MessageView `json:"messages"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

func encodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, v) != nil {
		return apierr.Validation("invalid cursor")
	}
	return nil
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return min(limit, 200)
}

// Chats lists the instance's chats, most recent first.
func (s *Messaging) Chats(ctx context.Context, instanceID string, limit int, cursor string) (ChatPage, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return ChatPage{}, mapErr(err)
	}
	var after *store.ChatCursor
	if cursor != "" {
		after = &store.ChatCursor{}
		if err := decodeCursor(cursor, after); err != nil {
			return ChatPage{}, err
		}
	}
	limit = pageLimit(limit)
	chats, cur, err := s.Store.ListChats(ctx, instanceID, limit, after)
	if err != nil {
		return ChatPage{}, err
	}
	page := ChatPage{Chats: make([]ChatView, 0, len(chats))}
	for _, c := range chats {
		page.Chats = append(page.Chats, ChatView{JID: c.JID, Name: c.Name, IsGroup: c.IsGroup,
			LastMessageAt: c.LastMessageAt, UnreadCount: c.UnreadCount})
	}
	if len(chats) == limit {
		page.NextCursor = encodeCursor(cur)
	}
	return page, nil
}

// ChatMessages pages through a chat's log, newest first.
func (s *Messaging) ChatMessages(ctx context.Context, instanceID, chatRaw string, limit int, cursor string) (MessagePage, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessagePage{}, mapErr(err)
	}
	chat, err := ChatJID(chatRaw)
	if err != nil {
		return MessagePage{}, err
	}
	var before *store.MessageCursor
	if cursor != "" {
		before = &store.MessageCursor{}
		if err := decodeCursor(cursor, before); err != nil {
			return MessagePage{}, err
		}
	}
	limit = pageLimit(limit)
	msgs, cur, err := s.Store.ListMessages(ctx, instanceID, chat, limit, before)
	if err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Messages: make([]MessageView, 0, len(msgs))}
	for _, m := range msgs {
		page.Messages = append(page.Messages, messageView(instanceID, m))
	}
	if len(msgs) == limit {
		page.NextCursor = encodeCursor(cur)
	}
	return page, nil
}

func (s *Messaging) client(instanceID string) (workerv1.WorkerClient, error) {
	cli, err := s.Manager.Client(instanceID)
	return cli, mapErr(err)
}

// MarkRead sends read receipts for the chat's latest received messages and clears its unread count.
func (s *Messaging) MarkRead(ctx context.Context, instanceID, chatRaw string) error {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return mapErr(err)
	}
	chat, err := ChatJID(chatRaw)
	if err != nil {
		return err
	}
	c, err := s.Store.GetChat(ctx, instanceID, chat)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("chat")
	}
	if err != nil {
		return err
	}
	msgs, err := s.Store.LatestInbound(ctx, instanceID, chat, min(max(c.UnreadCount, 1), 100))
	if err != nil {
		return err
	}
	if len(msgs) > 0 {
		cli, err := s.client(instanceID)
		if err != nil {
			return err
		}
		// In groups each receipt names one sender; in direct chats the sender is implicit.
		bySender := map[string][]string{}
		for _, m := range msgs {
			sender := ""
			if c.IsGroup {
				sender = m.SenderJID
			}
			bySender[sender] = append(bySender[sender], m.WAMessageID)
		}
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		defer cancel()
		for sender, ids := range bySender {
			if _, err := cli.MarkRead(rctx, &workerv1.MarkReadRequest{Chat: chat, Sender: sender, MessageIds: ids}); err != nil {
				return mapErr(err)
			}
		}
	}
	return s.Store.ResetUnread(ctx, instanceID, chat)
}

type ContactView struct {
	JID          string `json:"jid"`
	Name         string `json:"name,omitempty"`
	PushName     string `json:"push_name,omitempty"`
	BusinessName string `json:"business_name,omitempty"`
}

func contactView(c *workerv1.Contact) ContactView {
	return ContactView{JID: c.GetJid(), Name: c.GetName(), PushName: c.GetPushName(), BusinessName: c.GetBusinessName()}
}

func (s *Messaging) Contacts(ctx context.Context, instanceID string) ([]ContactView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	cli, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.GetContacts(rctx, &workerv1.GetContactsRequest{})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]ContactView, 0, len(resp.Contacts))
	for _, c := range resp.Contacts {
		out = append(out, contactView(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JID < out[j].JID })
	return out, nil
}

func (s *Messaging) Contact(ctx context.Context, instanceID, jidRaw string) (ContactView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return ContactView{}, mapErr(err)
	}
	jid, err := ChatJID(jidRaw)
	if err != nil {
		return ContactView{}, err
	}
	cli, err := s.client(instanceID)
	if err != nil {
		return ContactView{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.GetContact(rctx, &workerv1.GetContactRequest{Jid: jid})
	if err != nil {
		return ContactView{}, mapErr(err)
	}
	if !resp.Found {
		return ContactView{}, notFound("contact")
	}
	return contactView(resp.Contact), nil
}

type NumberView struct {
	Phone  string `json:"phone"`
	Exists bool   `json:"exists"`
	JID    string `json:"jid,omitempty"`
}

// CheckNumbers asks WhatsApp which phone numbers have an account (at most 10 per call, the size of the check bucket).
// Results are cached and uncached lookups are rate-limited per instance.
func (s *Messaging) CheckNumbers(ctx context.Context, instanceID string, phones []string) ([]NumberView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	if len(phones) == 0 || len(phones) > 10 {
		return nil, apierr.Validation("phones must have 1 to 10 numbers")
	}
	clean := make([]string, len(phones))
	for i, p := range phones {
		d := digitsOnly(p)
		if len(d) < 10 || len(d) > 15 {
			return nil, apierr.Validation("phone must have 10 to 15 digits including country code: " + p)
		}
		clean[i] = d
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	res, err := s.Resolver.Resolve(rctx, instanceID, clean, lookup.ForCheck)
	if errors.Is(err, lookup.ErrRateLimited) {
		return nil, apierr.New(http.StatusTooManyRequests, "rate_limited", err.Error(), true)
	}
	if errors.Is(err, lookup.ErrTooManyNumbers) {
		return nil, apierr.Validation("too many uncached numbers in one request; send at most 10 phones per call")
	}
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]NumberView, 0, len(res))
	for _, r := range res {
		out = append(out, NumberView{Phone: r.Phone, Exists: r.Found, JID: r.JID})
	}
	return out, nil
}

// Media returns a received attachment, downloading it through the worker the first time.
// Concurrent first reads of the same media share one download.
func (s *Messaging) Media(ctx context.Context, instanceID, mediaID string) (MediaFile, error) {
	md, err := s.Store.GetMedia(ctx, instanceID, mediaID)
	if errors.Is(err, store.ErrNotFound) {
		return MediaFile{}, notFound("media")
	}
	if err != nil {
		return MediaFile{}, err
	}
	f := MediaFile{Path: md.Path, Mime: md.Mime, FileName: md.FileName}
	if md.Path != "" {
		if _, err := os.Stat(md.Path); err == nil {
			return f, nil
		}
	}
	key := instanceID + "/" + mediaID
	s.flightMu.Lock()
	if s.flights == nil {
		s.flights = map[string]*mediaFlight{}
	}
	if fl, ok := s.flights[key]; ok {
		s.flightMu.Unlock()
		select {
		case <-fl.done:
			return fl.file, fl.err
		case <-ctx.Done():
			return MediaFile{}, ctx.Err()
		}
	}
	fl := &mediaFlight{done: make(chan struct{})}
	s.flights[key] = fl
	s.flightMu.Unlock()
	// The download outlives a caller that gives up: others may be waiting for it.
	fl.file, fl.err = s.download(context.WithoutCancel(ctx), instanceID, md, f)
	s.flightMu.Lock()
	delete(s.flights, key)
	s.flightMu.Unlock()
	close(fl.done)
	return fl.file, fl.err
}

// download has the worker write the attachment to a fresh temporary file next to its final
// path (no media over gRPC), then renames it into place.
func (s *Messaging) download(ctx context.Context, instanceID string, md store.Media, f MediaFile) (MediaFile, error) {
	cli, err := s.client(instanceID)
	if err != nil {
		return MediaFile{}, err
	}
	dir := filepath.Join(s.DataDir, "media", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return MediaFile{}, err
	}
	tmp, err := os.CreateTemp(dir, md.ID+"-*.part")
	if err != nil {
		return MediaFile{}, err
	}
	part := tmp.Name()
	tmp.Close()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := cli.DownloadMedia(dctx, &workerv1.DownloadMediaRequest{Kind: md.Kind, Ref: md.Ref, DestPath: part}); err != nil {
		os.Remove(part)
		return MediaFile{}, mapErr(err)
	}
	f.Path = filepath.Join(dir, md.ID)
	if err := os.Rename(part, f.Path); err != nil {
		os.Remove(part)
		return MediaFile{}, err
	}
	retention := s.MediaRetention
	if retention == 0 {
		retention = 7 * 24 * time.Hour
	}
	if err := s.Store.SetMediaFile(ctx, md.ID, f.Path, time.Now().Add(retention)); err != nil {
		return MediaFile{}, err
	}
	return f, nil
}

// SignedMediaURL returns a temporary URL that downloads the attachment without a token.
func (s *Messaging) SignedMediaURL(ctx context.Context, instanceID, mediaID string) (string, time.Time, error) {
	if !s.SignedURLs {
		return "", time.Time{}, apierr.New(http.StatusConflict, "conflict",
			"signed URLs need public_url when the API is not bound to loopback", false)
	}
	if _, err := s.Store.GetMedia(ctx, instanceID, mediaID); errors.Is(err, store.ErrNotFound) {
		return "", time.Time{}, notFound("media")
	} else if err != nil {
		return "", time.Time{}, err
	}
	u, exp := s.Signer.URL(instanceID, mediaID)
	return u, exp, nil
}

// VerifyMediaSignature checks the exp/sig of a signed media URL.
func (s *Messaging) VerifyMediaSignature(instanceID, mediaID, exp, sig string) bool {
	return s.Signer.Verify(instanceID, mediaID, exp, sig)
}
```

- [ ] **Step 3: Rodar e ver passar**

Run: `go test -race ./internal/service/`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
go mod tidy
go test ./...
git add internal/service
git commit -m "feat(service): chats, histórico paginado, marcar lido, contatos, números com cache e mídia sob demanda com download único" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 16: API e wiring

### Task 18: Rotas REST de mensagens e wiring do supervisor

**Files:**
- Modify: `internal/api/api.go`
- Create: `internal/api/messages.go`
- Test: `internal/api/api_test.go`, `internal/api/messages_test.go`
- Modify: `internal/serve/serve.go`
- Test: `internal/serve/serve_test.go`

Rotas novas, todas sob `/v1/instances/{id}` (token da instância ou admin): `GET|PUT /settings` (`proxy_url` e `unsafe_pacing` só com token admin: 403 para token de instância), `POST /messages[?wait=true&timeout=30s]` (202 enquanto na fila, 200 quando `sent`; corpo até 140MB, prazo de leitura estendido para 5 min com `http.ResponseController`, no máximo 3 corpos grandes ao mesmo tempo — os demais esperam vaga — e a vaga é liberada assim que a mensagem entra na fila, antes da espera do `?wait=true`, feita por `Messaging.Wait`), `GET /messages/{msg_id}`, `GET /chats`, `GET /chats/{jid}/messages`, `POST /chats/{jid}/read` (204), `GET /contacts`, `GET /contacts/{jid}`, `POST /check-numbers`, `GET /media/{media_id}[?signed=1]`. Corpo acima do limite → `413 media_too_large`. Fora de `/v1` e sem token: `GET /media/{id}/{media_id}?exp=&sig=` (URL assinada). Toda mídia servida leva `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'` e `Content-Disposition: attachment`, salvo JPEG/PNG/WebP/GIF, áudio e vídeo (`inline`).

No `serve`: `Recover` da fila antes de `Boot` (spec 9.2), `Queues` no `service.Instances` (o `Delete` encerra o despachante), observers `ingest` + `queue` no manager, `lookup` compartilhado por fila e service, carência de 15s após conectar, segredo de mídia em `data/media_key`, URLs assinadas só com `public_url` ou bind loopback, ambiente do worker com o proxy efetivo, e shutdown em ordem: HTTP (requisições que passam do prazo de 5s são cortadas com `srv.Close`) → despachantes (até 10s para envio em voo) → workers.

- [ ] **Step 1: Escrever os testes**

Em `internal/api/api_test.go` (imports), trocar:

```go
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/service"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
```

Em `internal/api/api_test.go` (`newEnvWith`), trocar:

```go
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-API") }
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		mgr.Shutdown(ctx)
	})
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir}
	opt := Options{AdminToken: admin, Ready: st.Ping}
	if tweak != nil {
		tweak(&opt)
	}
	h := New(svc
```

por:

```go
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-API") }
	log := slog.New(slog.DiscardHandler)
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	defaults := settings.Defaults()
	defaults.MinIntervalMS, defaults.JitterMS, defaults.TypingSimulation, defaults.Ramp.Enabled = 0, 0, false, false
	defaults.SendWindow, defaults.UnsafePacing = settings.Window{}, true
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	q := queue.New(st, mgr, queue.Options{Defaults: defaults, Log: log, Resolver: resolver})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log}, q})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
	})
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir, Queues: q}
	msg := &service.Messaging{Store: st, Manager: mgr, Queues: q, Fetcher: &media.Fetcher{AllowPrivate: true},
		Signer:  media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://example.invalid"),
		DataDir: dir, Defaults: defaults, Resolver: resolver, SignedURLs: true}
	opt := Options{AdminToken: admin, Ready: st.Ping}
	if tweak != nil {
		tweak(&opt)
	}
	h := New(svc
```

Em `internal/api/api_test.go` (`newEnvWith`, linha seguinte), trocar:

```go
	h := New(svc, opt, slog.New(slog.DiscardHandler))
```

por:

```go
	h := New(svc, msg, opt, log)
```

Criar `internal/api/messages_test.go`:

```go
package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
)

// connected creates an instance whose fake worker is connected.
func (e *env) connected(name string) (id, tok string) {
	e.t.Helper()
	id, tok = e.create(name)
	e.waitState(id, "qr_pending")
	e.sp.Last(id).Emit("connected")
	e.waitState(id, "connected")
	return id, tok
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSendAndReadMessages(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Msg")
	base := "/v1/instances/" + id

	code, body := e.do("POST", base+"/messages?wait=true&timeout=2s", tok, `{"to":"5511988887777","type":"text","text":"oi"}`)
	if code != 200 || body["status"] != "sent" || body["wa_message_id"] == nil {
		t.Fatalf("send wait = %d %v", code, body)
	}
	msgID := body["id"].(string)
	if code, body := e.do("GET", base+"/messages/"+msgID, tok, ""); code != 200 || body["status"] != "sent" {
		t.Fatalf("get message = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/messages?wait=true&timeout=nope", tok, `{"to":"5511988887777","type":"text","text":"x"}`); code != 400 {
		t.Fatalf("bad timeout = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/messages", tok, `{"to":"1","type":"text","text":"x"}`); code != 400 || errCode(body) != "validation_failed" {
		t.Fatalf("bad number = %d %v", code, body)
	}

	e.sp.Last(id).EmitMessage(&workerv1.Message{Id: "IN1", Chat: "5511988887777@s.whatsapp.net",
		Sender: "5511988887777@s.whatsapp.net", PushName: "João", TimestampMs: 1_800_000_000_000, Type: "text", Text: "olá"})
	e.eventually("chat with one unread message", func() bool {
		_, body := e.do("GET", base+"/chats", tok, "")
		chats, _ := body["chats"].([]any)
		return len(chats) == 1 && chats[0].(map[string]any)["unread_count"] == float64(1)
	})
	code, body = e.do("GET", base+"/chats/5511988887777@s.whatsapp.net/messages?limit=1", tok, "")
	msgs, _ := body["messages"].([]any)
	if code != 200 || len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "olá" || body["next_cursor"] == nil {
		t.Fatalf("chat messages = %d %v", code, body)
	}
	if code, _ := e.do("POST", base+"/chats/5511988887777/read", tok, ""); code != 204 {
		t.Fatalf("mark read = %d", code)
	}
	if reads := e.sp.Last(id).WA.Reads(); len(reads) != 1 || reads[0].MessageIds[0] != "IN1" {
		t.Fatalf("reads = %v", reads)
	}
}

func TestMessagingIsScopedToTheInstance(t *testing.T) {
	e := newEnv(t)
	id1, _ := e.connected("A")
	_, tok2 := e.create("B")
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/messages", `{"to":"5511988887777","type":"text","text":"oi"}`},
		{"GET", "/chats", ""},
		{"GET", "/settings", ""},
		{"POST", "/check-numbers", `{"phones":["5511988887777"]}`},
	} {
		if code, _ := e.do(c.method, "/v1/instances/"+id1+c.path, tok2, c.body); code != 403 {
			t.Fatalf("%s %s with another instance's token = %d", c.method, c.path, code)
		}
	}
}

func TestSettingsContactsAndNumbers(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Cfg")
	base := "/v1/instances/" + id
	if code, body := e.do("PUT", base+"/settings", tok, `{"proxy_url":"socks5://p:1080"}`); code != 403 || errCode(body) != "forbidden" {
		t.Fatalf("proxy_url with instance token = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/settings", tok, `{"unsafe_pacing":true}`); code != 403 {
		t.Fatalf("unsafe_pacing with instance token = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/settings", admin, `{"proxy_url":""}`); code != 200 {
		t.Fatalf("proxy_url with admin token = %d %v", code, body)
	}
	code, body := e.do("PUT", base+"/settings", tok, `{"max_per_minute":10,"ramp":{"enabled":true}}`)
	if code != 200 || body["max_per_minute"] != float64(10) || body["ramp"].(map[string]any)["enabled"] != true {
		t.Fatalf("put settings = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/settings", tok, `{"max_per_minute":0}`); code != 400 {
		t.Fatalf("invalid settings = %d %v", code, body)
	}
	if _, body := e.do("GET", base+"/settings", tok, ""); body["max_per_minute"] != float64(10) {
		t.Fatalf("get settings = %v", body)
	}

	f := e.sp.Last(id)
	f.WA.SetContacts(&workerv1.Contact{Jid: "5511988887777@s.whatsapp.net", Name: "João"})
	f.WA.SetUnregistered("5511900000000")
	if code, body := e.do("GET", base+"/contacts", tok, ""); code != 200 || len(body["contacts"].([]any)) != 1 {
		t.Fatalf("contacts = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/contacts/5511988887777", tok, ""); code != 200 || body["name"] != "João" {
		t.Fatalf("contact = %d %v", code, body)
	}
	code, body = e.do("POST", base+"/check-numbers", tok, `{"phones":["5511988887777","5511900000000"]}`)
	res, _ := body["results"].([]any)
	if code != 200 || len(res) != 2 || res[0].(map[string]any)["exists"] != true {
		t.Fatalf("check numbers = %d %v", code, body)
	}
}

func TestMediaDownloadAndSignedURL(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Media")
	f := e.sp.Last(id)
	f.WA.SetMedia("ref", []byte("ogg-bytes"))
	f.EmitMessage(&workerv1.Message{Id: "AUD1", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "audio",
		Media: &workerv1.Media{Kind: "audio", Mime: "audio/ogg", Ptt: true, Ref: []byte("ref")}})
	var mediaURL string
	e.eventually("media url in the log", func() bool {
		_, body := e.do("GET", "/v1/instances/"+id+"/chats/5511988887777/messages", tok, "")
		if msgs, _ := body["messages"].([]any); len(msgs) == 1 {
			mediaURL, _ = msgs[0].(map[string]any)["media"].(map[string]any)["url"].(string)
		}
		return mediaURL != ""
	})
	var lastHeader http.Header
	get := func(path, token string) (int, string, string) {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		lastHeader = resp.Header
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	if code, ct, body := get(mediaURL, tok); code != 200 || ct != "audio/ogg" || body != "ogg-bytes" {
		t.Fatalf("media = %d %s %q", code, ct, body)
	}
	if code, _, _ := get(mediaURL, ""); code != 401 {
		t.Fatalf("media without token = %d", code)
	}
	code, body := e.do("GET", mediaURL+"?signed=1", tok, "")
	signed, _ := body["url"].(string)
	if code != 200 || !strings.HasPrefix(signed, "http://example.invalid/media/"+id+"/") {
		t.Fatalf("signed = %d %v", code, body)
	}
	u, _ := url.Parse(signed)
	if code, _, body := get(u.RequestURI(), ""); code != 200 || body != "ogg-bytes" {
		t.Fatalf("signed fetch = %d %q", code, body)
	}
	if lastHeader.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(lastHeader.Get("Content-Security-Policy"), "sandbox") ||
		lastHeader.Get("Content-Disposition") != "inline" {
		t.Fatalf("headers = %v", lastHeader)
	}
	q := u.Query()
	q.Set("sig", "00"+q.Get("sig")[2:])
	if code, _, _ := get(u.Path+"?"+q.Encode(), ""); code != 403 {
		t.Fatalf("tampered signature = %d", code)
	}
}

func TestReceivedSVGIsNeverInline(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("SVG")
	f := e.sp.Last(id)
	f.WA.SetMedia("svg", []byte("<svg onload=alert(1)/>"))
	f.EmitMessage(&workerv1.Message{Id: "DOC1", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "document",
		Media: &workerv1.Media{Kind: "document", Mime: "image/svg+xml", FileName: "x.svg", Ref: []byte("svg")}})
	var mediaURL string
	e.eventually("svg logged", func() bool {
		_, body := e.do("GET", "/v1/instances/"+id+"/chats/5511988887777/messages", tok, "")
		if msgs, _ := body["messages"].([]any); len(msgs) == 1 {
			mediaURL, _ = msgs[0].(map[string]any)["media"].(map[string]any)["url"].(string)
		}
		return mediaURL != ""
	})
	req, _ := http.NewRequest("GET", e.srv.URL+mediaURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "x.svg") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Fatalf("headers = %v", resp.Header)
	}
}

// A body over the limit is 413, not a JSON error.
func TestOversizedBodyIs413(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Big")
	big := `{"to":"5511988887777","type":"text","text":"` + strings.Repeat("x", 1<<20) + `"}`
	if code, body := e.do("PUT", "/v1/instances/"+id+"/settings", tok, big); code != 413 || errCode(body) != "media_too_large" {
		t.Fatalf("oversized settings body = %d %v", code, body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"`+strings.Repeat("x", 100)+`"}`))
	var v map[string]any
	var ae *apierr.Error
	if err := decodeMax(rec, req, &v, 10); !errors.As(err, &ae) || ae.Status != 413 || ae.Code != "media_too_large" {
		t.Fatalf("decodeMax over the limit = %v", err)
	}
}
```

Em `internal/serve/serve_test.go` (imports), trocar:

```go
import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
)
```

por:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)
```

Acrescentar ao fim de `internal/serve/serve_test.go`:

```go
func TestWorkerEnvPassesEffectiveProxy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for id, js := range map[string]string{"own": `{"proxy_url":"http://own:3128"}`, "dflt": "{}"} {
		if err := st.CreateInstance(ctx, store.Instance{ID: id, Name: id, State: instance.Created, TokenHash: id, SettingsJSON: js}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.DefaultProxyURL = "socks5://default:1080"
	env := workerEnv(st, cfg)
	for id, want := range map[string]string{"own": "http://own:3128", "dflt": "socks5://default:1080"} {
		got, err := env(ctx, id)
		if err != nil || len(got) != 1 || got[0] != "LITEZAP_WORKER_PROXY_URL="+want {
			t.Errorf("%s: env = %v, %v", id, got, err)
		}
	}
	cfg.DefaultProxyURL = ""
	if got, _ := workerEnv(st, cfg)(ctx, "dflt"); got[0] != "LITEZAP_WORKER_PROXY_URL=" {
		t.Errorf("no proxy: env = %v", got)
	}
	if _, err := env(ctx, "missing"); err == nil {
		t.Error("expected error for unknown instance")
	}
}

// A request still running when the graceful shutdown gives up is cut (srv.Close) before the
// queues, workers and store go away.
func TestShutdownClosesHungRequests(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	dir := t.TempDir()
	t.Setenv("LITEZAP_DATA_DIR", dir)
	t.Setenv("LITEZAP_LISTEN_ADDR", addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "", slog.New(slog.DiscardHandler)) }()
	var conn net.Conn
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("server did not start: %v", err)
	}
	defer conn.Close()
	tok, err := os.ReadFile(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatal(err)
	}
	// A send whose body never finishes arriving: the handler blocks reading it.
	fmt.Fprintf(conn, "POST /v1/instances/inst_x/messages HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\nContent-Length: 1000000\r\n\r\n{\"to\":", strings.TrimSpace(string(tok)))
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("the hung request's connection is still open after Run returned")
		}
	}
}
```

Run: `go test ./internal/api/ ./internal/serve/`
Expected: FAIL — `too many arguments in call to New`, `undefined: workerEnv`; `TestOversizedBodyIs413` e `TestShutdownClosesHungRequests` falham.

- [ ] **Step 2: API: interface, rotas e decode com limite**

Em `internal/api/api.go` (imports), trocar:

```go
	"net/http"
	"strings"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
```

por:

```go
	"net/http"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
```

Em `internal/api/api.go` (imports), trocar:

```go
	"errors"
	"log/slog"
```

por:

```go
	"errors"
	"fmt"
	"log/slog"
```

Em `internal/api/api.go`, trocar:

```go
type Options struct {
```

por:

```go
// MessagingService is the subset of service.Messaging used by the API.
type MessagingService interface {
	Send(ctx context.Context, instanceID string, in service.SendInput, wait time.Duration) (service.MessageView, error)
	Wait(ctx context.Context, instanceID, id string, wait time.Duration) (service.MessageView, error)
	GetMessage(ctx context.Context, instanceID, id string) (service.MessageView, error)
	Chats(ctx context.Context, instanceID string, limit int, cursor string) (service.ChatPage, error)
	ChatMessages(ctx context.Context, instanceID, chat string, limit int, cursor string) (service.MessagePage, error)
	MarkRead(ctx context.Context, instanceID, chat string) error
	Contacts(ctx context.Context, instanceID string) ([]service.ContactView, error)
	Contact(ctx context.Context, instanceID, jid string) (service.ContactView, error)
	CheckNumbers(ctx context.Context, instanceID string, phones []string) ([]service.NumberView, error)
	Settings(ctx context.Context, instanceID string) (settings.Settings, error)
	UpdateSettings(ctx context.Context, instanceID string, patch settings.Patch) (settings.Settings, error)
	Media(ctx context.Context, instanceID, mediaID string) (service.MediaFile, error)
	SignedMediaURL(ctx context.Context, instanceID, mediaID string) (string, time.Time, error)
	VerifyMediaSignature(instanceID, mediaID, exp, sig string) bool
}

type Options struct {
```

Em `internal/api/api.go`, trocar:

```go
type server struct {
	svc InstanceService
	opt Options
	log *slog.Logger
}
```

por:

```go
type server struct {
	svc InstanceService
	msg MessagingService
	opt Options
	log *slog.Logger
	// bodySlots bounds concurrent large send bodies (memory).
	bodySlots chan struct{}
}
```

Em `internal/api/api.go`, trocar:

```go
func New(svc InstanceService, opt Options, log *slog.Logger) http.Handler {
	s := &server{svc: svc, opt: opt, log: log}
```

por:

```go
func New(svc InstanceService, msg MessagingService, opt Options, log *slog.Logger) http.Handler {
	s := &server{svc: svc, msg: msg, opt: opt, log: log, bodySlots: make(chan struct{}, 3)}
```

Em `internal/api/api.go`, trocar:

```go
		r.Get("/readyz", s.readyz)
	})
```

por:

```go
		r.Get("/readyz", s.readyz)
	})

	// Signed media URLs work without a token (spec 4.5); the signature is the credential.
	r.Get("/media/{id}/{media_id}", s.signedMedia)
```

Em `internal/api/api.go`, trocar:

```go
			r.Get("/qr", s.qr)
			r.Post("/pair-phone", s.pairPhone)
```

por:

```go
			r.Get("/qr", s.qr)
			r.Post("/pair-phone", s.pairPhone)

			r.Get("/settings", s.getSettings)
			r.Put("/settings", s.putSettings)
			r.Post("/messages", s.sendMessage)
			r.Get("/messages/{msg_id}", s.getMessage)
			r.Get("/chats", s.listChats)
			r.Get("/chats/{jid}/messages", s.chatMessages)
			r.Post("/chats/{jid}/read", s.markRead)
			r.Get("/contacts", s.listContacts)
			r.Get("/contacts/{jid}", s.getContact)
			r.Post("/check-numbers", s.checkNumbers)
			r.Get("/media/{media_id}", s.getMedia)
```

Em `internal/api/api.go`, trocar:

```go
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return apierr.Validation("invalid JSON body")
	}
	return nil
}
```

por (um corpo acima do limite é `413`, não JSON inválido):

```go
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeMax(w, r, v, 1<<20)
}

func decodeMax(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return apierr.New(http.StatusRequestEntityTooLarge, "media_too_large",
				fmt.Sprintf("request body exceeds %d bytes", mbe.Limit), false)
		}
		return apierr.Validation("invalid JSON body")
	}
	return nil
}
```

- [ ] **Step 3: Handlers**

Criar `internal/api/messages.go`:

```go
package api

import (
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/go-chi/chi/v5"
)

const (
	// maxSendBody fits a 100MB document as base64 (spec 4.5) plus the JSON around it.
	maxSendBody = 140 << 20
	// bigBody is the size from which a send body takes one of the server's bodySlots.
	bigBody = 1 << 20
	// sendReadTimeout replaces the server's 30s ReadTimeout for send bodies: ~140MB over a
	// slow link takes minutes.
	sendReadTimeout = 5 * time.Minute
)

func (s *server) getSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.msg.Settings(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, set)
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	var patch settings.Patch
	if err := decode(w, r, &patch); err != nil {
		s.writeErr(w, err)
		return
	}
	// The proxy decides the egress IP and unsafe_pacing lifts the anti-ban floors: admin only.
	if (patch.ProxyURL != nil || patch.UnsafePacing != nil) && !principalFrom(r).admin {
		apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "proxy_url and unsafe_pacing need the admin token", false))
		return
	}
	set, err := s.msg.UpdateSettings(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, set)
}

// sendMessage answers 202 while the message is queued and 200 once it was sent
// (?wait=true&timeout=30s waits for the outcome, spec 4.1).
func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var wait time.Duration
	if q := r.URL.Query(); q.Get("wait") == "true" {
		wait = 30 * time.Second
		if t := q.Get("timeout"); t != "" {
			d, err := time.ParseDuration(t)
			if err != nil || d <= 0 {
				s.writeErr(w, apierr.Validation("timeout must be a duration such as 30s"))
				return
			}
			wait = d
		}
	}
	v, err := s.enqueue(w, r)
	if err == nil && wait > 0 && (v.Status == "queued" || v.Status == "in_flight") {
		v, err = s.msg.Wait(r.Context(), chi.URLParam(r, "id"), v.ID, wait)
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	code := http.StatusAccepted
	switch v.Status {
	case "sent", "delivered", "read":
		code = http.StatusOK
	}
	writeJSON(w, code, v)
}

// enqueue reads the body and queues the message. A large body holds one of bodySlots only
// until the message is queued: a ?wait=true request does not keep it (or its memory).
func (s *server) enqueue(w http.ResponseWriter, r *http.Request) (service.MessageView, error) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(sendReadTimeout))
	if r.ContentLength > bigBody || r.ContentLength < 0 {
		select { // large bodies are held in memory while decoded: a few at a time
		case s.bodySlots <- struct{}{}:
			defer func() { <-s.bodySlots }()
		case <-r.Context().Done():
			return service.MessageView{}, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many large sends in progress", true)
		}
	}
	var in service.SendInput
	if err := decodeMax(w, r, &in, maxSendBody); err != nil {
		return service.MessageView{}, err
	}
	return s.msg.Send(r.Context(), chi.URLParam(r, "id"), in, 0)
}

func (s *server) getMessage(w http.ResponseWriter, r *http.Request) {
	v, err := s.msg.GetMessage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "msg_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func limitParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

func (s *server) listChats(w http.ResponseWriter, r *http.Request) {
	page, err := s.msg.Chats(r.Context(), chi.URLParam(r, "id"), limitParam(r), r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *server) chatMessages(w http.ResponseWriter, r *http.Request) {
	page, err := s.msg.ChatMessages(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"),
		limitParam(r), r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *server) markRead(w http.ResponseWriter, r *http.Request) {
	if err := s.msg.MarkRead(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listContacts(w http.ResponseWriter, r *http.Request) {
	list, err := s.msg.Contacts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"contacts": list})
}

func (s *server) getContact(w http.ResponseWriter, r *http.Request) {
	c, err := s.msg.Contact(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *server) checkNumbers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phones []string `json:"phones"`
	}
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	res, err := s.msg.CheckNumbers(r.Context(), chi.URLParam(r, "id"), req.Phones)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}

func (s *server) getMedia(w http.ResponseWriter, r *http.Request) {
	id, mediaID := chi.URLParam(r, "id"), chi.URLParam(r, "media_id")
	if r.URL.Query().Get("signed") == "1" {
		u, exp, err := s.msg.SignedMediaURL(r.Context(), id, mediaID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		noStore(w)
		writeJSON(w, 200, map[string]any{"url": u, "expires_at": exp})
		return
	}
	s.serveMedia(w, r, id, mediaID)
}

func (s *server) signedMedia(w http.ResponseWriter, r *http.Request) {
	id, mediaID := chi.URLParam(r, "id"), chi.URLParam(r, "media_id")
	q := r.URL.Query()
	if !s.msg.VerifyMediaSignature(id, mediaID, q.Get("exp"), q.Get("sig")) {
		apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "invalid or expired media URL", false))
		return
	}
	s.serveMedia(w, r, id, mediaID)
}

func (s *server) serveMedia(w http.ResponseWriter, r *http.Request, id, mediaID string) {
	mf, err := s.msg.Media(r.Context(), id, mediaID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	f, err := os.Open(mf.Path)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	if mf.Mime != "" {
		h.Set("Content-Type", mf.Mime)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	// Received media is attacker-controlled: never sniffed, never scripted, and anything
	// but plain images, audio and video is downloaded instead of rendered.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	disposition := "attachment"
	if inlineSafe(mf.Mime) {
		disposition = "inline"
	}
	params := map[string]string{}
	if mf.FileName != "" {
		params["filename"] = mf.FileName
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, params))
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// inlineSafe lists the types a browser may render in place (never SVG or HTML).
func inlineSafe(mimeType string) bool {
	m, _, _ := strings.Cut(strings.ToLower(mimeType), ";")
	switch strings.TrimSpace(m) {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}
	return strings.HasPrefix(m, "audio/") || strings.HasPrefix(m, "video/")
}
```

- [ ] **Step 4: Wiring no `serve`**

Em `internal/serve/serve.go` (imports), trocar:

```go
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/service"
```

por:

```go
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
```

Em `internal/serve/serve.go` (de `sp :=` até `api.New`), trocar:

```go
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log}
	mgr := manager.New(st, sp, manager.DefaultOptions(), log)
	if err := mgr.Boot(ctx); err != nil {
		shutdownManager(mgr)
		return err
	}

	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir}
	handler := api.New(svc, api.Options{
```

por:

```go
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log,
		Env: workerEnv(st, cfg)}
	mgr := manager.New(st, sp, manager.DefaultOptions(), log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	q := queue.New(st, mgr, queue.Options{Defaults: cfg.InstanceDefaults, Log: log, Resolver: resolver,
		ConnectGrace: 15 * time.Second})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log}, q})
	// Before any worker starts: sends interrupted by the last shutdown go back to the queue.
	if n, err := q.Recover(ctx); err != nil {
		return fmt.Errorf("recover send queue: %w", err)
	} else if n > 0 {
		log.Warn("requeued sends interrupted by the last shutdown", "count", n)
	}
	if err := mgr.Boot(ctx); err != nil {
		shutdownAll(q, mgr)
		return err
	}

	baseURL := cfg.PublicURL
	if baseURL == "" {
		baseURL = "http://" + cfg.ListenAddr
	}
	mediaKey, err := media.LoadOrCreateKey(filepath.Join(cfg.DataDir, "media_key"))
	if err != nil {
		shutdownAll(q, mgr)
		return fmt.Errorf("media key: %w", err)
	}
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir, Queues: q}
	msg := &service.Messaging{
		Store: st, Manager: mgr, Queues: q, Resolver: resolver,
		Fetcher:  &media.Fetcher{AllowPrivate: cfg.MediaAllowPrivateURLs},
		Signer:   media.NewSigner(mediaKey, baseURL),
		DataDir:  cfg.DataDir,
		Defaults: cfg.InstanceDefaults, DefaultProxy: cfg.DefaultProxyURL,
		// A URL built from a non-loopback listen address may be unreachable for third parties.
		SignedURLs: cfg.PublicURL != "" || isLoopback(cfg.ListenAddr),
	}
	handler := api.New(svc, msg, api.Options{
```

Em `internal/serve/serve.go` (fim de `Run` e funções de shutdown), trocar:

```go
	_ = srv.Shutdown(hctx)
	cancel()
	shutdownManager(mgr)
	return serveErr
}

// shutdownManager halts every worker with its own budget, so supervisor cleanups finish
// their store writes before the deferred st.Close runs.
func shutdownManager(mgr *manager.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), manager.DefaultOptions().StopTimeout+5*time.Second)
	defer cancel()
	mgr.Shutdown(ctx)
}
```

por:

```go
	if err := srv.Shutdown(hctx); err != nil {
		// Requests still running after the grace period are cut, so none outlives the
		// queues, workers and store stopped below.
		_ = srv.Close()
	}
	cancel()
	shutdownAll(q, mgr)
	return serveErr
}

// shutdownAll stops the dispatchers (a send in flight gets up to 10s, spec 9.2), then
// halts every worker with its own budget, so supervisor cleanups finish their store
// writes before the deferred st.Close runs.
func shutdownAll(q *queue.Queues, mgr *manager.Manager) {
	qctx, qcancel := context.WithTimeout(context.Background(), 10*time.Second)
	q.Shutdown(qctx)
	qcancel()
	ctx, cancel := context.WithTimeout(context.Background(), manager.DefaultOptions().StopTimeout+5*time.Second)
	defer cancel()
	mgr.Shutdown(ctx)
}

// workerEnv passes each worker its effective proxy (instance setting, else the default);
// an empty value means a direct connection. The spawner strips HTTP(S)_PROXY and friends.
func workerEnv(st *store.Store, cfg config.Config) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, id string) ([]string, error) {
		in, err := st.GetInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		set, err := settings.Effective(cfg.InstanceDefaults, cfg.DefaultProxyURL, in.SettingsJSON)
		if err != nil {
			return nil, err
		}
		return []string{"LITEZAP_WORKER_PROXY_URL=" + set.ProxyURL}, nil
	}
}
```

- [ ] **Step 5: Rodar tudo**

Run: `go vet ./... && go test -race ./...`
Expected: todos os pacotes `ok`; `go build -o litezap ./cmd/litezap` gera o binário.

- [ ] **Step 6: Commit**

```bash
go mod tidy
go test ./...
git add internal/api internal/serve
git commit -m "feat(api): rotas de mensagens, chats, contatos, settings e mídia; wiring da fila, ingestão e lookup" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Expected: `go mod tidy` não altera `go.mod`/`go.sum` (se alterar, inclua-os no commit); todos os pacotes `ok`.

---

## Chunk 17: Verificação manual e documentação

### Task 19: Verificação manual com WhatsApp real + README

**Files:**
- Modify: `README.md` (reescrita)
- Modify: `litezap.example.yaml` (reescrita)
- Test: `internal/config/config_test.go`

Requer o número de teste pareado (a instância do marco 1 serve), um segundo celular como contato, um grupo de teste com os dois e, se possível, um contato brasileiro salvo **sem** o 9º dígito. Não roda no CI. Variáveis usadas abaixo: `ID` e `TOK` da instância, `ADMIN=$(cat data/admin_token)`, `CONTATO` (só dígitos, com DDI), `GRUPO` (JID `...@g.us`, visível em `GET /chats` depois de uma mensagem no grupo), `B=localhost:8787/v1/instances/$ID` e `AUTH="Authorization: Bearer $TOK"`.

- [ ] **Step 1: Documentação e exemplo de configuração**

Substituir o conteúdo inteiro de `README.md` por:

````markdown
# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

Licença: Apache-2.0. Roda no seu PC, no seu servidor ou na nuvem gerenciada do LiteZap.

> Status: marco 2 (envio com fila persistente, recebimento, chats, histórico, mídia e proxy por instância). Webhooks/SSE, MCP e hook de agente vêm nos próximos marcos.
> Design: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md`

## Rodar

```bash
go build -o litezap ./cmd/litezap
./litezap serve            # usa litezap.yaml se existir; veja litezap.example.yaml
```

O token admin fica em `data/admin_token` (ou defina `LITEZAP_ADMIN_TOKEN`). O segredo das URLs assinadas de mídia fica em `data/media_key` (apagá-lo invalida as URLs já emitidas).

Configuração (`litezap.yaml`; variáveis `LITEZAP_*` têm precedência):

| Chave | Variável | Padrão |
|---|---|---|
| `listen_addr` | `LITEZAP_LISTEN_ADDR` | `127.0.0.1:8787` |
| `public_url` | `LITEZAP_PUBLIC_URL` | vazio (URLs assinadas usam `http://<listen_addr>`) |
| `data_dir` | `LITEZAP_DATA_DIR` | `data` |
| `admin_token` | `LITEZAP_ADMIN_TOKEN` | gerado em `data/admin_token` |
| `worker_memory_max_mb` | `LITEZAP_WORKER_MEMORY_MAX_MB` | `512` |
| `default_proxy_url` | `LITEZAP_DEFAULT_PROXY_URL` | vazio (conexão direta) |
| `media_allow_private_urls` | `LITEZAP_MEDIA_ALLOW_PRIVATE_URLS` | `false` |
| `instance_defaults` (bloco) | só no arquivo | ritmo 3s + 0–2s, 20/min, digitando ligado, rampa 50→150 em 14 dias, janela 08:00–20:00 no fuso do servidor |

Workers rodam em grupo de processos próprio e encerram sozinhos assim que o supervisor morre, mesmo à força (SIGKILL): cada worker vigia um pipe no stdin mantido aberto pelo supervisor e sai ao receber EOF. A limpeza de órfãos no próximo `serve` (via `data/run/*.pid`) é só um fallback.

## API

Autenticação: `Authorization: Bearer <token>`. O token de instância só acessa rotas da própria instância.

| Rota | Token |
|---|---|
| `POST /v1/instances` `{"name"}` → `{instance, token}` | admin |
| `GET /v1/instances` | admin |
| `GET /v1/instances/{id}` | instância ou admin |
| `POST /v1/instances/{id}/start\|stop\|restart\|logout` | instância ou admin |
| `GET /v1/instances/{id}/qr[?format=png]` | instância ou admin |
| `POST /v1/instances/{id}/pair-phone` `{"phone"}` → `{code}` | instância ou admin |
| `POST /v1/instances/{id}/token/rotate` | admin |
| `DELETE /v1/instances/{id}` | admin |
| `GET\|PUT /v1/instances/{id}/settings` | instância ou admin (`proxy_url` e `unsafe_pacing` só admin) |
| `POST /v1/instances/{id}/messages[?wait=true&timeout=30s]` | instância ou admin |
| `GET /v1/instances/{id}/messages/{msg_id}` | instância ou admin |
| `GET /v1/instances/{id}/chats[?limit=&cursor=]` | instância ou admin |
| `GET /v1/instances/{id}/chats/{jid}/messages[?limit=&cursor=]` | instância ou admin |
| `POST /v1/instances/{id}/chats/{jid}/read` | instância ou admin |
| `GET /v1/instances/{id}/contacts` · `/contacts/{jid}` | instância ou admin |
| `POST /v1/instances/{id}/check-numbers` `{"phones":[...]}` | instância ou admin |
| `GET /v1/instances/{id}/media/{media_id}[?signed=1]` | instância ou admin (`?signed=1` exige `public_url` se o `listen_addr` não for loopback) |
| `GET /media/{id}/{media_id}?exp=&sig=` (URL assinada, vale 1h) | nenhum |
| `GET /healthz`, `GET /readyz` | público (admin se o `listen_addr` não for loopback) |

As ações respondem com a instância. `start` e `restart` respondem assim que o worker é disparado, então a resposta pode ainda mostrar o estado anterior: consulte `GET /v1/instances/{id}` até ver o novo estado.

### Enviar mensagens

```bash
# texto; 202 {"id","status":"queued"} — ou 200 com wa_message_id se usar ?wait=true
curl -s -X POST "localhost:8787/v1/instances/$ID/messages?wait=true" -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"text","text":"Olá!","client_message_id":"pedido-123"}'

# mídia por URL (o servidor baixa; IPs privados bloqueados) ou base64
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"image","caption":"foto","media":{"url":"https://exemplo.com/a.jpg"}}'

# reação a uma mensagem do log (id do LiteZap ou wa_message_id)
curl -s -X POST localhost:8787/v1/instances/$ID/messages -H "Authorization: Bearer $TOK" \
  -d '{"to":"5511999999999","type":"reaction","reaction":{"message_id":"msg_...","emoji":"👍"}}'
```

Tipos: `text`, `image`, `video`, `audio` (`media.ptt: true` envia como mensagem de voz), `document`, `reaction`. `to` aceita número com DDI ou JID (`...@s.whatsapp.net`, `...@g.us`). Limites: imagem 16MB, vídeo/áudio 64MB, documento 100MB. `expires_after` (padrão `24h`, máximo `168h`): não enviada até lá, a mensagem falha com `message_expired`.

A fila é persistente e por instância: ordem estrita por destinatário, pausa enquanto a instância não está `connected` (e por 15–30s depois de conectar), janela de envio para quem não mandou nada nas últimas 24h, ritmo e rampa conforme `settings`, até 5 tentativas em falhas temporárias. Antes do primeiro envio a um número, o LiteZap confere se ele tem WhatsApp (e usa o JID que o WhatsApp devolve, com ou sem o 9º dígito); número sem WhatsApp falha com `invalid_number` sem enviar nada. Envios de mídia grande esperam vaga (no máximo 3 ao mesmo tempo) em vez de falhar. `client_message_id` repetido devolve a mesma mensagem (idempotência); repetido com outro conteúdo responde `409 conflict`. O status (`queued`, `in_flight`, `sent`, `delivered`, `read`, `failed`) fica em `GET /messages/{id}`.

### Settings

```bash
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"min_interval_ms":5000,"ramp":{"enabled":true},"send_window":{"start":"09:00","end":"18:00"},"timezone":"America/Sao_Paulo"}'
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $ADMIN" \
  -d '{"proxy_url":"socks5://user:senha@host:1080"}'
```

Campos ausentes mantêm o valor atual. Pisos: `min_interval_ms` ≥ 1000 e `max_per_minute` ≤ 60, a menos que o admin ligue `unsafe_pacing`. `send_window` vazio (`{"start":"","end":""}`) libera qualquer horário; fora da janela, mensagens para quem não mandou nada nas últimas 24h esperam o início da próxima (com alguns minutos de variação), enquanto respostas a conversas ativas saem a qualquer hora; itens adiados pela rampa vão para a janela do dia seguinte (nunca à meia-noite). Falhas na verificação do número adiam só aquele item (1, 2, 5, 15 min; na 5ª falha, `failed`). `"proxy_url":""` volta ao `default_proxy_url`. Trocar o proxy reinicia o worker; se o proxy não responde, a instância fica `reconnecting` com `state_reason: proxy_unreachable` (nunca cai para conexão direta, e variáveis `HTTP(S)_PROXY` do ambiente não são herdadas pelos workers).

Erros: `{"error":{"code","message","retryable"}}`. Códigos:

| Código | HTTP | Quando |
|---|---|---|
| `validation_failed` | 400 | corpo ou campo inválido |
| `invalid_number` | 400 | o número não tem WhatsApp |
| `unauthorized` | 401 | token ausente ou inválido |
| `forbidden` | 403 | token sem acesso ao recurso; URL assinada inválida ou expirada |
| `instance_not_found` | 404 | instância inexistente |
| `not_found` | 404 | mensagem, chat, contato ou mídia inexistente |
| `conflict` | 409 | `client_message_id` reusado com outro conteúdo; URLs assinadas desativadas |
| `instance_not_connected` | 409 | worker da instância não está rodando ou número deslogado |
| `instance_banned` | 409 | número banido; apague a instância |
| `instance_not_pairing` | 409 | instância não está aguardando QR/pareamento |
| `media_unavailable` | 410 | mídia recebida expirou no WhatsApp |
| `message_expired` | 410 | mensagem não enviada antes de `expires_after` |
| `media_too_large` | 413 | mídia acima do limite do tipo |
| `media_fetch_failed` | 422 | `media.url` não pôde ser baixada (`retryable`) |
| `rate_limited` | 429 | muitas consultas de números por minuto em `check-numbers` (cota própria, separada da fila) (`retryable`) |
| `worker_error` | 502 | o worker da instância recusou ou falhou a operação (`retryable`) |
| `worker_timeout` | 504 | o worker da instância não respondeu a tempo (`retryable`) |
| `shutting_down` | 503 | servidor encerrando |
| `internal` | 500 | erro interno |

## Como testar com seu WhatsApp

Requer um número de teste no celular e um segundo número (contato) para conversar.

1. Suba o servidor: `go build -o litezap ./cmd/litezap && ./litezap serve`.
2. Em outro terminal, crie a instância com o token admin e guarde `ID` e `TOK` (o token da instância só aparece uma vez):
   ```bash
   ADMIN=$(cat data/admin_token)
   RESP=$(curl -s -X POST localhost:8787/v1/instances -H "Authorization: Bearer $ADMIN" -d '{"name":"teste"}')
   ID=$(echo "$RESP" | jq -r .instance.id)
   TOK=$(echo "$RESP" | jq -r .token)
   ```
3. Baixe o QR e escaneie em WhatsApp > Aparelhos conectados:
   ```bash
   curl -s "localhost:8787/v1/instances/$ID/qr?format=png" -H "Authorization: Bearer $TOK" -o qr.png && open qr.png
   ```
4. Confira o estado: `curl -s localhost:8787/v1/instances/$ID -H "Authorization: Bearer $TOK"` deve mostrar `"state":"connected"` e `phone`.
5. Envie um texto ao contato com `?wait=true` (seção "Enviar mensagens") e responda pelo celular do contato; a resposta aparece em `GET /v1/instances/$ID/chats` e `.../chats/<jid>/messages`.
6. Crash do worker: `kill -9 $(cat data/run/$ID.pid)`; em alguns segundos o worker volta sozinho a `connected`, sem novo QR.
7. Pare o `serve` (Ctrl+C) e suba de novo: a instância reconecta sem QR e mensagens que estavam na fila são enviadas.
8. Logout: `curl -s -X POST localhost:8787/v1/instances/$ID/logout -H "Authorization: Bearer $TOK"` leva o estado a `logged_out` e o aparelho some da lista no celular.

Os estados mudam de forma assíncrona: depois de cada ação, repita o `GET` do passo 4 até ver o estado esperado.

## Limitações conhecidas (marco 2)

- Sem eventos, SSE ou webhooks ainda (marco 3): acompanhe por polling em `GET /chats` e `GET /messages/{id}`.
- Sem busca full-text e sem envio de `sticker`, `location` e `contact` (marco 2b); essas mensagens são recebidas e registradas.
- Mídia recebida baixada fica em `data/media/` sem limpeza automática ainda (o prazo de 7 dias é gravado, a limpeza vem no marco 2b).
- Mídia passa entre supervisor e worker por arquivos em `data/` (mesma máquina). Um spawner em container (nuvem) vai precisar de um volume compartilhado.
- Se o supervisor morrer entre o worker entregar um evento e o supervisor gravá-lo, esse evento se perde (sem confirmação de entrega worker→supervisor ainda).
- Mensagens editadas e apagadas ainda não atualizam o log.
- Suporte a Windows não testado (compila, mas não foi executado).

## Desenvolvimento

```bash
go test -race ./...
go tool buf generate   # após mudar proto/
```
````

Substituir o conteúdo inteiro de `litezap.example.yaml` por:

```yaml
# Copie para litezap.yaml. Variáveis LITEZAP_* sobrescrevem estes valores.
listen_addr: 127.0.0.1:8787
public_url: ""
data_dir: data
# admin_token: se vazio, é gerado em data/admin_token na primeira execução
worker_memory_max_mb: 512

# Proxy usado por toda instância sem settings.proxy_url própria (http://, https:// ou socks5://).
default_proxy_url: ""
# true libera media.url para IPs privados/loopback (uso local). Mantenha false em servidores.
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
  # Janela diária de envio no fuso abaixo; start e end vazios liberam qualquer horário.
  send_window:
    start: "08:00"
    end: "20:00"
  timezone: ""   # IANA, ex.: America/Sao_Paulo; vazio = fuso do servidor
```

Acrescentar ao fim de `internal/config/config_test.go`:

```go
// The documented example must stay loadable.
func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("LITEZAP_DATA_DIR", t.TempDir())
	cfg, err := Load(filepath.Join("..", "..", "litezap.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstanceDefaults.SendWindow.Start != "08:00" || cfg.InstanceDefaults.MinIntervalMS != 3000 {
		t.Fatalf("example defaults = %+v", cfg.InstanceDefaults)
	}
}
```

Run: `go test ./internal/config/`
Expected: `ok` (o exemplo documentado carrega).

- [ ] **Step 2: Subir a versão nova**

```bash
go build -o litezap ./cmd/litezap && ./litezap serve
```

Em outro terminal:

```bash
curl -s $B -H "$AUTH"
curl -s $B/settings -H "$AUTH"
```

Expected: a migração 0002 roda sem erro no boot e `data/media_key` é criado (0600); a instância volta a `"state":"connected"`; settings mostram `min_interval_ms: 3000`, `jitter_ms: 2000`, `max_per_minute: 20`, `typing_simulation: true`, rampa `50 → 150` em 14 dias e `send_window` 08:00–20:00. Para o teste ficar rápido (e funcionar a qualquer hora): `curl -s -X PUT $B/settings -H "$AUTH" -d '{"min_interval_ms":1000,"jitter_ms":0,"send_window":{"start":"","end":""}}'`. Confirme o piso: `curl -s -X PUT $B/settings -H "$AUTH" -d '{"min_interval_ms":500}'` responde 400.

- [ ] **Step 3: Verificar números (cache e 9º dígito)**

```bash
curl -s -X POST $B/check-numbers -H "$AUTH" -d '{"phones":["'$CONTATO'","5511000000000"]}'
```

Expected: `{"results":[{"phone":"<CONTATO>","exists":true,"jid":"...@s.whatsapp.net"},{"phone":"5511000000000","exists":false}]}`; a segunda chamada igual responde na hora (cache). Com o contato brasileiro sem o 9º dígito (`CONTATO8`, ex.: `551188887777`), o `jid` devolvido pode trazer o número com ou sem o 9º dígito (é o que o WhatsApp usa para aquela conta).

- [ ] **Step 4: Enviar texto, imagem e documentos grandes**

```bash
curl -s -X POST "$B/messages?wait=true" -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"Olá do LiteZap"}'
curl -s -X POST "$B/messages?wait=true&timeout=60s" -H "$AUTH" \
  -d '{"to":"'$CONTATO'","type":"image","caption":"teste","media":{"url":"https://picsum.photos/600"}}'
curl -s -X POST "$B/messages?wait=true" -H "$AUTH" -d '{"to":"'$CONTATO8'","type":"text","text":"sem o 9"}'
```

Expected: HTTP 200, `"status":"sent"` e `wa_message_id` começando com `3EB0`; o contato vê "digitando..." por um instante e recebe o texto e a imagem; o contato de 8 dígitos recebe "sem o 9". O `chat` em `GET /messages/<id>` pode ser diferente do que foi digitado (com ou sem o 9º dígito, conforme o WhatsApp responder): envie sempre na forma digitada e confirme que a resposta do contato cai **no mesmo chat** em `GET /chats` (não num segundo chat). Guarde o `id` do primeiro texto em `MSG`.

Documento de ~90MB, por URL e por base64 (o worker lê o arquivo do disco; o supervisor não deve passar de algumas centenas de MB de memória):

```bash
head -c 90000000 /dev/urandom > /tmp/grande.bin
python3 -m http.server 9999 --directory /tmp &   # servidor local temporário
curl -s -X PUT $B/settings -H "Authorization: Bearer $ADMIN" -d '{}' >/dev/null
LITEZAP_MEDIA_ALLOW_PRIVATE_URLS=true   # reinicie o serve com esta variável para aceitar a URL local
curl -s -X POST "$B/messages?wait=true&timeout=120s" -H "$AUTH" \
  -d '{"to":"'$CONTATO'","type":"document","media":{"url":"http://127.0.0.1:9999/grande.bin","file_name":"grande.bin"}}'
printf '{"to":"%s","type":"document","media":{"base64":"%s","file_name":"grande64.bin"}}' "$CONTATO" "$(base64 -i /tmp/grande.bin)" > /tmp/body.json
curl -s -X POST "$B/messages?wait=true&timeout=120s" -H "$AUTH" --data-binary @/tmp/body.json
kill %1
```

Expected: os dois chegam ao contato; sem `LITEZAP_MEDIA_ALLOW_PRIVATE_URLS` a URL local responde 400 (`private or local address`).

- [ ] **Step 5: Grupo e reação em grupo**

```bash
curl -s -X POST "$B/messages?wait=true" -H "$AUTH" -d '{"to":"'$GRUPO'","type":"text","text":"olá grupo"}'
```

Do celular do contato, mande uma mensagem no grupo; pegue o `id` dela em `GET $B/chats/$GRUPO/messages` e reaja:

```bash
curl -s -X POST "$B/messages?wait=true" -H "$AUTH" -d '{"to":"'$GRUPO'","type":"reaction","reaction":{"message_id":"<id>","emoji":"👍"}}'
curl -s $B/chats -H "$AUTH"
```

Expected: a mensagem e a reação aparecem no grupo; o chat do grupo continua com `"is_group":true`.

- [ ] **Step 6: Recibos e wait que estoura**

Abra a conversa no celular do contato e rode `curl -s $B/messages/$MSG -H "$AUTH"`. Expected: `"status":"read"` (ou `delivered`, se o contato desligou a confirmação de leitura).

Com a instância parada (`curl -s -X POST $B/stop -H "$AUTH"`), `curl -s -o /dev/null -w "%{http_code}\n" -X POST "$B/messages?wait=true&timeout=3s" -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"depois"}'` responde `202` depois de ~3s; suba de novo com `POST $B/start` e a mensagem sai.

- [ ] **Step 7: Receber, listar e baixar mídia (também via proxy)**

Do celular do contato, responda com um texto e uma foto. Depois:

```bash
curl -s $B/chats -H "$AUTH"
curl -s "$B/chats/$CONTATO/messages?limit=5" -H "$AUTH"
```

Expected: o chat do contato no topo, com o nome dele e `unread_count: 2`; a foto com `"media":{"id":"med_...","kind":"image",...,"url":"/v1/instances/.../media/med_..."}`; as enviadas com `source: api`. Baixe a foto e gere a URL assinada:

```bash
curl -s -D - -o foto.jpg "localhost:8787<url da mídia>" -H "$AUTH" | grep -i -E "content-(type|disposition|security)|nosniff" && open foto.jpg
curl -s "localhost:8787<url da mídia>?signed=1" -H "$AUTH"
curl -s -o /dev/null -w "%{http_code}\n" "<url assinada devolvida>"
```

Expected: cabeçalhos `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'`, `Content-Disposition: inline`; a foto abre; a URL assinada responde `200` sem token; trocar um caractere do `sig` responde `403`. Se tiver um proxy real, configure-o (passo do proxy abaixo), peça outra foto ao contato e baixe-a: chega igual (download pelo proxy).

- [ ] **Step 8: Marcar como lido e contatos**

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST $B/chats/$CONTATO/read -H "$AUTH"
curl -s $B/contacts -H "$AUTH" | head -c 400
```

Expected: `204` e as mensagens do contato ficam com os dois tiques azuis no celular dele; `GET /chats` mostra `unread_count: 0`; `contacts` lista a agenda sincronizada.

- [ ] **Step 9: Idempotência e id duplicado**

Rode duas vezes:

```bash
curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"uma vez só","client_message_id":"teste-idem-1"}'
```

Expected: as duas respostas trazem o mesmo `id`; o contato recebe a mensagem uma única vez. A mesma chamada com outro `text` responde `409 conflict`.

Agora o teste determinístico do reenvio com o mesmo `wa_message_id` (o que acontece na recuperação após queda), direto no worker:

```bash
LITEZAP_E2E_DATA_DIR=$PWD/data LITEZAP_E2E_INSTANCE=$ID LITEZAP_E2E_TO=$CONTATO@s.whatsapp.net \
  go test -tags=e2e -run TestE2EDuplicateMessageID -v ./internal/worker/
```

Expected: o log mostra dois `Send` com o mesmo id; no celular do contato aparece **uma** mensagem "teste de id duplicado ...". Se aparecerem duas, registre: a recuperação de `in_flight` passa a poder duplicar (risco listado no fim).

- [ ] **Step 10: Rampa e janela**

```bash
curl -s -X PUT $B/settings -H "$AUTH" -d '{"ramp":{"enabled":true,"start_per_day":1,"end_per_day":1},"send_window":{"start":"08:00","end":"20:00"}}'
curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"<número novo 1, sem conversa prévia>","type":"text","text":"rampa 1"}'
curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"<número novo 2, sem conversa prévia>","type":"text","text":"rampa 2"}'
```

Expected (rode dentro da janela): "rampa 1" sai; "rampa 2" fica `queued` com `error` começando com `ramp_limit` e, em `GET /messages/<id>`, nada é enviado até o dia seguinte entre 08:00 e ~08:05 (confira no dia seguinte). Depois das 20:00: uma resposta ao `CONTATO` logo após ele mandar uma mensagem sai na hora (conversa ativa), e uma mensagem a um número que não mandou nada nas últimas 24h fica `queued` com `error` `send_window: ...` até a janela seguinte. Volte depois para `{"send_window":{"start":"","end":""},"ramp":{"start_per_day":50,"end_per_day":150}}`.

- [ ] **Step 11: Fila parada sem conexão e recuperação após reinício**

```bash
curl -s -X POST $B/stop -H "$AUTH"
curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"fila 1"}'
curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"fila 2"}'
```

Expected: HTTP 202 com `"status":"queued"`; `GET /messages/<id>` continua `queued` sem `attempts`. Pare o `serve` com Ctrl+C, suba de novo e rode `curl -s -X POST $B/start -H "$AUTH"`. Expected: 15–30s depois de `connected`, "fila 1" e "fila 2" chegam nessa ordem e ficam `sent`.

- [ ] **Step 12: Queda do supervisor com envios pendentes**

```bash
curl -s -X PUT $B/settings -H "$AUTH" -d '{"min_interval_ms":15000}'
for i in 1 2 3; do curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"lote '$i'"}'; done
```

Assim que "lote 1" chegar, mate o supervisor à força: `kill -9 $(pgrep -f "litezap serve")`. Suba o `serve` de novo. Expected: "lote 2" e "lote 3" chegam (uma vez cada) e "lote 1" não é reenviado; se o kill pegou um envio em voo, o log mostra `requeued sends interrupted by the last shutdown` e esse envio sai de novo com o mesmo `wa_message_id`. Volte o intervalo: `curl -s -X PUT $B/settings -H "$AUTH" -d '{"min_interval_ms":1000}'`.

- [ ] **Step 13: Proxy: só admin, inacessível não cai para direto**

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X PUT $B/settings -H "$AUTH" -d '{"proxy_url":"socks5://127.0.0.1:9"}'
curl -s -X PUT $B/settings -H "Authorization: Bearer $ADMIN" -d '{"proxy_url":"socks5://127.0.0.1:9"}'
sleep 5; curl -s $B -H "$AUTH"
curl -s -X POST $B/messages -H "$AUTH" -d '{"to":"'$CONTATO'","type":"text","text":"via proxy"}'
```

Expected: `403` com o token da instância; com o admin, o worker reinicia, o estado vira `"state":"reconnecting","state_reason":"proxy_unreachable"` e a mensagem fica `queued` (nada chega ao contato). Rode o `serve` com `HTTPS_PROXY=http://127.0.0.1:9` no ambiente e confirme que, sem `proxy_url`, a instância conecta normalmente (a variável não é herdada). Depois `curl -s -X PUT $B/settings -H "Authorization: Bearer $ADMIN" -d '{"proxy_url":""}'`: o worker reinicia sem proxy, volta a `connected` e "via proxy" é enviada. Com um proxy real à mão, repita com ele e confirme `connected`, envio e recebimento de mídia normais.

- [ ] **Step 14: History sync grande sem travar o ao vivo**

Crie uma instância nova e pareie com um número que tenha **muitas** conversas. Enquanto o histórico é ingerido (log `history synced` repetido), mande mensagens do contato para esse número. Expected: as mensagens ao vivo aparecem em `GET /chats/<jid>/messages` em poucos segundos, sem esperar o fim do histórico; `GET /instances/<id>` responde normalmente; ao fim, `GET /chats` lista conversas antigas (com nome) com `source: history` e sem `unread_count`; o log do worker não mostra `worker event queue full`.

- [ ] **Step 15: Commit**

```bash
go mod tidy
go test ./...
git add README.md litezap.example.yaml internal/config/config_test.go
git commit -m "docs: README e exemplo de configuração do marco 2" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Marco 2b (adiado deste plano)

Itens do escopo do marco 2 no spec que ficaram de fora para manter o marco entregável; cada um cabe numa task própria sobre o código deste plano:

1. **Busca full-text** `GET /v1/instances/{id}/messages/search?q=`: migração `0003` com tabela FTS5 sobre `messages.text` (triggers de insert/delete), `store.SearchMessages` com cursor e rota/ferramenta.
2. **Envio de `sticker`, `location` e `contact`**: novos tipos em `queue.Payload`/`SendRequest` (`waE2E.StickerMessage` com upload `MediaImage`, `LocationMessage`, `ContactMessage{Vcard}`); a recepção desses tipos já existe.
3. **Retenção**: limpeza periódica de `data/media` por `expires_at` (`media_retention_days`, padrão 7), `message_retention_days` para o log, e limites de mídia configuráveis (hoje constantes em `media.MaxSize`).
4. **Confirmação de entrega worker→supervisor**: número de sequência nos eventos de dados + RPC `Ack`, para não perder eventos se o supervisor morrer entre o `Send` do stream e a gravação.
5. **Edições e revogações** no log (`ProtocolMessage` REVOKE/EDIT), base do evento `message.revoked` do marco 3.
6. **Nome de grupos** para chats criados por mensagem ao vivo (`GetGroupInfo`), e respostas citadas (`reply_to` com `ContextInfo`).
7. **Métricas** (`/metrics`): profundidade e latência da fila, descartes de eventos do worker, consultas de número.

## Mudanças no spec

Já aplicadas em `docs/superpowers/specs/2026-10-17-litezap-engine-design.md` junto com este plano: contrato do worker com prioridade, contrapressão, limite também no retorno ao backlog e mídia por arquivo restrita a `outbox/<instância>` e `media/<instância>` (3.2); corpo do envio, TTL, pisos de ritmo, janela/fuso só para conversas frias (sem mensagem recebida nas últimas 24h), carência, verificação de número com backoff por item e cotas separadas, e regras da rampa (4.1); rota e segredo das URLs assinadas e cabeçalhos de mídia (4.5); proxy por variável de ambiente, só admin, sem variáveis herdadas (4.6); colunas e tabelas novas (8); `qr_pending ↔ reconnecting` (9.1); códigos `not_found`, `conflict`, `invalid_number`, `media_too_large`, `media_unavailable` (410), `media_fetch_failed` (422), `message_expired`, `rate_limited` (429 só em `check-numbers`; envios de mídia esperam vaga) (9.3); chaves novas de config (10).

## Riscos

- **Reenvio após queda**: um item `in_flight` sem recibo é reenviado com o mesmo `wa_message_id`. Esperamos que o WhatsApp deduplique pelo id, mas isso só é verificado no passo de id duplicado da última task; no pior caso o destinatário recebe duplicado (at-least-once).
- **Perda de eventos recebidos**: se o supervisor morrer depois de o worker entregar um evento ao stream e antes de gravá-lo, o evento se perde (item 4 do 2b). Descartes por fila cheia são logados e contados, mas não recuperados.
- **Contrapressão do histórico**: com o supervisor lento, o handler de history sync do whatsmeow fica bloqueado; isso atrasa outros eventos que o whatsmeow processa na mesma goroutine (ao vivo e recibos), que é o custo de não descartar histórico.
- **LID sem mapeamento**: se o whatsmeow não souber o telefone de um LID, o contato vira um chat `@lid` separado e a rampa o trata como novo.
- **Detecção de número**: a fila consulta `IsOnWhatsApp` antes do primeiro envio (cota de 30/min por instância, separada das 10/min de `check-numbers`); se o WhatsApp limitar ou falhar, só os itens para números novos esperam (backoff até 15 min, `failed` na 5ª falha). O fallback para a string `"no LID found"` do whatsmeow continua frágil.
- **Janela e conversas ativas**: "ativa" é ter recebido algo nas últimas 24h; um contato que escreve às 19:59 recebe resposta às 21:00, mas uma mensagem fria para ele no dia seguinte, fora da janela, espera.
- **"Digitando..."** pode não aparecer para o contato quando a conta não está "online"; não mandamos presença `available` de propósito (isso silencia as notificações do celular).
- **Memória e disco**: o corpo base64 de até 140MB é decodificado em memória no supervisor (no máximo 3 por vez); o worker faz upload a partir do arquivo, mas o whatsmeow grava uma cópia cifrada em arquivo temporário do sistema durante o upload.
- **Mídia por arquivo** exige supervisor e worker no mesmo sistema de arquivos; o spawner em container da nuvem vai precisar de volume compartilhado ou de outro transporte.
- **`proxy_unreachable`** é inferido do tipo do erro de conexão; depois que a sessão conecta, quedas do proxy aparecem como `reconnecting`/`disconnected` (o auto-reconnect do whatsmeow não expõe a causa).
- **SQLite com uma conexão**: gravação de histórico (blocos de até 250 mensagens), despachantes e API dividem a mesma conexão; picos de latência são esperados durante o primeiro pareamento.
- **Fuso e horário de verão**: a janela usa o fuso IANA da instância; no dia da mudança de horário a janela pode começar uma hora antes/depois do esperado pela conta do Go (`time.Date` normaliza horários inexistentes).
- **Verificação real** depende do roteiro manual da última task; nada aqui foi testado contra o WhatsApp de verdade ainda.

## Ao terminar o marco 2

- `go test -race ./...` verde e o roteiro da última task executado com sucesso.
- Próximo passo: plano do **marco 2b** (lista acima) ou do **marco 3** (barramento de eventos, SSE e webhooks consumindo `ingest` e a fila).

