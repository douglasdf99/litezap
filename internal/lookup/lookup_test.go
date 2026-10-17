package lookup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

type workers map[string]workerv1.WorkerClient

func (w workers) Client(id string) (workerv1.WorkerClient, error) { return w[id], nil }

func setup(t *testing.T) (*Resolver, *workertest.FakeWA, *clock.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	sp := workertest.NewSpawner()
	h, err := sp.Start(context.Background(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	return &Resolver{Store: st, Workers: workers{"i1": h.Client()}, Clock: fc, PerMinute: 3, CheckPerMinute: 1}, sp.Last("i1").WA, fc
}

func TestResolveCachesAndHandlesNinthDigit(t *testing.T) {
	r, wa, fc := setup(t)
	ctx := context.Background()
	wa.SetRegistered("551188887777", "5511988887777@s.whatsapp.net")
	wa.SetUnregistered("5511000000000")
	res, err := r.Resolve(ctx, "i1", []string{"551188887777", "5511000000000", "551188887777"}, ForSend)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Found || res[0].JID != "5511988887777@s.whatsapp.net" || res[1].Found || res[2].JID != res[0].JID {
		t.Fatalf("results = %+v", res)
	}
	if wa.Checks() != 2 {
		t.Fatalf("worker lookups = %d, want 2 (duplicates collapsed)", wa.Checks())
	}
	if _, err := r.Resolve(ctx, "i1", []string{"551188887777", "5511000000000", "5511988887777"}, ForSend); err != nil || wa.Checks() != 2 {
		t.Fatalf("cached lookup (including the canonical number) hit the worker: %d, %v", wa.Checks(), err)
	}
	fc.Advance(25 * time.Hour) // the negative result expired, the positive one did not
	if _, err := r.Resolve(ctx, "i1", []string{"551188887777", "5511000000000"}, ForSend); err != nil || wa.Checks() != 3 {
		t.Fatalf("after 25h: checks = %d, %v", wa.Checks(), err)
	}
}

func TestResolveRateLimit(t *testing.T) {
	r, wa, fc := setup(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000001", "5511900000002", "5511900000003"}, ForSend); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000004"}, ForSend); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("4th lookup in a minute = %v", err)
	}
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000001"}, ForSend); err != nil {
		t.Fatalf("cached numbers are not limited: %v", err)
	}
	// check-numbers has its own bucket: exhausting it never blocks the send queue.
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000005"}, ForCheck); err != nil {
		t.Fatalf("check bucket = %v", err)
	}
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000006"}, ForCheck); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("2nd check lookup = %v", err)
	}
	fc.Advance(time.Minute)
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000004"}, ForSend); err != nil || wa.Checks() != 5 {
		t.Fatalf("after a minute: %v (checks %d)", err, wa.Checks())
	}
}

func TestResolveBatchLargerThanBucket(t *testing.T) {
	r, wa, _ := setup(t)
	ctx := context.Background()
	// check bucket is 1/min: two uncached numbers can never be served, so it must not be retryable.
	_, err := r.Resolve(ctx, "i1", []string{"5511900000001", "5511900000002"}, ForCheck)
	if !errors.Is(err, ErrTooManyNumbers) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if wa.Checks() != 0 {
		t.Fatalf("worker was called: %d", wa.Checks())
	}
	// no quota was consumed
	if _, err := r.Resolve(ctx, "i1", []string{"5511900000003"}, ForCheck); err != nil {
		t.Fatalf("quota consumed by the rejected batch: %v", err)
	}
}
