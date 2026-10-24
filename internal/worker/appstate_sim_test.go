package worker

import (
	"bytes"
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
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// A simulation of the app state sync between the phone and this companion, to check that
// the patches LiteZap builds by hand leave every device with the same LT-hash. The
// "server" only orders patches (it never checks MACs, like WhatsApp's); each device
// applies every patch, its own included, with whatsmeow's own decoder (MACs on), and
// encodes with whatsmeow's own encoder (or ours, for the REMOVE). A patch that another
// device verifies against a different hash fails with "mismatching LTHash", which is the
// error the real device logged on 2026-10-23.

// simStore is a per-device app state store: versions, hashes and value MACs by index MAC
// (latest version wins, as whatsmeow's SQL store), one shared sync key.
type simStore struct {
	mu       sync.Mutex
	keyID    []byte
	key      store.AppStateSyncKey
	versions map[string]simVersion
	macs     map[string]map[string][]simMAC // name -> index MAC -> versions
	// beforePutVersion, when set, runs before each PutAppStateVersion (race tests).
	beforePutVersion func(name string, version uint64)
}

type simVersion struct {
	version uint64
	hash    [128]byte
}

type simMAC struct {
	version uint64
	value   []byte
}

func newSimStore(keyID []byte, key []byte) *simStore {
	return &simStore{keyID: keyID, key: store.AppStateSyncKey{Data: key}, versions: map[string]simVersion{}, macs: map[string]map[string][]simMAC{}}
}

func (m *simStore) PutAppStateSyncKey(context.Context, []byte, store.AppStateSyncKey) error {
	return nil
}
func (m *simStore) GetAppStateSyncKey(_ context.Context, id []byte) (*store.AppStateSyncKey, error) {
	if !bytes.Equal(id, m.keyID) {
		return nil, nil
	}
	k := m.key
	return &k, nil
}
func (m *simStore) GetLatestAppStateSyncKeyID(context.Context) ([]byte, error) { return m.keyID, nil }
func (m *simStore) GetAllAppStateSyncKeys(context.Context) ([]*store.AppStateSyncKey, error) {
	return []*store.AppStateSyncKey{&m.key}, nil
}
func (m *simStore) PutAppStateVersion(_ context.Context, name string, version uint64, hash [128]byte) error {
	if f := m.beforePutVersion; f != nil {
		f(name, version)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versions[name] = simVersion{version, hash}
	return nil
}
func (m *simStore) GetAppStateVersion(_ context.Context, name string) (uint64, [128]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.versions[name]
	return v.version, v.hash, nil
}
func (m *simStore) DeleteAppStateVersion(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.versions, name)
	delete(m.macs, name) // ON DELETE CASCADE in whatsmeow's schema
	return nil
}
func (m *simStore) PutAppStateMutationMACs(_ context.Context, name string, version uint64, ms []store.AppStateMutationMAC) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.macs[name] == nil {
		m.macs[name] = map[string][]simMAC{}
	}
	for _, x := range ms {
		m.macs[name][string(x.IndexMAC)] = append(m.macs[name][string(x.IndexMAC)], simMAC{version, x.ValueMAC})
	}
	return nil
}
func (m *simStore) DeleteAppStateMutationMACs(_ context.Context, name string, idx [][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, i := range idx {
		delete(m.macs[name], string(i))
	}
	return nil
}
func (m *simStore) GetAppStateMutationMAC(_ context.Context, name string, idx []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *simMAC
	for i, x := range m.macs[name][string(idx)] {
		if best == nil || x.version >= best.version {
			best = &m.macs[name][string(idx)][i]
		}
	}
	if best == nil {
		return nil, nil
	}
	return best.value, nil
}

// simServer orders the patches of each collection, like WhatsApp's server: a patch
// built on an older version is refused (409) and nothing is verified.
type simServer struct {
	patches map[appstate.WAPatchName][]*waServerSync.SyncdPatch
	// snapshotAt, when set for a collection, makes a full sync start from a snapshot of
	// that version (its records, with the snapshot MAC the patch of that version wrote).
	snapshotAt map[appstate.WAPatchName]uint64
	pageSize   int // patches per response (has_more_patches); 0: all
}

// fetch answers a sync query like WhatsApp's server: the patches after from, or a
// snapshot (when configured) and the patches after it.
func (s *simServer) fetch(name appstate.WAPatchName, from uint64, snapshot bool) *appstate.PatchList {
	list := &appstate.PatchList{Name: name}
	if k := s.snapshotAt[name]; snapshot && k > 0 {
		list.Snapshot, from = s.snapshot(name, k), k
	}
	rest := s.patches[name][from:]
	if s.pageSize > 0 && len(rest) > s.pageSize {
		rest, list.HasMorePatches = rest[:s.pageSize], true
	}
	list.Patches = append([]*waServerSync.SyncdPatch(nil), rest...)
	return list
}

// snapshot is the collection at version k: the live records after patches 1..k, with
// the snapshot MAC and key of patch k (what its sender computed).
func (s *simServer) snapshot(name appstate.WAPatchName, k uint64) *waServerSync.SyncdSnapshot {
	recs := map[string]*waServerSync.SyncdRecord{}
	var order []string
	for _, p := range s.patches[name][:k] {
		for _, m := range p.GetMutations() {
			key := string(m.GetRecord().GetIndex().GetBlob())
			if m.GetOperation() == waServerSync.SyncdMutation_REMOVE {
				delete(recs, key)
				continue
			}
			if _, ok := recs[key]; !ok {
				order = append(order, key)
			}
			recs[key] = m.GetRecord()
		}
	}
	last := s.patches[name][k-1]
	ss := &waServerSync.SyncdSnapshot{Version: &waServerSync.SyncdVersion{Version: proto.Uint64(k)}, Mac: last.GetSnapshotMAC(), KeyID: last.GetKeyID()}
	seen := map[string]bool{}
	for _, key := range order {
		if r, ok := recs[key]; ok && !seen[key] {
			seen[key] = true
			ss.Records = append(ss.Records, r)
		}
	}
	return ss
}

func (s *simServer) push(t *testing.T, name appstate.WAPatchName, base uint64, raw []byte) error {
	t.Helper()
	if base != uint64(len(s.patches[name])) {
		return fmt.Errorf("409 conflict: patch on v%d, server at v%d", base, len(s.patches[name]))
	}
	var p waServerSync.SyncdPatch
	if err := proto.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(base + 1)}
	s.patches[name] = append(s.patches[name], &p)
	return nil
}

func (s *simServer) since(name appstate.WAPatchName, v uint64) *appstate.PatchList {
	return &appstate.PatchList{Name: name, Patches: append([]*waServerSync.SyncdPatch(nil), s.patches[name][v:]...)}
}

// simDevice is one device of the account (the phone or LiteZap).
type simDevice struct {
	name string
	mem  *simStore // nil on a device backed by whatsmeow's SQL store
	dev  *store.Device
	proc *appstate.Processor
	// tolerant devices skip the LT-hash and patch MAC checks (the official clients that
	// sync a history whatsmeow refuses).
	tolerant bool
	view     map[string]string // the app state entries: index -> value (chat/contact state)
}

var (
	simKeyID   = []byte("sync-key-1")
	simKeyData = bytes.Repeat([]byte{9}, 32)
)

func newSimDevice(name string) *simDevice {
	mem := newSimStore(simKeyID, simKeyData)
	dev := &store.Device{AppState: mem, AppStateKeys: mem}
	return &simDevice{name: name, mem: mem, dev: dev, proc: appstate.NewProcessor(dev, waLog.Noop), view: map[string]string{}}
}

// newSQLSimDevice is a device on whatsmeow's real SQL store (foreign keys on, as run.go
// opens it), with the account's sync key.
func newSQLSimDevice(t *testing.T, name string) *simDevice {
	t.Helper()
	dev := sqlDevice(t, selfPN, selfLID)
	if err := dev.AppStateKeys.PutAppStateSyncKey(context.Background(), simKeyID, store.AppStateSyncKey{Data: simKeyData, Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	return &simDevice{name: name, dev: dev, proc: appstate.NewProcessor(dev, waLog.Noop), view: map[string]string{}}
}

func (d *simDevice) state(name appstate.WAPatchName) appstate.HashState {
	v, h, _ := d.dev.AppState.GetAppStateVersion(context.Background(), string(name))
	return appstate.HashState{Version: v, Hash: h}
}

// applyView records decoded mutations in the device's view of the entries.
func (d *simDevice) applyView(muts []appstate.Mutation) {
	for _, m := range muts {
		key := strings.Join(m.Index, "|")
		if m.Operation == waServerSync.SyncdMutation_REMOVE {
			delete(d.view, key)
			continue
		}
		b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m.Action)
		d.view[key] = string(b)
	}
}

// sync applies the server's patches after the device's version, as fetchAppState does.
func (d *simDevice) sync(srv *simServer, name appstate.WAPatchName) error {
	st := d.state(name)
	muts, next, err := d.proc.DecodePatches(context.Background(), srv.since(name, st.Version), st, !d.tolerant)
	if err != nil {
		return fmt.Errorf("%s: %w", d.name, err)
	}
	d.applyView(muts)
	if next.Version != uint64(len(srv.patches[name])) {
		return fmt.Errorf("%s: at v%d after sync, server at v%d", d.name, next.Version, len(srv.patches[name]))
	}
	return nil
}

// send encodes info with whatsmeow's own encoder (what SendAppState does) and pushes it.
func (d *simDevice) send(t *testing.T, srv *simServer, info appstate.PatchInfo) {
	t.Helper()
	st := d.state(info.Type)
	raw, err := d.proc.EncodePatch(context.Background(), simKeyID, st, info)
	if err != nil {
		t.Fatalf("%s: encode: %v", d.name, err)
	}
	if err := srv.push(t, info.Type, st.Version, raw); err != nil {
		t.Fatalf("%s: %v", d.name, err)
	}
}

// sendRemove encodes a REMOVE with LiteZap's encoder (appstate_remove.go) and pushes it.
func (d *simDevice) sendRemove(t *testing.T, srv *simServer, name appstate.WAPatchName, m appstate.MutationInfo) {
	t.Helper()
	st := d.state(name)
	raw, _, err := encodeAppStateMutation(context.Background(), d.dev, name, waServerSync.SyncdMutation_REMOVE, m, st, time.Now())
	if err != nil {
		t.Fatalf("%s: encode remove: %v", d.name, err)
	}
	if err := srv.push(t, name, st.Version, raw); err != nil {
		t.Fatalf("%s: %v", d.name, err)
	}
}

// syncAll brings every device to the server's version and checks they agree.
func syncAll(t *testing.T, srv *simServer, name appstate.WAPatchName, devs ...*simDevice) {
	t.Helper()
	for _, d := range devs {
		if err := d.sync(srv, name); err != nil {
			t.Fatalf("sync %s: %v", name, err)
		}
	}
	for _, d := range devs[1:] {
		if a, b := devs[0].state(name), d.state(name); a != b {
			t.Fatalf("%s and %s disagree on %s: v%d vs v%d (hashes equal: %v)", devs[0].name, d.name, name, a.Version, b.Version, a.Hash == b.Hash)
		}
	}
}

func simContact(user string, action string) appstate.MutationInfo {
	pn := types.NewJID(user, types.DefaultUserServer)
	req := &workerv1.ContactActionRequest{Action: action}
	if action == "save" {
		req.FirstName, req.FullName, req.SaveOnPhone = "Ana", "Ana Teste "+user, true
	}
	return contactMutation(pn, pn, types.EmptyJID, req)
}

// The contact save (whatsmeow's encoder), the contact REMOVE (ours) and a later patch of
// the phone verify on both devices with one LT-hash. Fails if our encoding diverges.
func TestAppStateSimContactSetRemoveThenPhonePatch(t *testing.T) {
	name := appstate.WAPatchCriticalUnblockLow
	srv := &simServer{patches: map[appstate.WAPatchName][]*waServerSync.SyncdPatch{}}
	phone, lz := newSimDevice("phone"), newSimDevice("litezap")

	// The account already has contacts, written by the phone.
	phone.send(t, srv, appstate.PatchInfo{Type: name, Mutations: []appstate.MutationInfo{
		simContact("5511900000001", "save"), simContact("5511900000002", "save"),
	}})
	syncAll(t, srv, name, phone, lz)

	// LiteZap saves a new contact, renames an existing one, then removes both.
	lz.send(t, srv, appstate.PatchInfo{Type: name, Mutations: []appstate.MutationInfo{simContact("5511900000003", "save")}})
	syncAll(t, srv, name, phone, lz)
	lz.send(t, srv, appstate.PatchInfo{Type: name, Mutations: []appstate.MutationInfo{simContact("5511900000001", "save")}})
	syncAll(t, srv, name, phone, lz)
	lz.sendRemove(t, srv, name, simContact("5511900000003", "remove"))
	syncAll(t, srv, name, phone, lz)
	lz.sendRemove(t, srv, name, simContact("5511900000001", "remove"))
	syncAll(t, srv, name, phone, lz)

	// The phone's next patches (a rename, a re-add of a removed entry, its own REMOVE)
	// verify on LiteZap: this is where the real device failed (v49).
	phone.send(t, srv, appstate.PatchInfo{Type: name, Mutations: []appstate.MutationInfo{
		simContact("5511900000002", "save"), simContact("5511900000003", "save"),
	}})
	syncAll(t, srv, name, phone, lz)
	phone.sendRemove(t, srv, name, simContact("5511900000002", "remove"))
	syncAll(t, srv, name, phone, lz)
	phone.send(t, srv, appstate.PatchInfo{Type: name, Mutations: []appstate.MutationInfo{simContact("5511900000004", "save")}})
	syncAll(t, srv, name, phone, lz)

	// A device that joins later (full snapshot = the same records) agrees too.
	late := newSimDevice("late")
	syncAll(t, srv, name, phone, late)
}

// Clear chat (buildClearChat through whatsmeow's encoder), twice on the same chat (the
// second replaces the first entry), then the phone's normal patches.
func TestAppStateSimClearChatThenPhonePatch(t *testing.T) {
	name := appstate.WAPatchRegularHigh
	srv := &simServer{patches: map[appstate.WAPatchName][]*waServerSync.SyncdPatch{}}
	phone, lz := newSimDevice("phone"), newSimDevice("litezap")
	chat := types.NewJID("5511900000001", types.DefaultUserServer)
	other := types.NewJID("5511900000002", types.DefaultUserServer)

	phone.send(t, srv, appstate.BuildMuteAbs(chat, true, nil))
	syncAll(t, srv, name, phone, lz)

	key := &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String("3EB0AAAA")}
	lz.send(t, srv, buildClearChat(chat, time.Now(), key, false, false))
	syncAll(t, srv, name, phone, lz)
	lz.send(t, srv, buildClearChat(chat, time.Now(), nil, false, false)) // same index: replaces
	syncAll(t, srv, name, phone, lz)
	lz.send(t, srv, buildClearChat(chat, time.Now(), nil, true, true)) // another index
	syncAll(t, srv, name, phone, lz)

	phone.send(t, srv, appstate.BuildMuteAbs(chat, false, nil))
	syncAll(t, srv, name, phone, lz)
	phone.send(t, srv, buildClearChat(chat, time.Now(), nil, false, false))
	syncAll(t, srv, name, phone, lz)
	phone.send(t, srv, appstate.BuildStar(other, other, "3EB0BBBB", true, true))
	syncAll(t, srv, name, phone, lz)
}

// whatsmeow's SendAppState, on a 409, applies the patches of the response outside its
// app state lock, while the server_sync notification of the same patches makes another
// goroutine fetch and apply them under the lock. This reproduces one interleaving of the
// two writers on whatsmeow's own decoder and store calls: the local version goes back
// while the value MACs stay ahead, and every later sync fails with "failed to verify
// patch vN: mismatching LTHash" (the error of the real device). sendAppStateSafe never
// takes that path: it sends without whatsmeow's retry and, on a 409, refreshes through
// FetchAppState (under the lock) before retrying.
func TestWhatsmeowConflictApplyRaceCorruptsState(t *testing.T) {
	ctx := context.Background()
	name := appstate.WAPatchRegularHigh
	srv := &simServer{patches: map[appstate.WAPatchName][]*waServerSync.SyncdPatch{}}
	phone, lz := newSimDevice("phone"), newSimDevice("litezap")
	chat := types.NewJID("5511900000001", types.DefaultUserServer)

	phone.send(t, srv, appstate.BuildMuteAbs(chat, true, nil))
	syncAll(t, srv, name, phone, lz)
	// The phone writes two patches touching the same entry; LiteZap has not synced them
	// yet, so its own send gets a 409 carrying both.
	phone.send(t, srv, appstate.BuildMuteAbs(chat, false, nil))
	if err := phone.sync(srv, name); err != nil {
		t.Fatal(err)
	}
	phone.send(t, srv, appstate.BuildMuteAbs(chat, true, nil))
	if err := phone.sync(srv, name); err != nil {
		t.Fatal(err)
	}
	stale := lz.state(name) // v1: what the 409 path decodes from
	conflict := srv.since(name, stale.Version)

	// The 409 path validates the first patch, then (before it stores it) the locked
	// fetch of the notification applies both patches; then the 409 path goes on.
	raced := false
	lz.mem.beforePutVersion = func(_ string, v uint64) {
		if raced || v != stale.Version+1 {
			return
		}
		raced = true
		lz.mem.beforePutVersion = nil
		if err := lz.sync(srv, name); err != nil {
			t.Fatalf("notification fetch: %v", err)
		}
		lz.mem.beforePutVersion = func(string, uint64) {}
	}
	_, _, err := lz.proc.DecodePatches(ctx, conflict, stale, true)
	if !raced {
		t.Fatal("the interleaving did not happen")
	}
	t.Logf("409 path: %v", err)

	// The phone's next patch no longer verifies on LiteZap: the corruption is persistent.
	phone.send(t, srv, appstate.BuildMuteAbs(chat, false, nil))
	if err := lz.sync(srv, name); err == nil {
		t.Fatal("expected the race to leave LiteZap with a diverged LT-hash")
	} else {
		t.Logf("next sync: %v", err)
	}
	// A full resync (version and MACs dropped, snapshot = all records) repairs it. The
	// simulated snapshot is the replay of every patch.
	_ = lz.mem.DeleteAppStateVersion(ctx, string(name))
	syncAll(t, srv, name, phone, lz)
}

// strictFullSync is whatsmeow's full sync (fetchAppState with fullSync): the local copy
// dropped, then the snapshot and patches decoded with every MAC checked.
func (d *simDevice) strictFullSync(srv *simServer, name appstate.WAPatchName) error {
	ctx := context.Background()
	if err := d.dev.AppState.DeleteAppStateVersion(ctx, string(name)); err != nil {
		return err
	}
	var state appstate.HashState
	want := true
	for more := true; more; {
		list := srv.fetch(name, state.Version, want)
		want, more = false, list.HasMorePatches
		muts, next, err := d.proc.DecodePatches(ctx, list, state, true)
		if err != nil {
			return fmt.Errorf("failed to decode app state %s patches: %w", name, err)
		}
		d.applyView(muts)
		state = next
	}
	return nil
}

// lenientResync runs LiteZap's lenient full sync (appstate_lenient.go) on the device,
// applying the mutations through whatsmeow's own handler (DispatchAppState on a client
// over the device's store), as the worker does.
func (d *simDevice) lenientResync(t *testing.T, srv *simServer, name appstate.WAPatchName, logs io.Writer) (lenientResult, error) {
	t.Helper()
	cli := whatsmeow.NewClient(d.dev, nil)
	d.view = map[string]string{} // the reset drops the local copy
	return lenientFullSync(context.Background(), lenientSync{
		name: name, dev: d.dev, proc: d.proc, log: slog.New(slog.NewTextHandler(logs, nil)),
		fetch: func(_ context.Context, from uint64, snapshot bool) (*appstate.PatchList, error) {
			return srv.fetch(name, from, snapshot), nil
		},
		apply: func(ctx context.Context, muts []appstate.Mutation) error {
			for _, m := range muts {
				cli.DangerousInternals().DispatchAppState(ctx, name, m, true)
			}
			d.applyView(muts)
			return nil
		},
	})
}

// sendDiverged pushes a patch from a device whose local LT-hash is wrong (the old
// LiteZap after the 409 race): valid mutations, content/index/patch MACs, but a
// snapshot MAC computed from the wrong hash.
func sendDiverged(t *testing.T, srv *simServer, name appstate.WAPatchName, info appstate.PatchInfo) {
	t.Helper()
	old := newSimDevice("old-litezap")
	old.tolerant = true
	if err := old.sync(srv, name); err != nil {
		t.Fatal(err)
	}
	old.mem.mu.Lock()
	v := old.mem.versions[string(name)]
	v.hash[0] ^= 0xff
	old.mem.versions[string(name)] = v
	old.mem.mu.Unlock()
	old.send(t, srv, info)
}

func sameView(t *testing.T, name appstate.WAPatchName, a, b *simDevice) {
	t.Helper()
	if as, bs := a.state(name), b.state(name); as != bs {
		t.Fatalf("%s: %s v%d and %s v%d disagree (hashes equal: %v)", name, a.name, as.Version, b.name, bs.Version, as.Hash == bs.Hash)
	}
	if len(a.view) != len(b.view) {
		t.Fatalf("%s: %s has %d entries, %s has %d", name, a.name, len(a.view), b.name, len(b.view))
	}
	for k, v := range a.view {
		if b.view[k] != v {
			t.Fatalf("%s: entry %q differs between %s and %s", name, k, a.name, b.name)
		}
	}
}

// The real-device finding of 2026-10-24: the server's history of two collections holds
// a patch whose snapshot MAC does not match the LT-hash of its (valid) mutations, sent
// from a diverged copy. A brand-new device fails whatsmeow's strict full sync exactly
// like the device (on a patch, and on the snapshot), the official client (tolerant)
// syncs it. LiteZap's lenient resync accepts that one version, ends with the same
// LT-hash and the same chat/contact state as the official client, and the next patch of
// the phone (its snapshot MAC built on the true LT-hash, including the bad patch's
// mutations) verifies strictly.
func TestAppStateSimLenientResyncAcceptsDivergedHistoricalPatch(t *testing.T) {
	chatA := types.NewJID("5511900000001", types.DefaultUserServer)
	chatB := types.NewJID("5511900000002", types.DefaultUserServer)
	cases := []struct {
		name       appstate.WAPatchName
		snapshotAt uint64
		wantStrict string
		history    func(srv *simServer, phone *simDevice) // v1..v4, v3 from the diverged sender
		next       appstate.PatchInfo                     // the phone's patch after the resync
		check      func(t *testing.T, dev *store.Device)
	}{{
		name: appstate.WAPatchCriticalUnblockLow, snapshotAt: 2,
		wantStrict: "failed to verify patch v3: mismatching LTHash",
		history: func(srv *simServer, phone *simDevice) {
			phone.send(t, srv, appstate.PatchInfo{Type: appstate.WAPatchCriticalUnblockLow, Mutations: []appstate.MutationInfo{
				simContact("5511900000001", "save"), simContact("5511900000002", "save")}})
			syncAll(t, srv, appstate.WAPatchCriticalUnblockLow, phone)
			phone.send(t, srv, appstate.PatchInfo{Type: appstate.WAPatchCriticalUnblockLow, Mutations: []appstate.MutationInfo{simContact("5511900000003", "save")}})
			syncAll(t, srv, appstate.WAPatchCriticalUnblockLow, phone)
			sendDiverged(t, srv, appstate.WAPatchCriticalUnblockLow, appstate.PatchInfo{Type: appstate.WAPatchCriticalUnblockLow, Mutations: []appstate.MutationInfo{
				simContact("5511900000004", "save"), simContact("5511900000001", "save")}})
			syncAll(t, srv, appstate.WAPatchCriticalUnblockLow, phone)
			phone.sendRemove(t, srv, appstate.WAPatchCriticalUnblockLow, simContact("5511900000002", "remove"))
			syncAll(t, srv, appstate.WAPatchCriticalUnblockLow, phone)
		},
		next: appstate.PatchInfo{Type: appstate.WAPatchCriticalUnblockLow, Mutations: []appstate.MutationInfo{
			simContact("5511900000004", "save"), simContact("5511900000005", "save")}},
		check: func(t *testing.T, dev *store.Device) {
			c, err := dev.Contacts.GetContact(context.Background(), types.NewJID("5511900000004", types.DefaultUserServer))
			if err != nil || c.FullName != "Ana Teste 5511900000004" {
				t.Fatalf("contact of the lenient patch not in the store: %+v %v", c, err)
			}
		},
	}, {
		name: appstate.WAPatchRegularHigh, snapshotAt: 3,
		wantStrict: "failed to verify snapshot: failed to verify patch v3: mismatching LTHash",
		history: func(srv *simServer, phone *simDevice) {
			phone.send(t, srv, appstate.BuildMuteAbs(chatA, true, nil))
			syncAll(t, srv, appstate.WAPatchRegularHigh, phone)
			phone.send(t, srv, appstate.BuildStar(chatA, chatA, "3EB0AAAA", false, true))
			syncAll(t, srv, appstate.WAPatchRegularHigh, phone)
			sendDiverged(t, srv, appstate.WAPatchRegularHigh, appstate.BuildMuteAbs(chatB, true, nil))
			syncAll(t, srv, appstate.WAPatchRegularHigh, phone)
			phone.send(t, srv, appstate.BuildMuteAbs(chatA, false, nil))
			syncAll(t, srv, appstate.WAPatchRegularHigh, phone)
		},
		next: appstate.BuildStar(chatA, chatA, "3EB0BBBB", false, true),
		check: func(t *testing.T, dev *store.Device) {
			s, err := dev.ChatSettings.GetChatSettings(context.Background(), chatB)
			if err != nil || s.MutedUntil.IsZero() {
				t.Fatalf("mute of the lenient patch not in the store: %+v %v", s, err)
			}
		},
	}}
	for _, tc := range cases {
		t.Run(string(tc.name), func(t *testing.T) {
			srv := &simServer{patches: map[appstate.WAPatchName][]*waServerSync.SyncdPatch{},
				snapshotAt: map[appstate.WAPatchName]uint64{tc.name: tc.snapshotAt}, pageSize: 1}
			phone := newSimDevice("whatsapp-web")
			phone.tolerant = true
			tc.history(srv, phone)

			// A brand-new LiteZap device: whatsmeow's strict full sync fails like the device.
			lz := newSQLSimDevice(t, "litezap")
			err := lz.strictFullSync(srv, tc.name)
			if !errors.Is(err, appstate.ErrMismatchingLTHash) || !strings.Contains(err.Error(), tc.wantStrict) {
				t.Fatalf("strict full sync = %v, want %q", err, tc.wantStrict)
			}
			// It also fails again from a clean state (a fresh pairing does not help).
			if err := lz.strictFullSync(srv, tc.name); !errors.Is(err, appstate.ErrMismatchingLTHash) {
				t.Fatalf("second strict full sync = %v", err)
			}

			var logs bytes.Buffer
			res, err := lz.lenientResync(t, srv, tc.name, &logs)
			if err != nil {
				t.Fatalf("lenient resync: %v", err)
			}
			if fmt.Sprint(res.skipped) != "[3]" || res.state.Version != 4 {
				t.Fatalf("lenient result: skipped %v, v%d", res.skipped, res.state.Version)
			}
			if l := logs.String(); !strings.Contains(l, "level=WARN") || !strings.Contains(l, "accepted unverifiable historical patch") ||
				!strings.Contains(l, "collection="+string(tc.name)) || !strings.Contains(l, "version=3") {
				t.Fatalf("log = %s", l)
			}
			sameView(t, tc.name, phone, lz)
			tc.check(t, lz.dev)

			// Back to strict: the phone's next patch verifies on LiteZap with every MAC on.
			phone.send(t, srv, tc.next)
			if err := phone.sync(srv, tc.name); err != nil {
				t.Fatal(err)
			}
			if err := lz.sync(srv, tc.name); err != nil {
				t.Fatalf("strict sync after the lenient resync: %v", err)
			}
			sameView(t, tc.name, phone, lz)
		})
	}
}

// The lenient decode relaxes only the LT-hash: a patch whose mutation content MAC or
// whose patch MAC is wrong is still refused, as is a history with too many bad patches.
func TestAppStateSimLenientRefusesBadMACs(t *testing.T) {
	name := appstate.WAPatchRegularHigh
	chat := types.NewJID("5511900000001", types.DefaultUserServer)
	build := func() *simServer {
		srv := &simServer{patches: map[appstate.WAPatchName][]*waServerSync.SyncdPatch{}}
		phone := newSimDevice("phone")
		phone.send(t, srv, appstate.BuildMuteAbs(chat, true, nil))
		sendDiverged(t, srv, name, appstate.BuildMuteAbs(chat, false, nil))
		return srv
	}
	tamper := map[string]func(p *waServerSync.SyncdPatch){
		"content MAC": func(p *waServerSync.SyncdPatch) { p.Mutations[0].Record.Value.Blob[20] ^= 1 },
		"index MAC":   func(p *waServerSync.SyncdPatch) { p.Mutations[0].Record.Index.Blob[0] ^= 1 },
		"patch MAC":   func(p *waServerSync.SyncdPatch) { p.PatchMAC[0] ^= 1 },
	}
	want := map[string]error{"content MAC": appstate.ErrMismatchingContentMAC, "index MAC": appstate.ErrMismatchingIndexMAC, "patch MAC": appstate.ErrMismatchingPatchMAC}
	for what, f := range tamper {
		srv := build()
		f(srv.patches[name][1])
		lz := newSimDevice("litezap")
		_, err := lz.lenientResync(t, srv, name, io.Discard)
		if !errors.Is(err, want[what]) || !strings.Contains(err.Error(), "not accepted leniently") {
			t.Fatalf("%s: lenient = %v", what, err)
		}
	}

	// More unverifiable patches than maxLenientPatches: refused.
	srv := &simServer{patches: map[appstate.WAPatchName][]*waServerSync.SyncdPatch{}}
	for i := 0; i <= maxLenientPatches; i++ {
		sendDiverged(t, srv, name, appstate.BuildMuteAbs(chat, i%2 == 0, nil))
	}
	if _, err := newSimDevice("litezap").lenientResync(t, srv, name, io.Discard); err == nil || !strings.Contains(err.Error(), "the limit") {
		t.Fatalf("too many = %v", err)
	}
}

// The reset before a full resync drops the version, the hash and the mutation MACs on
// whatsmeow's SQL store (ON DELETE CASCADE with foreign keys on, as run.go opens it).
func TestResetAppStateLocalOnSQLStore(t *testing.T) {
	ctx := context.Background()
	lz := newSQLSimDevice(t, "litezap")
	name := appstate.WAPatchCriticalUnblockLow
	s := lz.dev.AppState
	idx := bytes.Repeat([]byte{7}, 32)
	if err := s.PutAppStateVersion(ctx, string(name), 48, [128]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAppStateMutationMACs(ctx, string(name), 48, []store.AppStateMutationMAC{{IndexMAC: idx, ValueMAC: bytes.Repeat([]byte{8}, 32)}}); err != nil {
		t.Fatal(err)
	}
	if err := resetAppStateLocal(ctx, s, name); err != nil {
		t.Fatal(err)
	}
	if v, h, _ := s.GetAppStateVersion(ctx, string(name)); v != 0 || h != ([128]byte{}) {
		t.Fatalf("after reset: v%d", v)
	}
	if mac, err := s.GetAppStateMutationMAC(ctx, string(name), idx); err != nil || mac != nil {
		t.Fatalf("mutation MAC survived the reset: %x %v", mac, err)
	}
	// Resetting an empty collection is fine.
	if err := resetAppStateLocal(ctx, s, appstate.WAPatchRegular); err != nil {
		t.Fatal(err)
	}
}
