package agenthook

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// fakeBuiltin is a scripted built-in attendant.
type fakeBuiltin struct {
	mu     sync.Mutex
	reqs   []Request
	answer func(Request) (Response, error)
	block  chan struct{} // when set, Answer waits on it
	noCall bool          // WantsCall answers false
}

func (f *fakeBuiltin) WantsCall(context.Context, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.noCall
}

func (f *fakeBuiltin) Answer(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	ans, block := f.answer, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	if ans == nil {
		return Response{}, nil
	}
	return ans(req)
}

func (f *fakeBuiltin) requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request{}, f.reqs...)
}

func newBuiltinEnv(t *testing.T, fb *fakeBuiltin) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: inst, Name: inst, State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, clk: clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)),
		msgr: &fakeMessenger{}, rec: &recorder{}}
	opt := Options{Clock: e.clk, Held: func(string) bool { return e.held.Load() }}
	if fb != nil {
		opt.Builtin = fb
	}
	e.hook = New(st, opt)
	e.hook.SetMessenger(e.msgr)
	e.hook.SetPublisher(e.rec)
	e.bus = events.NewBus(e.clk, e.hook)
	e.hook.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		e.hook.Close(ctx)
	})
	c := DefaultConfig()
	c.Target, c.Secret, c.Enabled, c.Timeout = TargetBuiltin, secret, true, Duration(time.Second)
	b, _ := json.Marshal(c)
	if err := st.PutAgentHook(context.Background(), inst, string(b)); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBuiltinTargetValidation(t *testing.T) {
	norm := func(s string) (string, error) { return s, nil }
	c := DefaultConfig()
	c.Secret, c.Enabled = secret, true
	if err := c.Validate(norm); err == nil {
		t.Fatal("url target without url accepted")
	}
	c.Target = TargetBuiltin
	if err := c.Validate(norm); err != nil {
		t.Fatalf("builtin without url: %v", err)
	}
	c.Target = "ftp"
	if err := c.Validate(norm); err == nil {
		t.Fatal("unknown target accepted")
	}
	p := Patch{Target: ptrS("url")}
	if got := p.Apply(Config{Target: TargetBuiltin}); got.Target != "" {
		t.Fatalf("url target stored as %q", got.Target)
	}
}

func ptrS(s string) *string { return &s }

func TestBuiltinReplyEventsAndOrigin(t *testing.T) {
	fb := &fakeBuiltin{answer: func(req Request) (Response, error) {
		return Response{Reply: json.RawMessage(`"Pedido anotado!"`), Events: []AgentEvent{
			{Type: events.TypeAgentOrderDraft, Data: map[string]any{"total": 30.0, "chat": "spoofed@s.whatsapp.net"}},
			{Type: "message.sent", Data: map[string]any{"x": 1}}, // not allowed: dropped
		}}, nil
	}}
	e := newBuiltinEnv(t, fb)
	ctx := context.Background()
	// An automatic call reject message and an agent reply already in the chat.
	for _, o := range []struct{ id, cid string }{{"q-rej", "call-reject-c1"}, {"q-ag", "agent:old"}, {"q-crm", "crm-1"}} {
		if _, _, err := e.st.Enqueue(ctx, store.Outbound{ID: o.id, InstanceID: inst, ClientMessageID: o.cid, ChatJID: chat, Payload: "{}"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.InsertMessage(ctx, inst, store.Message{ID: o.id, WAMessageID: "W" + o.id, ChatJID: chat, FromMe: true,
			Source: "api", Type: "text", Text: o.cid, Status: "sent", Timestamp: e.clk.Now().Add(-time.Minute)}, store.ChatUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	m := e.receive(chat, "quero 2 pizzas")
	e.eventually("reply", func() bool { s, _ := e.msgr.snapshot(); return len(s) == 1 })
	e.settle()
	reqs := fb.requests()
	if len(reqs) != 1 || reqs[0].Trigger != "" {
		t.Fatalf("requests %+v", reqs)
	}
	origins := map[string]string{}
	for _, cm := range reqs[0].Context {
		origins[cm.ID] = cm.Origin
	}
	if origins["q-rej"] != "automatic" || origins["q-ag"] != "agent" || origins["q-crm"] != "api" || origins[m.ID] != "contact" {
		t.Fatalf("origins %v", origins)
	}
	sends, _ := e.msgr.snapshot()
	if sends[0].Text != "Pedido anotado!" || sends[0].ClientMessageID != "agent:"+m.ID || sends[0].To != chat {
		t.Fatalf("send %+v", sends[0])
	}
	if len(e.rec.of("message.sent")) != 0 {
		t.Fatal("non-agent event type published")
	}
	ev := e.rec.of(events.TypeAgentOrderDraft)
	if len(ev) != 1 {
		t.Fatalf("order events %v", ev)
	}
	d := ev[0].(map[string]any)
	if d["chat"] != chat || d["message_id"] != m.ID || d["total"] != 30.0 {
		t.Fatalf("event data %v", d)
	}
}

func TestBuiltinErrorsFailClosed(t *testing.T) {
	fb := &fakeBuiltin{answer: func(Request) (Response, error) {
		return Response{Reply: json.RawMessage(`"never"`)}, &CallError{Reason: "daily_cap", Detail: "cap of 3 calls reached"}
	}}
	e := newBuiltinEnv(t, fb)
	e.receive(chat, "oi")
	e.eventually("failure", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 1 })
	e.settle()
	if s, _ := e.msgr.snapshot(); len(s) != 0 {
		t.Fatalf("sent on failure: %+v", s)
	}
	f := e.rec.of(events.TypeAgentFailed)[0].(events.AgentFailed)
	if f.Reason != "daily_cap" {
		t.Fatalf("reason %q", f.Reason)
	}
	// No built-in agent on the server: not_configured.
	e2 := newBuiltinEnv(t, nil)
	e2.receive(chat, "oi")
	e2.eventually("failure", func() bool { return len(e2.rec.of(events.TypeAgentFailed)) == 1 })
	if r := e2.rec.of(events.TypeAgentFailed)[0].(events.AgentFailed).Reason; r != "not_configured" {
		t.Fatalf("reason %q", r)
	}
	// A plain error is internal.
	fb.mu.Lock()
	fb.answer = func(Request) (Response, error) { return Response{}, errors.New("boom") }
	fb.mu.Unlock()
	e.clk.Advance(time.Second)
	e.receive(chat, "de novo")
	e.eventually("failure", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 2 })
	if r := e.rec.of(events.TypeAgentFailed)[1].(events.AgentFailed).Reason; r != "internal" {
		t.Fatalf("reason %q", r)
	}
}

func TestCallFollowUp(t *testing.T) {
	fb := &fakeBuiltin{answer: func(req Request) (Response, error) {
		if req.Trigger != "call" {
			return Response{}, nil
		}
		return Response{Reply: json.RawMessage(`"Vi que você ligou! Como posso ajudar?"`)}, nil
	}}
	e := newBuiltinEnv(t, fb)
	if !e.hook.CallFollowUp(inst, chat, "CALL1", "Não atendemos ligações.") {
		t.Fatal("follow-up refused")
	}
	e.eventually("reply", func() bool { s, _ := e.msgr.snapshot(); return len(s) == 1 })
	e.settle()
	sends, _ := e.msgr.snapshot()
	if sends[0].ClientMessageID != "agent:call:CALL1" || sends[0].To != chat || !strings.Contains(sends[0].Text, "ligou") {
		t.Fatalf("send %+v", sends[0])
	}
	if log := e.callsLog(); len(log) != 1 || !log[0].Replied || log[0].MessageID != "call:CALL1" {
		t.Fatalf("log %+v", log)
	}
	// Groups, disabled chats and suspended sending are refused.
	if e.hook.CallFollowUp(inst, "123@g.us", "C2", "") {
		t.Fatal("group accepted")
	}
	off := false
	_ = e.st.SetChatAgent(context.Background(), inst, chat, &off, false)
	if e.hook.CallFollowUp(inst, chat, "C3", "") {
		t.Fatal("disabled chat accepted")
	}
	_ = e.st.SetChatAgent(context.Background(), inst, chat, nil, true)
	e.held.Store(true)
	if e.hook.CallFollowUp(inst, chat, "C4", "") {
		t.Fatal("held instance accepted")
	}
	e.held.Store(false)
	// The attendant fails: the static message goes out instead.
	fb.mu.Lock()
	fb.answer = func(Request) (Response, error) { return Response{}, &CallError{Reason: "llm_error", Detail: "x"} }
	fb.mu.Unlock()
	if !e.hook.CallFollowUp(inst, chat, "C5", "Não atendemos ligações.") {
		t.Fatal("refused")
	}
	e.eventually("fallback", func() bool { s, _ := e.msgr.snapshot(); return len(s) == 2 })
	sends, _ = e.msgr.snapshot()
	if sends[1].ClientMessageID != "call-reject-C5" || sends[1].Text != "Não atendemos ligações." {
		t.Fatalf("fallback %+v", sends[1])
	}
}

func TestCallFollowUpRefusedForURLHooks(t *testing.T) {
	e := newEnv(t) // URL target
	if e.hook.CallFollowUp(inst, chat, "C1", "x") {
		t.Fatal("external hook took a call follow-up")
	}
}

func TestCallFollowUpDoesNotDisplacePendingMessage(t *testing.T) {
	fb := &fakeBuiltin{block: make(chan struct{})}
	e := newBuiltinEnv(t, fb)
	e.receive(chat, "primeira")
	e.eventually("in flight", func() bool { return len(fb.requests()) == 1 })
	e.receive(chat, "segunda") // waits for its turn
	e.eventually("pending", func() bool {
		e.hook.mu.Lock()
		defer e.hook.mu.Unlock()
		cs := e.hook.chats[chatKey{inst, chat}]
		return cs != nil && cs.pending != nil
	})
	e.hook.CallFollowUp(inst, chat, "C1", "")
	close(fb.block)
	e.settle()
	reqs := fb.requests()
	if len(reqs) != 2 || reqs[1].Trigger != "" {
		t.Fatalf("requests %+v", reqs)
	}
}
