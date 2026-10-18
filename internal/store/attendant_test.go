package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/douglasdf99/litezap/internal/instance"
)

func TestAttendantSettingsAndAgents(t *testing.T) {
	ctx := context.Background()
	s := agentStore(t)
	if _, err := s.GetAttendantSettings(ctx, "i1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if err := s.PutAttendantSettings(ctx, "i1", AttendantSettings{SettingsJSON: `{"a":1}`, SecretsEnc: "v1:x"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAttendantSettings(ctx, "i1")
	if err != nil || got.SettingsJSON != `{"a":1}` || got.SecretsEnc != "v1:x" || got.UpdatedAt.IsZero() {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := s.CreateInstance(ctx, Instance{ID: "i2", Name: "i2", State: instance.Created, TokenHash: "h2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAttendantAgent(ctx, AttendantAgent{ID: "a1", InstanceID: "i1", Name: "Ana", ProfileJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	// Another instance never reaches the agent.
	if _, err := s.GetAttendantAgent(ctx, "i2", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-instance get = %v", err)
	}
	if err := s.UpdateAttendantAgent(ctx, AttendantAgent{ID: "a1", InstanceID: "i2", Name: "x", ProfileJSON: "{}"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-instance update = %v", err)
	}
	if err := s.DeleteAttendantAgent(ctx, "i2", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-instance delete = %v", err)
	}
	if err := s.UpdateAttendantAgent(ctx, AttendantAgent{ID: "a1", InstanceID: "i1", Name: "Bia", ProfileJSON: `{"x":1}`}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAttendantAgents(ctx, "i1")
	if err != nil || len(list) != 1 || list[0].Name != "Bia" || list[0].ProfileJSON != `{"x":1}` {
		t.Fatalf("list %+v %v", list, err)
	}
	if n, _ := s.CountAttendantAgents(ctx, "i1"); n != 1 {
		t.Fatalf("count %d", n)
	}
	if err := s.DeleteAttendantAgent(ctx, "i1", "a1"); err != nil {
		t.Fatal(err)
	}
	// Deleting the instance removes its rows.
	_ = s.PutAttendantSettings(ctx, "i2", AttendantSettings{SettingsJSON: "{}"})
	if err := s.DeleteInstance(ctx, "i2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAttendantSettings(ctx, "i2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settings survived the instance: %v", err)
	}
}

func TestAttendantUsageCap(t *testing.T) {
	ctx := context.Background()
	s := agentStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN := 0
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ReserveAttendantCall(ctx, "i1", "2026-10-18", 3)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				okN++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okN != 3 {
		t.Fatalf("reserved %d calls, want 3", okN)
	}
	if err := s.AddAttendantTokens(ctx, "i1", "2026-10-18", 100, 20, 400); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ReserveAttendantCall(ctx, "i1", "2026-10-19", 0); !ok {
		t.Fatal("no cap refused")
	}
	u, err := s.ListAttendantUsage(ctx, "i1", "2026-10-18")
	if err != nil || len(u) != 2 || u[0].Day != "2026-10-19" || u[1].Calls != 3 || u[1].InputTokens != 100 || u[1].CostMicros != 400 {
		t.Fatalf("usage %+v %v", u, err)
	}
}

func TestClientIDsForMessages(t *testing.T) {
	ctx := context.Background()
	s := agentStore(t)
	for _, o := range []Outbound{
		{ID: "q1", InstanceID: "i1", ClientMessageID: "agent:m1", ChatJID: "c", Payload: "{}"},
		{ID: "q2", InstanceID: "i1", ClientMessageID: "call-reject-x", ChatJID: "c", Payload: "{}"},
		{ID: "q3", InstanceID: "i1", ChatJID: "c", Payload: "{}"},
	} {
		if _, _, err := s.Enqueue(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.ClientIDsForMessages(ctx, "i1", []string{"q1", "q2", "q3", "nope"})
	if err != nil || len(m) != 2 || m["q1"] != "agent:m1" || m["q2"] != "call-reject-x" {
		t.Fatalf("got %v %v", m, err)
	}
	if m, _ := s.ClientIDsForMessages(ctx, "other", []string{"q1"}); len(m) != 0 {
		t.Fatalf("cross-instance: %v", m)
	}
}
