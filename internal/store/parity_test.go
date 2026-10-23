package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClearChatMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	chat, other := "5511999990000@s.whatsapp.net", "5511999990001@s.whatsapp.net"
	if _, err := s.ClearChatMessages(ctx, "i1", chat, t0, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing chat: %v", err)
	}
	add := func(id, jid, status string, at time.Time) {
		t.Helper()
		if _, err := s.InsertMessage(ctx, "i1", Message{ID: id, WAMessageID: "W" + id, ChatJID: jid, Source: "contact",
			Type: "text", Text: id, Status: status, Timestamp: at}, ChatUpdate{CountUnread: true, Inbound: true}); err != nil {
			t.Fatal(err)
		}
	}
	add("a", chat, "received", t0)
	add("b", chat, "received", t0.Add(time.Minute))
	add("p", chat, "pending", t0.Add(time.Minute)) // still being sent
	add("late", chat, "received", t0.Add(time.Hour))
	add("o", other, "received", t0)
	if _, err := s.SetStarred(ctx, "i1", chat, "Wa", true); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearChatMessages(ctx, "i1", chat, t0.Add(time.Minute), true)
	if err != nil || n != 1 {
		t.Fatalf("clear keeping starred = %d %v", n, err)
	}
	for id, want := range map[string]bool{"a": true, "b": false, "p": true, "late": true, "o": true} {
		if _, err := s.GetMessage(ctx, "i1", id); (err == nil) != want {
			t.Errorf("%s logged = %v, want %v", id, err == nil, want)
		}
	}
	if c, _ := s.GetChat(ctx, "i1", chat); c.UnreadCount != 0 {
		t.Fatalf("unread after clear = %d", c.UnreadCount)
	}
	if n, _ := s.ClearChatMessages(ctx, "i1", chat, t0.Add(time.Hour), false); n != 2 {
		t.Fatalf("clear all = %d (starred and later messages)", n)
	}
}
