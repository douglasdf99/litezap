package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMetaEditAndRevoke(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	meta := MessageMeta{ReplyTo: "Q1", Poll: &Poll{Name: "Vem?", Options: []string{"sim", "não"}, Selectable: 1}}
	if _, err := s.InsertMessage(ctx, "i1", Message{ID: "m1", WAMessageID: "W1", ChatJID: "a@s.whatsapp.net",
		FromMe: true, Source: "api", Type: "text", Text: "oi", Status: "sent", Timestamp: t0, MetaJSON: meta.JSON()}, ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetMessage(ctx, "i1", "m1")
	if err != nil || ParseMeta(m.MetaJSON).Poll.Options[1] != "não" || ParseMeta(m.MetaJSON).ReplyTo != "Q1" {
		t.Fatalf("meta = %+v, %v", m, err)
	}
	if (MessageMeta{}).JSON() != "" || !ParseMeta("garbage").Empty() {
		t.Fatal("empty meta must encode as empty")
	}
	e, err := s.EditMessage(ctx, "i1", "a@s.whatsapp.net", "W1", "olá", t0.Add(time.Minute))
	if err != nil || e.ID != "m1" || e.Text != "olá" || !e.EditedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("edit = %+v, %v", e, err)
	}
	if _, err := s.EditMessage(ctx, "i1", "b@s.whatsapp.net", "W1", "x", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit in another chat = %v", err)
	}
	r, err := s.RevokeMessage(ctx, "i1", "a@s.whatsapp.net", "W1", t0.Add(2*time.Minute))
	if err != nil || r.Text != "" || r.RevokedAt.IsZero() {
		t.Fatalf("revoke = %+v, %v", r, err)
	}
	if _, err := s.RevokeMessage(ctx, "i1", "a@s.whatsapp.net", "W1", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
	if _, err := s.EditMessage(ctx, "i1", "a@s.whatsapp.net", "W1", "x", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit after revoke = %v", err)
	}
	list, _, err := s.ListMessages(ctx, "i1", "a@s.whatsapp.net", 10, nil)
	if err != nil || len(list) != 1 || list[0].RevokedAt.IsZero() || list[0].MetaJSON == "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestCancelAndListQueue(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	enqueue(t, s, "q1", "a@s.whatsapp.net", "", t0)
	enqueue(t, s, "q2", "b@s.whatsapp.net", "", t0)
	enqueue(t, s, "q3", "c@s.whatsapp.net", "", t0)
	if err := inFlight(t, s, "q1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelOutbound(ctx, "i1", "q1", "by the API"); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("in flight = %v", err)
	}
	if _, err := s.CancelOutbound(ctx, "i1", "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	o, err := s.CancelOutbound(ctx, "i1", "q2", "by the API")
	if err != nil || o.Status != OutFailed || o.LastError != "canceled: by the API" {
		t.Fatalf("cancel = %+v, %v", o, err)
	}
	if _, err := s.CancelOutbound(ctx, "i1", "q2", "again"); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("cancel twice = %v", err)
	}
	// A canceled item can no longer go in flight.
	if err := inFlight(t, s, "q2", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("in flight after cancel = %v", err)
	}
	active, _, err := s.ListOutbound(ctx, "i1", []string{OutQueued, OutInFlight}, 10, 0)
	if err != nil || len(active) != 2 || active[0].ID != "q1" || active[1].ID != "q3" {
		t.Fatalf("active = %+v, %v", active, err)
	}
	page, cur, err := s.ListOutbound(ctx, "i1", nil, 2, 0)
	if err != nil || len(page) != 2 {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	page, _, err = s.ListOutbound(ctx, "i1", nil, 2, cur)
	if err != nil || len(page) != 1 || page[0].ID != "q3" {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	all, err := s.CancelQueued(ctx, "i1", "queue cleared")
	if err != nil || len(all) != 1 || all[0].ID != "q3" {
		t.Fatalf("clear = %+v, %v", all, err)
	}
	if o, _ := s.GetOutbound(ctx, "i1", "q1"); o.Status != OutInFlight {
		t.Fatalf("clear touched an in-flight item: %+v", o)
	}
	// Updates (edit/revoke/pin) go in flight without a log row.
	enqueue(t, s, "q4", "a@s.whatsapp.net", "", t0)
	if err := s.MarkInFlight(ctx, "q4", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent(ctx, "q4", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessage(ctx, "i1", "q4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update logged as a message: %v", err)
	}
}
