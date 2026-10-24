package worker

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
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
	mem  *simStore
	dev  *store.Device
	proc *appstate.Processor
}

func newSimDevice(name string) *simDevice {
	mem := newSimStore([]byte("sync-key-1"), bytes.Repeat([]byte{9}, 32))
	dev := &store.Device{AppState: mem, AppStateKeys: mem}
	return &simDevice{name: name, mem: mem, dev: dev, proc: appstate.NewProcessor(dev, waLog.Noop)}
}

func (d *simDevice) state(name appstate.WAPatchName) appstate.HashState {
	v, h, _ := d.mem.GetAppStateVersion(context.Background(), string(name))
	return appstate.HashState{Version: v, Hash: h}
}

// sync applies the server's patches after the device's version, as fetchAppState does.
func (d *simDevice) sync(srv *simServer, name appstate.WAPatchName) error {
	st := d.state(name)
	_, next, err := d.proc.DecodePatches(context.Background(), srv.since(name, st.Version), st, true)
	if err != nil {
		return fmt.Errorf("%s: %w", d.name, err)
	}
	if next.Version != uint64(len(srv.patches[name])) {
		return fmt.Errorf("%s: at v%d after sync, server at v%d", d.name, next.Version, len(srv.patches[name]))
	}
	return nil
}

// send encodes info with whatsmeow's own encoder (what SendAppState does) and pushes it.
func (d *simDevice) send(t *testing.T, srv *simServer, info appstate.PatchInfo) {
	t.Helper()
	st := d.state(info.Type)
	raw, err := d.proc.EncodePatch(context.Background(), d.mem.keyID, st, info)
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
