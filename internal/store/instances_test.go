package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/douglasdf99/litezap/internal/instance"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		if err := s.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestInstanceCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	in := Instance{ID: "inst_1", Name: "Loja", State: instance.Created, TokenHash: "h1"}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInstance(ctx, Instance{ID: "inst_2", Name: "B", State: instance.Stopped, TokenHash: "h2"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetInstance(ctx, "inst_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Loja" || got.State != instance.Created || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}

	if err := s.UpdateInstanceState(ctx, "inst_1", instance.Starting, "boot"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstanceJID(ctx, "inst_1", "5511999999999:3@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetInstance(ctx, "inst_1")
	if got.State != instance.Starting || got.StateReason != "boot" || got.JID == "" {
		t.Fatalf("after update %+v", got)
	}

	byTok, err := s.InstanceByTokenHash(ctx, "h2")
	if err != nil || byTok.ID != "inst_2" {
		t.Fatalf("by token: %+v %v", byTok, err)
	}
	if err := s.SetInstanceTokenHash(ctx, "inst_2", "h3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstanceByTokenHash(ctx, "h2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token should be gone, err=%v", err)
	}

	list, err := s.ListInstances(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "inst_1" {
		t.Fatalf("list %+v %v", list, err)
	}

	if err := s.DeleteInstance(ctx, "inst_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "inst_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.UpdateInstanceState(ctx, "nope", instance.Starting, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
