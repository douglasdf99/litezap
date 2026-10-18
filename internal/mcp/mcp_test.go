package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const admin = "lza_admin"

// env runs the real services over fake workers, the REST API with /mcp mounted, and
// lets a test connect an MCP client in three ways: in memory over Local, over the HTTP
// endpoint, and over the REST backend (what `litezap mcp` uses).
type env struct {
	t    *testing.T
	sp   *workertest.Spawner
	svc  *service.Instances
	msg  *service.Messaging
	bus  *events.Bus
	srv  *httptest.Server
	grp  *service.Groups
	acct *service.Account
}

func newEnv(t *testing.T, tweak func(*settings.Settings)) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-MCP") }
	log := slog.New(slog.DiscardHandler)
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	defaults := settings.Defaults()
	defaults.MinIntervalMS, defaults.JitterMS, defaults.TypingSimulation, defaults.Ramp.Enabled = 0, 0, false, false
	defaults.SendWindow, defaults.UnsafePacing = settings.Window{}, true
	if tweak != nil {
		tweak(&defaults)
	}
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	bus := events.NewBus(nil)
	q := queue.New(st, mgr, queue.Options{Defaults: defaults, Log: log, Resolver: resolver, Events: bus})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log, Events: bus}, q, events.Observer{Bus: bus}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
	})
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir, Queues: q, Events: bus}
	msg := &service.Messaging{Store: st, Manager: mgr, Queues: q, Fetcher: &media.Fetcher{AllowPrivate: true},
		Signer:  media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://example.invalid"),
		DataDir: dir, Defaults: defaults, Resolver: resolver, SignedURLs: true}
	grp := &service.Groups{Store: st, Workers: mgr, Resolver: resolver, Defaults: defaults}
	acct := &service.Account{Messaging: msg, Events: bus}
	h := api.New(svc, msg, api.Options{AdminToken: admin, Events: bus, Groups: grp, Account: acct,
		MCP: NewHTTPHandler(HTTPDeps{Instances: svc, Messaging: msg, Events: bus, Groups: grp, Logger: log, Account: acct})}, log)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { bus.CloseSubscribers(); srv.Close() })
	return &env{t: t, sp: sp, svc: svc, msg: msg, bus: bus, srv: srv, grp: grp, acct: acct}
}

func (e *env) create(name string) (id, token string) {
	e.t.Helper()
	v, tok, err := e.svc.Create(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	e.waitState(v.ID, "qr_pending")
	return v.ID, tok
}

func (e *env) connected(name string) (id, token string) {
	e.t.Helper()
	id, token = e.create(name)
	e.sp.Last(id).Emit("connected")
	e.waitState(id, "connected")
	return id, token
}

func (e *env) waitState(id, want string) {
	e.t.Helper()
	eventually(e.t, "state "+want, func() bool {
		v, err := e.svc.Get(context.Background(), id)
		return err == nil && string(v.State) == want
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type mode string

const (
	modeLocal mode = "local"
	modeHTTP  mode = "http"
	modeREST  mode = "rest"
)

var allModes = []mode{modeLocal, modeHTTP, modeREST}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// client connects an MCP client as the holder of token.
func (e *env) client(m mode, token string) *sdk.ClientSession {
	e.t.Helper()
	ctx := context.Background()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil)
	var tr sdk.Transport
	switch m {
	case modeHTTP:
		tr = &sdk.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1,
			HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}}
	default:
		var b Backend
		if m == modeREST {
			r, err := NewREST(e.srv.URL, token, nil)
			if err != nil {
				e.t.Fatal(err)
			}
			b = r
		} else {
			sc := service.Scope{Admin: token == admin}
			if !sc.Admin {
				id, err := e.svc.Authenticate(ctx, token)
				if err != nil {
					e.t.Fatal(err)
				}
				sc.InstanceID = id
			}
			b = &Local{Instances: e.svc, Messaging: e.msg, Events: e.bus, Groups: e.grp, Caller: sc, Account: e.acct}
		}
		st, ct := sdk.NewInMemoryTransports()
		ss, err := NewServer(b, Options{}).Connect(ctx, st, nil)
		if err != nil {
			e.t.Fatal(err)
		}
		e.t.Cleanup(func() { ss.Close() })
		tr = ct
	}
	cs, err := c.Connect(ctx, tr, nil)
	if err != nil {
		e.t.Fatalf("%s connect: %v", m, err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return cs
}

// call runs a tool and fails the test on protocol errors (tool errors are returned).
func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// ok runs a tool that must succeed and decodes its structured output into out.
func ok(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res := call(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s output %s: %v", name, b, err)
		}
	}
}

// fails runs a tool that must return a tool error containing want.
func fails(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, cs, name, args)
	if !res.IsError || !strings.Contains(text(res), want) {
		t.Fatalf("%s = error %v %q, want an error containing %q", name, res.IsError, text(res), want)
	}
}

func text(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
