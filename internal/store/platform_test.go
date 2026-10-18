package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

func TestInstanceMetaAndPage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i, name := range []string{"Loja Centro", "Loja_Sul", "Suporte"} {
		if err := s.CreateInstance(ctx, Instance{ID: "i" + string(rune('1'+i)), Name: name, State: instance.Created, TokenHash: name}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at
	}
	tags := []string{"vip", "sp"}
	allow := []string{"10.0.0.0/8"}
	name := "Loja Centro 2"
	if err := s.UpdateInstanceMeta(ctx, "i1", InstanceMeta{Name: &name, Tags: &tags, IPAllowlist: &allow}); err != nil {
		t.Fatal(err)
	}
	in, _ := s.GetInstance(ctx, "i1")
	if in.Name != name || !slices.Equal(in.Tags, tags) || !slices.Equal(in.IPAllowlist, allow) {
		t.Fatalf("meta = %+v", in)
	}
	if in2, _ := s.GetInstance(ctx, "i2"); in2.Tags == nil || len(in2.Tags) != 0 {
		t.Fatalf("default tags = %#v", in2.Tags)
	}
	if err := s.UpdateInstanceMeta(ctx, "nope", InstanceMeta{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	_ = s.UpdateInstanceState(ctx, "i3", instance.Connected, "")

	ids := func(q InstanceQuery) []string {
		t.Helper()
		list, err := s.ListInstancesPage(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, in := range list {
			out = append(out, in.ID)
		}
		return out
	}
	if got := ids(InstanceQuery{Tag: "vip"}); !slices.Equal(got, []string{"i1"}) {
		t.Errorf("tag = %v", got)
	}
	if got := ids(InstanceQuery{States: []string{"connected"}}); !slices.Equal(got, []string{"i3"}) {
		t.Errorf("state = %v", got)
	}
	if got := ids(InstanceQuery{Search: "loja"}); !slices.Equal(got, []string{"i1", "i2"}) {
		t.Errorf("search = %v", got)
	}
	// LIKE wildcards in the search are literal.
	if got := ids(InstanceQuery{Search: "a_s"}); !slices.Equal(got, []string{"i2"}) {
		t.Errorf("search underscore = %v", got)
	}
	first, _ := s.ListInstancesPage(ctx, InstanceQuery{Limit: 2})
	if len(first) != 2 {
		t.Fatalf("page = %v", first)
	}
	last := first[1]
	if got := ids(InstanceQuery{Limit: 2, AfterCreated: FormatTS(last.CreatedAt), AfterID: last.ID}); !slices.Equal(got, []string{"i3"}) {
		t.Errorf("next page = %v", got)
	}
}

func TestAPIKeysAndPairingLinks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if err := s.CreateInstance(ctx, Instance{ID: "i1", Name: "A", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := s.CreateAPIKey(ctx, APIKey{ID: "k1", InstanceID: "i1", Name: "bi", KeyHash: "kh", Scopes: []string{"read", "send"},
		IPAllowlist: []string{"1.2.3.4/32"}, ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	k, err := s.APIKeyByHash(ctx, "kh")
	if err != nil || !slices.Equal(k.Scopes, []string{"read", "send"}) || !k.ExpiresAt.Equal(exp) || k.IPAllowlist[0] != "1.2.3.4/32" {
		t.Fatalf("key = %+v %v", k, err)
	}
	if err := s.TouchAPIKey(ctx, "k1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListAPIKeys(ctx, "i1"); len(list) != 1 || list[0].LastUsedAt.IsZero() {
		t.Fatalf("list = %+v", list)
	}
	if err := s.DeleteAPIKey(ctx, "other", "k1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete from another instance = %v", err)
	}

	now := time.Now()
	_ = s.CreatePairingLink(ctx, PairingLink{ID: "p1", InstanceID: "i1", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)})
	_ = s.CreatePairingLink(ctx, PairingLink{ID: "p2", InstanceID: "i1", CreatedAt: now, ExpiresAt: now.Add(-time.Minute)})
	if n, _ := s.CountLivePairingLinks(ctx, "i1", now); n != 1 {
		t.Fatalf("live = %d", n)
	}
	if n, _ := s.MarkPairingLinksUsed(ctx, "i1", now); n != 1 {
		t.Fatalf("used = %d", n)
	}
	if l, _ := s.GetPairingLink(ctx, "p1"); l.UsedAt.IsZero() {
		t.Fatal("p1 not used")
	}
	if n, _ := s.RevokePairingLinks(ctx, "i1", now); n != 1 { // p2 (expired, unused)
		t.Fatalf("revoked = %d", n)
	}
	_ = s.PrunePairingLinks(ctx, now)
	if _, err := s.GetPairingLink(ctx, "p2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned = %v", err)
	}
	// Deleting the instance removes its keys and links.
	if err := s.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.APIKeyByHash(ctx, "kh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key after delete = %v", err)
	}
	if _, err := s.GetPairingLink(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("link after delete = %v", err)
	}
}
