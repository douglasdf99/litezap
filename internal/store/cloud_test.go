package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageAddListAndTotals(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if err := s.AddUsage(ctx, []Usage{
		{InstanceID: "i1", Day: "2026-10-17", MessagesSent: 2, MediaBytes: 100},
		{InstanceID: "i1", Day: "2026-10-17", MessagesSent: 1, MessagesReceived: 4, ConnectedSeconds: 60},
		{InstanceID: "i2", Day: "2026-10-18", MessagesReceived: 1},
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListUsage(ctx, "2026-10-01", "2026-10-31", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %+v, %v", all, err)
	}
	if u := all[0]; u.InstanceID != "i1" || u.MessagesSent != 3 || u.MessagesReceived != 4 || u.MediaBytes != 100 || u.ConnectedSeconds != 60 {
		t.Fatalf("i1 = %+v", u)
	}
	one, _ := s.ListUsage(ctx, "2026-10-18", "2026-10-18", "i1")
	if len(one) != 0 {
		t.Fatalf("filtered = %+v", one)
	}
	tot, err := s.UsageTotals(ctx)
	if err != nil || tot.MessagesSent != 3 || tot.MessagesReceived != 5 {
		t.Fatalf("totals = %+v, %v", tot, err)
	}
}

func TestSyncWebhookUsageCountsCustomerDeliveriesOnly(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for id, owner := range map[string]string{"wh_c": "", "wh_op": OwnerOperator} {
		if err := s.CreateWebhook(ctx, Webhook{ID: id, URL: "https://x", Secret: "s", Events: []string{"*"}, Version: 1, Enabled: true, Owner: owner}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetWebhook(ctx, "wh_op")
	if got.Owner != OwnerOperator {
		t.Fatalf("owner = %q", got.Owner)
	}
	day := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	if _, err := s.InsertDeliveries(ctx, []Delivery{
		dlv("d1", "wh_c", "e1", day), dlv("d2", "wh_c", "e2", day), dlv("d3", "wh_op", "e1", day), dlv("d4", "wh_c", "e3", day),
	}, day); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d1", "d2", "d3"} {
		if err := s.FinishDelivery(ctx, id, DeliveryDelivered, 1, day, 200, "", day); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SyncWebhookUsage(ctx, "2026-10-17"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.ListUsage(ctx, "2026-10-17", "2026-10-17", "i1")
	if len(u) != 1 || u[0].WebhookDeliveries != 2 {
		t.Fatalf("usage = %+v", u)
	}
	// Pruned deliveries never lower the count.
	if _, err := s.PurgeDeliveries(ctx, DeliveryDelivered, day.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncWebhookUsage(ctx, "2026-10-17"); err != nil {
		t.Fatal(err)
	}
	u, _ = s.ListUsage(ctx, "2026-10-17", "2026-10-17", "i1")
	if len(u) != 1 || u[0].WebhookDeliveries != 2 {
		t.Fatalf("after purge = %+v", u)
	}
}

func TestKVAndSuspensions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.GetKV(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	_ = s.SetKV(ctx, "k", "1")
	_ = s.SetKV(ctx, "k", "2")
	if v, _ := s.GetKV(ctx, "k"); v != "2" {
		t.Fatalf("kv = %q", v)
	}
	_ = s.DeleteKV(ctx, "k")
	if _, err := s.GetKV(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal("not deleted")
	}

	if err := s.SetSendingSuspended(ctx, "nope", true, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown instance = %v", err)
	}
	seedInstance(t, s, "i1")
	seedInstance(t, s, "i2")
	_ = s.SetSendingSuspended(ctx, "i1", true, "billing")
	_ = s.SetSendingSuspended(ctx, "i2", true, "abuse")
	_ = s.SetSendingSuspended(ctx, "i2", false, "")
	list, err := s.ListSuspensions(ctx)
	if err != nil || len(list) != 1 || list[0].InstanceID != "i1" || list[0].Reason != "billing" || list[0].Since.IsZero() {
		t.Fatalf("suspensions = %+v, %v", list, err)
	}
	if err := s.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSuspensions(ctx); len(list) != 0 {
		t.Fatalf("flags survived the instance: %+v", list)
	}
	if n, _ := s.CountInstances(ctx); n != 1 {
		t.Fatalf("count = %d", n)
	}
	by, _ := s.CountInstancesByState(ctx)
	if by["created"] != 1 {
		t.Fatalf("by state = %v", by)
	}
}

func TestRetentionPrunesMessagesAndExpiresMedia(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	dir := t.TempDir()
	chat := "5511988887777@s.whatsapp.net"
	old := Message{ID: "msg_old", WAMessageID: "W1", ChatJID: chat, Source: "contact", Type: "image", Status: "received",
		Timestamp: t0, Media: &Media{ID: "med_old", Kind: "image", Ref: []byte{1}}}
	recent := Message{ID: "msg_new", WAMessageID: "W2", ChatJID: chat, Source: "contact", Type: "image", Status: "received",
		Timestamp: t0.Add(48 * time.Hour), Media: &Media{ID: "med_new", Kind: "image", Ref: []byte{1}}}
	for _, m := range []Message{old, recent} {
		if _, err := s.InsertMessage(ctx, "i1", m, ChatUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	oldFile, newFile := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	for _, f := range []string{oldFile, newFile} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.SetMediaFile(ctx, "med_old", oldFile, t0.Add(time.Hour))
	_ = s.SetMediaFile(ctx, "med_new", newFile, t0.Add(time.Hour))

	n, err := s.PruneMessages(ctx, t0.Add(24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := os.Stat(oldFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old media file kept")
	}
	if _, err := s.GetMedia(ctx, "i1", "med_old"); !errors.Is(err, ErrNotFound) {
		t.Fatal("old media row kept")
	}

	dropped, err := s.ExpireMediaFiles(ctx, t0.Add(2*time.Hour))
	if err != nil || dropped != 1 {
		t.Fatalf("expired %d, %v", dropped, err)
	}
	if _, err := os.Stat(newFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired file kept")
	}
	md, err := s.GetMedia(ctx, "i1", "med_new")
	if err != nil || md.Path != "" {
		t.Fatalf("media after expiry = %+v, %v", md, err)
	}
}

func TestBackupTo(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	p := filepath.Join(t.TempDir(), "copy.db")
	if err := s.BackupTo(ctx, p); err != nil {
		t.Fatal(err)
	}
	c, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if n, _ := c.CountInstances(ctx); n != 1 {
		t.Fatalf("copy has %d instances", n)
	}
}
