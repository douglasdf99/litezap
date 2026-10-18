package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// delay_ms and typing_ms only add time: the min_interval floor still holds afterwards.
func TestDelayAndTypingOverride(t *testing.T) {
	s := fastSettings()
	s.MinIntervalMS = 3000 // typing simulation off: typing_ms forces it for one message
	e := newEnvAt(t, t0, s, instance.Connected, noLookup)
	e.enqueue(chatA, Payload{Type: "image", MediaFile: "", DelayMS: 1000, TypingMS: 2000})
	e.enqueue(chatB, Payload{Type: "text", Text: "olá"})
	e.eventually("delay timer", func() bool { return e.clock.Waiters() > 0 })
	if len(e.fake.WA.Presences()) != 0 || len(e.fake.WA.Sent()) != 0 {
		t.Fatal("typing or send before delay_ms")
	}
	e.clock.Advance(time.Second)
	e.eventually("typing", func() bool { return len(e.fake.WA.Presences()) == 1 && e.clock.Waiters() > 0 })
	if p := e.fake.WA.Presences()[0]; p.State != "composing" || p.To != chatA {
		t.Fatalf("presence = %+v", p)
	}
	e.clock.Advance(1900 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("sent before typing_ms elapsed")
	}
	e.clock.Advance(100 * time.Millisecond)
	e.eventually("first send", func() bool { return len(e.fake.WA.Sent()) == 1 && e.clock.Waiters() > 0 })
	e.clock.Advance(2900 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 1 {
		t.Fatal("second message ignored min_interval after the delayed one")
	}
	e.clock.Advance(100 * time.Millisecond)
	e.eventually("second send", func() bool { return len(e.fake.WA.Sent()) == 2 })
	if len(e.fake.WA.Presences()) != 1 {
		t.Fatalf("typing simulation is off: presences = %d", len(e.fake.WA.Presences()))
	}
}

func TestEditWindowCheckedAtDispatch(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, noLookup)
	old := e.enqueue(chatA, Payload{Type: "edit", Text: "x", Target: &Target{WAMessageID: "W1", FromMe: true, SentAt: t0.Add(-21 * time.Minute)}})
	revoke := e.enqueue(chatB, Payload{Type: "revoke", Target: &Target{WAMessageID: "W2", FromMe: true, SentAt: t0.Add(-49 * time.Hour)}})
	e.eventually("failed", func() bool {
		return e.status(old).Status == store.OutFailed && e.status(revoke).Status == store.OutFailed
	})
	if !strings.HasPrefix(e.status(old).LastError, "edit_window_expired:") || !strings.HasPrefix(e.status(revoke).LastError, "revoke_window_expired:") {
		t.Fatalf("errors = %q, %q", e.status(old).LastError, e.status(revoke).LastError)
	}
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("an expired edit/revoke was sent")
	}
}

func TestEditAndRevokeUpdateTheLog(t *testing.T) {
	pub := &recPub{}
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) { o.Events = pub; o.Resolver = nil })
	ctx := context.Background()
	if _, err := e.st.InsertMessage(ctx, "i1", store.Message{ID: "m1", WAMessageID: "W1", ChatJID: chatA, FromMe: true,
		Source: "api", Type: "text", Text: "oi", Status: "sent", Timestamp: t0}, store.ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	tgt := &Target{MessageID: "m1", WAMessageID: "W1", FromMe: true, SentAt: t0}
	ed := e.enqueue(chatA, Payload{Type: "edit", Text: "olá", Target: tgt})
	rv := e.enqueue(chatA, Payload{Type: "revoke", Target: tgt})
	e.eventually("sent", func() bool { return e.status(rv).Status == store.OutSent })
	req := e.fake.WA.Sent()
	if len(req) != 2 || req[0].Type != "edit" || req[0].TargetId != "W1" || req[0].Text != "olá" || req[1].Type != "revoke" || !req[1].TargetFromMe {
		t.Fatalf("requests = %+v", req)
	}
	m, _ := e.st.GetMessage(ctx, "i1", "m1")
	if m.RevokedAt.IsZero() || m.Text != "" || m.EditedAt.IsZero() {
		t.Fatalf("log = %+v", m)
	}
	for _, id := range []string{ed, rv} {
		if _, err := e.st.GetMessage(ctx, "i1", id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("update %s logged as a message", id)
		}
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	var types []string
	for _, ev := range pub.evs {
		types = append(types, ev.typ)
	}
	want := []string{events.TypeMessageEdited, events.TypeMessageStatus, events.TypeMessageRevoked, events.TypeMessageStatus}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v", types)
	}
	if u := pub.evs[0].data.(events.MessageUpdate); u.ID != "m1" || u.Text != "olá" || u.Source != "api" {
		t.Fatalf("edited = %+v", u)
	}
}

func TestNewTypesBuildRequestsAndLogMeta(t *testing.T) {
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, noLookup)
	meta := &store.MessageMeta{ReplyTo: "Q1", Mentions: []string{"5511777776666@s.whatsapp.net"},
		Poll: &store.Poll{Name: "Vem?", Options: []string{"sim", "não"}, Selectable: 1}}
	id := e.enqueue(chatA, Payload{Type: "poll", Text: "Vem?", Meta: meta,
		Quote: &QuoteTarget{WAMessageID: "Q1", Text: "pergunta"}, LinkPreview: &LinkPreview{URL: "https://x"}})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	r := e.fake.WA.Sent()[0]
	if r.Poll.GetName() != "Vem?" || len(r.Poll.GetOptions()) != 2 || r.Quote.GetMessageId() != "Q1" ||
		len(r.Mentions) != 1 || r.LinkPreview.GetUrl() != "https://x" {
		t.Fatalf("request = %+v", r)
	}
	m, _ := e.st.GetMessage(context.Background(), "i1", id)
	if got := store.ParseMeta(m.MetaJSON); got.Poll == nil || got.ReplyTo != "Q1" {
		t.Fatalf("logged meta = %q", m.MetaJSON)
	}
}

func TestCancelAndClear(t *testing.T) {
	pub := &recPub{}
	e := newEnvAt(t, t0, fastSettings(), instance.Reconnecting, func(o *Options) { o.Events = pub; o.Resolver = nil })
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := e.enqueue(chatA, Payload{Type: "image", MediaFile: file})
	e.enqueue(chatB, Payload{Type: "text", Text: "b"})
	e.enqueue(chatB, Payload{Type: "text", Text: "c"})
	waited := make(chan store.Outbound, 1)
	go func() {
		o, _ := e.q.Wait(ctx, "i1", a)
		waited <- o
	}()
	time.Sleep(20 * time.Millisecond)
	o, err := e.q.Cancel(ctx, "i1", a)
	if err != nil || o.Status != store.OutFailed || !Canceled(o.LastError) {
		t.Fatalf("cancel = %+v, %v", o, err)
	}
	if got := <-waited; got.Status != store.OutFailed {
		t.Fatalf("waiter = %+v", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("staged media not removed")
	}
	if _, err := e.q.Cancel(ctx, "i1", a); !errors.Is(err, store.ErrNotQueued) {
		t.Fatalf("cancel twice = %v", err)
	}
	n, err := e.q.Clear(ctx, "i1")
	if err != nil || n != 2 {
		t.Fatalf("clear = %d, %v", n, err)
	}
	if pub.len() != 3 {
		t.Fatalf("status events = %d", pub.len())
	}
	e.connect()
	time.Sleep(30 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("a canceled item was sent")
	}
}
