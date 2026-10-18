package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

func agentStore(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	if err := s.CreateInstance(context.Background(), Instance{ID: "i1", Name: "i1", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAgentHookConfigCRUD(t *testing.T) {
	ctx := context.Background()
	s := agentStore(t)
	if _, err := s.GetAgentHook(ctx, "i1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if err := s.PutAgentHook(ctx, "i1", `{"url":"a"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentHook(ctx, "i1", `{"url":"b"}`); err != nil {
		t.Fatal(err)
	}
	if js, err := s.GetAgentHook(ctx, "i1"); err != nil || js != `{"url":"b"}` {
		t.Fatalf("got %q %v", js, err)
	}
	if err := s.PutAgentHook(ctx, "nope", `{}`); err == nil {
		t.Fatal("hook of a missing instance accepted")
	}
	if err := s.DeleteAgentHook(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAgentHook(ctx, "i1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestChatAgentOverrideAndPause(t *testing.T) {
	ctx := context.Background()
	s := agentStore(t)
	jid := "5511999999999@s.whatsapp.net"
	if _, err := s.GetChatAgent(ctx, "i1", jid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing chat = %v", err)
	}
	off := false
	if err := s.SetChatAgent(ctx, "i1", jid, &off, false); err != nil { // creates the row
		t.Fatal(err)
	}
	c, err := s.GetChatAgent(ctx, "i1", jid)
	if err != nil || c.Enabled == nil || *c.Enabled || !c.PausedUntil.IsZero() {
		t.Fatalf("after off: %+v %v", c, err)
	}
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if err := s.PauseChatAgent(ctx, "i1", jid, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseChatAgent(ctx, "i1", jid, t0.Add(time.Minute)); err != nil { // shorter: kept longer
		t.Fatal(err)
	}
	c, _ = s.GetChatAgent(ctx, "i1", jid)
	if !c.PausedUntil.Equal(t0.Add(time.Hour)) {
		t.Fatalf("paused until %v", c.PausedUntil)
	}
	// Inserting messages keeps the agent columns.
	if _, err := s.InsertMessage(ctx, "i1", Message{ID: "m1", WAMessageID: "W1", ChatJID: jid, Source: "contact",
		Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{Name: "Ana", Inbound: true}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetChatAgent(ctx, "i1", jid)
	if c.Name != "Ana" || c.Enabled == nil || *c.Enabled || c.PausedUntil.IsZero() {
		t.Fatalf("after message: %+v", c)
	}
	list, err := s.ListChatAgents(ctx, "i1", t0, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if err := s.SetChatAgent(ctx, "i1", jid, nil, true); err != nil { // inherit + resume
		t.Fatal(err)
	}
	c, _ = s.GetChatAgent(ctx, "i1", jid)
	if c.Enabled != nil || !c.PausedUntil.IsZero() {
		t.Fatalf("after reset: %+v", c)
	}
	if list, _ := s.ListChatAgents(ctx, "i1", t0, 10); len(list) != 0 {
		t.Fatalf("list after reset = %+v", list)
	}
	// An expired pause is not listed.
	_ = s.PauseChatAgent(ctx, "i1", jid, t0.Add(-time.Minute))
	if list, _ := s.ListChatAgents(ctx, "i1", t0, 10); len(list) != 0 {
		t.Fatalf("expired pause listed: %+v", list)
	}
	// Group JIDs create group rows.
	if err := s.SetChatAgent(ctx, "i1", "123@g.us", &off, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetChatAgent(ctx, "i1", "123@g.us"); !g.IsGroup {
		t.Fatalf("group row %+v", g)
	}
}

func TestAgentCallsLogAndReplyCount(t *testing.T) {
	ctx := context.Background()
	s := agentStore(t)
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for i := 0; i < agentCallsKept+60; i++ {
		c := AgentCall{ID: fmt.Sprintf("c%d", i), InstanceID: "i1", ChatJID: "a@s.whatsapp.net", Status: "ok",
			Replied: i%2 == 0, CreatedAt: t0.Add(time.Duration(i) * time.Second)}
		if err := s.InsertAgentCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListAgentCalls(ctx, "i1", 1000)
	if err != nil || len(list) > agentCallsKept+50 || len(list) < agentCallsKept {
		t.Fatalf("kept %d %v", len(list), err)
	}
	if list[0].ID != fmt.Sprintf("c%d", agentCallsKept+59) || !list[0].CreatedAt.Equal(t0.Add(time.Duration(agentCallsKept+59)*time.Second)) {
		t.Fatalf("newest = %+v", list[0])
	}
	n, err := s.CountAgentReplies(ctx, "i1", "a@s.whatsapp.net", t0.Add(time.Duration(agentCallsKept+50)*time.Second))
	if err != nil || n != 5 {
		t.Fatalf("replies = %d %v", n, err)
	}
	if n, _ := s.CountAgentReplies(ctx, "i1", "b@s.whatsapp.net", t0); n != 0 {
		t.Fatalf("other chat = %d", n)
	}
}
