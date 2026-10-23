package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// memAppState is an in-memory app state store with one sync key.
type memAppState struct {
	keyID []byte
	key   store.AppStateSyncKey
	macs  map[string][]byte // name + index MAC -> value MAC
}

func (m *memAppState) PutAppStateSyncKey(context.Context, []byte, store.AppStateSyncKey) error {
	return nil
}
func (m *memAppState) GetAppStateSyncKey(_ context.Context, id []byte) (*store.AppStateSyncKey, error) {
	if !bytes.Equal(id, m.keyID) {
		return nil, nil
	}
	k := m.key
	return &k, nil
}
func (m *memAppState) GetLatestAppStateSyncKeyID(context.Context) ([]byte, error) {
	return m.keyID, nil
}
func (m *memAppState) GetAllAppStateSyncKeys(context.Context) ([]*store.AppStateSyncKey, error) {
	return []*store.AppStateSyncKey{&m.key}, nil
}
func (m *memAppState) PutAppStateVersion(context.Context, string, uint64, [128]byte) error {
	return nil
}
func (m *memAppState) GetAppStateVersion(context.Context, string) (uint64, [128]byte, error) {
	return 0, [128]byte{}, nil
}
func (m *memAppState) DeleteAppStateVersion(context.Context, string) error { return nil }
func (m *memAppState) PutAppStateMutationMACs(_ context.Context, name string, _ uint64, ms []store.AppStateMutationMAC) error {
	for _, x := range ms {
		m.macs[name+string(x.IndexMAC)] = x.ValueMAC
	}
	return nil
}
func (m *memAppState) DeleteAppStateMutationMACs(_ context.Context, name string, idx [][]byte) error {
	for _, i := range idx {
		delete(m.macs, name+string(i))
	}
	return nil
}
func (m *memAppState) GetAppStateMutationMAC(_ context.Context, name string, idx []byte) ([]byte, error) {
	return m.macs[name+string(idx)], nil
}

// CON-2: the REMOVE patch built by hand must pass whatsmeow's own decoder with MAC
// validation, right after the SET that whatsmeow's encoder produced for the same entry.
func TestRemovePatchDecodesWithWhatsmeow(t *testing.T) {
	ctx := context.Background()
	mem := &memAppState{keyID: []byte("key-1"), key: store.AppStateSyncKey{Data: bytes.Repeat([]byte{7}, 32)}, macs: map[string][]byte{}}
	dev := &store.Device{AppState: mem, AppStateKeys: mem}
	proc := appstate.NewProcessor(dev, waLog.Noop)
	name := appstate.WAPatchCriticalUnblockLow
	pn := types.NewJID("5511999990000", types.DefaultUserServer)
	save := &workerv1.ContactActionRequest{Action: "save", FirstName: "Ana", FullName: "Ana Teste", SaveOnPhone: true}
	m := contactMutation(pn, pn, types.EmptyJID, save)

	decode := func(raw []byte, state appstate.HashState) ([]appstate.Mutation, appstate.HashState) {
		t.Helper()
		var p waServerSync.SyncdPatch
		if err := proto.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		p.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(state.Version + 1)}
		muts, next, err := proc.DecodePatches(ctx, &appstate.PatchList{Name: name, Patches: []*waServerSync.SyncdPatch{&p}}, state, true)
		if err != nil {
			t.Fatalf("whatsmeow rejected the patch: %v", err)
		}
		return muts, next
	}

	setRaw, err := proc.EncodePatch(ctx, mem.keyID, appstate.HashState{}, appstate.PatchInfo{Type: name, Mutations: []appstate.MutationInfo{m}})
	if err != nil {
		t.Fatal(err)
	}
	muts, state := decode(setRaw, appstate.HashState{})
	if a := muts[0].Action.GetContactAction(); muts[0].Index[0] != "contact" || muts[0].Index[1] != pn.String() ||
		a.GetFullName() != "Ana Teste" || a.GetFirstName() != "Ana" || !a.GetSaveOnPrimaryAddressbook() || a.GetPnJID() != pn.String() {
		t.Fatalf("set = %+v", muts[0])
	}
	// Our own SET encoding passes too, with the state we computed (IVs differ, so hashes do)...
	ourSet, ourState, err := encodeAppStateMutation(ctx, dev, name, waServerSync.SyncdMutation_SET, m, appstate.HashState{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, s := decode(ourSet, appstate.HashState{}); s.Hash != ourState.Hash || s.Version != 1 {
		t.Fatal("whatsmeow computes another state for our SET")
	}
	_ = mem.PutAppStateMutationMACs(ctx, string(name), state.Version, []store.AppStateMutationMAC{{IndexMAC: muts[0].IndexMAC, ValueMAC: muts[0].ValueMAC}})

	// ...and the REMOVE decodes, removes the same index and brings the hash back to empty.
	rm := contactMutation(pn, pn, types.EmptyJID, &workerv1.ContactActionRequest{Action: "remove"})
	if ok, err := appStateHas(ctx, dev, name, rm.Index); err != nil || !ok {
		t.Fatalf("saved entry not found locally = %v %v", ok, err)
	}
	rmRaw, rmState, err := encodeAppStateMutation(ctx, dev, name, waServerSync.SyncdMutation_REMOVE, rm, state, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	muts, after := decode(rmRaw, state)
	if len(muts) != 1 || muts[0].Operation != waServerSync.SyncdMutation_REMOVE || muts[0].Index[1] != pn.String() {
		t.Fatalf("remove = %+v", muts)
	}
	if after.Hash != rmState.Hash || after.Hash != ([128]byte{}) || after.Version != 2 {
		t.Fatalf("state after remove = v%d (hash empty: %v)", after.Version, after.Hash == [128]byte{})
	}
	if ok, err := appStateHas(ctx, dev, name, rm.Index); err != nil || ok {
		t.Fatalf("entry still known after the removal was applied = %v %v", ok, err)
	}

	// An entry this device does not have is never removed (it would break the LT-hash).
	other := contactMutation(types.NewJID("5511999990001", types.DefaultUserServer), pn, types.EmptyJID, &workerv1.ContactActionRequest{Action: "remove"})
	if ok, _ := appStateHas(ctx, dev, name, other.Index); ok {
		t.Fatal("unknown entry reported as present")
	}
	if _, _, err := encodeAppStateMutation(ctx, dev, name, waServerSync.SyncdMutation_REMOVE, other, state, time.Now()); !errors.Is(err, errAppStateEntryMissing) {
		t.Fatalf("remove of an unknown entry = %v", err)
	}
}

func TestContactAction(t *testing.T) {
	w := newMsgAdapter(t)
	var sent []appstate.PatchInfo
	var removed []appstate.MutationInfo
	w.acct.sendAppState = func(_ context.Context, p appstate.PatchInfo) error { sent = append(sent, p); return nil }
	w.acct.removeAppState = func(_ context.Context, n appstate.WAPatchName, m appstate.MutationInfo) error {
		if n != appstate.WAPatchCriticalUnblockLow {
			t.Errorf("remove collection %s", n)
		}
		removed = append(removed, m)
		return nil
	}
	ctx := context.Background()
	pn := "5511999990000@s.whatsapp.net"
	lid := types.NewJID("123456", types.HiddenUserServer)
	w.lidFor = func(context.Context, types.JID) (types.JID, error) { return lid, nil }
	entries := map[string]bool{}
	w.acct.appStateHas = func(_ context.Context, _ appstate.WAPatchName, idx []string) (bool, error) {
		return entries[idx[1]], nil
	}
	// Nothing in the local address book: no REMOVE is sent.
	if c, code := codeOf2(w.ContactAction(ctx, &workerv1.ContactActionRequest{Jid: pn, Action: "remove"})); c != codes.NotFound || code != "not_found" || len(removed) != 0 {
		t.Fatalf("remove of unknown contact = %v %s (%d sent)", c, code, len(removed))
	}
	out, err := w.ContactAction(ctx, &workerv1.ContactActionRequest{Jid: pn, Action: "save", FirstName: " Ana ", FullName: "Ana Teste"})
	if err != nil || out.Jid != pn {
		t.Fatalf("save = %v %v", out, err)
	}
	if p := sent[0]; p.Type != appstate.WAPatchCriticalUnblockLow || p.Mutations[0].Version != 2 ||
		p.Mutations[0].Value.GetContactAction().GetFirstName() != "Ana" || p.Mutations[0].Index[1] != pn {
		t.Fatalf("patch = %+v", p)
	}
	if p := sent[0].Mutations[0].Value.GetContactAction(); p.GetLidJID() != lid.String() || p.GetPnJID() != pn {
		t.Fatalf("contact action = %+v", p)
	}
	entries[pn] = true
	if _, err := w.ContactAction(ctx, &workerv1.ContactActionRequest{Jid: pn, Action: "remove"}); err != nil || len(removed) != 1 ||
		removed[0].Index[0] != appstate.IndexContact || removed[0].Index[1] != pn {
		t.Fatalf("remove = %v %+v", err, removed)
	}
	// An entry kept under the LID is updated and removed under the LID.
	delete(entries, pn)
	entries[lid.String()] = true
	if _, err := w.ContactAction(ctx, &workerv1.ContactActionRequest{Jid: pn, Action: "save", FullName: "Ana"}); err != nil ||
		sent[len(sent)-1].Mutations[0].Index[1] != lid.String() {
		t.Fatalf("save under LID = %v %v", err, sent[len(sent)-1].Mutations[0].Index)
	}
	if _, err := w.ContactAction(ctx, &workerv1.ContactActionRequest{Jid: pn, Action: "remove"}); err != nil || removed[1].Index[1] != lid.String() {
		t.Fatalf("remove under LID = %v %+v", err, removed)
	}
	// A removal the encoder refuses (entry vanished meanwhile) is not_found too.
	w.acct.removeAppState = func(context.Context, appstate.WAPatchName, appstate.MutationInfo) error {
		return errAppStateEntryMissing
	}
	if c, _ := codeOf2(w.ContactAction(ctx, &workerv1.ContactActionRequest{Jid: pn, Action: "remove"})); c != codes.NotFound {
		t.Fatalf("vanished entry = %v", c)
	}
	for _, bad := range []*workerv1.ContactActionRequest{
		{Jid: pn, Action: "save"},
		{Jid: "120363000000000001@g.us", Action: "save", FullName: "x"},
		{Jid: "123@lid", Action: "remove"},
		{Jid: pn, Action: "rename"},
	} {
		if c, _ := codeOf2(w.ContactAction(ctx, bad)); c != codes.InvalidArgument {
			t.Errorf("%+v: %v", bad, c)
		}
	}
}

func codeOf2(_ *workerv1.ContactActionResponse, err error) (codes.Code, string) { return codeOf(err) }
