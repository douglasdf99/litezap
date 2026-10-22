package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

const (
	contact = "5511988887777@s.whatsapp.net"
	other   = "5511977776666@s.whatsapp.net"
)

// fakeClaude is a fake Messages API: script answers each request (n counts from 1) with
// the content blocks and stop reason of the reply.
type fakeClaude struct {
	srv *httptest.Server

	mu     sync.Mutex
	n      int
	bodies []map[string]any
	script func(n int, body map[string]any) (content []map[string]any, stop string)
}

func newFakeClaude(t *testing.T) *fakeClaude {
	f := &fakeClaude{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.n++
		n := f.n
		f.bodies = append(f.bodies, body)
		script := f.script
		f.mu.Unlock()
		content, stop := []map[string]any{{"type": "text", "text": "nada a fazer"}}, "end_turn"
		if script != nil {
			content, stop = script(n, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant",
			"model": "claude-sonnet-5-5", "content": content, "stop_reason": stop,
			"usage": map[string]any{"input_tokens": 1000, "output_tokens": 100}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeClaude) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.n }

func (f *fakeClaude) body(i int) map[string]any { f.mu.Lock(); defer f.mu.Unlock(); return f.bodies[i] }

func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// sendThenDone answers the first call with one send_message and then ends.
func sendThenDone(input map[string]any) func(int, map[string]any) ([]map[string]any, string) {
	return func(n int, _ map[string]any) ([]map[string]any, string) {
		if n%2 == 1 {
			return []map[string]any{text("vou responder"), toolUse(fmt.Sprintf("tu_%d", n), "send_message", input)}, "tool_use"
		}
		return []map[string]any{text("Respondi o cliente.")}, "end_turn"
	}
}

// env wires the real ingest, bus, queue, guarded send path, MCP tool table, agent hook
// (target agents) and attendant (provider settings, caps) over a fake worker and a fake
// Claude API.
type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	inst   string
	fake   *workertest.Fake
	claude *fakeClaude
	runner *Runner
	hook   *agenthook.Hook
	att    *attendant.Admin
	rec    *recorder
	msgs   int
}

type recorder struct {
	mu  sync.Mutex
	evs []string
}

func (r *recorder) Enqueue(e events.Envelope) {
	r.mu.Lock()
	r.evs = append(r.evs, e.Type)
	r.mu.Unlock()
}

func (r *recorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if e == typ {
			n++
		}
	}
	return n
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.DiscardHandler)
	claude := newFakeClaude(t)
	vault, _ := attendant.NewVault([]byte("attendant-key-0123456789abcdef01"))
	att := &attendant.Attendant{Store: st, Vault: vault, Log: log}
	runner := &Runner{Store: st, Sessions: FromAttendant(att), Log: log}
	hook := agenthook.New(st, agenthook.Options{Log: log, Agents: runner})
	rec := &recorder{}
	bus := events.NewBus(nil, hook, rec)
	hook.SetPublisher(bus)
	runner.Events = bus

	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	set := settings.Defaults()
	set.MinIntervalMS, set.JitterMS, set.TypingSimulation, set.Ramp.Enabled = 0, 0, false, false
	set.SendWindow, set.UnsafePacing = settings.Window{}, true
	q := queue.New(st, mgr, queue.Options{Defaults: set, Log: log, Resolver: resolver, Events: bus})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log, Events: bus}, q, events.Observer{Bus: bus}})
	msg := &service.Messaging{Store: st, Manager: mgr, Queues: q, Resolver: resolver, DataDir: dir, Defaults: set,
		Fetcher: &media.Fetcher{}, Signer: media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://127.0.0.1:1")}
	guarded := service.GuardedMessaging{Messaging: msg, Sending: &service.Sending{Store: st}}
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir, Queues: q, Events: bus}
	deps := mcp.HTTPDeps{Instances: svc, Messaging: guarded, Events: bus, Logger: log,
		Social: &service.Social{Store: st, Workers: mgr, Messaging: msg, Events: bus},
		Groups: &service.Groups{Store: st, Workers: mgr, Resolver: resolver, Defaults: set}}
	runner.Tools = func(id string) []ToolDef {
		var out []ToolDef
		for _, d := range mcp.AgentCatalog(deps.Local(service.Scope{InstanceID: id}), log) {
			out = append(out, ToolDef{Name: d.Name, Description: d.Description, Schema: d.InputSchema, Call: d.Call})
		}
		return out
	}
	hook.SetMessenger(guarded)
	hook.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hook.Close(ctx)
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
	})
	ctx := context.Background()
	v, _, err := svc.Create(ctx, "agentes")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, st: st, inst: v.ID, claude: claude, runner: runner, hook: hook, rec: rec,
		att: &attendant.Admin{A: att}}
	e.wait("connected", func() bool { in, _ := st.GetInstance(ctx, v.ID); return string(in.State) == "connected" })
	e.fake = sp.Last(v.ID)
	// Provider: the fake Claude API (base_url), as the admin would set it.
	if _, err := e.att.PutSettings(ctx, v.ID, attendant.SettingsPatch{Provider: ptr("anthropic"), BaseURL: ptr(claude.srv.URL),
		APIKey: ptr("sk-ant-test-0123456789")}); err != nil {
		t.Fatal(err)
	}
	// Agent hook: target agents, on for every chat.
	if _, err := (&agenthook.Admin{Store: st}).Put(ctx, v.ID, agenthook.Patch{Target: ptr("agents"), Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	return e
}

func ptr[T any](v T) *T { return &v }

func (e *env) wait(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// receive simulates an incoming message from chat.
func (e *env) receive(chat, msgText string) {
	e.msgs++
	e.fake.EmitMessage(&workerv1.Message{Id: fmt.Sprintf("IN%d", e.msgs), Chat: chat, Sender: chat, Type: "text", Text: msgText,
		PushName: "Ana", TimestampMs: time.Now().UnixMilli()})
}

func (e *env) agent(in Input) View {
	e.t.Helper()
	v, err := e.runner.Create(e.ctx, e.inst, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// lastRun waits for an agent's newest finished run.
func (e *env) lastRun(agentID string) RunView {
	e.t.Helper()
	var run RunView
	e.wait("a finished run", func() bool {
		runs, err := e.runner.Runs(e.ctx, e.inst, agentID, 1)
		if err != nil || len(runs) == 0 || runs[0].Status == StatusRunning {
			return false
		}
		run = runs[0]
		return true
	})
	return run
}

func (e *env) sentTexts() []string {
	var out []string
	for _, s := range e.fake.WA.Sent() {
		out = append(out, s.String())
	}
	return out
}

func joined(ss []string) string { return strings.Join(ss, "\n") }

// fromPhone simulates the owner answering from the phone (a from_me message LiteZap did
// not send).
func (e *env) fromPhone(chat, msgText string) {
	e.msgs++
	e.fake.EmitMessage(&workerv1.Message{Id: fmt.Sprintf("PHONE%d", e.msgs), Chat: chat, Sender: "5511000000000@s.whatsapp.net",
		FromMe: true, Type: "text", Text: msgText, TimestampMs: time.Now().UnixMilli()})
}
