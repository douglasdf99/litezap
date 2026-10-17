# LiteZap Marco 1 — Esqueleto do motor — Plano de implementação

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Um binário `litezap` que cria instâncias via REST, sobe um processo worker whatsmeow por instância, pareia por QR ou código, supervisiona (heartbeat, restart com backoff, circuit breaker) e reconecta sozinho após crash ou reinício.

**Architecture:** Supervisor (`litezap serve`) com `store` SQLite, `manager` (máquina de estados + supervisão) e `api` REST sobre uma camada `service`. Cada instância roda em `litezap worker`, que expõe gRPC por unix socket. O `spawner` é uma interface; aqui implementamos `Process` (real) e um fake em memória (bufconn) usado nos testes.

**Tech Stack:** Go 1.26, `go.mau.fi/whatsmeow`, `modernc.org/sqlite`, `google.golang.org/grpc` + protobuf (codegen via `buf` como `go tool`), `github.com/go-chi/chi/v5`, `gopkg.in/yaml.v3`, `github.com/skip2/go-qrcode`.

**Spec:** `docs/superpowers/specs/2026-10-17-litezap-engine-design.md` (seções 3, 4.4, 6, 6.1, 8, 9.1, 9.2, 10, 13 item 1).

**Fora deste marco (vão para os planos dos marcos 2–5):** fila de envio e mensagens, eventos/webhooks/SSE, MCP, hook de agente, watchdog de sessão zumbi, kill por teto de memória (aqui só `GOMEMLIMIT`), `/metrics`, settings de ritmo.

---

## Estrutura de arquivos

```
cmd/litezap/main.go                 subcomandos serve | worker | version
internal/config/config.go           Config, Load (yaml + LITEZAP_*), EnsureAdminToken
internal/secure/secure.go           IDs, tokens de instância, hash, comparação constante
internal/instance/state.go          State + CanTransition (tabela 9.1 do spec)
internal/store/store.go             Open, migrações embutidas, Ping, Close
internal/store/migrations/0001_init.sql
internal/store/instances.go         CRUD de instances
proto/worker/v1/worker.proto        contrato supervisor↔worker
gen/worker/v1/*.pb.go               gerado (commitado)
internal/worker/server.go           Server gRPC + hub de eventos sobre WAClient
internal/worker/whatsmeow.go        adaptador WAClient → whatsmeow
internal/worker/run.go              entrada do subcomando worker
internal/spawner/spawner.go         interfaces Spawner / WorkerHandle
internal/spawner/socket.go          caminho curto do socket
internal/spawner/process.go         Process spawner + CleanupOrphans
internal/workertest/fake.go         worker fake (bufconn) + Spawner fake
internal/manager/backoff.go         Backoff + Breaker
internal/manager/manager.go         supervisão das instâncias
internal/apierr/apierr.go           formato de erro da API
internal/service/instances.go       casos de uso de instância
internal/api/api.go                 router, auth, handlers
internal/serve/serve.go             wiring do supervisor
```

Convenção de testes: `go test ./...` deve passar ao fim de cada task. Use `-race` nos pacotes concorrentes (`worker`, `spawner`, `manager`, `api`).

Convenção de dependências: a partir da Task 2, rode `go mod tidy` antes de cada commit e inclua `go.mod`/`go.sum` no commit se mudaram (a Task 1 só registra as dependências; o `tidy` passa a mantê-las conforme o código as importa).

---

## Chunk 1: Fundação

### Task 1: Módulo, ferramentas e .gitignore

**Files:**
- Create: `go.mod`, `.gitignore`, `litezap.example.yaml`

- [ ] **Step 1: Inicializar o módulo e as dependências**

```bash
cd ~/Documents/projects/litezap
go mod init github.com/douglasdf99/litezap
go get go.mau.fi/whatsmeow@latest modernc.org/sqlite@latest google.golang.org/grpc@latest google.golang.org/protobuf@latest github.com/go-chi/chi/v5@latest gopkg.in/yaml.v3@latest github.com/skip2/go-qrcode@latest
go get -tool github.com/bufbuild/buf/cmd/buf@latest
go get -tool google.golang.org/protobuf/cmd/protoc-gen-go@latest
go get -tool google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

Expected: `go.mod` com `go 1.26.x`, as dependências e um bloco `tool (...)` com as três ferramentas.

- [ ] **Step 2: Criar `.gitignore`**

```gitignore
/litezap
/data/
/litezap.yaml
*.test
*.out
```

- [ ] **Step 3: Criar `litezap.example.yaml`**

```yaml
# Copie para litezap.yaml. Variáveis LITEZAP_* sobrescrevem estes valores.
listen_addr: 127.0.0.1:8787
public_url: ""
data_dir: data
# admin_token: se vazio, é gerado em data/admin_token na primeira execução
worker_memory_max_mb: 512
```

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum .gitignore litezap.example.yaml
git commit -m "chore: inicializa módulo Go e ferramentas de codegen"
```

### Task 2: Config

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Escrever os testes**

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8787" || cfg.WorkerMemoryMB != 512 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("data dir must be absolute, got %q", cfg.DataDir)
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "litezap.yaml")
	if err := os.WriteFile(p, []byte("listen_addr: 0.0.0.0:9000\nadmin_token: fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LITEZAP_ADMIN_TOKEN", "fromenv")
	t.Setenv("LITEZAP_WORKER_MEMORY_MAX_MB", "256")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("listen addr = %q", cfg.ListenAddr)
	}
	if cfg.AdminToken != "fromenv" {
		t.Errorf("admin token = %q", cfg.AdminToken)
	}
	if cfg.WorkerMemoryMB != 256 {
		t.Errorf("memory = %d", cfg.WorkerMemoryMB)
	}
}

func TestLoadRejectsBadMemoryEnv(t *testing.T) {
	t.Setenv("LITEZAP_WORKER_MEMORY_MAX_MB", "lots")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureAdminTokenCreatesAndReuses(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	if err := EnsureAdminToken(&cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.AdminToken, "lza_") {
		t.Fatalf("token = %q", cfg.AdminToken)
	}
	info, err := os.Stat(filepath.Join(cfg.DataDir, "admin_token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", info.Mode().Perm())
	}
	again := Config{DataDir: cfg.DataDir}
	if err := EnsureAdminToken(&again); err != nil {
		t.Fatal(err)
	}
	if again.AdminToken != cfg.AdminToken {
		t.Errorf("token not reused: %q vs %q", again.AdminToken, cfg.AdminToken)
	}
}

func TestEnsureAdminTokenKeepsConfigured(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), AdminToken: "set"}
	if err := EnsureAdminToken(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AdminToken != "set" {
		t.Fatal("configured token overwritten")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/config/`
Expected: FAIL (`undefined: Load`).

- [ ] **Step 3: Implementar**

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

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr     string `yaml:"listen_addr"`
	PublicURL      string `yaml:"public_url"`
	DataDir        string `yaml:"data_dir"`
	AdminToken     string `yaml:"admin_token"`
	WorkerMemoryMB int64  `yaml:"worker_memory_max_mb"`
}

func Default() Config {
	return Config{ListenAddr: "127.0.0.1:8787", DataDir: "data", WorkerMemoryMB: 512}
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
	if v := os.Getenv("LITEZAP_ADMIN_TOKEN"); v != "" {
		cfg.AdminToken = v
	}
	if v := os.Getenv("LITEZAP_WORKER_MEMORY_MAX_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_WORKER_MEMORY_MAX_MB: %w", err)
		}
		cfg.WorkerMemoryMB = n
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
		cfg.AdminToken = strings.TrimSpace(string(b))
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

- [ ] **Step 4: Rodar e ver passar**

Run: `go test ./internal/config/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): carrega yaml + env e gera token admin"
```

### Task 3: Utilitários de segurança (IDs e tokens)

**Files:**
- Create: `internal/secure/secure.go`
- Test: `internal/secure/secure_test.go`

- [ ] **Step 1: Escrever os testes**

```go
package secure

import (
	"strings"
	"testing"
)

func TestNewIDHasPrefixAndIsUnique(t *testing.T) {
	a, b := NewID("inst_"), NewID("inst_")
	if !strings.HasPrefix(a, "inst_") || len(a) != len("inst_")+16 {
		t.Fatalf("id = %q", a)
	}
	if a == b {
		t.Fatal("ids must differ")
	}
}

func TestInstanceTokenAndHash(t *testing.T) {
	tok := NewInstanceToken()
	if !strings.HasPrefix(tok, "lzi_") || len(tok) < 40 {
		t.Fatalf("token = %q", tok)
	}
	h := HashToken(tok)
	if len(h) != 64 || h != HashToken(tok) || h == HashToken(tok+"x") {
		t.Fatalf("bad hash %q", h)
	}
}

func TestEqual(t *testing.T) {
	if !Equal("abc", "abc") || Equal("abc", "abd") || Equal("", "x") {
		t.Fatal("Equal misbehaves")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/secure/`
Expected: FAIL (`undefined: NewID`).

- [ ] **Step 3: Implementar**

```go
// Package secure generates identifiers and tokens and compares secrets safely.
package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// NewID returns prefix followed by 16 random hex characters.
func NewID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// NewInstanceToken returns a new instance API token ("lzi_" + 32 random bytes).
func NewInstanceToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "lzi_" + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the hex SHA-256 of a token; only hashes are stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Equal compares two secrets in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go test ./internal/secure/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/secure
git commit -m "feat(secure): ids, tokens de instância e hash"
```

### Task 4: Máquina de estados da instância

**Files:**
- Create: `internal/instance/state.go`
- Test: `internal/instance/state_test.go`

- [ ] **Step 1: Escrever os testes (tabela 9.1 do spec)**

```go
package instance

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to State
		want     bool
	}{
		{Created, Starting, true},
		{Created, Stopped, true},
		{Created, Connected, false},
		{Starting, QRPending, true},
		{Starting, Connecting, true},
		{Starting, Connected, true},    // worker snapshot may skip connecting
		{Starting, Reconnecting, true}, // initial connect failed, retrying
		{QRPending, Connecting, true},
		{QRPending, Connected, true},
		{Connecting, Connected, true},
		{Connecting, Reconnecting, true},
		{Connected, Reconnecting, true},
		{Reconnecting, Connected, true},
		{Connected, QRPending, false},
		{Connected, LoggedOut, true},
		{Reconnecting, Banned, true},
		{QRPending, Failed, true},
		{Connected, Starting, true}, // worker crash
		{Connected, Stopped, true},
		{LoggedOut, Starting, true},
		{Failed, Starting, true},
		{Stopped, Starting, true},
		{LoggedOut, Connected, false},
		{Banned, Starting, false},
		{Banned, Stopped, false},
		{Connected, Connected, true}, // no-op
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestPredicates(t *testing.T) {
	for _, s := range []State{Starting, QRPending, Connecting, Connected, Reconnecting} {
		if !s.Active() || s.Terminal() {
			t.Errorf("%s should be active, not terminal", s)
		}
	}
	for _, s := range []State{LoggedOut, Banned, Failed} {
		if s.Active() || !s.Terminal() {
			t.Errorf("%s should be terminal, not active", s)
		}
	}
	if Stopped.Active() || Stopped.Terminal() || Created.Active() {
		t.Error("stopped/created are neither active nor terminal")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/instance/`
Expected: FAIL (`undefined: CanTransition`).

- [ ] **Step 3: Implementar**

```go
// Package instance defines the lifecycle states of a WhatsApp instance.
package instance

type State string

const (
	Created      State = "created"
	Starting     State = "starting"
	QRPending    State = "qr_pending"
	Connecting   State = "connecting"
	Connected    State = "connected"
	Reconnecting State = "reconnecting"
	LoggedOut    State = "logged_out"
	Banned       State = "banned"
	Failed       State = "failed"
	Stopped      State = "stopped"
)

// Active reports whether a worker is expected to be running in this state.
func (s State) Active() bool {
	switch s {
	case Starting, QRPending, Connecting, Connected, Reconnecting:
		return true
	}
	return false
}

// Terminal reports whether the state needs human action before restarting.
func (s State) Terminal() bool {
	return s == LoggedOut || s == Banned || s == Failed
}

var edges = map[State][]State{
	Created:      {Starting, Stopped},
	// The worker's event snapshot carries only its latest state, so from starting
	// any worker-reported state may arrive first.
	Starting:     {QRPending, Connecting, Connected, Reconnecting},
	QRPending:    {Connecting, Connected},
	Connecting:   {Connected, Reconnecting},
	Connected:    {Reconnecting},
	Reconnecting: {Connected},
	LoggedOut:    {Starting, Stopped},
	Failed:       {Starting, Stopped},
	Stopped:      {Starting},
}

// CanTransition implements the transition table of spec section 9.1.
func CanTransition(from, to State) bool {
	if from == to {
		return true
	}
	if from == Banned {
		return false
	}
	if from.Active() {
		switch to {
		case LoggedOut, Banned, Failed, Starting, Stopped:
			return true
		}
	}
	for _, s := range edges[from] {
		if s == to {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go test ./internal/instance/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/instance
git commit -m "feat(instance): máquina de estados com tabela de transições"
```

### Task 5: Store SQLite + instances

**Files:**
- Create: `internal/store/store.go`, `internal/store/migrations/0001_init.sql`, `internal/store/instances.go`
- Test: `internal/store/instances_test.go`

- [ ] **Step 1: Escrever os testes**

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/douglasdf99/litezap/internal/instance"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		if err := s.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestInstanceCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	in := Instance{ID: "inst_1", Name: "Loja", State: instance.Created, TokenHash: "h1"}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInstance(ctx, Instance{ID: "inst_2", Name: "B", State: instance.Stopped, TokenHash: "h2"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetInstance(ctx, "inst_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Loja" || got.State != instance.Created || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}

	if err := s.UpdateInstanceState(ctx, "inst_1", instance.Starting, "boot"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstanceJID(ctx, "inst_1", "5511999999999:3@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetInstance(ctx, "inst_1")
	if got.State != instance.Starting || got.StateReason != "boot" || got.JID == "" {
		t.Fatalf("after update %+v", got)
	}

	byTok, err := s.InstanceByTokenHash(ctx, "h2")
	if err != nil || byTok.ID != "inst_2" {
		t.Fatalf("by token: %+v %v", byTok, err)
	}
	if err := s.SetInstanceTokenHash(ctx, "inst_2", "h3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstanceByTokenHash(ctx, "h2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token should be gone, err=%v", err)
	}

	list, err := s.ListInstances(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "inst_1" {
		t.Fatalf("list %+v %v", list, err)
	}

	if err := s.DeleteInstance(ctx, "inst_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "inst_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.UpdateInstanceState(ctx, "nope", instance.Starting, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/store/`
Expected: FAIL (`undefined: Open`).

- [ ] **Step 3: Criar a migração `internal/store/migrations/0001_init.sql`**

```sql
CREATE TABLE instances (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  state        TEXT NOT NULL,
  state_reason TEXT NOT NULL DEFAULT '',
  jid          TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
```

- [ ] **Step 4: Implementar `internal/store/store.go`**

```go
// Package store persists LiteZap state in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// One connection serializes writes and avoids SQLITE_BUSY; enough for marco 1.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	for _, e := range entries { // ReadDir returns entries sorted by name
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, e.Name()).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			e.Name(), time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// exec1 runs a statement that must affect at least one row.
func (s *Store) exec1(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// tsLayout is fixed-width so lexicographic order equals chronological order.
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

func now() string { return time.Now().UTC().Format(tsLayout) }
```

- [ ] **Step 5: Implementar `internal/store/instances.go`**

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
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const instanceCols = `id, name, state, state_reason, jid, token_hash, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanInstance(row scanner) (Instance, error) {
	var in Instance
	var state, created, updated string
	err := row.Scan(&in.ID, &in.Name, &state, &in.StateReason, &in.JID, &in.TokenHash, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	in.State = instance.State(state)
	in.CreatedAt, _ = time.Parse(tsLayout, created)
	in.UpdatedAt, _ = time.Parse(tsLayout, updated)
	return in, nil
}

func (s *Store) CreateInstance(ctx context.Context, in Instance) error {
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO instances (`+instanceCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, string(in.State), in.StateReason, in.JID, in.TokenHash, ts, ts)
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
	var out []Instance
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
```

- [ ] **Step 6: Rodar e ver passar**

Run: `go test ./internal/store/`
Expected: `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/store
git commit -m "feat(store): sqlite com migrações e CRUD de instâncias"
```

---

## Chunk 2: Protocolo e worker

### Task 6: Contrato gRPC supervisor↔worker

**Files:**
- Create: `buf.yaml`, `buf.gen.yaml`, `proto/worker/v1/worker.proto`
- Generated: `gen/worker/v1/worker.pb.go`, `gen/worker/v1/worker_grpc.pb.go`

- [ ] **Step 1: Criar `buf.yaml`**

```yaml
version: v2
modules:
  - path: proto
```

- [ ] **Step 2: Criar `buf.gen.yaml`**

```yaml
version: v2
plugins:
  - local: ["go", "tool", "protoc-gen-go"]
    out: gen
    opt: paths=source_relative
  - local: ["go", "tool", "protoc-gen-go-grpc"]
    out: gen
    opt: paths=source_relative
```

- [ ] **Step 3: Criar `proto/worker/v1/worker.proto`**

Marco 1 só define o necessário para conexão e supervisão; `Send`, `DownloadMedia` etc. entram no marco 2.

```proto
syntax = "proto3";

package worker.v1;

option go_package = "github.com/douglasdf99/litezap/gen/worker/v1;workerv1";

// Worker is served by `litezap worker` (one process per WhatsApp instance).
service Worker {
  rpc Ping(PingRequest) returns (PingResponse);
  rpc PairPhone(PairPhoneRequest) returns (PairPhoneResponse);
  rpc Logout(LogoutRequest) returns (LogoutResponse);
  // Events streams the latest snapshot first (connection state, then QR if pairing), then live events.
  rpc Events(EventsRequest) returns (stream Event);
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
```

- [ ] **Step 4: Gerar o código**

Run: `go tool buf generate && go mod tidy && go build ./gen/...`
Expected: cria `gen/worker/v1/worker.pb.go` e `gen/worker/v1/worker_grpc.pb.go`; o build passa.

- [ ] **Step 5: Commit**

```bash
git add buf.yaml buf.gen.yaml proto gen go.mod go.sum
git commit -m "feat(proto): contrato gRPC supervisor-worker"
```

### Task 7: Worker server (hub de eventos)

**Files:**
- Create: `internal/worker/server.go`
- Test: `internal/worker/server_test.go`

O `Server` não conhece o whatsmeow: depende da interface `WAClient`. Ele guarda o último estado e o último QR para que um assinante novo (supervisor que reconectou) receba o snapshot.

- [ ] **Step 1: Escrever os testes**

```go
package worker

import (
	"context"
	"net"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type stubWA struct {
	sink    Sink
	logouts int
}

func (s *stubWA) Run(ctx context.Context, sink Sink) error {
	s.sink = sink
	sink.ConnState("qr_pending", "", "")
	sink.QR("QR-1", time.Minute)
	return nil
}
func (s *stubWA) PairPhone(ctx context.Context, phone string) (string, error) { return "ABCD-EFGH", nil }
func (s *stubWA) Logout(ctx context.Context) error {
	s.logouts++
	s.sink.ConnState("logged_out", "api", "")
	return nil
}
func (s *stubWA) Disconnect() {}

func dial(t *testing.T, srv *Server) workerv1.WorkerClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	workerv1.RegisterWorkerServer(g, srv)
	go g.Serve(lis)
	t.Cleanup(g.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return workerv1.NewWorkerClient(cc)
}

func TestServerSnapshotAndLiveEvents(t *testing.T) {
	wa := &stubWA{}
	srv := NewServer(wa)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv()
	if err != nil || ev.GetConn().GetState() != "qr_pending" {
		t.Fatalf("first event = %v, %v", ev, err)
	}
	ev, err = stream.Recv()
	if err != nil || ev.GetQr().GetCode() != "QR-1" {
		t.Fatalf("second event = %v, %v", ev, err)
	}

	ping, err := cli.Ping(ctx, &workerv1.PingRequest{})
	if err != nil || ping.State != "qr_pending" {
		t.Fatalf("ping = %v, %v", ping, err)
	}

	pair, err := cli.PairPhone(ctx, &workerv1.PairPhoneRequest{Phone: "5511999999999"})
	if err != nil || pair.Code != "ABCD-EFGH" {
		t.Fatalf("pair = %v, %v", pair, err)
	}

	if _, err := cli.Logout(ctx, &workerv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	ev, err = stream.Recv()
	if err != nil || ev.GetConn().GetState() != "logged_out" {
		t.Fatalf("after logout = %v, %v", ev, err)
	}
	if wa.logouts != 1 {
		t.Fatalf("logouts = %d", wa.logouts)
	}
}

func TestServerDropsQRSnapshotAfterConnect(t *testing.T) {
	wa := &stubWA{}
	srv := NewServer(wa)
	srv.Start(context.Background())
	srv.ConnState("connected", "", "5511@s.whatsapp.net")

	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := stream.Recv()
	if ev.GetConn().GetState() != "connected" {
		t.Fatalf("snapshot = %v", ev)
	}
	srv.QR("late", time.Minute) // QR after connect must not be replayed later, but is broadcast live
	ev, _ = stream.Recv()
	if ev.GetQr().GetCode() != "late" {
		t.Fatalf("live = %v", ev)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test -race ./internal/worker/`
Expected: FAIL (`undefined: NewServer`).

- [ ] **Step 3: Implementar `internal/worker/server.go`**

```go
// Package worker runs one WhatsApp connection and serves it over gRPC to the supervisor.
package worker

import (
	"context"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

// Sink receives connection events from a WAClient.
type Sink interface {
	ConnState(state, reason, jid string)
	QR(code string, timeout time.Duration)
}

// WAClient is the WhatsApp connection used by Server; whatsmeow in production, stubs in tests.
type WAClient interface {
	// Run starts connecting (pairing via QR when there is no session) and returns once started.
	Run(ctx context.Context, sink Sink) error
	PairPhone(ctx context.Context, phone string) (string, error)
	Logout(ctx context.Context) error
	Disconnect()
}

// Server implements workerv1.WorkerServer and fans out WAClient events to subscribers.
type Server struct {
	workerv1.UnimplementedWorkerServer

	wa   WAClient
	mu   sync.Mutex
	conn *workerv1.ConnState
	qr   *workerv1.QR
	subs map[chan *workerv1.Event]struct{}
}

func NewServer(wa WAClient) *Server {
	return &Server{wa: wa, subs: map[chan *workerv1.Event]struct{}{}}
}

// Start runs the WAClient with the server as its sink.
func (s *Server) Start(ctx context.Context) error { return s.wa.Run(ctx, s) }

// Stop disconnects the WAClient.
func (s *Server) Stop() { s.wa.Disconnect() }

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

func (s *Server) QR(code string, timeout time.Duration) {
	q := &workerv1.QR{Code: code, TimeoutMs: timeout.Milliseconds()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn.GetState() == "qr_pending" {
		s.qr = q
	}
	s.broadcast(&workerv1.Event{Kind: &workerv1.Event_Qr{Qr: q}})
}

// broadcast must be called with s.mu held. A full subscriber buffer drops the event;
// the supervisor consumes continuously, and state is re-sent on every change.
func (s *Server) broadcast(ev *workerv1.Event) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) Events(_ *workerv1.EventsRequest, stream workerv1.Worker_EventsServer) error {
	ch := make(chan *workerv1.Event, 256)
	s.mu.Lock()
	if s.conn != nil {
		ch <- &workerv1.Event{Kind: &workerv1.Event_Conn{Conn: s.conn}}
	}
	if s.qr != nil {
		ch <- &workerv1.Event{Kind: &workerv1.Event_Qr{Qr: s.qr}}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-ch:
			if err := stream.Send(ev); err != nil {
				return err
			}
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
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go mod tidy && go test -race ./internal/worker/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/server.go internal/worker/server_test.go go.mod go.sum
git commit -m "feat(worker): servidor gRPC com hub de eventos e snapshot"
```

### Task 8: Adaptador whatsmeow e entrada do worker

**Files:**
- Create: `internal/worker/whatsmeow.go`, `internal/worker/run.go`

Este código fala com o WhatsApp real e não tem teste unitário (é coberto pelo teste manual da Task 17). Mantenha-o fino: só traduz eventos.

- [ ] **Step 1: Implementar `internal/worker/whatsmeow.go`**

```go
package worker

import (
	"context"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Whatsmeow adapts a whatsmeow client to WAClient.
type Whatsmeow struct {
	cli  *whatsmeow.Client
	log  *slog.Logger
	sink Sink
}

func NewWhatsmeow(cli *whatsmeow.Client, log *slog.Logger) *Whatsmeow {
	return &Whatsmeow{cli: cli, log: log}
}

func (w *Whatsmeow) Run(ctx context.Context, sink Sink) error {
	w.sink = sink
	w.cli.AddEventHandler(w.handle)
	if w.cli.Store.ID != nil {
		sink.ConnState("connecting", "", w.jid())
		go w.connectLoop(ctx)
		return nil
	}
	go w.qrLoop(ctx)
	return nil
}

// connectLoop retries the initial connection (e.g. no network at boot) instead of
// exiting. With whatsmeow's InitialAutoReconnect enabled, Connect returns nil and
// retries internally, so this loop simply ends. Without it, we retry here instead of
// exiting, which would trip the supervisor's circuit breaker. Once connected,
// whatsmeow's own auto-reconnect takes over.
func (w *Whatsmeow) connectLoop(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		err := w.cli.Connect()
		if err == nil {
			return
		}
		w.log.Warn("connect failed; retrying", "err", err, "in", delay)
		w.sink.ConnState("reconnecting", "connect_failed", w.jid())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

// qrLoop keeps offering QR codes until pairing succeeds or ctx ends.
func (w *Whatsmeow) qrLoop(ctx context.Context) {
	for ctx.Err() == nil {
		ch, err := w.cli.GetQRChannel(ctx)
		if err != nil {
			w.log.Error("get qr channel", "err", err)
			return
		}
		if err := w.cli.Connect(); err != nil {
			w.log.Error("connect for pairing", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		announced := false
		for item := range ch {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				// Only report qr_pending once the socket is ready: PairPhone must wait for the first QR.
				if !announced {
					w.sink.ConnState("qr_pending", "", "")
					announced = true
				}
				w.sink.QR(item.Code, item.Timeout)
			case whatsmeow.QRChannelSuccess.Event:
				return
			case whatsmeow.QRChannelEventError:
				w.log.Warn("pairing error", "err", item.Error)
			}
		}
		// Channel closed without success (codes exhausted): start a new round.
		w.cli.Disconnect()
	}
}

func (w *Whatsmeow) handle(evt any) {
	loggedIn := w.cli.Store.ID != nil
	switch e := evt.(type) {
	case *events.PairSuccess:
		w.sink.ConnState("connecting", "", e.ID.String())
	case *events.Connected, *events.KeepAliveRestored:
		w.sink.ConnState("connected", "", w.jid())
	case *events.Disconnected:
		if loggedIn {
			w.sink.ConnState("reconnecting", "disconnected", w.jid())
		}
	case *events.KeepAliveTimeout:
		if loggedIn {
			w.sink.ConnState("reconnecting", "keepalive_timeout", w.jid())
		}
	case *events.LoggedOut:
		w.sink.ConnState("logged_out", e.Reason.String(), "")
	case *events.StreamReplaced:
		// Another client took over this session; stop instead of fighting for it.
		w.sink.ConnState("logged_out", "stream_replaced", w.jid())
	case *events.TemporaryBan:
		w.sink.ConnState("banned", e.String(), w.jid())
	case *events.ClientOutdated:
		w.sink.ConnState("failed", "client_outdated", w.jid())
	}
}

func (w *Whatsmeow) jid() string {
	if w.cli.Store.ID == nil {
		return ""
	}
	return w.cli.Store.ID.String()
}

func (w *Whatsmeow) PairPhone(ctx context.Context, phone string) (string, error) {
	return w.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
}

// Logout unlinks the device. whatsmeow emits no event for a self-initiated logout,
// so we report logged_out ourselves.
func (w *Whatsmeow) Logout(ctx context.Context) error {
	if err := w.cli.Logout(ctx); err != nil {
		return err
	}
	w.sink.ConnState("logged_out", "api", "")
	return nil
}

func (w *Whatsmeow) Disconnect() { w.cli.Disconnect() }
```

- [ ] **Step 2: Implementar `internal/worker/run.go`**

```go
package worker

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/grpc"
	_ "modernc.org/sqlite"
)

type Options struct {
	InstanceID string
	Socket     string
	DataDir    string
}

// Run is the body of `litezap worker`. It blocks until ctx is cancelled or serving fails.
func Run(ctx context.Context, opt Options, log *slog.Logger) error {
	if opt.InstanceID == "" || opt.Socket == "" || opt.DataDir == "" {
		return fmt.Errorf("worker: --instance, --socket and --data-dir are required")
	}
	dir := filepath.Join(opt.DataDir, "instances", opt.InstanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// whatsmeow's sqlstore requires foreign keys; modernc registers the "sqlite" driver,
	// so we open the DB ourselves and tell whatsmeow to use the "sqlite3" dialect.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "session.db")+
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open session db: %w", err)
	}
	defer db.Close()
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Noop)
	if err := container.Upgrade(ctx); err != nil {
		return fmt.Errorf("upgrade session db: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}
	cli := whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false))
	srv := NewServer(NewWhatsmeow(cli, log))

	_ = os.Remove(opt.Socket)
	lis, err := net.Listen("unix", opt.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opt.Socket, err)
	}
	g := grpc.NewServer()
	workerv1.RegisterWorkerServer(g, srv)

	go func() {
		<-ctx.Done()
		srv.Stop()
		// Events streams never finish on their own; give unary calls a moment, then force.
		done := make(chan struct{})
		go func() { g.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			g.Stop()
		}
	}()

	if err := srv.Start(ctx); err != nil {
		g.Stop()
		return fmt.Errorf("start whatsapp client: %w", err)
	}
	log.Info("worker serving", "socket", opt.Socket)
	if err := g.Serve(lis); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
```

- [ ] **Step 3: Compilar e vet**

Run: `go mod tidy && go build ./... && go vet ./internal/worker/`
Expected: sem erros. (Se o whatsmeow tiver mudado alguma assinatura, ajuste conforme `go doc go.mau.fi/whatsmeow.Client`.)

- [ ] **Step 4: Commit**

```bash
git add internal/worker go.mod go.sum
git commit -m "feat(worker): adaptador whatsmeow e entrada do subcomando worker"
```

---

## Chunk 3: Spawner, worker fake e manager

### Task 9: Interfaces do spawner e caminho do socket

**Files:**
- Create: `internal/spawner/spawner.go`, `internal/spawner/socket.go`
- Test: `internal/spawner/socket_test.go`

- [ ] **Step 1: Escrever o teste**

```go
package spawner

import "testing"

func TestSocketPathIsShortAndStable(t *testing.T) {
	long := "/Users/someone/Library/Application Support/LiteZap/a/very/long/data/directory/that/keeps/going"
	a := SocketPath(long, "inst_0123456789abcdef")
	if len(a) >= 100 {
		t.Fatalf("socket path too long (%d): %s", len(a), a)
	}
	if a != SocketPath(long, "inst_0123456789abcdef") {
		t.Fatal("not deterministic")
	}
	if a == SocketPath(long, "inst_other") || a == SocketPath("/other", "inst_0123456789abcdef") {
		t.Fatal("collision between instances or data dirs")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/spawner/`
Expected: FAIL (`undefined: SocketPath`).

- [ ] **Step 3: Implementar `internal/spawner/spawner.go`**

```go
// Package spawner starts and stops instance workers.
package spawner

import (
	"context"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

// WorkerHandle controls one running worker.
type WorkerHandle interface {
	Client() workerv1.WorkerClient
	// Done is closed when the worker has exited.
	Done() <-chan struct{}
	// Kill stops the worker immediately and waits for it to exit.
	Kill() error
	// Close asks the worker to stop gracefully, killing it if ctx expires first.
	Close(ctx context.Context) error
}

// Spawner starts workers. ProcessSpawner runs local processes; a container
// implementation arrives with the cloud piece.
type Spawner interface {
	Start(ctx context.Context, instanceID string) (WorkerHandle, error)
}
```

- [ ] **Step 4: Implementar `internal/spawner/socket.go`**

```go
package spawner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// SocketPath returns a short unix socket path (macOS limits paths to ~104 bytes):
// <tmp>/lz-<hash(dataDir)>/<hash(instanceID)>.sock
func SocketPath(dataDir, instanceID string) string {
	return filepath.Join(os.TempDir(), "lz-"+short(dataDir), short(instanceID)+".sock")
}

func short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `go test ./internal/spawner/`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/spawner
git commit -m "feat(spawner): interfaces e caminho curto de socket"
```

### Task 10: Process spawner

**Files:**
- Create: `internal/spawner/process.go`, `internal/spawner/detach_unix.go`, `internal/spawner/detach_windows.go`
- Test: `internal/spawner/process_test.go`

O teste usa o próprio binário de teste como worker (padrão "re-exec" via `TestMain`), servindo um `worker.Server` com um WAClient stub.

- [ ] **Step 1: Escrever os testes**

```go
package spawner

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
)

type connectedWA struct{}

func (connectedWA) Run(ctx context.Context, s worker.Sink) error {
	s.ConnState("connected", "", "x@s.whatsapp.net")
	return nil
}
func (connectedWA) PairPhone(context.Context, string) (string, error) { return "", nil }
func (connectedWA) Logout(context.Context) error                    { return nil }
func (connectedWA) Disconnect()                                     {}

func TestMain(m *testing.M) {
	switch os.Getenv("LITEZAP_TEST_WORKER") {
	case "serve":
		lis, err := net.Listen("unix", os.Getenv("LITEZAP_TEST_SOCKET"))
		if err != nil {
			os.Exit(3)
		}
		srv := worker.NewServer(connectedWA{})
		srv.Start(context.Background())
		g := grpc.NewServer()
		workerv1.RegisterWorkerServer(g, srv)
		go g.Serve(lis)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		g.Stop()
		os.Exit(0)
	case "exit":
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func testSpawner(t *testing.T, mode string) *Process {
	return &Process{
		DataDir:      t.TempDir(),
		StartTimeout: 5 * time.Second,
		Log:          slog.New(slog.DiscardHandler),
		Command: func(id, sock string) *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), "LITEZAP_TEST_WORKER="+mode, "LITEZAP_TEST_SOCKET="+sock)
			return cmd
		},
	}
}

func TestProcessStartPingClose(t *testing.T) {
	p := testSpawner(t, "serve")
	ctx := context.Background()
	h, err := p.Start(ctx, "inst_a")
	if err != nil {
		t.Fatal(err)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := h.Client().Ping(pctx, &workerv1.PingRequest{})
	if err != nil || resp.State != "connected" {
		t.Fatalf("ping = %v, %v", resp, err)
	}
	if _, err := os.Stat(p.pidFile("inst_a")); err != nil {
		t.Fatalf("pid file missing: %v", err)
	}
	if err := h.Close(pctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit")
	}
}

func TestProcessKill(t *testing.T) {
	p := testSpawner(t, "serve")
	h, err := p.Start(context.Background(), "inst_b")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Kill(); err != nil {
		t.Fatal(err)
	}
	<-h.Done()
}

func TestProcessStartFailsWhenWorkerExits(t *testing.T) {
	p := testSpawner(t, "exit")
	_, err := p.Start(context.Background(), "inst_c")
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected exit error, got %v", err)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test -race ./internal/spawner/`
Expected: FAIL (`undefined: Process`).

- [ ] **Step 3: Implementar `internal/spawner/process.go`**

```go
package spawner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// CommandFunc builds the worker command for an instance.
type CommandFunc func(instanceID, socket string) *exec.Cmd

// Process starts each worker as a child process of the current executable.
type Process struct {
	DataDir       string
	MemoryLimitMB int64
	Command       CommandFunc   // nil: `<this executable> worker ...`
	StartTimeout  time.Duration // default 10s
	Log           *slog.Logger
}

func (p *Process) pidFile(id string) string { return filepath.Join(p.DataDir, "run", id+".pid") }

func (p *Process) command(id, sock string) *exec.Cmd {
	if p.Command != nil {
		return p.Command(id, sock)
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exec.Command(exe, "worker", "--instance", id, "--socket", sock, "--data-dir", p.DataDir)
}

func (p *Process) Start(ctx context.Context, id string) (WorkerHandle, error) {
	sock := SocketPath(p.DataDir, id)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(p.DataDir, "run"), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(sock)

	cmd := p.command(id, sock)
	if p.MemoryLimitMB > 0 {
		cmd.Env = append(cmd.Environ(), fmt.Sprintf("GOMEMLIMIT=%dMiB", p.MemoryLimitMB))
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	detach(cmd) // own process group: Ctrl+C on the supervisor must not hit workers directly
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	h := &procHandle{cmd: cmd, done: make(chan struct{}), pidFile: p.pidFile(id)}
	_ = os.WriteFile(h.pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	go func() {
		_ = cmd.Wait()
		_ = os.Remove(h.pidFile)
		close(h.done)
	}()

	timeout := p.StartTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if err := waitForSocket(ctx, sock, h.done, timeout); err != nil {
		_ = h.Kill()
		return nil, err
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = h.Kill()
		return nil, fmt.Errorf("dial worker: %w", err)
	}
	h.conn = conn
	h.client = workerv1.NewWorkerClient(conn)
	// The socket file exists from bind(), slightly before listen(); wait until the worker answers.
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() { // abort the readiness wait if the worker dies
		select {
		case <-h.done:
			cancel()
		case <-pctx.Done():
		}
	}()
	if _, err := h.client.Ping(pctx, &workerv1.PingRequest{}, grpc.WaitForReady(true)); err != nil {
		_ = h.Kill()
		return nil, fmt.Errorf("worker not answering: %w", err)
	}
	return h, nil
}

func waitForSocket(ctx context.Context, sock string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return errors.New("worker exited before opening its socket")
		case <-deadline.C:
			return fmt.Errorf("worker socket %s not ready after %s", sock, timeout)
		case <-tick.C:
		}
	}
}

type procHandle struct {
	cmd       *exec.Cmd
	done      chan struct{}
	pidFile   string
	conn      *grpc.ClientConn
	client    workerv1.WorkerClient
	closeOnce sync.Once
}

func (h *procHandle) Client() workerv1.WorkerClient { return h.client }
func (h *procHandle) Done() <-chan struct{}         { return h.done }

func (h *procHandle) closeConn() {
	h.closeOnce.Do(func() {
		if h.conn != nil {
			h.conn.Close()
		}
	})
}

func (h *procHandle) Kill() error {
	h.closeConn()
	err := h.cmd.Process.Kill()
	<-h.done
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (h *procHandle) Close(ctx context.Context) error {
	h.closeConn()
	if runtime.GOOS == "windows" {
		return h.Kill() // no SIGINT for child processes on Windows
	}
	_ = h.cmd.Process.Signal(os.Interrupt)
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return h.Kill()
	}
}

// CleanupOrphans kills workers left behind by a previous supervisor that crashed.
// Only PIDs whose command line still looks like a litezap worker are killed.
func CleanupOrphans(dataDir string, log *slog.Logger) {
	files, _ := filepath.Glob(filepath.Join(dataDir, "run", "*.pid"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		_ = os.Remove(f)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || !isWorkerProcess(pid) {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			log.Warn("killing orphan worker", "pid", pid, "pid_file", f)
			_ = proc.Kill()
		}
	}
}

func isWorkerProcess(pid int) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	cmd := string(out)
	return strings.Contains(cmd, " worker ") && strings.Contains(cmd, "--instance")
}
```

- [ ] **Step 4: Implementar `detach_unix.go` e `detach_windows.go`**

```go
//go:build !windows

package spawner

import (
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
```

```go
//go:build windows

package spawner

import "os/exec"

func detach(cmd *exec.Cmd) {}
```

- [ ] **Step 5: Rodar e ver passar**

Run: `go test -race ./internal/spawner/`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/spawner go.mod go.sum
git commit -m "feat(spawner): spawner de processos com pid files e limpeza de órfãos"
```

### Task 11: Worker fake e spawner fake para testes

**Files:**
- Create: `internal/workertest/fake.go`
- Test: `internal/workertest/fake_test.go`

Reaproveita `worker.Server` (mesmo hub de eventos da produção) com um WAClient roteirizável, servido em memória via bufconn.

- [ ] **Step 1: Escrever o teste de sanidade**

```go
package workertest

import (
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

func TestFakeEmitsAndCrashes(t *testing.T) {
	sp := NewSpawner()
	sp.OnStart = func(f *Fake) { f.Emit("qr_pending"); f.EmitQR("QR-9") }
	h, err := sp.Start(context.Background(), "inst_x")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := h.Client().Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ev, _ := stream.Recv(); ev.GetConn().GetState() != "qr_pending" {
		t.Fatalf("got %v", ev)
	}
	if ev, _ := stream.Recv(); ev.GetQr().GetCode() != "QR-9" {
		t.Fatalf("got %v", ev)
	}
	if sp.Starts("inst_x") != 1 || sp.Last("inst_x") == nil {
		t.Fatal("start not recorded")
	}
	sp.Last("inst_x").Crash()
	select {
	case <-h.Done():
	case <-time.After(time.Second):
		t.Fatal("done not closed")
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test -race ./internal/workertest/`
Expected: FAIL (`undefined: NewSpawner`).

- [ ] **Step 3: Implementar `internal/workertest/fake.go`**

```go
// Package workertest provides an in-memory worker and spawner for supervisor tests.
package workertest

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const testJID = "5511999999999:7@s.whatsapp.net"

// FakeWA is a scriptable worker.WAClient.
type FakeWA struct {
	mu       sync.Mutex
	sink     worker.Sink
	PairCode string
	Logouts  atomic.Int32
}

func (f *FakeWA) Run(ctx context.Context, sink worker.Sink) error {
	f.mu.Lock()
	f.sink = sink
	f.mu.Unlock()
	return nil
}

func (f *FakeWA) getSink() worker.Sink {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink
}

func (f *FakeWA) PairPhone(context.Context, string) (string, error) { return f.PairCode, nil }

func (f *FakeWA) Logout(context.Context) error {
	f.Logouts.Add(1)
	f.getSink().ConnState("logged_out", "api", "")
	return nil
}

func (f *FakeWA) Disconnect() {}

// Fake is one in-memory worker; it implements spawner.WorkerHandle.
type Fake struct {
	ID        string
	WA        *FakeWA
	PingBlock atomic.Bool
	Closed    atomic.Bool

	server *worker.Server
	grpc   *grpc.Server
	cc     *grpc.ClientConn
	client workerv1.WorkerClient
	done   chan struct{}
	once   sync.Once
}

type fakeService struct {
	*worker.Server
	f *Fake
}

func (s fakeService) Ping(ctx context.Context, req *workerv1.PingRequest) (*workerv1.PingResponse, error) {
	if s.f.PingBlock.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.Server.Ping(ctx, req)
}

func newFake(id string) *Fake {
	f := &Fake{ID: id, WA: &FakeWA{PairCode: "PAIR-CODE"}, done: make(chan struct{})}
	f.server = worker.NewServer(f.WA)
	_ = f.server.Start(context.Background())
	lis := bufconn.Listen(1 << 20)
	f.grpc = grpc.NewServer()
	workerv1.RegisterWorkerServer(f.grpc, fakeService{Server: f.server, f: f})
	go f.grpc.Serve(lis)
	f.cc, _ = grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	f.client = workerv1.NewWorkerClient(f.cc)
	return f
}

// Emit reports a connection state from the fake WhatsApp connection.
func (f *Fake) Emit(state string) {
	jid := ""
	if state == "connected" {
		jid = testJID
	}
	f.WA.getSink().ConnState(state, "", jid)
}

func (f *Fake) EmitQR(code string) { f.WA.getSink().QR(code, time.Minute) }

// Crash simulates the worker process dying.
func (f *Fake) Crash() {
	f.once.Do(func() {
		close(f.done)
		f.grpc.Stop()
		f.cc.Close()
	})
}

func (f *Fake) Client() workerv1.WorkerClient { return f.client }
func (f *Fake) Done() <-chan struct{}         { return f.done }
func (f *Fake) Kill() error                   { f.Crash(); return nil }
// Close mimics a graceful stop: in-flight RPCs (e.g. Logout) finish before the fake dies.
func (f *Fake) Close(context.Context) error {
	f.Closed.Store(true)
	done := make(chan struct{})
	go func() { f.grpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
	f.Crash()
	return nil
}

// Spawner is a spawner.Spawner that creates Fakes and records every start.
type Spawner struct {
	mu        sync.Mutex
	fakes     map[string][]*Fake
	OnStart   func(*Fake)
	FailStart error
}

func NewSpawner() *Spawner { return &Spawner{fakes: map[string][]*Fake{}} }

func (s *Spawner) Start(ctx context.Context, id string) (spawner.WorkerHandle, error) {
	s.mu.Lock()
	fail, onStart := s.FailStart, s.OnStart
	s.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	f := newFake(id)
	s.mu.Lock()
	s.fakes[id] = append(s.fakes[id], f)
	s.mu.Unlock()
	if onStart != nil {
		onStart(f)
	}
	return f, nil
}

func (s *Spawner) Starts(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.fakes[id])
}

func (s *Spawner) Last(id string) *Fake {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.fakes[id]
	if len(l) == 0 {
		return nil
	}
	return l[len(l)-1]
}

func (s *Spawner) Nth(id string, n int) *Fake {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakes[id][n]
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go test -race ./internal/workertest/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/workertest
git commit -m "test(workertest): worker e spawner fakes em memória"
```

### Task 12: Backoff, breaker e manager

**Files:**
- Create: `internal/manager/backoff.go`, `internal/manager/manager.go`
- Test: `internal/manager/backoff_test.go`, `internal/manager/manager_test.go`

- [ ] **Step 1: Escrever `backoff_test.go`**

```go
package manager

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base, max := time.Second, 5*time.Minute
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := Backoff(i, base, max); got != w {
			t.Errorf("attempt %d = %s, want %s", i, got, w)
		}
	}
	if got := Backoff(30, base, max); got != max {
		t.Errorf("capped = %s", got)
	}
}

func TestBreaker(t *testing.T) {
	b := Breaker{Max: 3, Window: time.Minute}
	t0 := time.Now()
	if b.Record(t0) || b.Record(t0.Add(10*time.Second)) {
		t.Fatal("opened too early")
	}
	if !b.Record(t0.Add(20 * time.Second)) {
		t.Fatal("should open at 3 crashes inside window")
	}
	b2 := Breaker{Max: 3, Window: time.Minute}
	b2.Record(t0)
	b2.Record(t0.Add(30 * time.Second))
	if b2.Record(t0.Add(2 * time.Minute)) {
		t.Fatal("old crashes must fall out of the window")
	}
}
```

- [ ] **Step 2: Implementar `backoff.go`**

```go
package manager

import "time"

// Backoff returns base * 2^attempt, capped at max.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// Breaker opens after Max crashes inside Window.
type Breaker struct {
	Max    int
	Window time.Duration
	times  []time.Time
}

// Record registers a crash at now and reports whether the breaker is open.
func (b *Breaker) Record(now time.Time) bool {
	cut := now.Add(-b.Window)
	kept := b.times[:0]
	for _, t := range b.times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	b.times = append(kept, now)
	return len(b.times) >= b.Max
}
```

- [ ] **Step 3: Rodar os testes de backoff**

Run: `go test ./internal/manager/ -run 'TestBackoff|TestBreaker'`
Expected: `ok`.

- [ ] **Step 4: Escrever `manager_test.go`**

```go
package manager

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

func testOptions() Options {
	return Options{
		HeartbeatInterval: 30 * time.Millisecond,
		HeartbeatFailures: 2,
		BackoffBase:       time.Millisecond,
		BackoffMax:        5 * time.Millisecond,
		BreakerMax:        3,
		BreakerWindow:     time.Minute,
		StopTimeout:       time.Second,
	}
}

func setup(t *testing.T, ids ...string) (*Manager, *store.Store, *workertest.Spawner) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range ids {
		if err := st.CreateInstance(context.Background(), store.Instance{ID: id, Name: id, State: instance.Created, TokenHash: "h-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	sp := workertest.NewSpawner()
	m := New(st, sp, testOptions(), slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})
	return m, st, sp
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stateIs(t *testing.T, st *store.Store, id string, want instance.State) func() bool {
	return func() bool {
		in, err := st.GetInstance(context.Background(), id)
		return err == nil && in.State == want
	}
}

func TestStartReachesQRPendingAndExposesQR(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-1") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "qr_pending", stateIs(t, st, "i1", instance.QRPending))
	eventually(t, "qr code", func() bool { c, ok := m.QR("i1"); return ok && c == "QR-1" })

	sp.Last("i1").Emit("connected")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if _, ok := m.QR("i1"); ok {
		t.Fatal("QR must be cleared after connect")
	}
	in, _ := st.GetInstance(context.Background(), "i1")
	if in.JID == "" {
		t.Fatal("JID not saved")
	}
}

func TestCrashRestartsWorker(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connecting"); f.Emit("connected") }
	m.Start(context.Background(), "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	sp.Last("i1").Crash()
	eventually(t, "second start", func() bool { return sp.Starts("i1") == 2 })
	eventually(t, "connected again", stateIs(t, st, "i1", instance.Connected))
}

func TestCrashLoopOpensBreaker(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Crash() }
	m.Start(context.Background(), "i1")
	eventually(t, "failed", stateIs(t, st, "i1", instance.Failed))
	eventually(t, "not running", func() bool { return !m.Running("i1") })
	if n := sp.Starts("i1"); n != 3 {
		t.Fatalf("starts = %d, want 3", n)
	}
}

func TestHeartbeatFailureKillsAndRestarts(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) {
		if sp.Starts("i1") == 1 {
			f.PingBlock.Store(true)
		}
		f.Emit("connected")
	}
	m.Start(context.Background(), "i1")
	eventually(t, "restart after heartbeat failure", func() bool { return sp.Starts("i1") == 2 })
	select {
	case <-sp.Nth("i1", 0).Done():
	default:
		t.Fatal("hung worker should have been killed")
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
}

func TestLogoutIsTerminal(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	m.Start(context.Background(), "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if err := m.Logout(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "logged_out", stateIs(t, st, "i1", instance.LoggedOut))
	eventually(t, "not running", func() bool { return !m.Running("i1") })
	if !sp.Last("i1").Closed.Load() || sp.Starts("i1") != 1 {
		t.Fatal("worker should be closed once and not restarted")
	}
	if err := m.Logout(context.Background(), "i1"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestStartRightAfterLogoutRestarts(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if err := m.Logout(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second start", func() bool { return sp.Starts("i1") == 2 })
	eventually(t, "connected again", stateIs(t, st, "i1", instance.Connected))
}

func TestStopAndRestart(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if err := m.Stop(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped", stateIs(t, st, "i1", instance.Stopped))
	if m.Running("i1") || !sp.Last("i1").Closed.Load() {
		t.Fatal("worker should be closed after stop")
	}
	if err := m.Restart(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected after restart", stateIs(t, st, "i1", instance.Connected))
	if sp.Starts("i1") != 2 {
		t.Fatalf("starts = %d", sp.Starts("i1"))
	}
}

func TestBootStartsOnlyActiveInstances(t *testing.T) {
	m, st, sp := setup(t, "on", "off", "out")
	ctx := context.Background()
	st.UpdateInstanceState(ctx, "on", instance.Connected, "")
	st.UpdateInstanceState(ctx, "off", instance.Stopped, "")
	st.UpdateInstanceState(ctx, "out", instance.LoggedOut, "")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "on started", func() bool { return sp.Starts("on") == 1 })
	time.Sleep(50 * time.Millisecond)
	if sp.Starts("off") != 0 || sp.Starts("out") != 0 {
		t.Fatal("stopped/logged_out instances must not boot")
	}
}

func TestPairPhone(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending") }
	m.Start(context.Background(), "i1")
	eventually(t, "qr_pending", stateIs(t, st, "i1", instance.QRPending))
	code, err := m.PairPhone(context.Background(), "i1", "5511999999999")
	if err != nil || code != "PAIR-CODE" {
		t.Fatalf("code = %q, %v", code, err)
	}
}

func TestStartRefusesBanned(t *testing.T) {
	m, st, _ := setup(t, "i1")
	st.UpdateInstanceState(context.Background(), "i1", instance.Banned, "")
	if err := m.Start(context.Background(), "i1"); !errors.Is(err, ErrBanned) {
		t.Fatalf("expected ErrBanned, got %v", err)
	}
}
```

- [ ] **Step 5: Rodar e ver falhar**

Run: `go test -race ./internal/manager/`
Expected: FAIL (`undefined: New`).

- [ ] **Step 6: Implementar `manager.go`**

```go
// Package manager supervises instance workers: lifecycle, heartbeat, restart and circuit breaker.
package manager

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/store"
)

var (
	ErrNotRunning = errors.New("instance worker is not running")
	ErrBanned     = errors.New("instance is banned")
)

type Options struct {
	HeartbeatInterval time.Duration
	HeartbeatFailures int
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	BreakerMax        int
	BreakerWindow     time.Duration
	StopTimeout       time.Duration
}

// DefaultOptions follows spec section 9.2.
func DefaultOptions() Options {
	return Options{
		HeartbeatInterval: 10 * time.Second,
		HeartbeatFailures: 3,
		BackoffBase:       time.Second,
		BackoffMax:        5 * time.Minute,
		BreakerMax:        5,
		BreakerWindow:     10 * time.Minute,
		StopTimeout:       10 * time.Second,
	}
}

type Manager struct {
	store   *store.Store
	spawner spawner.Spawner
	opt     Options
	log     *slog.Logger

	mu      sync.Mutex
	running map[string]*supervised
}

type supervised struct {
	id      string
	cancel  context.CancelFunc
	done    chan struct{}
	exiting atomic.Bool // supervise is returning; a new Start must wait for done
	breaker Breaker

	mu     sync.Mutex
	handle spawner.WorkerHandle
	qr     string
}

func (s *supervised) setHandle(h spawner.WorkerHandle) { s.mu.Lock(); s.handle = h; s.mu.Unlock() }
func (s *supervised) getHandle() spawner.WorkerHandle   { s.mu.Lock(); defer s.mu.Unlock(); return s.handle }
func (s *supervised) setQR(c string)                    { s.mu.Lock(); s.qr = c; s.mu.Unlock() }
func (s *supervised) getQR() string                     { s.mu.Lock(); defer s.mu.Unlock(); return s.qr }

func New(st *store.Store, sp spawner.Spawner, opt Options, log *slog.Logger) *Manager {
	return &Manager{store: st, spawner: sp, opt: opt, log: log, running: map[string]*supervised{}}
}

// Start begins supervising an instance; it is a no-op if already running.
func (m *Manager) Start(ctx context.Context, id string) error {
	in, err := m.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	if in.State == instance.Banned {
		return ErrBanned
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		old, ok := m.running[id]
		if !ok {
			break
		}
		if !old.exiting.Load() {
			return nil
		}
		// The previous supervisor is shutting down (e.g. just logged out): wait for it.
		m.mu.Unlock()
		select {
		case <-old.done:
		case <-ctx.Done():
			m.mu.Lock()
			return ctx.Err()
		}
		m.mu.Lock()
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &supervised{
		id: id, cancel: cancel, done: make(chan struct{}),
		breaker: Breaker{Max: m.opt.BreakerMax, Window: m.opt.BreakerWindow},
	}
	m.running[id] = s
	go m.supervise(sctx, s)
	return nil
}

// halt stops supervision and waits for the worker to exit, without changing the stored state.
func (m *Manager) halt(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if _, err := m.store.GetInstance(ctx, id); err != nil {
		return err
	}
	if err := m.halt(ctx, id); err != nil {
		return err
	}
	m.setState(id, instance.Stopped, "")
	return nil
}

func (m *Manager) Restart(ctx context.Context, id string) error {
	if err := m.halt(ctx, id); err != nil {
		return err
	}
	return m.Start(ctx, id)
}

// Shutdown halts every worker but keeps stored states, so Boot resumes them.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.halt(ctx, id) }()
	}
	wg.Wait()
}

// Boot starts every instance that was not stopped or waiting for human action.
func (m *Manager) Boot(ctx context.Context) error {
	list, err := m.store.ListInstances(ctx)
	if err != nil {
		return err
	}
	for _, in := range list {
		if in.State == instance.Created || in.State.Active() {
			if err := m.Start(ctx, in.ID); err != nil {
				m.log.Error("boot instance", "instance_id", in.ID, "err", err)
			}
		}
	}
	return nil
}

func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

func (m *Manager) handle(id string) (spawner.WorkerHandle, error) {
	m.mu.Lock()
	s := m.running[id]
	m.mu.Unlock()
	if s == nil {
		return nil, ErrNotRunning
	}
	h := s.getHandle()
	if h == nil {
		return nil, ErrNotRunning
	}
	return h, nil
}

// QR returns the latest QR code while the instance is waiting to be paired.
func (m *Manager) QR(id string) (string, bool) {
	m.mu.Lock()
	s := m.running[id]
	m.mu.Unlock()
	if s == nil {
		return "", false
	}
	c := s.getQR()
	return c, c != ""
}

func (m *Manager) PairPhone(ctx context.Context, id, phone string) (string, error) {
	h, err := m.handle(id)
	if err != nil {
		return "", err
	}
	resp, err := h.Client().PairPhone(ctx, &workerv1.PairPhoneRequest{Phone: phone})
	if err != nil {
		return "", err
	}
	return resp.Code, nil
}

// Logout unlinks the number; the worker then reports logged_out, which ends supervision.
func (m *Manager) Logout(ctx context.Context, id string) error {
	h, err := m.handle(id)
	if err != nil {
		return err
	}
	_, err = h.Client().Logout(ctx, &workerv1.LogoutRequest{})
	return err
}

func (m *Manager) supervise(ctx context.Context, s *supervised) {
	defer func() {
		s.exiting.Store(true)
		m.mu.Lock()
		if m.running[s.id] == s {
			delete(m.running, s.id)
		}
		m.mu.Unlock()
		close(s.done)
	}()
	attempt := 0
	for ctx.Err() == nil {
		m.setState(s.id, instance.Starting, "")
		h, err := m.spawner.Start(ctx, s.id)
		if err != nil {
			m.log.Error("spawn worker", "instance_id", s.id, "err", err)
		} else {
			s.setHandle(h)
			terminal, healthy := m.runWorker(ctx, s, h)
			s.setHandle(nil)
			s.setQR("")
			if terminal || ctx.Err() != nil {
				return
			}
			if healthy {
				attempt = 0
			}
		}
		if s.breaker.Record(time.Now()) {
			s.exiting.Store(true)
			m.log.Error("worker crash loop; giving up", "instance_id", s.id)
			m.setState(s.id, instance.Failed, "crash_loop")
			return
		}
		d := Backoff(attempt, m.opt.BackoffBase, m.opt.BackoffMax)
		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// runWorker consumes worker events and heartbeats until the worker ends.
// terminal: supervision must stop. healthy: the worker reached connected.
func (m *Manager) runWorker(ctx context.Context, s *supervised, h spawner.WorkerHandle) (terminal, healthy bool) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	closeWorker := func() {
		cancel() // end our Events stream first so the worker's graceful stop is not held open
		cctx, c := context.WithTimeout(context.Background(), m.opt.StopTimeout)
		defer c()
		_ = h.Close(cctx)
	}

	stream, err := h.Client().Events(sctx, &workerv1.EventsRequest{})
	if err != nil {
		m.log.Warn("subscribe worker events", "instance_id", s.id, "err", err)
		_ = h.Kill()
		return false, false
	}
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
	defer tick.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			s.exiting.Store(true)
			closeWorker()
			return true, healthy
		case <-h.Done():
			m.log.Warn("worker exited", "instance_id", s.id)
			return false, healthy
		case err := <-errc:
			m.log.Warn("worker event stream ended", "instance_id", s.id, "err", err)
			_ = h.Kill()
			return false, healthy
		case ev := <-evs:
			if qr := ev.GetQr(); qr != nil {
				s.setQR(qr.Code)
				continue
			}
			c := ev.GetConn()
			if c == nil {
				continue
			}
			st := instance.State(c.State)
			if st != instance.QRPending {
				s.setQR("")
			}
			if st == instance.Connected {
				healthy = true
				if c.Jid != "" {
					if err := m.store.SetInstanceJID(context.Background(), s.id, c.Jid); err != nil {
						m.log.Error("save jid", "instance_id", s.id, "err", err)
					}
				}
			}
			m.setState(s.id, st, c.Reason)
			if st.Terminal() {
				s.exiting.Store(true) // before the (possibly slow) close, so a concurrent Start waits
				closeWorker()
				return true, healthy
			}
		case <-tick.C:
			pctx, pc := context.WithTimeout(ctx, m.opt.HeartbeatInterval)
			_, err := h.Client().Ping(pctx, &workerv1.PingRequest{})
			pc()
			if err != nil {
				fails++
			} else {
				fails = 0
			}
			if fails >= m.opt.HeartbeatFailures {
				m.log.Warn("worker heartbeat failed; killing", "instance_id", s.id, "failures", fails)
				_ = h.Kill()
				return false, healthy
			}
		}
	}
}

// setState persists a transition if the state machine allows it.
func (m *Manager) setState(id string, to instance.State, reason string) {
	ctx := context.Background()
	in, err := m.store.GetInstance(ctx, id)
	if err != nil {
		m.log.Error("load instance for state change", "instance_id", id, "err", err)
		return
	}
	if in.State == to && in.StateReason == reason {
		return
	}
	if !instance.CanTransition(in.State, to) {
		m.log.Warn("invalid state transition ignored", "instance_id", id, "from", in.State, "to", to)
		return
	}
	if err := m.store.UpdateInstanceState(ctx, id, to, reason); err != nil {
		m.log.Error("update state", "instance_id", id, "err", err)
		return
	}
	m.log.Info("instance state", "instance_id", id, "from", in.State, "to", to, "reason", reason)
}
```

- [ ] **Step 7: Rodar e ver passar (com race, várias vezes para pegar flakiness)**

Run: `go test -race -count=5 ./internal/manager/`
Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
git add internal/manager
git commit -m "feat(manager): supervisão com heartbeat, backoff e circuit breaker"
```

---

## Chunk 4: Service, API, serve e verificação

### Task 13: Formato de erro da API

**Files:**
- Create: `internal/apierr/apierr.go`
- Test: `internal/apierr/apierr_test.go`

- [ ] **Step 1: Escrever o teste**

```go
package apierr

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestWriteAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, NotConnected())
	if rec.Code != 409 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error Error `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "instance_not_connected" || !body.Error.Retryable {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWriteUnknownErrorIsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, errors.New("boom"))
	if rec.Code != 500 || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/apierr/`
Expected: FAIL.

- [ ] **Step 3: Implementar**

```go
// Package apierr defines the API error envelope {"error":{code,message,retryable}}.
package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
)

type Error struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(status int, code, msg string, retryable bool) *Error {
	return &Error{Status: status, Code: code, Message: msg, Retryable: retryable}
}

func Validation(msg string) *Error { return New(http.StatusBadRequest, "validation_failed", msg, false) }
func Unauthorized() *Error {
	return New(http.StatusUnauthorized, "unauthorized", "missing or invalid token", false)
}
func Forbidden() *Error {
	return New(http.StatusForbidden, "forbidden", "token not allowed for this resource", false)
}
func InstanceNotFound() *Error {
	return New(http.StatusNotFound, "instance_not_found", "instance not found", false)
}
func NotConnected() *Error {
	return New(http.StatusConflict, "instance_not_connected", "instance worker is not running", true)
}
func Internal() *Error {
	return New(http.StatusInternalServerError, "internal", "internal error", true)
}

// Write renders err; anything that is not an *Error becomes "internal".
func Write(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = Internal()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]*Error{"error": e})
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go test ./internal/apierr/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/apierr
git commit -m "feat(apierr): envelope de erro da API"
```

### Task 14: Service de instâncias

**Files:**
- Create: `internal/service/instances.go`

Coberto pelos testes de API da Task 15 (que usam o service real com o spawner fake). Aqui só implementamos e compilamos.

- [ ] **Step 1: Implementar**

```go
// Package service holds LiteZap use cases shared by the REST and MCP adapters.
package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

type Instances struct {
	Store   *store.Store
	Manager *manager.Manager
	DataDir string
}

type InstanceView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	State       instance.State `json:"state"`
	StateReason string         `json:"state_reason,omitempty"`
	Phone       string         `json:"phone,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func view(in store.Instance) InstanceView {
	user, _, _ := strings.Cut(in.JID, "@")
	phone, _, _ := strings.Cut(user, ":") // drop device suffix
	return InstanceView{
		ID: in.ID, Name: in.Name, State: in.State, StateReason: in.StateReason,
		Phone: phone, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
	}
}

// mapErr turns known domain errors into API errors; unknown errors pass through (rendered as internal).
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return apierr.InstanceNotFound()
	case errors.Is(err, manager.ErrNotRunning):
		return apierr.NotConnected()
	case errors.Is(err, manager.ErrBanned):
		return apierr.New(http.StatusConflict, "instance_banned", "instance is banned; delete it", false)
	}
	return err
}

func (s *Instances) Create(ctx context.Context, name string) (InstanceView, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return InstanceView{}, "", apierr.Validation("name is required")
	}
	id := secure.NewID("inst_")
	tok := secure.NewInstanceToken()
	if err := s.Store.CreateInstance(ctx, store.Instance{
		ID: id, Name: name, State: instance.Created, TokenHash: secure.HashToken(tok),
	}); err != nil {
		return InstanceView{}, "", err
	}
	if err := s.Manager.Start(ctx, id); err != nil {
		return InstanceView{}, "", mapErr(err)
	}
	v, err := s.Get(ctx, id)
	return v, tok, err
}

func (s *Instances) List(ctx context.Context) ([]InstanceView, error) {
	list, err := s.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InstanceView, 0, len(list))
	for _, in := range list {
		out = append(out, view(in))
	}
	return out, nil
}

func (s *Instances) Get(ctx context.Context, id string) (InstanceView, error) {
	in, err := s.Store.GetInstance(ctx, id)
	if err != nil {
		return InstanceView{}, mapErr(err)
	}
	return view(in), nil
}

// Delete unlinks the number when possible, stops the worker and removes all instance data.
func (s *Instances) Delete(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
	}
	if s.Manager.Running(id) {
		_ = s.Manager.Logout(ctx, id) // best effort: frees the linked-device slot on the phone
	}
	if err := s.Manager.Stop(ctx, id); err != nil {
		return mapErr(err)
	}
	if err := os.RemoveAll(filepath.Join(s.DataDir, "instances", id)); err != nil {
		return err
	}
	return mapErr(s.Store.DeleteInstance(ctx, id))
}

func (s *Instances) Start(ctx context.Context, id string) error {
	return mapErr(s.Manager.Start(ctx, id))
}

func (s *Instances) Stop(ctx context.Context, id string) error {
	return mapErr(s.Manager.Stop(ctx, id))
}

func (s *Instances) Restart(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
	}
	return mapErr(s.Manager.Restart(ctx, id))
}

func (s *Instances) Logout(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
	}
	return mapErr(s.Manager.Logout(ctx, id))
}

func (s *Instances) RotateToken(ctx context.Context, id string) (string, error) {
	tok := secure.NewInstanceToken()
	if err := s.Store.SetInstanceTokenHash(ctx, id, secure.HashToken(tok)); err != nil {
		return "", mapErr(err)
	}
	return tok, nil
}

func (s *Instances) QR(ctx context.Context, id string) (string, error) {
	in, err := s.Store.GetInstance(ctx, id)
	if err != nil {
		return "", mapErr(err)
	}
	code, ok := s.Manager.QR(id)
	if in.State != instance.QRPending || !ok {
		return "", apierr.New(http.StatusConflict, "instance_not_pairing",
			"instance is not waiting for a QR scan (state: "+string(in.State)+")", in.State.Active())
	}
	return code, nil
}

func (s *Instances) PairPhone(ctx context.Context, id, phone string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) < 10 || len(digits) > 15 {
		return "", apierr.Validation("phone must have 10 to 15 digits including country code")
	}
	in, err := s.Store.GetInstance(ctx, id)
	if err != nil {
		return "", mapErr(err)
	}
	if in.State != instance.QRPending {
		return "", apierr.New(http.StatusConflict, "instance_not_pairing",
			"instance is not waiting to be paired (state: "+string(in.State)+")", in.State.Active())
	}
	code, err := s.Manager.PairPhone(ctx, id, digits)
	return code, mapErr(err)
}

// Authenticate resolves an instance token to its instance ID.
func (s *Instances) Authenticate(ctx context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, "lzi_") {
		return "", apierr.Unauthorized()
	}
	in, err := s.Store.InstanceByTokenHash(ctx, secure.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return "", apierr.Unauthorized()
	}
	if err != nil {
		return "", err
	}
	return in.ID, nil
}
```

- [ ] **Step 2: Compilar**

Run: `go build ./... && go vet ./internal/service/`
Expected: sem erros.

- [ ] **Step 3: Commit**

```bash
git add internal/service
git commit -m "feat(service): casos de uso de instância"
```

### Task 15: API REST

**Files:**
- Create: `internal/api/api.go`
- Test: `internal/api/api_test.go`

- [ ] **Step 1: Escrever os testes**

```go
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

const admin = "lza_admin"

type env struct {
	t   *testing.T
	srv *httptest.Server
	sp  *workertest.Spawner
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-API") }
	opt := manager.DefaultOptions()
	opt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, opt, slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		mgr.Shutdown(ctx)
	})
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir}
	h := New(svc, Options{AdminToken: admin, Ready: st.Ping}, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, sp: sp}
}

func (e *env) do(method, path, token, body string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (e *env) create(name string) (id, token string) {
	e.t.Helper()
	code, body := e.do("POST", "/v1/instances", admin, `{"name":"`+name+`"}`)
	if code != http.StatusCreated {
		e.t.Fatalf("create = %d %v", code, body)
	}
	inst := body["instance"].(map[string]any)
	return inst["id"].(string), body["token"].(string)
}

func (e *env) waitState(id, want string) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body := e.do("GET", "/v1/instances/"+id, admin, "")
		if body["state"] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("instance %s never reached %s", id, want)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("GET", "/healthz", "", ""); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if code, _ := e.do("GET", "/readyz", "", ""); code != 200 {
		t.Fatalf("readyz = %d", code)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	if code, body := e.do("GET", "/v1/instances", "", ""); code != 401 || errCode(body) != "unauthorized" {
		t.Fatalf("no token = %d %v", code, body)
	}
	if code, _ := e.do("GET", "/v1/instances", "lzi_wrong", ""); code != 401 {
		t.Fatalf("bad token = %d", code)
	}
	id1, tok1 := e.create("A")
	id2, _ := e.create("B")
	if code, _ := e.do("GET", "/v1/instances/"+id1, tok1, ""); code != 200 {
		t.Fatalf("own instance = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances/"+id2, tok1, ""); code != 403 {
		t.Fatalf("other instance = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances", tok1, ""); code != 403 {
		t.Fatalf("list with instance token = %d", code)
	}
	if code, _ := e.do("DELETE", "/v1/instances/"+id1, tok1, ""); code != 403 {
		t.Fatalf("delete with instance token = %d", code)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	code, body := e.do("POST", "/v1/instances", admin, `{"name":"  "}`)
	if code != 400 || errCode(body) != "validation_failed" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestPairingFlow(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Loja")
	e.waitState(id, "qr_pending")

	code, body := e.do("GET", "/v1/instances/"+id+"/qr", tok, "")
	if code != 200 || body["code"] != "QR-API" {
		t.Fatalf("qr = %d %v", code, body)
	}

	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/instances/"+id+"/qr?format=png", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("png = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	if code, body := e.do("POST", "/v1/instances/"+id+"/pair-phone", tok, `{"phone":"123"}`); code != 400 {
		t.Fatalf("bad phone = %d %v", code, body)
	}
	code, body = e.do("POST", "/v1/instances/"+id+"/pair-phone", tok, `{"phone":"+55 11 99999-9999"}`)
	if code != 200 || body["code"] != "PAIR-CODE" {
		t.Fatalf("pair = %d %v", code, body)
	}

	e.sp.Last(id).Emit("connected")
	e.waitState(id, "connected")
	_, body = e.do("GET", "/v1/instances/"+id, tok, "")
	if body["phone"] != "5511999999999" {
		t.Fatalf("phone = %v", body["phone"])
	}
	if code, body := e.do("GET", "/v1/instances/"+id+"/qr", tok, ""); code != 409 || errCode(body) != "instance_not_pairing" {
		t.Fatalf("qr after connect = %d %v", code, body)
	}
}

func TestStopStartRestartLogout(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Ops")
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/stop", tok, ""); code != 200 {
		t.Fatalf("stop = %d", code)
	}
	e.waitState(id, "stopped")
	if code, body := e.do("POST", "/v1/instances/"+id+"/logout", tok, ""); code != 409 || errCode(body) != "instance_not_connected" {
		t.Fatalf("logout while stopped = %d %v", code, body)
	}
	if code, _ := e.do("POST", "/v1/instances/"+id+"/start", tok, ""); code != 200 {
		t.Fatalf("start = %d", code)
	}
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/restart", tok, ""); code != 200 {
		t.Fatalf("restart = %d", code)
	}
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/logout", tok, ""); code != 200 {
		t.Fatalf("logout = %d", code)
	}
	e.waitState(id, "logged_out")
}

func TestRotateAndDelete(t *testing.T) {
	e := newEnv(t)
	id, oldTok := e.create("Rot")
	code, body := e.do("POST", "/v1/instances/"+id+"/token/rotate", admin, "")
	if code != 200 {
		t.Fatalf("rotate = %d", code)
	}
	newTok := body["token"].(string)
	if code, _ := e.do("GET", "/v1/instances/"+id, oldTok, ""); code != 401 {
		t.Fatalf("old token = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances/"+id, newTok, ""); code != 200 {
		t.Fatalf("new token = %d", code)
	}
	if code, _ := e.do("DELETE", "/v1/instances/"+id, admin, ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, body := e.do("GET", "/v1/instances/"+id, admin, ""); code != 404 || errCode(body) != "instance_not_found" {
		t.Fatalf("after delete = %d %v", code, body)
	}
}

func TestListInstances(t *testing.T) {
	e := newEnv(t)
	e.create("A")
	e.create("B")
	code, body := e.do("GET", "/v1/instances", admin, "")
	list, _ := body["instances"].([]any)
	if code != 200 || len(list) != 2 {
		t.Fatalf("list = %d %v", code, body)
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test -race ./internal/api/`
Expected: FAIL (`undefined: New`).

- [ ] **Step 3: Implementar `internal/api/api.go`**

```go
// Package api exposes the LiteZap REST API (spec section 6).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"
)

// InstanceService is the subset of service.Instances used by the API.
type InstanceService interface {
	Create(ctx context.Context, name string) (service.InstanceView, string, error)
	List(ctx context.Context) ([]service.InstanceView, error)
	Get(ctx context.Context, id string) (service.InstanceView, error)
	Delete(ctx context.Context, id string) error
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
	Logout(ctx context.Context, id string) error
	RotateToken(ctx context.Context, id string) (string, error)
	QR(ctx context.Context, id string) (string, error)
	PairPhone(ctx context.Context, id, phone string) (string, error)
	Authenticate(ctx context.Context, token string) (string, error)
}

type Options struct {
	AdminToken string
	// ProtectOps requires the admin token on /healthz and /readyz (set when not bound to loopback).
	ProtectOps bool
	Ready      func(context.Context) error
}

type server struct {
	svc InstanceService
	opt Options
	log *slog.Logger
}

type principal struct {
	admin      bool
	instanceID string
}

type ctxKey struct{}

func New(svc InstanceService, opt Options, log *slog.Logger) http.Handler {
	s := &server{svc: svc, opt: opt, log: log}
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		if opt.ProtectOps {
			r.Use(s.authenticate, s.requireAdmin)
		}
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
		r.Get("/readyz", s.readyz)
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authenticate)
		r.With(s.requireAdmin).Post("/instances", s.createInstance)
		r.With(s.requireAdmin).Get("/instances", s.listInstances)
		r.Route("/instances/{id}", func(r chi.Router) {
			r.Use(s.requireInstance)
			r.Get("/", s.getInstance)
			r.With(s.requireAdmin).Delete("/", s.deleteInstance)
			r.Post("/start", s.action(svc.Start))
			r.Post("/stop", s.action(svc.Stop))
			r.Post("/restart", s.action(svc.Restart))
			r.Post("/logout", s.action(svc.Logout))
			r.With(s.requireAdmin).Post("/token/rotate", s.rotateToken)
			r.Get("/qr", s.qr)
			r.Post("/pair-phone", s.pairPhone)
		})
	})
	return r
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || tok == "" {
			apierr.Write(w, apierr.Unauthorized())
			return
		}
		var p principal
		if secure.Equal(tok, s.opt.AdminToken) {
			p.admin = true
		} else {
			id, err := s.svc.Authenticate(r.Context(), tok)
			if err != nil {
				s.writeErr(w, err)
				return
			}
			p.instanceID = id
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func principalFrom(r *http.Request) principal {
	p, _ := r.Context().Value(ctxKey{}).(principal)
	return p
}

func (s *server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalFrom(r).admin {
			apierr.Write(w, apierr.Forbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) requireInstance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if !p.admin && p.instanceID != chi.URLParam(r, "id") {
			apierr.Write(w, apierr.Forbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeErr logs unexpected errors before rendering them as internal.
func (s *server) writeErr(w http.ResponseWriter, err error) {
	var e *apierr.Error
	if !errors.As(err, &e) {
		s.log.Error("request failed", "err", err)
	}
	apierr.Write(w, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return apierr.Validation("invalid JSON body")
	}
	return nil
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.opt.Ready != nil {
		if err := s.opt.Ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

func (s *server) createInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	v, tok, err := s.svc.Create(r.Context(), req.Name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"instance": v, "token": tok})
}

func (s *server) listInstances(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.List(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"instances": list})
}

func (s *server) getInstance(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// action runs a lifecycle operation and returns the instance as it is right after.
func (s *server) action(fn func(context.Context, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := fn(r.Context(), id); err != nil {
			s.writeErr(w, err)
			return
		}
		v, err := s.svc.Get(r.Context(), id)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func (s *server) rotateToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.svc.RotateToken(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"token": tok})
}

func (s *server) qr(w http.ResponseWriter, r *http.Request) {
	code, err := s.svc.QR(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if r.URL.Query().Get("format") == "png" {
		png, err := qrcode.Encode(code, qrcode.Medium, 320)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
		return
	}
	writeJSON(w, 200, map[string]string{"code": code})
}

func (s *server) pairPhone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if err := decode(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	code, err := s.svc.PairPhone(r.Context(), chi.URLParam(r, "id"), req.Phone)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"code": code})
}
```

- [ ] **Step 4: Rodar e ver passar**

Run: `go mod tidy && go test -race -count=3 ./internal/api/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/api go.mod go.sum
git commit -m "feat(api): REST de instâncias com auth admin/instância"
```

### Task 16: Wiring do supervisor e main

**Files:**
- Create: `internal/serve/serve.go`, `cmd/litezap/main.go`
- Test: `internal/serve/serve_test.go`

- [ ] **Step 1: Escrever o teste de `isLoopback`**

```go
package serve

import "testing"

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8787": true,
		"localhost:8787": true,
		"[::1]:8787":     true,
		"0.0.0.0:8787":   false,
		":8787":          false,
		"10.0.0.5:8787":  false,
		"garbage":        false,
	}
	for addr, want := range cases {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
```

- [ ] **Step 2: Rodar e ver falhar**

Run: `go test ./internal/serve/`
Expected: FAIL.

- [ ] **Step 3: Implementar `internal/serve/serve.go`**

```go
// Package serve wires the supervisor process (`litezap serve`).
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/store"
)

func Run(ctx context.Context, cfgPath string, log *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if err := config.EnsureAdminToken(&cfg); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "litezap.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	spawner.CleanupOrphans(cfg.DataDir, log)
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log}
	mgr := manager.New(st, sp, manager.DefaultOptions(), log)
	if err := mgr.Boot(ctx); err != nil {
		return err
	}

	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir}
	handler := api.New(svc, api.Options{
		AdminToken: cfg.AdminToken,
		ProtectOps: !isLoopback(cfg.ListenAddr),
		Ready:      st.Ping,
	}, log)
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() {
		log.Info("litezap listening", "addr", cfg.ListenAddr, "data_dir", cfg.DataDir,
			"admin_token_file", filepath.Join(cfg.DataDir, "admin_token"))
		errc <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	mgr.Shutdown(sctx)
	return serveErr
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

- [ ] **Step 4: Implementar `cmd/litezap/main.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/douglasdf99/litezap/internal/serve"
	"github.com/douglasdf99/litezap/internal/worker"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func usage() {
	fmt.Fprintln(os.Stderr, "usage: litezap <serve|worker|version> [flags]")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		cfg := fs.String("config", "litezap.yaml", "config file (optional)")
		fs.Parse(os.Args[2:])
		err = serve.Run(ctx, *cfg, log)
	case "worker":
		fs := flag.NewFlagSet("worker", flag.ExitOnError)
		id := fs.String("instance", "", "instance id")
		sock := fs.String("socket", "", "unix socket path")
		dataDir := fs.String("data-dir", "", "data directory")
		fs.Parse(os.Args[2:])
		err = worker.Run(ctx, worker.Options{InstanceID: *id, Socket: *sock, DataDir: *dataDir}, log.With("instance_id", *id))
	case "version":
		fmt.Println(version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Rodar todos os testes e compilar o binário**

Run: `go test -race ./... && go build -o litezap ./cmd/litezap && ./litezap version`
Expected: todos `ok`; imprime `dev`.

- [ ] **Step 6: Commit**

```bash
git add internal/serve cmd
git commit -m "feat: subcomandos serve/worker/version e wiring do supervisor"
```

### Task 17: Verificação manual com WhatsApp real + README

**Files:**
- Create: `README.md`

Requer um número de WhatsApp de teste no celular. Não roda no CI.

- [ ] **Step 1: Subir o supervisor**

```bash
go build -o litezap ./cmd/litezap && ./litezap serve
```

Expected: log `litezap listening addr=127.0.0.1:8787` e o caminho do `admin_token`.

- [ ] **Step 2: Criar uma instância (outro terminal)**

```bash
ADMIN=$(cat data/admin_token)
curl -s -X POST localhost:8787/v1/instances -H "Authorization: Bearer $ADMIN" -d '{"name":"teste"}'
```

Expected: `201` com `instance.id` e `token`. Guarde em `ID` e `TOK`.

- [ ] **Step 3: Parear**

```bash
curl -s "localhost:8787/v1/instances/$ID/qr?format=png" -H "Authorization: Bearer $TOK" -o qr.png && open qr.png
```

Escaneie em WhatsApp → Aparelhos conectados. Depois:

```bash
curl -s localhost:8787/v1/instances/$ID -H "Authorization: Bearer $TOK"
```

Expected: `"state":"connected"` e `"phone"` preenchido.

- [ ] **Step 4: Crash do worker → restart automático**

```bash
kill -9 $(cat data/run/$ID.pid)
sleep 3 && curl -s localhost:8787/v1/instances/$ID -H "Authorization: Bearer $TOK"
```

Expected: logs `worker exited` + `instance state ... to=starting`, e de volta a `connected` sem novo QR.

- [ ] **Step 5: Reinício do supervisor → reconexão sem QR**

Pare o `serve` com Ctrl+C, suba de novo e consulte o estado. Expected: `connected` em poucos segundos.

- [ ] **Step 6: Logout pela API**

```bash
curl -s -X POST localhost:8787/v1/instances/$ID/logout -H "Authorization: Bearer $TOK"
```

Expected: estado `logged_out`; o aparelho some da lista no celular.

- [ ] **Step 7: Escrever `README.md`**

````markdown
# LiteZap

API de WhatsApp (não oficial, baseada em [whatsmeow](https://github.com/tulir/whatsmeow)) que roda local ou na nuvem.
Cada número roda em um processo worker isolado, supervisionado com heartbeat, restart automático e circuit breaker.

> Status: marco 1 (conexão, pareamento e supervisão). Envio de mensagens, webhooks, MCP e hook de agente vêm nos próximos marcos.
> Design: `docs/superpowers/specs/2026-10-17-litezap-engine-design.md`

## Rodar

```bash
go build -o litezap ./cmd/litezap
./litezap serve            # usa litezap.yaml se existir; veja litezap.example.yaml
```

O token admin fica em `data/admin_token` (ou defina `LITEZAP_ADMIN_TOKEN`).

Workers rodam em grupo de processos próprio. Se o supervisor morrer à força (SIGKILL), eles são encerrados no próximo `serve` (limpeza de órfãos via `data/run/*.pid`).

## API (marco 1)

| Rota | Token |
|---|---|
| `POST /v1/instances` `{"name"}` → `{instance, token}` | admin |
| `GET /v1/instances` | admin |
| `GET /v1/instances/{id}` | instância ou admin |
| `POST /v1/instances/{id}/start\|stop\|restart\|logout` | instância ou admin |
| `GET /v1/instances/{id}/qr[?format=png]` | instância ou admin |
| `POST /v1/instances/{id}/pair-phone` `{"phone"}` | instância ou admin |
| `POST /v1/instances/{id}/token/rotate` | admin |
| `DELETE /v1/instances/{id}` | admin |

Erros: `{"error":{"code","message","retryable"}}`.

## Desenvolvimento

```bash
go test -race ./...
go tool buf generate   # após mudar proto/
```
````

- [ ] **Step 8: Commit**

```bash
git add README.md
git commit -m "docs: README com quickstart do marco 1"
```

---

## Ao terminar o marco 1

- `go test -race ./...` verde e o roteiro da Task 17 executado com sucesso.
- Próximo passo: escrever o plano do **marco 2** (fila de envio com ritmo e idempotência, log de mensagens, chats e history sync), a partir do código real deste marco.
