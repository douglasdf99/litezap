package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWebhookCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	g := Webhook{ID: "wh_g", URL: "https://example.com/g", Secret: "s1", Events: []string{"*"}, Version: 1, Enabled: true}
	w := Webhook{ID: "wh_i", InstanceID: "i1", URL: "https://example.com/i", Secret: "s2", Events: []string{"message.*"}, Version: 1, Enabled: true}
	for _, x := range []Webhook{g, w} {
		if err := s.CreateWebhook(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetWebhook(ctx, "wh_i")
	if err != nil || got.InstanceID != "i1" || got.Secret != "s2" || got.Events[0] != "message.*" || !got.Enabled || got.CreatedAt.IsZero() {
		t.Fatalf("get = %+v, %v", got, err)
	}
	got.Enabled, got.URL, got.Events = false, "https://example.com/i2", []string{"instance.status"}
	if err := s.UpdateWebhook(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListWebhooks(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "wh_g" || list[1].Enabled || list[1].URL != "https://example.com/i2" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.DeleteWebhook(ctx, "wh_g"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWebhook(ctx, "wh_g"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted webhook: %v", err)
	}
	// Deleting the instance removes its webhooks.
	if err := s.DeleteInstance(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWebhook(ctx, "wh_i"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("instance webhook survived: %v", err)
	}
}

func dlv(id, webhook, event string, at time.Time) Delivery {
	return Delivery{ID: id, WebhookID: webhook, EventID: event, EventType: "message.received", InstanceID: "i1",
		Payload: []byte(`{"id":"` + event + `"}`), NextAttemptAt: at, ExpiresAt: at.Add(24 * time.Hour)}
}

func TestDeliveriesLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, id := range []string{"wh_a", "wh_b"} {
		if err := s.CreateWebhook(ctx, Webhook{ID: id, URL: "https://x", Secret: "s", Events: []string{"*"}, Version: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.InsertDeliveries(ctx, []Delivery{
		dlv("d1", "wh_a", "e1", t0), dlv("d2", "wh_b", "e1", t0.Add(time.Second)),
		dlv("dup", "wh_a", "e1", t0),     // same webhook and event: ignored
		dlv("gone", "wh_gone", "e1", t0), // webhook deleted meanwhile: skipped, batch still commits
		dlv("d3", "wh_a", "e2", t0.Add(time.Hour)),
	}, t0)
	if err != nil || n != 3 {
		t.Fatalf("inserted %d, %v", n, err)
	}
	due, err := s.DueDeliveries(ctx, t0.Add(time.Minute), nil, 10)
	if err != nil || len(due) != 2 || due[0].ID != "d1" || string(due[0].Payload) != `{"id":"e1"}` || due[0].Status != DeliveryPending {
		t.Fatalf("due = %+v, %v", due, err)
	}
	due, _ = s.DueDeliveries(ctx, t0.Add(time.Minute), []string{"wh_a"}, 10)
	if len(due) != 1 || due[0].ID != "d2" {
		t.Fatalf("due excluding wh_a = %+v", due)
	}
	if at, ok, err := s.NextDeliveryAt(ctx, []string{"wh_b"}); err != nil || !ok || !at.Equal(t0) {
		t.Fatalf("next = %v %v %v", at, ok, err)
	}

	if err := s.FinishDelivery(ctx, "d1", DeliveryDelivered, 1, t0, 200, "", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDelivery(ctx, "d2", DeliveryPending, 1, t0.Add(10*time.Second), 500, "http 500", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	d2, _ := s.GetDelivery(ctx, "wh_b", "d2")
	if d2.Attempts != 1 || d2.LastStatusCode != 500 || d2.LastError != "http 500" || !d2.NextAttemptAt.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("d2 = %+v", d2)
	}
	if err := s.FinishDelivery(ctx, "d2", DeliveryFailed, 2, t0, 0, "timeout", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A failed delivery takes no more retries or failures...
	if err := s.FinishDelivery(ctx, "d2", DeliveryPending, 3, t0, 500, "x", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry recorded over a failed delivery: %v", err)
	}

	failed, cursor, err := s.ListDeliveries(ctx, "wh_b", DeliveryFailed, 10, 0)
	if err != nil || len(failed) != 1 || failed[0].ID != "d2" || cursor != 0 {
		t.Fatalf("failed = %+v, %d, %v", failed, cursor, err)
	}
	all, cursor, _ := s.ListDeliveries(ctx, "wh_a", "", 1, 0)
	if len(all) != 1 || all[0].ID != "d3" || cursor == 0 { // newest first
		t.Fatalf("page 1 = %+v, %d", all, cursor)
	}
	all, cursor, _ = s.ListDeliveries(ctx, "wh_a", "", 1, cursor)
	if len(all) != 1 || all[0].ID != "d1" || all[0].DeliveredAt.IsZero() {
		t.Fatalf("page 2 = %+v, %d", all, cursor)
	}

	// Manual retry only applies to failed deliveries of that webhook.
	if err := s.RetryDelivery(ctx, "wh_a", "d2", t0, t0.Add(24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry through another webhook: %v", err)
	}
	if err := s.RetryDelivery(ctx, "wh_b", "d2", t0.Add(time.Hour), t0.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	d2, _ = s.GetDelivery(ctx, "wh_b", "d2")
	if d2.Status != DeliveryPending || d2.Attempts != 0 || !d2.ExpiresAt.Equal(t0.Add(25*time.Hour)) {
		t.Fatalf("retried = %+v", d2)
	}

	// Expiry sweeps pending deliveries past their deadline into the dead-letter.
	if n, err := s.ExpireDeliveries(ctx, t0.Add(24*time.Hour+time.Second)); err != nil || n != 0 {
		t.Fatalf("expire too early: %d %v", n, err) // d3 expires at t0+25h, d2 at t0+25h
	}
	if n, err := s.ExpireDeliveries(ctx, t0.Add(26*time.Hour)); err != nil || n != 2 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if d3, _ := s.GetDelivery(ctx, "wh_a", "d3"); d3.Status != DeliveryFailed || d3.LastError == "" {
		t.Fatalf("d3 = %+v", d3)
	}

	// Retention.
	if n, err := s.PurgeDeliveries(ctx, DeliveryDelivered, t0.Add(30*time.Second)); err != nil || n != 0 {
		t.Fatalf("purge too early: %d %v", n, err)
	}
	if n, err := s.PurgeDeliveries(ctx, DeliveryDelivered, t0.Add(48*time.Hour)); err != nil || n != 1 {
		t.Fatalf("purge delivered: %d %v", n, err)
	}
	if _, err := s.GetDelivery(ctx, "wh_a", "d1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged delivery: %v", err)
	}
	// ...but a success is recorded over one (expired while its last attempt was in flight).
	if err := s.FinishDelivery(ctx, "d3", DeliveryDelivered, 1, t0, 200, "", t0.Add(26*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d3, _ := s.GetDelivery(ctx, "wh_a", "d3"); d3.Status != DeliveryDelivered || d3.LastError != "" {
		t.Fatalf("d3 = %+v", d3)
	}
	// Deleting a webhook removes its deliveries.
	if err := s.DeleteWebhook(ctx, "wh_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDelivery(ctx, "wh_b", "d2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delivery of a deleted webhook: %v", err)
	}
}
