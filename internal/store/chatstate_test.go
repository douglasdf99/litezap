package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyChatChange(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	chat := "5511888887777@s.whatsapp.net"
	if _, _, err := s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "archived", Value: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing chat: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, _ = s.InsertMessage(ctx, "i1", Message{ID: "m" + string(rune('a'+i)), WAMessageID: "W" + string(rune('a'+i)),
			ChatJID: chat, Source: "contact", Type: "text", Status: "received", Timestamp: t0.Add(time.Duration(i) * time.Minute)},
			ChatUpdate{CountUnread: true, Inbound: true})
	}
	c, changed, err := s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "pinned", Value: true})
	if err != nil || !changed || !c.Pinned {
		t.Fatalf("pin: %+v %v %v", c, changed, err)
	}
	if _, changed, _ := s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "pinned", Value: true}); changed {
		t.Fatal("an echo must not count as a change")
	}
	if n, _ := s.PinnedChats(ctx, "i1"); n != 1 {
		t.Fatalf("pinned = %d", n)
	}
	c, _, _ = s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "archived", Value: true})
	if !c.Archived || c.Pinned {
		t.Fatalf("archive must unpin: %+v", c.ChatState)
	}
	c, _, _ = s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "muted", Value: true})
	if !c.MutedUntil.Equal(MutedForever) || !c.Muted(time.Now()) {
		t.Fatalf("forever mute: %v", c.MutedUntil)
	}
	until := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	c, _, _ = s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "muted", Value: true, MutedUntil: until})
	if !c.MutedUntil.Equal(until) {
		t.Fatalf("mute until: %v", c.MutedUntil)
	}
	c, _, _ = s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "unread", Value: true})
	if !c.MarkedUnread || c.UnreadCount != 2 {
		t.Fatalf("unread: %+v", c)
	}
	c, changed, _ = s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "unread", Value: false})
	if c.MarkedUnread || c.UnreadCount != 0 || !changed {
		t.Fatalf("read: %+v", c)
	}
	got, _ := s.GetChat(ctx, "i1", chat)
	if !got.Archived || got.MarkedUnread || !got.MutedUntil.Equal(until) {
		t.Fatalf("stored = %+v", got.ChatState)
	}

	// Deleted chats leave the list until a newer message arrives.
	_, _, _ = s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "deleted", Value: true, At: t0.Add(5 * time.Minute)})
	list, _, _ := s.ListChats(ctx, "i1", 10, nil)
	if len(list) != 0 {
		t.Fatalf("deleted chat listed: %+v", list)
	}
	_, _ = s.InsertMessage(ctx, "i1", Message{ID: "mz", WAMessageID: "Wz", ChatJID: chat, Source: "contact", Type: "text",
		Status: "received", Timestamp: t0.Add(10 * time.Minute)}, ChatUpdate{CountUnread: true, Inbound: true})
	list, _, _ = s.ListChats(ctx, "i1", 10, nil)
	if len(list) != 1 || !list[0].Archived {
		t.Fatalf("chat back after a new message: %+v", list)
	}
	if m, err := s.LastChatMessage(ctx, "i1", chat); err != nil || m.WAMessageID != "Wz" {
		t.Fatalf("last = %+v %v", m, err)
	}
}

func TestSetStarred(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	chat := "5511888887777@s.whatsapp.net"
	_, _ = s.InsertMessage(ctx, "i1", Message{ID: "m1", WAMessageID: "W1", ChatJID: chat, Source: "contact", Type: "text",
		Status: "received", Timestamp: t0}, ChatUpdate{})
	_, _ = s.InsertMessage(ctx, "i1", Message{ID: "m2", WAMessageID: "W2", ChatJID: chat, Source: "contact", Type: "text",
		Status: "received", Timestamp: t0, MetaJSON: `{"forwarded":true}`}, ChatUpdate{})
	for _, id := range []string{"W1", "W2"} {
		if ch, err := s.SetStarred(ctx, "i1", chat, id, true); err != nil || !ch {
			t.Fatalf("%s: %v %v", id, ch, err)
		}
		if ch, _ := s.SetStarred(ctx, "i1", chat, id, true); ch {
			t.Fatal("starring twice is not a change")
		}
	}
	m2, _ := s.GetMessage(ctx, "i1", "m2")
	if meta := ParseMeta(m2.MetaJSON); !meta.Starred || !meta.Forwarded {
		t.Fatalf("meta = %s", m2.MetaJSON)
	}
	if ch, _ := s.SetStarred(ctx, "i1", chat, "W2", false); !ch {
		t.Fatal("unstar")
	}
	m2, _ = s.GetMessage(ctx, "i1", "m2")
	if meta := ParseMeta(m2.MetaJSON); meta.Starred || !meta.Forwarded {
		t.Fatalf("meta = %s", m2.MetaJSON)
	}
	if _, err := s.SetStarred(ctx, "i1", chat, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}
