package service

import (
	"context"
	"net/http"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
)

// App state writes behind experimental_app_state_writes, and the app state repair
// (v0.1.1, after the 2026-10-23 LT-hash divergence on a real device).

// AppStateWritesEnabled reports whether clearing chats and removing contacts is enabled
// (the MCP server hides those tools otherwise).
func (a *Account) AppStateWritesEnabled() bool { return a.ExperimentalAppStateWrites }

func (a *Account) requireAppStateWrites(what string) error {
	if a.ExperimentalAppStateWrites {
		return nil
	}
	return apierr.New(http.StatusForbidden, "feature_disabled",
		what+" is disabled while it is revalidated on a real device (it builds an app state change by hand); "+
			"set experimental_app_state_writes: true (LITEZAP_EXPERIMENTAL_APP_STATE_WRITES=true) to enable it at your own risk", false)
}

// appStateResyncTimeout bounds a manual resync: up to five snapshots, one at a time.
const appStateResyncTimeout = 5 * time.Minute

// AppStateResyncInput is the body of POST /instances/{id}/app-state/resync.
type AppStateResyncInput struct {
	// Collections: critical_block, critical_unblock_low, regular_low, regular_high,
	// regular. Empty: all five.
	Collections []string `json:"collections,omitempty"`
}

// AppStateCollectionResult is the result of the resync of one collection.
type AppStateCollectionResult struct {
	Name          string `json:"name"`
	OK            bool   `json:"ok"`
	VersionBefore uint64 `json:"version_before"`
	VersionAfter  uint64 `json:"version_after"`
	Error         string `json:"error,omitempty"`
	// Lenient: the strict resync failed on a historical patch of the server whose LT-hash
	// does not verify, and the collection was accepted with that check skipped for the
	// versions in SkippedPatchVersions (their mutations verified).
	Lenient              bool     `json:"lenient"`
	SkippedPatchVersions []uint64 `json:"skipped_patch_versions,omitempty"`
}

// AppStateResyncResult answers the resync; OK is set when every collection resynced.
type AppStateResyncResult struct {
	OK          bool                       `json:"ok"`
	Collections []AppStateCollectionResult `json:"collections"`
}

// ResyncAppState drops the local copy of app state collections in the instance's worker
// and fetches them again from WhatsApp (the repair of "mismatching LTHash" errors). The
// chat state it brings back is recorded without chat.updated events (full sync).
func (a *Account) ResyncAppState(ctx context.Context, instanceID string, in AppStateResyncInput) (AppStateResyncResult, error) {
	if _, err := a.st().GetInstance(ctx, instanceID); err != nil {
		return AppStateResyncResult{}, mapErr(err)
	}
	if len(in.Collections) > 5 {
		return AppStateResyncResult{}, apierr.Validation("collections lists at most the five app state collections")
	}
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return AppStateResyncResult{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, appStateResyncTimeout)
	defer cancel()
	resp, err := cli.ResyncAppState(rctx, &workerv1.ResyncAppStateRequest{Collections: in.Collections})
	if err != nil {
		return AppStateResyncResult{}, accountErr(err)
	}
	out := AppStateResyncResult{OK: true, Collections: make([]AppStateCollectionResult, 0, len(resp.Collections))}
	for _, c := range resp.Collections {
		out.OK = out.OK && c.Ok
		out.Collections = append(out.Collections, AppStateCollectionResult{Name: c.Name, OK: c.Ok,
			VersionBefore: c.VersionBefore, VersionAfter: c.VersionAfter, Error: c.Error,
			Lenient: c.Lenient, SkippedPatchVersions: c.SkippedPatchVersions})
	}
	return out, nil
}
