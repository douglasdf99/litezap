package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"log/slog"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/cbcutil"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Lenient historical decode (2026-10-24). On a real account the server holds patches
// whose snapshot MAC does not match the LT-hash obtained by applying their mutations:
// they were sent from a diverged local copy (most likely by an older LiteZap, the 409
// race). Their mutations are valid (content and index MACs verify, and the patch MAC
// covers the wrong snapshot MAC the sender wrote), only the sender's LT-hash was wrong.
// Official clients sync these collections; whatsmeow refuses them forever, even on a
// brand-new linked device, so LiteZap never got those collections.
//
// During a full resync (automatic or manual) only, when the strict fetch fails with
// "mismatching LTHash", the collection is fetched again from zero and decoded patch by
// patch: a patch (or snapshot) whose LT-hash check fails is accepted when every one of
// its mutations passes the content and index MAC checks and the patch MAC verifies; its
// state is the hash computed from its mutations (the true LT-hash, which the following
// patches of the other devices are built on). Everything else is decoded strictly, and
// the incremental syncs after it are whatsmeow's own, strict. Nothing is sent: this
// only reads patches from the server and updates the local copy.
//
// whatsmeow's app state lock is not reachable from here, so a server_sync notification
// handled while this runs could apply the same patches twice; the strict incremental
// fetch that follows a lenient resync detects that and the resync reports the error
// (and the usual automatic resync repairs it).

// maxLenientPatches bounds how many unverifiable patches one resync accepts: a few
// historical ones are the diverged-sender case; many mean something else is wrong.
const maxLenientPatches = 16

// lenientResult is the outcome of a lenient full sync of one collection.
type lenientResult struct {
	state   appstate.HashState
	skipped []uint64 // versions whose LT-hash check was skipped
}

// lenientSync is the input of lenientFullSync, so the simulator can drive it.
type lenientSync struct {
	name appstate.WAPatchName
	dev  *store.Device // app state store and sync keys
	proc *appstate.Processor
	log  *slog.Logger
	// fetch returns the patches after fromVersion (with a snapshot when asked).
	fetch func(ctx context.Context, fromVersion uint64, snapshot bool) (*appstate.PatchList, error)
	// apply hands the decoded mutations to the app (store updates and events).
	apply func(ctx context.Context, mutations []appstate.Mutation) error
}

// resetAppStateLocal drops the local copy of a collection: version, hash and (ON DELETE
// CASCADE, foreign_keys on) the mutation MACs. It checks the version is back to 0.
func resetAppStateLocal(ctx context.Context, s store.AppStateStore, name appstate.WAPatchName) error {
	if err := s.DeleteAppStateVersion(ctx, string(name)); err != nil {
		return fmt.Errorf("failed to reset app state %s: %w", name, err)
	}
	v, h, err := s.GetAppStateVersion(ctx, string(name))
	if err != nil {
		return fmt.Errorf("failed to read app state %s after reset: %w", name, err)
	}
	if v != 0 || h != ([128]byte{}) {
		return fmt.Errorf("app state %s still at v%d after reset", name, v)
	}
	return nil
}

// lenientFullSync resets the collection and fetches it from zero like whatsmeow's full
// sync, accepting patches whose only failure is the LT-hash check (see above).
func lenientFullSync(ctx context.Context, ls lenientSync) (lenientResult, error) {
	var res lenientResult
	if err := resetAppStateLocal(ctx, ls.dev.AppState, ls.name); err != nil {
		return res, err
	}
	if ls.proc == nil {
		ls.proc = appstate.NewProcessor(ls.dev, waLog.Noop)
	}
	wantSnapshot := true
	for hasMore := true; hasMore; {
		list, err := ls.fetch(ctx, res.state.Version, wantSnapshot)
		if err != nil {
			return res, fmt.Errorf("failed to fetch app state %s patches: %w", ls.name, err)
		} else if !wantSnapshot && list.Snapshot != nil {
			return res, fmt.Errorf("server unexpectedly returned snapshot for %s without asking", ls.name)
		}
		list.Name = ls.name
		wantSnapshot = false
		hasMore = list.HasMorePatches
		from := res.state.Version
		muts, err := decodeLenient(ctx, ls, list, &res)
		if err != nil {
			return res, fmt.Errorf("failed to decode app state %s patches: %w", ls.name, err)
		}
		if err := ls.apply(ctx, muts); err != nil {
			return res, err
		}
		if hasMore && res.state.Version == from {
			return res, fmt.Errorf("app state %s: server reported more patches but none came after v%d", ls.name, from)
		}
	}
	return res, nil
}

// decodeLenient decodes a patch list one step at a time (snapshot, then each patch), so a
// failure is attributed to one version and only that step is retried without the LT-hash.
func decodeLenient(ctx context.Context, ls lenientSync, list *appstate.PatchList, res *lenientResult) ([]appstate.Mutation, error) {
	var out []appstate.Mutation
	step := func(one *appstate.PatchList, version uint64, verify func() error) error {
		muts, next, err := ls.proc.DecodePatches(ctx, one, res.state, true)
		if errors.Is(err, appstate.ErrMismatchingLTHash) {
			if len(res.skipped) >= maxLenientPatches {
				return fmt.Errorf("%w (and %d unverifiable patches were already accepted, the limit)", err, len(res.skipped))
			}
			if verr := verify(); verr != nil {
				return fmt.Errorf("%w; not accepted leniently: %w", err, verr)
			}
			muts, next, err = ls.proc.DecodePatches(ctx, one, res.state, false)
			if err == nil {
				res.skipped = append(res.skipped, version)
				ls.log.Warn("app state: accepted unverifiable historical patch",
					"collection", string(ls.name), "version", version, "snapshot", one.Snapshot != nil)
			}
		}
		if err != nil {
			return err
		}
		res.state = next
		out = append(out, muts...)
		return nil
	}
	if ss := list.Snapshot; ss != nil {
		one := &appstate.PatchList{Name: ls.name, Snapshot: ss}
		err := step(one, ss.GetVersion().GetVersion(), func() error {
			for i, rec := range ss.GetRecords() {
				m := &waServerSync.SyncdMutation{Operation: waServerSync.SyncdMutation_SET.Enum(), Record: rec}
				if err := verifyMutationMACs(ctx, ls.dev, m); err != nil {
					return fmt.Errorf("snapshot record #%d: %w", i+1, err)
				}
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	for _, p := range list.Patches {
		one := &appstate.PatchList{Name: ls.name, Patches: []*waServerSync.SyncdPatch{p}}
		err := step(one, p.GetVersion().GetVersion(), func() error {
			for i, m := range p.GetMutations() {
				if err := verifyMutationMACs(ctx, ls.dev, m); err != nil {
					return fmt.Errorf("mutation #%d: %w", i+1, err)
				}
			}
			return verifyPatchMAC(ctx, ls.dev, ls.name, p)
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func appStateKeysFor(ctx context.Context, dev *store.Device, keyID []byte) (appStateKeys, error) {
	k, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, keyID)
	if err != nil {
		return appStateKeys{}, fmt.Errorf("failed to get app state key %X: %w", keyID, err)
	} else if k == nil {
		return appStateKeys{}, fmt.Errorf("app state key %X: %w", keyID, appstate.ErrKeyNotFound)
	}
	return expandAppStateKeys(k.Data), nil
}

// verifyMutationMACs checks a mutation's content MAC and index MAC, as whatsmeow's
// decodeMutation does with validateMACs (appstate/decode.go of the pinned version).
func verifyMutationMACs(ctx context.Context, dev *store.Device, m *waServerSync.SyncdMutation) error {
	keyID := m.GetRecord().GetKeyID().GetID()
	keys, err := appStateKeysFor(ctx, dev, keyID)
	if err != nil {
		return err
	}
	blob := m.GetRecord().GetValue().GetBlob()
	if len(blob) < 16+32 {
		return fmt.Errorf("value too short (%d bytes): %w", len(blob), appstate.ErrMismatchingContentMAC)
	}
	content, valueMAC := blob[:len(blob)-32], blob[len(blob)-32:]
	want := hmacOf(sha512.New, keys.valueMAC, []byte{byte(m.GetOperation()) + 1}, keyID, content, uint64Bytes(uint64(len(keyID)+1)))[:32]
	if !hmac.Equal(want, valueMAC) {
		return appstate.ErrMismatchingContentMAC
	}
	// cbcutil.Decrypt works in place: decrypt a copy, the record is decoded again after.
	content = bytes.Clone(content)
	plain, err := cbcutil.Decrypt(keys.valueEncryption, content[:16], content[16:])
	if err != nil {
		return fmt.Errorf("failed to decrypt: %w", err)
	}
	var data waSyncAction.SyncActionData
	if err := proto.Unmarshal(plain, &data); err != nil {
		return fmt.Errorf("failed to unmarshal: %w", err)
	}
	if !hmac.Equal(hmacOf(sha256.New, keys.index, data.GetIndex()), m.GetRecord().GetIndex().GetBlob()) {
		return appstate.ErrMismatchingIndexMAC
	}
	return nil
}

// verifyPatchMAC checks the patch MAC (over the snapshot MAC the sender wrote, the value
// MACs, the version and the collection name), as whatsmeow's validatePatch does.
func verifyPatchMAC(ctx context.Context, dev *store.Device, name appstate.WAPatchName, p *waServerSync.SyncdPatch) error {
	keys, err := appStateKeysFor(ctx, dev, p.GetKeyID().GetID())
	if err != nil {
		return err
	}
	parts := [][]byte{p.GetSnapshotMAC()}
	for _, m := range p.GetMutations() {
		v := m.GetRecord().GetValue().GetBlob()
		if len(v) < 32 {
			return appstate.ErrMismatchingPatchMAC
		}
		parts = append(parts, v[len(v)-32:])
	}
	parts = append(parts, uint64Bytes(p.GetVersion().GetVersion()), []byte(name))
	if !hmac.Equal(hmacOf(sha256.New, keys.patchMAC, parts...), p.GetPatchMAC()) {
		return fmt.Errorf("patch v%d: %w", p.GetVersion().GetVersion(), appstate.ErrMismatchingPatchMAC)
	}
	return nil
}

// lenientResync is the production lenient full sync: patches fetched with whatsmeow's
// query, mutations applied through whatsmeow's own handler (store updates and events,
// as a full sync with EmitAppStateEventsOnFullSync), then AppStateSyncComplete.
func (w *Whatsmeow) lenientResync(ctx context.Context, name appstate.WAPatchName) (lenientResult, error) {
	di := w.cli.DangerousInternals()
	emit := w.cli.EmitAppStateEventsOnFullSync
	var evts []any
	res, err := lenientFullSync(ctx, lenientSync{
		name: name,
		dev:  w.cli.Store,
		log:  w.log,
		fetch: func(ctx context.Context, from uint64, snapshot bool) (*appstate.PatchList, error) {
			return di.FetchAppStatePatches(ctx, name, from, snapshot)
		},
		apply: func(ctx context.Context, muts []appstate.Mutation) error {
			for _, m := range muts {
				if emit && m.Operation == waServerSync.SyncdMutation_SET {
					evts = append(evts, &events.AppState{Index: m.Index, SyncActionValue: m.Action})
				}
				if evt := di.DispatchAppState(ctx, name, m, true); emit && evt != nil {
					evts = append(evts, evt)
				}
			}
			return nil
		},
	})
	if err != nil {
		return res, err
	}
	evts = append(evts, &events.AppStateSyncComplete{Name: name, Version: res.state.Version})
	for _, evt := range evts {
		di.DispatchEvent(evt)
	}
	return res, nil
}
