package attendant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeFromCatalog(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	calls := 0
	var fail error
	price := 59.9
	a := &Attendant{Clock: func() time.Time { return now },
		Catalog: func(context.Context, string) ([]KnowledgeEntry, error) {
			calls++
			if fail != nil {
				return nil, fail
			}
			return []KnowledgeEntry{{Kind: "product", Title: "Camiseta", Content: "Algodão (em estoque)", Price: &price}}, nil
		}}
	manual := 10.0
	p := Profile{Name: "x", Mode: ModeSeller, KnowledgeSource: KnowledgeCatalog, Currency: "BRL",
		Knowledge: []KnowledgeEntry{{Kind: "product", Title: "Manual", Price: &manual}, {Kind: "faq", Title: "Entrega?", Content: "Sim"}}}
	got := a.withCatalog(context.Background(), "i1", p)
	if len(got.Knowledge) != 2 || got.Knowledge[0].Kind != "faq" || got.Knowledge[1].Title != "Camiseta" {
		t.Fatalf("knowledge = %+v", got.Knowledge)
	}
	if s := systemPromptFor(got, Settings{}, "canary"); !strings.Contains(s, "Camiseta — BRL 59.90") || strings.Contains(s, "Manual") {
		t.Fatalf("prompt lacks the catalog:\n%s", s)
	}
	// Cached for 10 minutes; on a failure the last good list stays.
	a.withCatalog(context.Background(), "i1", p)
	now = now.Add(11 * time.Minute)
	fail = errors.New("down")
	if got := a.withCatalog(context.Background(), "i1", p); calls != 2 || got.Knowledge[1].Title != "Camiseta" {
		t.Fatalf("calls = %d, knowledge = %+v", calls, got.Knowledge)
	}
	// Another instance with no good list yet keeps its manual products.
	if got := a.withCatalog(context.Background(), "i2", p); got.Knowledge[0].Title != "Manual" {
		t.Fatalf("fallback = %+v", got.Knowledge)
	}
	// Manual profiles never call the catalog.
	p.KnowledgeSource = KnowledgeManual
	a.withCatalog(context.Background(), "i3", p)
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
	bad := Profile{Name: "x", Mode: ModeSeller, KnowledgeSource: "planilha"}
	bad.normalize()
	if err := bad.Validate(); err == nil {
		t.Fatal("an unknown knowledge_source must be refused")
	}
}
