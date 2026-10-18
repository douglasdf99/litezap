package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

func TestSweep(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateInstance(ctx, store.Instance{ID: "i1", Name: "i1", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	chat := "5511988887777@s.whatsapp.net"
	for i, ts := range []time.Time{t0, t0.AddDate(0, 0, 10)} {
		id := []string{"old", "new"}[i]
		m := store.Message{ID: "msg_" + id, WAMessageID: "W" + id, ChatJID: chat, Source: "contact", Type: "image",
			Status: "received", Timestamp: ts, Media: &store.Media{ID: "med_" + id, Kind: "image", Ref: []byte{1}}}
		if _, err := st.InsertMessage(ctx, "i1", m, store.ChatUpdate{}); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, id)
		_ = os.WriteFile(f, []byte("x"), 0o600)
		_ = st.SetMediaFile(ctx, "med_"+id, f, ts.AddDate(0, 0, 7))
	}
	now := t0.AddDate(0, 0, 20)
	off := &Sweeper{Store: st, Now: func() time.Time { return now }}
	if off.Enabled() {
		t.Fatal("enabled without configuration")
	}
	off.Sweep(ctx)
	if _, err := os.Stat(filepath.Join(dir, "old")); err != nil {
		t.Fatal("deleted with retention off")
	}

	s := &Sweeper{Store: st, Media: true, Messages: 15 * 24 * time.Hour, Now: func() time.Time { return now }}
	s.Sweep(ctx)
	if _, err := st.GetMessage(ctx, "i1", "msg_old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old message kept: %v", err)
	}
	if _, err := st.GetMessage(ctx, "i1", "msg_new"); err != nil {
		t.Fatalf("recent message pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old media file kept")
	}
	// The recent message's copy expired (expiry t0+17 days): file gone, row kept.
	if _, err := os.Stat(filepath.Join(dir, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired copy kept")
	}
	if md, err := st.GetMedia(ctx, "i1", "med_new"); err != nil || md.Path != "" {
		t.Fatalf("media row = %+v, %v", md, err)
	}
	s.Start()
	s.Close()
}
