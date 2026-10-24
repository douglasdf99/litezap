package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/appstate/lthash"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

// whatsmeow's EncodePatch only writes SET mutations. Removing an entry of the app state
// (a contact from the address book, CON-2) takes a REMOVE mutation, which WhatsApp Web
// and Baileys send with the same encryption and MACs as a SET. This file is that encoder,
// following appstate/encode.go and appstate/hash.go of the pinned whatsmeow line by line,
// with the operation as a parameter. Tests decode its output with whatsmeow's own
// decoder (MAC validation on).

// appStateKeys are the keys expanded from an app state sync key.
type appStateKeys struct {
	index, valueEncryption, valueMAC, snapshotMAC, patchMAC []byte
}

func expandAppStateKeys(keyData []byte) appStateKeys {
	k := hkdfutil.SHA256(keyData, nil, []byte("WhatsApp Mutation Keys"), 160)
	return appStateKeys{k[0:32], k[32:64], k[64:96], k[96:128], k[128:160]}
}

func hmacOf(alg func() hash.Hash, key []byte, data ...[]byte) []byte {
	h := hmac.New(alg, key)
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

func uint64Bytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// encodeAppStateMutation encodes a one-mutation patch with the given operation on top of
// state, returning the patch bytes and the state after it. dev supplies the latest app
// state sync key and the value MAC of the entry being replaced or removed.
func encodeAppStateMutation(ctx context.Context, dev *store.Device, name appstate.WAPatchName,
	op waServerSync.SyncdMutation_SyncdOperation, m appstate.MutationInfo, state appstate.HashState, now time.Time,
) ([]byte, appstate.HashState, error) {
	keyID, err := dev.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	if err != nil {
		return nil, state, fmt.Errorf("failed to get latest app state key ID: %w", err)
	} else if keyID == nil {
		return nil, state, errors.New("no app state keys found, creating app state keys is not yet supported")
	}
	keyData, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, keyID)
	if err != nil || keyData == nil {
		return nil, state, fmt.Errorf("failed to get app state key %x: %w", keyID, errors.Join(err, appstate.ErrKeyNotFound))
	}
	keys := expandAppStateKeys(keyData.Data)

	value := proto.Clone(m.Value).(*waSyncAction.SyncActionValue)
	value.Timestamp = proto.Int64(now.UnixMilli())
	indexBytes, err := json.Marshal(m.Index)
	if err != nil {
		return nil, state, err
	}
	version := m.Version
	content, err := proto.Marshal(&waSyncAction.SyncActionData{Index: indexBytes, Value: value, Padding: []byte{}, Version: &version})
	if err != nil {
		return nil, state, err
	}
	encrypted, err := cbcutil.Encrypt(keys.valueEncryption, nil, content)
	if err != nil {
		return nil, state, err
	}
	valueMAC := hmacOf(sha512.New, keys.valueMAC, []byte{byte(op) + 1}, keyID, encrypted, uint64Bytes(uint64(len(keyID)+1)))[:32]
	indexMAC := hmacOf(sha256.New, keys.index, indexBytes)
	mutation := &waServerSync.SyncdMutation{
		Operation: op.Enum(),
		Record: &waServerSync.SyncdRecord{
			Index: &waServerSync.SyncdIndex{Blob: indexMAC},
			Value: &waServerSync.SyncdValue{Blob: append(encrypted, valueMAC...)},
			KeyID: &waServerSync.KeyId{ID: keyID},
		},
	}

	// The LT-hash drops the replaced or removed entry's value MAC and adds the new one.
	prev, err := dev.AppState.GetAppStateMutationMAC(ctx, string(name), indexMAC)
	if err != nil {
		return nil, state, fmt.Errorf("failed to get value MAC of previous SET operation: %w", err)
	}
	if prev == nil && op == waServerSync.SyncdMutation_REMOVE {
		// Removing an entry this device never saw would corrupt the LT-hash on every device.
		return nil, state, errAppStateEntryMissing
	}
	var removed, added [][]byte
	if prev != nil {
		removed = [][]byte{prev}
	}
	if op == waServerSync.SyncdMutation_SET {
		added = [][]byte{valueMAC}
	}
	next := state
	lthash.WAPatchIntegrity.SubtractThenAddInPlace(next.Hash[:], removed, added)
	next.Version++

	snapshotMAC := hmacOf(sha256.New, keys.snapshotMAC, next.Hash[:], uint64Bytes(next.Version), []byte(name))
	patch := &waServerSync.SyncdPatch{
		SnapshotMAC: snapshotMAC,
		KeyID:       &waServerSync.KeyId{ID: keyID},
		Mutations:   []*waServerSync.SyncdMutation{mutation},
	}
	patch.PatchMAC = hmacOf(sha256.New, keys.patchMAC, snapshotMAC, valueMAC, uint64Bytes(next.Version), []byte(name))
	out, err := proto.Marshal(patch)
	return out, next, err
}

// errAppStateEntryMissing is a REMOVE of an entry the local app state does not have.
var errAppStateEntryMissing = errors.New("app state entry not found locally")

// appStateHas reports whether the local app state has an entry at index (its value MAC
// is known), with the index MAC of the latest sync key, as encodeAppStateMutation uses.
func appStateHas(ctx context.Context, dev *store.Device, name appstate.WAPatchName, index []string) (bool, error) {
	keyID, err := dev.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	if err != nil {
		return false, err
	} else if keyID == nil {
		return false, errors.New("no app state keys found, creating app state keys is not yet supported")
	}
	keyData, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, keyID)
	if err != nil || keyData == nil {
		return false, fmt.Errorf("failed to get app state key %x: %w", keyID, errors.Join(err, appstate.ErrKeyNotFound))
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		return false, err
	}
	mac, err := dev.AppState.GetAppStateMutationMAC(ctx, string(name), hmacOf(sha256.New, expandAppStateKeys(keyData.Data).index, indexBytes))
	return mac != nil, err
}

// errAppStateConflict is WhatsApp refusing a patch built on an outdated state (409).
var errAppStateConflict = errors.New("app state conflict")

// removeAppState sends a REMOVE of one app state entry, like SendAppState sends a SET:
// it encodes on the local state, sends it, and then fetches the collection so the local
// state (version, hash, MACs) and the other events follow. On a conflict the local state
// is refreshed and the removal is retried once.
func (w *Whatsmeow) removeAppState(ctx context.Context, name appstate.WAPatchName, m appstate.MutationInfo) (err error) {
	w.asg.writeMu.Lock()
	defer w.asg.writeMu.Unlock()
	if err := w.ensureAppStateSyncedLocked(ctx, name); err != nil {
		return err
	}
	defer func() { w.noteAppStateErr(name, err) }()
	err = w.removeAppStateOnce(ctx, name, m)
	if errors.Is(err, errAppStateConflict) {
		if ferr := w.cli.FetchAppState(ctx, name, false, false); ferr != nil {
			return fmt.Errorf("%w (also, refreshing failed: %w)", err, ferr)
		}
		err = w.removeAppStateOnce(ctx, name, m)
	}
	if err != nil {
		return err
	}
	if err := w.cli.FetchAppState(ctx, name, false, false); err != nil {
		return fmt.Errorf("failed to fetch app state after sending update: %w", err)
	}
	return nil
}

func (w *Whatsmeow) removeAppStateOnce(ctx context.Context, name appstate.WAPatchName, m appstate.MutationInfo) error {
	dev := w.cli.Store
	version, h, err := dev.AppState.GetAppStateVersion(ctx, string(name))
	if err != nil {
		return err
	}
	encoded, _, err := encodeAppStateMutation(ctx, dev, name, waServerSync.SyncdMutation_REMOVE, m,
		appstate.HashState{Version: version, Hash: h}, time.Now())
	if err != nil {
		return err
	}
	resp, err := w.cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:sync:app:state", Type: "set", To: types.ServerJID,
		Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{
			Tag:     "collection",
			Attrs:   waBinary.Attrs{"name": string(name), "version": version, "return_snapshot": false},
			Content: []waBinary.Node{{Tag: "patch", Content: encoded}},
		}}}},
	})
	if err != nil {
		return err
	}
	coll, ok := resp.GetOptionalChildByTag("sync", "collection")
	if !ok {
		return errors.New("app state send response without a collection")
	}
	if coll.AttrGetter().OptionalString("type") == "error" {
		if e, ok := coll.GetOptionalChildByTag("error"); ok {
			if e.AttrGetter().Int("code") == 409 {
				return fmt.Errorf("%w: %s", errAppStateConflict, &e)
			}
			return fmt.Errorf("%w (%s): %s", whatsmeow.ErrAppStateUpdate, name, &e)
		}
		return fmt.Errorf("%w: %s", whatsmeow.ErrAppStateUpdate, &coll)
	}
	return nil
}
