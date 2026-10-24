package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeAppState records the whatsmeow app state calls of appstate_sync.go.
type fakeAppState struct {
	mu       sync.Mutex
	calls    []string
	versions map[appstate.WAPatchName]uint64
	sendErrs []error // returned in order by send
	fullErr  map[appstate.WAPatchName]error
	lenient  map[appstate.WAPatchName]error // lenientResync result (nil: ok, skipping v49)
	fetchErr error
	timers   []func()
	delays   []time.Duration
	now      time.Time
}

func (f *fakeAppState) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeAppState) Calls() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func newAppStateAdapter(t *testing.T) (*Whatsmeow, *fakeAppState) {
	t.Helper()
	own := types.NewJID("5511900000000", types.DefaultUserServer)
	w := NewWhatsmeow(whatsmeow.NewClient(&store.Device{ID: &own}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := &fakeAppState{versions: map[appstate.WAPatchName]uint64{}, fullErr: map[appstate.WAPatchName]error{},
		lenient: map[appstate.WAPatchName]error{}, now: time.Unix(1_800_000_000, 0)}
	w.ash = appStateHooks{
		reset: func(_ context.Context, n appstate.WAPatchName) error {
			f.record("reset:" + string(n))
			f.mu.Lock()
			f.versions[n] = 0
			f.mu.Unlock()
			return nil
		},
		lenientResync: func(_ context.Context, n appstate.WAPatchName) (lenientResult, error) {
			f.record("lenient:" + string(n))
			if err := f.lenient[n]; err != nil {
				return lenientResult{}, err
			}
			f.mu.Lock()
			f.versions[n] = 52
			f.mu.Unlock()
			return lenientResult{state: appstate.HashState{Version: 52}, skipped: []uint64{49}}, nil
		},
		fullResync: func(_ context.Context, n appstate.WAPatchName) error {
			f.record("full:" + string(n))
			if err := f.fullErr[n]; err != nil {
				return err
			}
			f.mu.Lock()
			f.versions[n] += 10
			f.mu.Unlock()
			return nil
		},
		fetch: func(_ context.Context, n appstate.WAPatchName) error {
			f.record("fetch:" + string(n))
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.fetchErr
		},
		send: func(_ context.Context, p appstate.PatchInfo) error {
			f.record("send:" + string(p.Type))
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.sendErrs) == 0 {
				return nil
			}
			err := f.sendErrs[0]
			f.sendErrs = f.sendErrs[1:]
			return err
		},
		version: func(_ context.Context, n appstate.WAPatchName) (uint64, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.versions[n], nil
		},
		connected: func() bool { return true },
		afterFunc: func(d time.Duration, fn func()) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.delays, f.timers = append(f.delays, d), append(f.timers, fn)
		},
		now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
	}
	return w, f
}

func lthashErr(v int) error {
	return fmt.Errorf("failed to decode app state critical_unblock_low patches: %w",
		fmt.Errorf("failed to verify patch v%d: %w", v, appstate.ErrMismatchingLTHash))
}

func conflictErr() error {
	return fmt.Errorf(`%w (regular_high): <error code="409" text="conflict"/>`, whatsmeow.ErrAppStateUpdate)
}

// A 409 is refreshed under whatsmeow's lock (FetchAppState) and retried once; whatsmeow's
// own in-place retry is never used.
func TestSendAppStateSafeRetriesConflictAfterFetch(t *testing.T) {
	w, f := newAppStateAdapter(t)
	f.sendErrs = []error{conflictErr()}
	if err := w.sendAppStateSafe(context.Background(), buildClearChat(types.NewJID("1", types.DefaultUserServer), time.Now(), nil, false, false)); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "send:regular_high,fetch:regular_high,send:regular_high" {
		t.Fatalf("calls = %s", got)
	}
	// Two conflicts in a row: the error comes back (no loop).
	f.calls, f.sendErrs = nil, []error{conflictErr(), conflictErr()}
	if err := w.sendAppStateSafe(context.Background(), appstate.BuildPin(types.NewJID("1", types.DefaultUserServer), true)); !isAppStateConflict(err) {
		t.Fatalf("second conflict = %v", err)
	}
}

// A sync that fails to verify (the event whatsmeow dispatches, or the error of our own
// call) schedules a full resync with backoff, at most maxAutoResyncsPerHour an hour.
func TestAppStateVerifyErrorSchedulesResyncWithLimit(t *testing.T) {
	w, f := newAppStateAdapter(t)
	name := appstate.WAPatchCriticalUnblockLow

	if !w.onAppStateSync(&events.AppStateSyncError{Name: name, Error: lthashErr(49)}) {
		t.Fatal("event not handled")
	}
	// A second report while one is scheduled does not stack.
	w.noteAppStateErr(name, lthashErr(49))
	if len(f.timers) != 1 || f.delays[0] != autoResyncDelays[0] {
		t.Fatalf("scheduled %d (%v)", len(f.timers), f.delays)
	}
	if w.appStateFailed(name) == "" {
		t.Fatal("collection not marked as failed")
	}
	f.timers[0]()
	if got := f.Calls(); got != "reset:critical_unblock_low,full:critical_unblock_low" || w.appStateFailed(name) != "" {
		t.Fatalf("after the timer: calls %s, failed %q", got, w.appStateFailed(name))
	}

	// Keeps failing: backoff grows, then stops at the hourly limit.
	for i := 1; i < maxAutoResyncsPerHour; i++ {
		w.noteAppStateErr(name, lthashErr(50))
		if len(f.timers) != i+1 || f.delays[i] != autoResyncDelays[min(i, len(autoResyncDelays)-1)] {
			t.Fatalf("attempt %d: delays %v", i, f.delays)
		}
		f.timers[i]()
	}
	w.noteAppStateErr(name, lthashErr(51))
	if len(f.timers) != maxAutoResyncsPerHour {
		t.Fatalf("scheduled beyond the hourly limit: %d", len(f.timers))
	}
	// An hour later it may try again.
	f.now = f.now.Add(time.Hour + time.Second)
	w.noteAppStateErr(name, lthashErr(52))
	if len(f.timers) != maxAutoResyncsPerHour+1 {
		t.Fatalf("not rescheduled after an hour: %d", len(f.timers))
	}

	// Other sync errors (a missing key, a network failure) are only logged.
	w.onAppStateSync(&events.AppStateSyncError{Name: appstate.WAPatchRegular, Error: errors.New("failed to fetch: timeout")})
	if w.appStateFailed(appstate.WAPatchRegular) != "" {
		t.Fatal("non-verify error marked the collection")
	}
	// A completed full sync (from anywhere) marks the collection healthy.
	w.onAppStateSync(&events.AppStateSyncComplete{Name: name})
	if w.appStateFailed(name) != "" {
		t.Fatal("still failed after AppStateSyncComplete")
	}
}

// A write on a collection whose last sync failed resyncs it first; without resyncs left
// (or when the resync fails) the write is refused with app_state_out_of_sync.
func TestAppStateWriteResyncsDivergedCollectionFirst(t *testing.T) {
	w, f := newAppStateAdapter(t)
	name := appstate.WAPatchRegularHigh
	w.noteAppStateErr(name, lthashErr(35))
	clear := buildClearChat(types.NewJID("1", types.DefaultUserServer), time.Now(), nil, false, false)
	if err := w.sendAppStateSafe(context.Background(), clear); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "reset:regular_high,full:regular_high,send:regular_high" {
		t.Fatalf("calls = %s", got)
	}

	// The resync fails: nothing is sent.
	f.calls = nil
	f.fullErr[name] = lthashErr(35)
	f.lenient[name] = fmt.Errorf("mutation #1: %w", appstate.ErrMismatchingContentMAC)
	w.noteAppStateErr(name, lthashErr(36))
	err := w.sendAppStateSafe(context.Background(), clear)
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || !strings.HasPrefix(st.Message(), "app_state_out_of_sync:") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(f.Calls(), "send:") {
		t.Fatalf("sent on a diverged state: %s", f.Calls())
	}
	// The hourly resyncs are spent: refused without trying.
	f.calls = nil
	_ = w.sendAppStateSafe(context.Background(), clear)
	err = w.sendAppStateSafe(context.Background(), clear)
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || !strings.Contains(st.Message(), "app-state/resync") {
		t.Fatalf("err = %v", err)
	}
	// accountErr keeps the mapped error, and maps a raw verify error the same way.
	if st, _ := status.FromError(accountErr(err)); !strings.HasPrefix(st.Message(), "app_state_out_of_sync:") {
		t.Fatalf("accountErr = %v", accountErr(err))
	}
	if st, _ := status.FromError(accountErr(lthashErr(1))); st.Code() != codes.FailedPrecondition || !strings.HasPrefix(st.Message(), "app_state_out_of_sync:") {
		t.Fatalf("accountErr(verify) = %v", accountErr(lthashErr(1)))
	}
}

func TestResyncAppStateReportsEachCollection(t *testing.T) {
	w, f := newAppStateAdapter(t)
	f.versions[appstate.WAPatchCriticalUnblockLow] = 48
	f.fullErr[appstate.WAPatchRegularHigh] = errors.New("boom")
	w.noteAppStateErr(appstate.WAPatchCriticalUnblockLow, lthashErr(49))

	res, err := w.ResyncAppState(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 5 {
		t.Fatalf("results = %v", res)
	}
	byName := map[string]*workerv1.AppStateCollectionResult{}
	for _, r := range res {
		byName[r.Name] = r
	}
	if r := byName["critical_unblock_low"]; !r.Ok || r.VersionBefore != 48 || r.VersionAfter != 10 || r.Error != "" || r.Lenient {
		t.Fatalf("critical_unblock_low = %+v", r)
	}
	if r := byName["regular_high"]; r.Ok || r.Error != "boom" {
		t.Fatalf("regular_high = %+v", r)
	}
	if w.appStateFailed(appstate.WAPatchCriticalUnblockLow) != "" {
		t.Fatal("manual resync did not clear the failure")
	}

	// A subset, deduplicated, in the order given.
	f.calls = nil
	if _, err := w.ResyncAppState(context.Background(), []string{"regular", "regular", "critical_block"}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "reset:regular,full:regular,reset:critical_block,full:critical_block" {
		t.Fatalf("calls = %s", got)
	}
	if _, err := w.ResyncAppState(context.Background(), []string{"contacts"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown collection = %v", err)
	}
	w.ash.connected = func() bool { return false }
	if _, err := w.ResyncAppState(context.Background(), nil); status.Code(err) != codes.Unavailable {
		t.Fatalf("disconnected = %v", err)
	}
}

// A full resync starts from v0: the local copy is reset before the fetch, so a resync
// that fails part way reports where the fetch stopped, not the version it started from.
// When the strict fetch fails on the LT-hash of the server's history (and only then),
// the lenient decode runs, then a strict incremental sync confirms it; the result says
// lenient with the skipped versions.
func TestResyncFallsBackToLenientOnlyOnLTHash(t *testing.T) {
	w, f := newAppStateAdapter(t)
	cul, rh, reg := appstate.WAPatchCriticalUnblockLow, appstate.WAPatchRegularHigh, appstate.WAPatchRegular
	f.versions[cul], f.versions[rh] = 48, 40
	f.fullErr[cul] = lthashErr(49)
	f.fullErr[rh] = fmt.Errorf("failed to decode app state regular_high patches: %w", fmt.Errorf("failed to verify snapshot: %w",
		fmt.Errorf("failed to verify patch v41: %w", appstate.ErrMismatchingLTHash)))
	f.fullErr[reg] = fmt.Errorf("failed to verify patch v3: %w", appstate.ErrMismatchingPatchMAC)

	res, err := w.ResyncAppState(context.Background(), []string{"critical_unblock_low", "regular_high", "regular"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); got != "reset:critical_unblock_low,full:critical_unblock_low,lenient:critical_unblock_low,fetch:critical_unblock_low,"+
		"reset:regular_high,full:regular_high,lenient:regular_high,fetch:regular_high,reset:regular,full:regular" {
		t.Fatalf("calls = %s", got)
	}
	for _, r := range res[:2] {
		if !r.Ok || !r.Lenient || fmt.Sprint(r.SkippedPatchVersions) != "[49]" || r.VersionAfter != 52 || r.Error != "" {
			t.Fatalf("%s = %+v", r.Name, r)
		}
	}
	if r := res[0]; r.VersionBefore != 48 {
		t.Fatalf("version_before = %d", r.VersionBefore)
	}
	// A patch MAC mismatch is not the diverged-sender case: no leniency.
	if r := res[2]; r.Ok || r.Lenient || !strings.Contains(r.Error, "mismatching patch MAC") {
		t.Fatalf("regular = %+v", r)
	}
	if w.appStateFailed(cul) != "" || w.appStateFailed(rh) != "" {
		t.Fatal("lenient resync did not mark the collections healthy")
	}

	// The strict sync after the lenient decode fails: the resync fails, lenient reported.
	f.calls, f.fetchErr = nil, lthashErr(53)
	res, _ = w.ResyncAppState(context.Background(), []string{"critical_unblock_low"})
	if r := res[0]; r.Ok || !r.Lenient || !strings.Contains(r.Error, "strict sync after the lenient resync") {
		t.Fatalf("after confirm failure = %+v", r)
	}
	if w.appStateFailed(cul) == "" {
		t.Fatal("collection not marked failed")
	}
	// The lenient decode refuses (bad mutation MAC): the resync fails.
	f.calls, f.fetchErr = nil, nil
	f.lenient[cul] = fmt.Errorf("mutation #1: %w", appstate.ErrMismatchingIndexMAC)
	res, _ = w.ResyncAppState(context.Background(), []string{"critical_unblock_low"})
	if r := res[0]; r.Ok || !r.Lenient || !strings.Contains(r.Error, "lenient resync") || strings.Contains(f.Calls(), "fetch:") {
		t.Fatalf("lenient refused = %+v (calls %s)", r, f.Calls())
	}
}
