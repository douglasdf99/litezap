package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func enqueue(t *testing.T, s *Store, id, chat, clientID string, due time.Time) Outbound {
	t.Helper()
	o, created, err := s.Enqueue(context.Background(), Outbound{ID: id, InstanceID: "i1", ClientMessageID: clientID,
		ChatJID: chat, WAMessageID: "WA-" + id, Payload: `{"type":"text"}`, NextAttemptAt: due})
	if err != nil || !created {
		t.Fatalf("enqueue %s = %v, %v", id, created, err)
	}
	return o
}

// inFlight marks an item in flight with its pending log row, as the dispatcher does.
func inFlight(t *testing.T, s *Store, id string, newRecipient bool) error {
	t.Helper()
	o, err := s.GetOutbound(context.Background(), "i1", id)
	if err != nil {
		t.Fatal(err)
	}
	return s.MarkInFlight(context.Background(), id, newRecipient, &Message{ID: id, WAMessageID: o.WAMessageID,
		ChatJID: o.ChatJID, FromMe: true, Source: "api", Type: "text", Text: "oi", Timestamp: t0})
}

func TestEnqueueIsIdempotentByClientID(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	first, created, err := s.Enqueue(ctx, Outbound{ID: "q1", InstanceID: "i1", ClientMessageID: "c-1",
		ChatJID: "a@s.whatsapp.net", WAMessageID: "WA-q1", Payload: "{}", RequestHash: "h1", ExpiresAt: t0.Add(time.Hour)})
	if err != nil || !created || first.RequestHash != "h1" || !first.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("first = %+v, %v, %v", first, created, err)
	}
	again, created, err := s.Enqueue(ctx, Outbound{ID: "q2", InstanceID: "i1", ClientMessageID: "c-1",
		ChatJID: "a@s.whatsapp.net", WAMessageID: "WA-q2", Payload: "{}", RequestHash: "h2"})
	if err != nil || created || again.ID != first.ID || again.RequestHash != "h1" || again.Status != OutQueued {
		t.Fatalf("again = %+v, created=%v, err=%v", again, created, err)
	}
	if got, err := s.OutboundByClientID(ctx, "i1", "c-1"); err != nil || got.ID != "q1" {
		t.Fatalf("by client id = %+v, %v", got, err)
	}
	if _, err := s.OutboundByClientID(ctx, "i1", "c-404"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown client id = %v", err)
	}
	// Items without a client id never collide.
	enqueue(t, s, "q3", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q4", "a@s.whatsapp.net", "", t0)
}

func TestNextOutboundKeepsPerChatOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "a1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "a2", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "b1", "b@s.whatsapp.net", "", t0)

	next := func() string {
		t.Helper()
		o, err := s.NextOutbound(ctx, "i1", t0)
		if errors.Is(err, ErrNotFound) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return o.ID
	}
	if got := next(); got != "a1" {
		t.Fatalf("next = %q, want a1", got)
	}
	// a1 in backoff (due later) holds a2, but b1 goes ahead.
	if err := inFlight(t, s, "a1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Requeue(ctx, "a1", t0.Add(time.Minute), 1, "worker_timeout: slow"); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "b1" {
		t.Fatalf("next = %q, want b1", got)
	}
	if err := inFlight(t, s, "b1", true); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "" {
		t.Fatalf("next = %q, want none (a2 waits for a1)", got)
	}
	due, ok, err := s.NextDue(ctx, "i1")
	if err != nil || !ok || !due.Equal(t0.Add(time.Minute)) {
		t.Fatalf("next due = %v %v %v", due, ok, err)
	}
	if err := inFlight(t, s, "b1", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double in_flight = %v", err)
	}
}

func TestMarkSentLogsMessageAndCountsRamp(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q2", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q3", "b@s.whatsapp.net", "", t0)
	for _, id := range []string{"q1", "q2", "q3"} {
		if err := inFlight(t, s, id, true); err != nil {
			t.Fatal(err)
		}
		if m, _ := s.GetMessage(ctx, "i1", id); m.Status != "pending" {
			t.Fatalf("in-flight log row = %+v", m)
		}
		if err := s.MarkSent(ctx, id, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetOutbound(ctx, "i1", "q1")
	if got.Status != OutSent || !got.SentAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("q1 = %+v", got)
	}
	m, err := s.GetMessage(ctx, "i1", "q1")
	if err != nil || m.Source != "api" || m.Status != "sent" || !m.Timestamp.Equal(t0.Add(time.Minute)) {
		t.Fatalf("logged message = %+v, %v", m, err)
	}
	if c, err := s.GetChat(ctx, "i1", "a@s.whatsapp.net"); err != nil || !c.LastMessageAt.Equal(t0.Add(time.Minute)) || c.HasInbound {
		t.Fatalf("chat = %+v, %v", c, err)
	}
	n, counted, err := s.NewRecipientsSince(ctx, "i1", "a@s.whatsapp.net", t0)
	if err != nil || n != 2 || !counted {
		t.Fatalf("ramp count = %d %v %v", n, counted, err)
	}
	if n, counted, _ := s.NewRecipientsSince(ctx, "i1", "z@s.whatsapp.net", t0.Add(time.Hour)); n != 0 || counted {
		t.Fatalf("later window = %d %v", n, counted)
	}
	if own, _ := s.IsOwnSent(ctx, "i1", "WA-q1"); !own {
		t.Fatal("WA-q1 should be ours")
	}
	if own, _ := s.IsOwnSent(ctx, "i1", "WA-phone"); own {
		t.Fatal("WA-phone is not ours")
	}
}

// Regression: a read receipt can arrive between the worker's Send and MarkSent.
func TestReceiptBeforeMarkSentIsKept(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyReceipt(ctx, "i1", []string{"WA-q1"}, "read"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent(ctx, "q1", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.GetMessage(ctx, "i1", "q1"); m.Status != "read" {
		t.Fatalf("status = %q, want read", m.Status)
	}
}

// Regression: sending to a group (an update without IsGroup) must not turn it into a direct chat.
func TestSendDoesNotDowngradeGroup(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	group := "1203630@g.us"
	if _, err := s.InsertMessage(ctx, "i1", Message{ID: "in1", WAMessageID: "IN1", ChatJID: group, Source: "contact",
		Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{IsGroup: true, Inbound: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessage(ctx, "i1", Message{ID: "in2", WAMessageID: "IN2", ChatJID: group, Source: "contact",
		Type: "text", Status: "received", Timestamp: t0}, ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "q1", group, "", t0)
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent(ctx, "q1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetChat(ctx, "i1", group); !c.IsGroup {
		t.Fatalf("group downgraded: %+v", c)
	}
}

func TestDeferFailRetargetAndRecover(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q2", "b@s.whatsapp.net", "", t0)
	enqueue(t, s, "q3", "c@s.whatsapp.net", "", t0)
	enqueue(t, s, "q4", "551188887777@s.whatsapp.net", "", t0)
	enqueue(t, s, "q5", "551188887777@s.whatsapp.net", "", t0)
	if err := s.Defer(ctx, "q1", t0.Add(24*time.Hour), "ramp_limit"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.GetOutbound(ctx, "i1", "q1"); o.Status != OutQueued || o.Attempts != 0 || o.LastError != "ramp_limit" {
		t.Fatalf("deferred = %+v", o)
	}
	if err := s.DeferLookup(ctx, "q1", t0.Add(time.Minute), 2, "lookup_failed: boom"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.GetOutbound(ctx, "i1", "q1"); o.LookupFailures != 2 || o.Attempts != 0 || !o.NextAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("lookup-deferred = %+v", o)
	}
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, "q1", 1, "invalid_number: nope"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessage(ctx, "i1", "q1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed item left its pending log row: %v", err)
	}
	if err := s.RetargetChat(ctx, "i1", "551188887777@s.whatsapp.net", "5511988887777@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"q4", "q5"} {
		if o, _ := s.GetOutbound(ctx, "i1", id); o.ChatJID != "5511988887777@s.whatsapp.net" {
			t.Fatalf("%s not retargeted: %+v", id, o)
		}
	}
	// q2 crashed mid-send before any receipt; q3 was sent and already read.
	for _, id := range []string{"q2", "q3"} {
		if err := inFlight(t, s, id, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApplyReceipt(ctx, "i1", []string{"WA-q3"}, "delivered"); err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverInFlight(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d, %v", n, err)
	}
	for id, want := range map[string]string{"q1": OutFailed, "q2": OutQueued, "q3": OutSent} {
		if o, _ := s.GetOutbound(ctx, "i1", id); o.Status != want {
			t.Errorf("%s = %s, want %s", id, o.Status, want)
		}
	}
}

func TestNumberCache(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	if _, err := s.GetCachedNumber(ctx, "i1", "551188887777"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty cache = %v", err)
	}
	c := CachedNumber{Phone: "551188887777", Found: true, JID: "5511988887777@s.whatsapp.net", CheckedAt: t0}
	if err := s.PutCachedNumber(ctx, "i1", c); err != nil {
		t.Fatal(err)
	}
	c.Found, c.JID, c.CheckedAt = false, "", t0.Add(time.Hour)
	if err := s.PutCachedNumber(ctx, "i1", c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCachedNumber(ctx, "i1", "551188887777")
	if err != nil || got.Found || !got.CheckedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("cached = %+v, %v", got, err)
	}
}

func TestLookupsSince(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	for i, at := range []time.Time{t0.Add(-25 * time.Hour), t0, t0.Add(time.Minute)} {
		c := CachedNumber{Phone: "55119000000" + string(rune('0'+i)), CheckedAt: at}
		if err := s.PutCachedNumber(ctx, "i1", c); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.LookupsSince(ctx, "i1", t0); err != nil || n != 2 {
		t.Fatalf("lookups since t0 = %d, %v", n, err)
	}
}
