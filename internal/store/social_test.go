package store

import (
	"context"
	"testing"
	"time"
)

func TestLabels(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	if id, err := s.NextLabelID(ctx, "i1"); err != nil || id != "1" {
		t.Fatalf("first id = %q, %v", id, err)
	}
	if has, _ := s.HasLabels(ctx, "i1"); has {
		t.Fatal("no labels yet")
	}
	if ch, err := s.UpsertLabel(ctx, "i1", Label{ID: "9", Deleted: true}); err != nil || ch {
		t.Fatalf("deleting an unknown label = %v, %v", ch, err)
	}
	if id, _ := s.NextLabelID(ctx, "i1"); id != "10" {
		t.Fatalf("an unknown deleted id is not reused: next = %q", id)
	}
	if has, _ := s.HasLabels(ctx, "i1"); !has {
		t.Fatal("a deleted label counts as synced")
	}
	for _, l := range []Label{{ID: "5", Name: "Novo", Color: 1}, {ID: "12", Name: "VIP", Color: 3}} {
		if ch, err := s.UpsertLabel(ctx, "i1", l); err != nil || !ch {
			t.Fatalf("upsert %s = %v, %v", l.ID, ch, err)
		}
	}
	if ch, _ := s.UpsertLabel(ctx, "i1", Label{ID: "5", Name: "Novo", Color: 1}); ch {
		t.Fatal("an echo must not count as a change")
	}
	chat := "5511888887777@s.whatsapp.net"
	if ch, _ := s.SetLabelChat(ctx, "i1", "5", chat, true); !ch {
		t.Fatal("label chat")
	}
	if ch, _ := s.SetLabelChat(ctx, "i1", "5", chat, true); ch {
		t.Fatal("labeling twice changes nothing")
	}
	_, _ = s.SetLabelChat(ctx, "i1", "12", chat, true)
	if ch, _ := s.SetLabelMessage(ctx, "i1", "12", chat, "W1", true); !ch {
		t.Fatal("label message")
	}
	ls, err := s.ListLabels(ctx, "i1")
	if err != nil || len(ls) != 2 || ls[0].ID != "5" || ls[1].ID != "12" || len(ls[0].Chats) != 1 || ls[0].Chats[0] != chat {
		t.Fatalf("labels = %+v, %v", ls, err)
	}
	if ids, _ := s.ChatLabels(ctx, "i1", chat); len(ids) != 2 || ids[0] != "5" {
		t.Fatalf("chat labels = %v", ids)
	}
	if ch, _ := s.UpsertLabel(ctx, "i1", Label{ID: "12", Deleted: true}); !ch {
		t.Fatal("delete")
	}
	if _, err := s.GetLabel(ctx, "i1", "12"); err != ErrNotFound {
		t.Fatalf("deleted label = %v", err)
	}
	if ids, _ := s.ChatLabels(ctx, "i1", chat); len(ids) != 1 {
		t.Fatalf("a deleted label loses its chats: %v", ids)
	}
	if id, _ := s.NextLabelID(ctx, "i1"); id != "13" {
		t.Fatalf("next id = %q (deleted ids are not reused)", id)
	}
}

func TestStatusChatHiddenAndCounted(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	_, _ = s.InsertMessage(ctx, "i1", Message{ID: "s1", WAMessageID: "S1", ChatJID: StatusChat, SenderJID: "5511888887777@s.whatsapp.net",
		Source: "contact", Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{IsGroup: true})
	_, _ = s.InsertMessage(ctx, "i1", Message{ID: "m1", WAMessageID: "M1", ChatJID: "5511888887777@s.whatsapp.net",
		Source: "contact", Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{Inbound: true})
	chats, _, err := s.ListChats(ctx, "i1", 10, nil)
	if err != nil || len(chats) != 1 || chats[0].JID == StatusChat {
		t.Fatalf("chats = %+v, %v", chats, err)
	}
	if msgs, _, _ := s.ListMessages(ctx, "i1", StatusChat, 10, nil); len(msgs) != 1 {
		t.Fatalf("statuses = %v", msgs)
	}
	for i, id := range []string{"q1", "q2"} {
		if _, _, err := s.Enqueue(ctx, Outbound{ID: id, InstanceID: "i1", ChatJID: StatusChat, WAMessageID: "W" + id, Payload: "{}"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := s.MarkFailed(ctx, "q2", 1, "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Enqueue(ctx, Outbound{ID: "q3", InstanceID: "i1", ChatJID: StatusChat, WAMessageID: "Wq3", Payload: `{"type":"revoke"}`}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountPostsSince(ctx, "i1", StatusChat, time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("count = %d, %v (failed items and revokes do not count)", n, err)
	}
}
