package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
)

func ordersStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), Instance{ID: "i1", Name: "i1", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUpsertOrderOwnershipAndOnce(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	o := Order{OrderID: "1", ChatJID: "a@s.whatsapp.net", Token: "t1", ItemCount: 1, ReceivedAt: time.Now()}
	// Concurrent copies of one order: exactly one is created.
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := st.UpsertOrder(ctx, "i1", o)
			if err != nil {
				t.Error(err)
			}
			if c {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("created %d times", created)
	}
	// The same chat may update it; another chat may not take it over.
	if c, err := st.UpsertOrder(ctx, "i1", Order{OrderID: "1", ChatJID: "a@s.whatsapp.net", Token: "t2", ItemCount: 2}); c || err != nil {
		t.Fatalf("update = %v %v", c, err)
	}
	if c, err := st.UpsertOrder(ctx, "i1", Order{OrderID: "1", ChatJID: "evil@s.whatsapp.net", Token: "stolen"}); c || err != nil {
		t.Fatalf("takeover = %v %v", c, err)
	}
	got, _ := st.GetOrder(ctx, "i1", "1")
	if got.Token != "t2" || got.ChatJID != "a@s.whatsapp.net" || got.ItemCount != 2 {
		t.Fatalf("stored = %+v", got)
	}
}

func TestUpsertOrderCaps(t *testing.T) {
	st := ordersStore(t)
	ctx := context.Background()
	for i := 0; i < MaxOrdersPerChatDay; i++ {
		if c, err := st.UpsertOrder(ctx, "i1", Order{OrderID: fmt.Sprint(i), ChatJID: "a@s.whatsapp.net"}); !c || err != nil {
			t.Fatalf("order %d = %v %v", i, c, err)
		}
	}
	if _, err := st.UpsertOrder(ctx, "i1", Order{OrderID: "x", ChatJID: "a@s.whatsapp.net"}); !errors.Is(err, ErrOrderCap) {
		t.Fatalf("over the chat cap = %v", err)
	}
	if c, err := st.UpsertOrder(ctx, "i1", Order{OrderID: "y", ChatJID: "b@s.whatsapp.net"}); !c || err != nil {
		t.Fatalf("another chat = %v %v", c, err)
	}
}
