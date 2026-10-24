package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
)

// App state health (v0.1.1 repair). The app state is replicated on every device of the
// account and each device checks every patch against its own LT-hash; once the local
// copy diverges, every later patch of that collection fails with "mismatching LTHash"
// and our own writes get 409 conflicts. The only repair is a full resync: drop the local
// version, hash and value MACs and fetch the collection again from a snapshot.
//
// This file:
//   - serializes LiteZap's app state writes (and resyncs) per worker;
//   - sends SETs with whatsmeow's own encoder but without its 409 retry, which applies the
//     conflicting patches outside whatsmeow's app state lock and can race the
//     server_sync fetch of the same patches into a diverged local state (see
//     TestWhatsmeowConflictApplyRaceCorruptsState); on a 409 we refresh with
//     FetchAppState, under that lock, and retry once;
//   - schedules a full resync, with backoff and at most maxAutoResyncsPerHour per
//     collection, when a sync fails to verify; and runs one before a write on a
//     collection whose last sync failed;
//   - serves the manual resync (ResyncAppState);
//   - in a resync only, falls back to the lenient decode of appstate_lenient.go when the
//     server's history holds a patch whose LT-hash does not verify.

// appStateCollections are the five collections, in the order WhatsApp syncs them.
var appStateCollections = []appstate.WAPatchName{
	appstate.WAPatchCriticalBlock, appstate.WAPatchCriticalUnblockLow,
	appstate.WAPatchRegularLow, appstate.WAPatchRegularHigh, appstate.WAPatchRegular,
}

const maxAutoResyncsPerHour = 3

// autoResyncDelays is the backoff of automatic resyncs: by attempts in the last hour.
var autoResyncDelays = []time.Duration{15 * time.Second, 2 * time.Minute, 10 * time.Minute}

// appStateResyncTimeout bounds one automatic resync (a snapshot can be large).
const appStateResyncTimeout = 2 * time.Minute

// appStateGuard is the app state health of one worker.
type appStateGuard struct {
	// writeMu serializes LiteZap's app state writes and resyncs (never held by whatsmeow).
	writeMu sync.Mutex

	mu        sync.Mutex
	failed    map[appstate.WAPatchName]string      // collection -> last verify error
	attempts  map[appstate.WAPatchName][]time.Time // automatic resyncs, last hour
	scheduled map[appstate.WAPatchName]bool
}

// appStateHooks are the whatsmeow calls of this file; nil fields default to the client.
type appStateHooks struct {
	reset      func(context.Context, appstate.WAPatchName) error
	fullResync func(context.Context, appstate.WAPatchName) error
	// lenientResync is the full resync that accepts historical patches whose only
	// failure is the LT-hash check (appstate_lenient.go); resyncs only.
	lenientResync func(context.Context, appstate.WAPatchName) (lenientResult, error)
	fetch         func(context.Context, appstate.WAPatchName) error
	send          func(context.Context, appstate.PatchInfo) error // no whatsmeow retry
	version       func(context.Context, appstate.WAPatchName) (uint64, error)
	connected     func() bool
	afterFunc     func(time.Duration, func())
	now           func() time.Time
}

func (w *Whatsmeow) appStateHooks() appStateHooks {
	h := w.ash
	if h.reset == nil {
		h.reset = func(ctx context.Context, name appstate.WAPatchName) error {
			return resetAppStateLocal(ctx, w.cli.Store.AppState, name)
		}
	}
	if h.lenientResync == nil {
		h.lenientResync = w.lenientResync
	}
	if h.fullResync == nil {
		h.fullResync = func(ctx context.Context, name appstate.WAPatchName) error {
			return w.cli.FetchAppState(ctx, name, true, false)
		}
	}
	if h.fetch == nil {
		h.fetch = func(ctx context.Context, name appstate.WAPatchName) error {
			return w.cli.FetchAppState(ctx, name, false, false)
		}
	}
	if h.send == nil {
		h.send = func(ctx context.Context, p appstate.PatchInfo) error {
			return w.cli.DangerousInternals().SendAppState(ctx, p, false)
		}
	}
	if h.version == nil {
		h.version = func(ctx context.Context, name appstate.WAPatchName) (uint64, error) {
			v, _, err := w.cli.Store.AppState.GetAppStateVersion(ctx, string(name))
			return v, err
		}
	}
	if h.connected == nil {
		h.connected = w.cli.IsConnected
	}
	if h.afterFunc == nil {
		h.afterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	if h.now == nil {
		h.now = time.Now
	}
	return h
}

// isAppStateVerifyErr reports a local app state that no longer matches the server's
// patches (the errors a full resync repairs).
func isAppStateVerifyErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, appstate.ErrMismatchingLTHash) || errors.Is(err, appstate.ErrMismatchingPatchMAC) ||
		errors.Is(err, appstate.ErrMismatchingContentMAC) || errors.Is(err, appstate.ErrMismatchingIndexMAC) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "mismatching LTHash") || strings.Contains(s, "mismatching patch MAC")
}

// isAppStateConflict reports WhatsApp refusing a patch built on an outdated version.
func isAppStateConflict(err error) bool {
	return errors.Is(err, errAppStateConflict) ||
		(errors.Is(err, whatsmeow.ErrAppStateUpdate) && strings.Contains(err.Error(), `code="409"`))
}

// onAppStateSync watches whatsmeow's sync results; it reports whether evt was one.
func (w *Whatsmeow) onAppStateSync(evt any) bool {
	switch e := evt.(type) {
	case *events.AppStateSyncError:
		if isAppStateVerifyErr(e.Error) {
			w.noteAppStateFailure(e.Name, e.Error)
		} else {
			w.log.Warn("app state sync failed", "collection", string(e.Name), "full_sync", e.FullSync, "error", e.Error)
		}
		return true
	case *events.AppStateSyncComplete:
		w.markAppStateHealthy(e.Name)
		return true
	}
	return false
}

// noteAppStateErr records err of an app state call when it is a verify failure.
func (w *Whatsmeow) noteAppStateErr(name appstate.WAPatchName, err error) {
	if isAppStateVerifyErr(err) {
		w.noteAppStateFailure(name, err)
	}
}

// noteAppStateFailure marks the collection as diverged and schedules a full resync.
func (w *Whatsmeow) noteAppStateFailure(name appstate.WAPatchName, err error) {
	h := w.appStateHooks()
	g := &w.asg
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed == nil {
		g.failed, g.attempts, g.scheduled = map[appstate.WAPatchName]string{}, map[appstate.WAPatchName][]time.Time{}, map[appstate.WAPatchName]bool{}
	}
	g.failed[name] = err.Error()
	if g.scheduled[name] {
		return
	}
	n := g.recentAttemptsLocked(name, h.now())
	if n >= maxAutoResyncsPerHour {
		w.log.Error("app state out of sync; automatic resync limit reached, run POST /v1/instances/{id}/app-state/resync",
			"collection", string(name), "error", err, "resyncs_last_hour", n)
		return
	}
	delay := autoResyncDelays[min(n, len(autoResyncDelays)-1)]
	g.scheduled[name] = true
	w.log.Warn("app state out of sync; full resync scheduled", "collection", string(name), "error", err, "in", delay.String())
	h.afterFunc(delay, func() { w.autoResync(name) })
}

func (g *appStateGuard) recentAttemptsLocked(name appstate.WAPatchName, now time.Time) int {
	keep := g.attempts[name][:0]
	for _, t := range g.attempts[name] {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	g.attempts[name] = keep
	return len(keep)
}

func (w *Whatsmeow) markAppStateHealthy(name appstate.WAPatchName) {
	g := &w.asg
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed[name] != "" {
		w.log.Info("app state back in sync", "collection", string(name))
	}
	delete(g.failed, name)
}

// appStateFailed returns the last verify error of the collection ("" when healthy).
func (w *Whatsmeow) appStateFailed(name appstate.WAPatchName) string {
	g := &w.asg
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failed[name]
}

// takeResyncAttempt spends one automatic resync of the hour; false when none is left.
func (w *Whatsmeow) takeResyncAttempt(name appstate.WAPatchName) bool {
	h := w.appStateHooks()
	g := &w.asg
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attempts == nil {
		g.attempts = map[appstate.WAPatchName][]time.Time{}
	}
	now := h.now()
	if g.recentAttemptsLocked(name, now) >= maxAutoResyncsPerHour {
		return false
	}
	g.attempts[name] = append(g.attempts[name], now)
	return true
}

// autoResync runs a scheduled resync, unless the collection recovered meanwhile.
func (w *Whatsmeow) autoResync(name appstate.WAPatchName) {
	g := &w.asg
	g.mu.Lock()
	g.scheduled[name] = false
	failed := g.failed[name] != ""
	g.mu.Unlock()
	if !failed || w.cli == nil || w.cli.Store == nil || w.cli.Store.ID == nil || w.terminal.Load() {
		return
	}
	h := w.appStateHooks()
	if !h.connected() {
		// Not connected: whatsmeow syncs again on reconnect, and a failure schedules anew.
		w.log.Info("app state resync postponed: not connected", "collection", string(name))
		return
	}
	if !w.takeResyncAttempt(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), appStateResyncTimeout)
	defer cancel()
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	_, _ = w.resyncLocked(ctx, name, "auto")
}

// resyncOutcome is the result of the full resync of one collection.
type resyncOutcome struct {
	before, after uint64
	lenient       bool
	skipped       []uint64 // versions accepted without the LT-hash check
}

// resyncLocked runs a full resync of one collection; the caller holds asg.writeMu. The
// local copy is reset to v0 first (version, hash and MACs). When the strict fetch fails
// on the LT-hash of the server's history, the collection is fetched again with the
// lenient decode, and then once more incrementally (strict) to confirm.
func (w *Whatsmeow) resyncLocked(ctx context.Context, name appstate.WAPatchName, why string) (out resyncOutcome, err error) {
	h := w.appStateHooks()
	out.before, _ = h.version(ctx, name)
	defer func() {
		out.after, _ = h.version(ctx, name)
		if err != nil {
			w.log.Error("app state full resync failed", "collection", string(name), "trigger", why,
				"version_before", out.before, "version_after", out.after, "lenient", out.lenient, "error", err)
			w.noteAppStateErr(name, err)
			return
		}
		w.log.Info("app state full resync done", "collection", string(name), "trigger", why,
			"version_before", out.before, "version_after", out.after, "lenient", out.lenient, "skipped_patch_versions", out.skipped)
		w.markAppStateHealthy(name)
	}()
	if err = h.reset(ctx, name); err != nil {
		return out, err
	}
	err = h.fullResync(ctx, name)
	if err == nil || !errors.Is(err, appstate.ErrMismatchingLTHash) {
		return out, err
	}
	w.log.Warn("app state strict resync failed on the LT-hash of the server's history; retrying with the lenient decode",
		"collection", string(name), "trigger", why, "error", err)
	out.lenient = true
	res, err := h.lenientResync(ctx, name)
	out.skipped = res.skipped
	if err != nil {
		return out, fmt.Errorf("lenient resync: %w", err)
	}
	// Incremental syncs are whatsmeow's own and strict: one now confirms the state the
	// lenient decode left verifies the patches after it (and fetches any newer ones).
	if err = h.fetch(ctx, name); err != nil {
		return out, fmt.Errorf("strict sync after the lenient resync: %w", err)
	}
	return out, nil
}

// ensureAppStateSyncedLocked resyncs a collection whose last sync failed before a write
// on it (a patch built on a diverged state is refused or diverges the other devices).
// The caller holds asg.writeMu.
func (w *Whatsmeow) ensureAppStateSyncedLocked(ctx context.Context, name appstate.WAPatchName) error {
	last := w.appStateFailed(name)
	if last == "" {
		return nil
	}
	if !w.takeResyncAttempt(name) {
		return grpcErr(codes.FailedPrecondition, "app_state_out_of_sync", fmt.Sprintf(
			"the local copy of the app state %s is out of sync and the automatic resyncs of the hour were spent; run POST /v1/instances/{id}/app-state/resync", name))
	}
	if _, err := w.resyncLocked(ctx, name, "before_write"); err != nil {
		return grpcErr(codes.FailedPrecondition, "app_state_out_of_sync", fmt.Sprintf(
			"the local copy of the app state %s is out of sync and a full resync failed (%v); run POST /v1/instances/{id}/app-state/resync", name, err))
	}
	return nil
}

// ensureAppStateSynced is ensureAppStateSyncedLocked for callers that only read the
// local app state before writing (ContactAction checks the entry first).
func (w *Whatsmeow) ensureAppStateSynced(ctx context.Context, name appstate.WAPatchName) error {
	if w.appStateFailed(name) == "" {
		return nil
	}
	w.asg.writeMu.Lock()
	defer w.asg.writeMu.Unlock()
	return w.ensureAppStateSyncedLocked(ctx, name)
}

// sendAppStateSafe sends a patch built by whatsmeow's encoder (SendAppState without its
// in-place 409 retry): serialized with our other writes, after a resync when the
// collection is known to be diverged, and on a 409 refreshed under whatsmeow's lock
// and retried once.
func (w *Whatsmeow) sendAppStateSafe(ctx context.Context, patch appstate.PatchInfo) error {
	h := w.appStateHooks()
	w.asg.writeMu.Lock()
	defer w.asg.writeMu.Unlock()
	if err := w.ensureAppStateSyncedLocked(ctx, patch.Type); err != nil {
		return err
	}
	err := h.send(ctx, patch)
	if isAppStateConflict(err) {
		w.log.Info("app state conflict; refreshing and retrying once", "collection", string(patch.Type))
		if ferr := h.fetch(ctx, patch.Type); ferr != nil {
			w.noteAppStateErr(patch.Type, ferr)
			return fmt.Errorf("%w (also, refreshing failed: %w)", err, ferr)
		}
		err = h.send(ctx, patch)
	}
	w.noteAppStateErr(patch.Type, err)
	return err
}

// ResyncAppState is the manual repair: a full resync of each collection (all five when
// names is empty), one at a time, with the version before and after.
func (w *Whatsmeow) ResyncAppState(ctx context.Context, names []string) ([]*workerv1.AppStateCollectionResult, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	cols, err := parseCollections(names)
	if err != nil {
		return nil, err
	}
	if !w.appStateHooks().connected() {
		return nil, grpcErr(codes.Unavailable, "instance_not_connected", "websocket not connected")
	}
	out := make([]*workerv1.AppStateCollectionResult, 0, len(cols))
	for _, name := range cols {
		r := &workerv1.AppStateCollectionResult{Name: string(name)}
		if cerr := ctx.Err(); cerr != nil {
			r.Error = cerr.Error()
			out = append(out, r)
			continue
		}
		w.asg.writeMu.Lock()
		o, err := w.resyncLocked(ctx, name, "manual")
		w.asg.writeMu.Unlock()
		r.VersionBefore, r.VersionAfter, r.Ok = o.before, o.after, err == nil
		r.Lenient, r.SkippedPatchVersions = o.lenient, o.skipped
		if err != nil {
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return out, nil
}

func parseCollections(names []string) ([]appstate.WAPatchName, error) {
	if len(names) == 0 {
		return appStateCollections, nil
	}
	seen := map[appstate.WAPatchName]bool{}
	var out []appstate.WAPatchName
	for _, n := range names {
		name := appstate.WAPatchName(strings.TrimSpace(n))
		valid := false
		for _, c := range appStateCollections {
			valid = valid || c == name
		}
		if !valid {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", fmt.Sprintf(
				"unknown app state collection %q (critical_block, critical_unblock_low, regular_low, regular_high, regular)", n))
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}
