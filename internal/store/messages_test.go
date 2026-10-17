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
