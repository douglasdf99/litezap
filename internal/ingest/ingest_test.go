package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

const chat = "5511988887777@s.whatsapp.net"

func setup(t *testing.T) (*Ingestor, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	return &Ingestor{Store: st, Log: slog.New(slog.DiscardHandler)}, st
}

func msgEv(m *workerv1.Message) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Message{Message: m}}
}

var ts = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).UnixMilli()

func TestReceivedMessageIsLoggedOnce(t *testing.T) {
	in, st := setup(t)
	ctx := context.Background()
	m := &workerv1.Message{Id: "WA1", Chat: chat, Sender: chat, PushName: "João", TimestampMs: ts,
		Type: "image", Text: "foto", Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 9, Ref: []byte("r")}}
	in.WorkerEvent("i1", msgEv(m))
	in.WorkerEvent("i1", msgEv(m)) // redelivered after a worker restart
	msgs, _, err := st.ListMessages(ctx, "i1", chat, 10, nil)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	got := msgs[0]
	if got.Source != "contact" || got.Status != "received" || got.Text != "foto" || got.Timestamp.UnixMilli() != ts {
		t.Fatalf("message = %+v", got)
	}
	var info store.MediaInfo
	if err := json.Unmarshal([]byte(got.MediaJSON), &info); err != nil || info.ID == "" || info.Mime != "image/jpeg" {
		t.Fatalf("media json = %q, %v", got.MediaJSON, err)
	}
	if md, err := st.GetMedia(ctx, "i1", info.ID); err != nil || string(md.Ref) != "r" {
		t.Fatalf("media row = %+v, %v", md, err)
	}
	c, _ := st.GetChat(ctx, "i1", chat)
	if c.Name != "João" || c.UnreadCount != 1 || !c.HasInbound {
		t.Fatalf("chat = %+v", c)
	}
}

func TestFromMeSourceAndReceipts(t *testing.T) {
	in, st := setup(t)
	ctx := context.Background()
	if _, _, err := st.Enqueue(ctx, store.Outbound{ID: "q1", InstanceID: "i1", ChatJID: chat, WAMessageID: "OWN1", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "PHONE1", Chat: chat, FromMe: true, TimestampMs: ts, Type: "text", Text: "eu"}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "OWN1", Chat: chat, FromMe: true, TimestampMs: ts, Type: "text", Text: "api"}))
	phone, _ := st.MessageByWAID(ctx, "i1", chat, "PHONE1")
	own, _ := st.MessageByWAID(ctx, "i1", chat, "OWN1")
	if phone.Source != "device" || own.Source != "api" || phone.Status != "sent" {
		t.Fatalf("phone=%+v own=%+v", phone, own)
	}
	c, _ := st.GetChat(ctx, "i1", chat)
	if c.UnreadCount != 0 || c.HasInbound {
		t.Fatalf("our own messages must not count as inbound: %+v", c)
	}
	in.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: &workerv1.Receipt{
		MessageIds: []string{"PHONE1"}, Chat: chat, Type: "played"}}})
	if m, _ := st.MessageByWAID(ctx, "i1", chat, "PHONE1"); m.Status != "played" {
		t.Fatalf("status = %q", m.Status)
	}
	// played is the top status: a late read receipt never lowers it.
	in.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: &workerv1.Receipt{
		MessageIds: []string{"PHONE1"}, Chat: chat, Type: "read"}}})
	if m, _ := st.MessageByWAID(ctx, "i1", chat, "PHONE1"); m.Status != "played" {
		t.Fatalf("status after late read = %q", m.Status)
	}
}

func TestHistoryIsLoggedWithoutUnread(t *testing.T) {
	in, st := setup(t)
	ctx := context.Background()
	group := "1203630@g.us"
	in.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{
		Chat: group, Name: "Família", IsGroup: true, Messages: []*workerv1.Message{
			{Id: "H1", Chat: group, Sender: chat, IsGroup: true, PushName: "João", TimestampMs: ts, Type: "text", Text: "a"},
			{Id: "H2", Chat: group, FromMe: true, IsGroup: true, TimestampMs: ts + 1000, Type: "text", Text: "b"},
		}}}})
	msgs, _, _ := st.ListMessages(ctx, "i1", group, 10, nil)
	if len(msgs) != 2 || msgs[0].Source != "history" {
		t.Fatalf("messages = %+v", msgs)
	}
	c, _ := st.GetChat(ctx, "i1", group)
	if c.Name != "Família" || !c.IsGroup || c.UnreadCount != 0 || !c.HasInbound {
		t.Fatalf("chat = %+v", c)
	}
}
