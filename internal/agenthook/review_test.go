package agenthook

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/douglasdf99/litezap/internal/events"
)

// Review fixes: the follow-up needs the agent's opt-in, a silent or skipped follow-up
// keeps the static reject message, and events are marked only once published.
func TestCallFollowUpOptInAndFallbacks(t *testing.T) {
	fb := &fakeBuiltin{noCall: true, answer: func(Request) (Response, error) { return Response{}, nil }}
	e := newBuiltinEnv(t, fb)
	if e.hook.CallFollowUp(inst, chat, "C0", "fixa") {
		t.Fatal("follow-up taken without the agent's opt-in")
	}
	fb.mu.Lock()
	fb.noCall = false
	fb.mu.Unlock()
	// The agent stays silent: the static message still goes out.
	if !e.hook.CallFollowUp(inst, chat, "C1", "Não atendemos ligações.") {
		t.Fatal("refused")
	}
	e.eventually("fallback", func() bool { s, _ := e.msgr.snapshot(); return len(s) == 1 })
	sends, _ := e.msgr.snapshot()
	if sends[0].ClientMessageID != "call-reject-C1" {
		t.Fatalf("send %+v", sends[0])
	}
	// No static message configured: a silent follow-up sends nothing.
	if !e.hook.CallFollowUp(inst, chat, "C2", "") {
		t.Fatal("refused")
	}
	e.settle()
	if s, _ := e.msgr.snapshot(); len(s) != 1 {
		t.Fatalf("sent %+v", s)
	}
}

func TestEventsMarkedAfterPublish(t *testing.T) {
	var published atomic.Int32
	fb := &fakeBuiltin{answer: func(Request) (Response, error) {
		return Response{Reply: json.RawMessage(`"ok"`), Events: []AgentEvent{{Type: events.TypeAgentLead,
			Data: map[string]any{"need": "x"}, Published: func() { published.Add(1) }}}}, nil
	}}
	e := newBuiltinEnv(t, fb)
	e.receive(chat, "oi")
	e.eventually("published", func() bool { return published.Load() == 1 })
	// The next reply is refused by the queue: its event is not published.
	e.settle()
	e.msgr.mu.Lock()
	e.msgr.sendErr = errSend
	e.msgr.mu.Unlock()
	e.receive(chat, "de novo")
	e.eventually("failure", func() bool { return len(e.rec.of(events.TypeAgentFailed)) == 1 })
	e.settle()
	if published.Load() != 1 {
		t.Fatalf("published %d times", published.Load())
	}
}

var errSend = &CallError{Reason: "x", Detail: "queue refused"}
