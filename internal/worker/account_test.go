package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func codeOf(err error) (codes.Code, string) {
	st, _ := status.FromError(err)
	code, _, _ := strings.Cut(st.Message(), ":")
	return st.Code(), code
}

func TestChatActionPatches(t *testing.T) {
	w := newMsgAdapter(t)
	var sent []appstate.PatchInfo
	w.acct.sendAppState = func(_ context.Context, p appstate.PatchInfo) error { sent = append(sent, p); return nil }
	w.acct.chatSettings = func(context.Context, types.JID) (types.LocalChatSettings, error) {
		return types.LocalChatSettings{}, nil
	}
	chat := "5511888887777@s.whatsapp.net"
	last := &workerv1.MessageRef{Id: "WA9", TimestampMs: 1_800_000_000_000}
	cases := []struct {
		req   *workerv1.ChatActionRequest
		index string
	}{
		{&workerv1.ChatActionRequest{Chat: chat, Action: "archive", Message: last}, appstate.IndexArchive},
		{&workerv1.ChatActionRequest{Chat: chat, Action: "pin"}, appstate.IndexPin},
		{&workerv1.ChatActionRequest{Chat: chat, Action: "mute", MuteUntilMs: 1_900_000_000_000}, appstate.IndexMute},
		{&workerv1.ChatActionRequest{Chat: chat, Action: "unread", Message: last}, appstate.IndexMarkChatAsRead},
		{&workerv1.ChatActionRequest{Chat: chat, Action: "delete", Message: last}, appstate.IndexDeleteChat},
		{&workerv1.ChatActionRequest{Chat: chat, Action: "star", Message: &workerv1.MessageRef{Id: "WA1"}}, appstate.IndexStar},
	}
	for _, c := range cases {
		if err := w.ChatAction(context.Background(), c.req); err != nil {
			t.Fatalf("%s: %v", c.req.Action, err)
		}
		p := sent[len(sent)-1]
		m := p.Mutations[0]
		if m.Index[0] != c.index || m.Index[1] != chat {
			t.Fatalf("%s: index %v", c.req.Action, m.Index)
		}
	}
	mute := sent[2].Mutations[0].Value.GetMuteAction()
	if !mute.GetMuted() || mute.GetMuteEndTimestamp() != 1_900_000_000_000 {
		t.Fatalf("mute = %+v", mute)
	}
	if sent[3].Mutations[0].Value.GetMarkChatAsReadAction().GetRead() {
		t.Fatal("unread must send read=false")
	}
	if r := sent[0].Mutations[0].Value.GetArchiveChatAction().GetMessageRange(); r.GetLastMessageTimestamp() != 1_800_000_000 ||
		r.GetMessages()[0].GetKey().GetID() != "WA9" {
		t.Fatalf("archive range = %+v", r)
	}
	// forever mute
	_ = w.ChatAction(context.Background(), &workerv1.ChatActionRequest{Chat: chat, Action: "mute"})
	if got := sent[len(sent)-1].Mutations[0].Value.GetMuteAction().GetMuteEndTimestamp(); got != -1 {
		t.Fatalf("forever mute end = %d", got)
	}
	for _, bad := range []*workerv1.ChatActionRequest{
		{Chat: chat, Action: "explode"},
		{Chat: "status@broadcast", Action: "pin"},
		{Chat: "120363@g.us", Action: "star", Message: &workerv1.MessageRef{Id: "X"}}, // group star without sender
	} {
		if c, _ := codeOf(w.ChatAction(context.Background(), bad)); c != codes.InvalidArgument {
			t.Errorf("%+v: code %v", bad, c)
		}
	}
}

func TestChatActionUsesLIDForMigratedAccounts(t *testing.T) {
	w := newMsgAdapter(t)
	w.cli.Store.LID = types.NewJID("999", types.HiddenUserServer)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	lid := types.NewJID("123456", types.HiddenUserServer)
	w.lidFor = func(_ context.Context, j types.JID) (types.JID, error) {
		if j == pn {
			return lid, nil
		}
		return types.EmptyJID, nil
	}
	known := map[types.JID]bool{}
	w.acct.chatSettings = func(_ context.Context, j types.JID) (types.LocalChatSettings, error) {
		return types.LocalChatSettings{Found: known[j]}, nil
	}
	var got []string
	w.acct.sendAppState = func(_ context.Context, p appstate.PatchInfo) error {
		got = append(got, p.Mutations[0].Index[1])
		return nil
	}
	_ = w.ChatAction(context.Background(), &workerv1.ChatActionRequest{Chat: pn.String(), Action: "pin"})
	known[pn] = true // an old chat only known by phone number keeps that form
	_ = w.ChatAction(context.Background(), &workerv1.ChatActionRequest{Chat: pn.String(), Action: "pin"})
	if len(got) != 2 || got[0] != lid.String() || got[1] != pn.String() {
		t.Fatalf("targets = %v", got)
	}
}

func TestChatActionErrors(t *testing.T) {
	w := newMsgAdapter(t)
	w.acct.sendAppState = func(context.Context, appstate.PatchInfo) error {
		return errors.New("failed to get latest app state key ID: no app state keys found, creating app state keys is not yet supported")
	}
	err := w.ChatAction(context.Background(), &workerv1.ChatActionRequest{Chat: "5511888887777@s.whatsapp.net", Action: "pin"})
	if c, code := codeOf(err); c != codes.FailedPrecondition || code != "app_state_not_synced" {
		t.Fatalf("err = %v", err)
	}
	w.acct.sendAppState = func(context.Context, appstate.PatchInfo) error { return whatsmeow.ErrNotConnected }
	err = w.ChatAction(context.Background(), &workerv1.ChatActionRequest{Chat: "5511888887777@s.whatsapp.net", Action: "pin"})
	if c, _ := codeOf(err); c != codes.Unavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestAppStateEventsBecomeChatUpdates(t *testing.T) {
	w := newMsgAdapter(t)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	ts := time.Unix(1_800_000_000, 0)
	w.handle(&events.Archive{JID: pn, Timestamp: ts, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
	if u := lastEvent(t, w).GetChatUpdate(); u.GetField() != "archived" || !u.GetValue() || u.GetChat() != pn.String() || u.GetTimestampMs() != ts.UnixMilli() {
		t.Fatalf("archive = %+v", u)
	}
	w.handle(&events.Mute{JID: pn, Action: &waSyncAction.MuteAction{Muted: proto.Bool(true), MuteEndTimestamp: proto.Int64(-1)}})
	if u := lastEvent(t, w).GetChatUpdate(); u.GetField() != "muted" || u.GetMutedUntilMs() != -1 {
		t.Fatalf("mute = %+v", u)
	}
	w.handle(&events.MarkChatAsRead{JID: pn, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)}})
	if u := lastEvent(t, w).GetChatUpdate(); u.GetField() != "unread" || !u.GetValue() {
		t.Fatalf("unread = %+v", u)
	}
	w.handle(&events.Star{ChatJID: pn, MessageID: "WA1", IsFromMe: true, Action: &waSyncAction.StarAction{Starred: proto.Bool(true)}})
	if u := lastEvent(t, w).GetChatUpdate(); u.GetField() != "starred" || u.GetMessageId() != "WA1" || !u.GetFromMe() {
		t.Fatalf("star = %+v", u)
	}
	n := eventCount(w)
	w.handle(&events.Pin{JID: types.StatusBroadcastJID, Action: &waSyncAction.PinAction{Pinned: proto.Bool(true)}})
	if eventCount(w) != n {
		t.Fatal("broadcast chats are out of scope")
	}
}

func TestProfilePictureNotFound(t *testing.T) {
	w := newMsgAdapter(t)
	for _, e := range []error{whatsmeow.ErrProfilePictureNotSet, whatsmeow.ErrProfilePictureUnauthorized} {
		w.acct.pictureInfo = func(context.Context, types.JID, *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
			return nil, e
		}
		_, err := w.ProfilePicture(context.Background(), "5511888887777@s.whatsapp.net", false)
		if c, code := codeOf(err); c != codes.NotFound || code != "not_found" {
			t.Fatalf("%v -> %v", e, err)
		}
	}
}

func TestDownloadProfilePicture(t *testing.T) {
	w := newMsgAdapter(t)
	w.dataDir, w.instanceID = t.TempDir(), "inst_1"
	dir := filepath.Join(w.dataDir, "media", "inst_1")
	_ = os.MkdirAll(dir, 0o700)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(rw, "JPEGDATA")
	}))
	defer srv.Close()
	w.acct.httpClient = srv.Client()
	url := srv.URL + "/pic.jpg"
	w.acct.pictureInfo = func(context.Context, types.JID, *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
		return &types.ProfilePictureInfo{ID: "P1", URL: url}, nil
	}
	dest := filepath.Join(dir, "avatar.jpg")
	// The test server is not on WhatsApp's CDN: refused.
	if _, err := w.DownloadProfilePicture(context.Background(), "5511888887777@s.whatsapp.net", false, dest); err == nil {
		t.Fatal("non-WhatsApp URL must be refused")
	}
	if !pictureURL("https://pps.whatsapp.net/v/t61/x.jpg?oh=1") || pictureURL("http://pps.whatsapp.net/x") ||
		pictureURL("https://evil.com/x?pps.whatsapp.net") || pictureURL("https://whatsapp.net.evil.com/x") {
		t.Fatal("pictureURL")
	}
	if _, err := w.DownloadProfilePicture(context.Background(), "", false, filepath.Join(w.dataDir, "x.jpg")); err == nil {
		t.Fatal("dest outside media dir must be refused")
	}
}

func TestBlocklistPrefersPhoneJIDs(t *testing.T) {
	w := newMsgAdapter(t)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	lid := types.NewJID("123456", types.HiddenUserServer)
	var action events.BlocklistChangeAction
	w.acct.updateBlocklist = func(_ context.Context, j types.JID, a events.BlocklistChangeAction, _ string) (*types.Blocklist, error) {
		action = a
		return &types.Blocklist{Items: []types.BlocklistItem{{LID: lid, PN: pn}, {LID: types.NewJID("777", types.HiddenUserServer)}}}, nil
	}
	got, err := w.UpdateBlocklist(context.Background(), pn.String(), true)
	if err != nil || action != events.BlocklistChangeActionBlock || len(got) != 2 || got[0] != "5511888887777@s.whatsapp.net" || got[1] != "777@lid" {
		t.Fatalf("got %v %v %v", got, action, err)
	}
	if _, err := w.UpdateBlocklist(context.Background(), "120363@g.us", true); err == nil {
		t.Fatal("groups cannot be blocked")
	}
}

func TestUpdateProfile(t *testing.T) {
	w := newMsgAdapter(t)
	w.dataDir, w.instanceID = t.TempDir(), "inst_1"
	var patches []appstate.PatchInfo
	var about string
	var photoTarget types.JID
	var photo []byte
	w.acct.sendAppState = func(_ context.Context, p appstate.PatchInfo) error { patches = append(patches, p); return nil }
	w.acct.setStatus = func(_ context.Context, in types.SetStatusInput) error { about = *in.Text; return nil }
	w.acct.setPhoto = func(_ context.Context, j types.JID, b []byte) (string, error) {
		photoTarget, photo = j, b
		return "PIC1", nil
	}
	dir := filepath.Join(w.dataDir, "outbox", "inst_1")
	_ = os.MkdirAll(dir, 0o700)
	pic := filepath.Join(dir, "avatar.jpg")
	_ = os.WriteFile(pic, []byte("JPEG"), 0o600)
	out, err := w.UpdateProfile(context.Background(), &workerv1.UpdateProfileRequest{
		SetName: true, Name: "Loja", SetAbout: true, About: "Atendimento 9-18h", PicturePath: pic})
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].Type != appstate.WAPatchCriticalBlock || patches[0].Mutations[0].Value.GetPushNameSetting().GetName() != "Loja" {
		t.Fatalf("push name patch = %+v", patches)
	}
	if about != "Atendimento 9-18h" || !photoTarget.IsEmpty() || string(photo) != "JPEG" || out.PictureId != "PIC1" {
		t.Fatalf("about %q target %v photo %q out %+v", about, photoTarget, photo, out)
	}
	if _, err := w.UpdateProfile(context.Background(), &workerv1.UpdateProfileRequest{SetName: true, Name: strings.Repeat("x", 26)}); err == nil {
		t.Fatal("long name must be refused")
	}
	if _, err := w.UpdateProfile(context.Background(), &workerv1.UpdateProfileRequest{RemovePicture: true}); err != nil || photo != nil {
		t.Fatalf("remove: %v %q", err, photo)
	}
}

func TestPrivacy(t *testing.T) {
	w := newMsgAdapter(t)
	cur := types.PrivacySettings{LastSeen: types.PrivacySettingContacts, ReadReceipts: types.PrivacySettingAll}
	w.acct.privacy = func(context.Context, bool) (*types.PrivacySettings, error) { c := cur; return &c, nil }
	w.acct.setPrivacy = func(_ context.Context, n types.PrivacySettingType, v types.PrivacySetting) (types.PrivacySettings, error) {
		if n == types.PrivacySettingTypeReadReceipts {
			cur.ReadReceipts = v
		}
		return cur, nil
	}
	var timer time.Duration = -1
	w.acct.setDisappearing = func(_ context.Context, d time.Duration) error { timer = d; return nil }
	m, err := w.Privacy(context.Background())
	if err != nil || m["last"] != "contacts" || m["readreceipts"] != "all" {
		t.Fatalf("privacy = %v %v", m, err)
	}
	m, err = w.SetPrivacy(context.Background(), &workerv1.SetPrivacyRequest{Name: "readreceipts", Value: "none"})
	if err != nil || m["readreceipts"] != "none" {
		t.Fatalf("set = %v %v", m, err)
	}
	if _, err := w.SetPrivacy(context.Background(), &workerv1.SetPrivacyRequest{Name: "readreceipts", Value: "contacts"}); err == nil {
		t.Fatal("invalid value must be refused")
	}
	if _, err := w.SetPrivacy(context.Background(), &workerv1.SetPrivacyRequest{SetDefaultDisappearing: true, DefaultDisappearingSeconds: 86400}); err != nil || timer != 24*time.Hour {
		t.Fatalf("disappearing: %v %v", err, timer)
	}
	if _, err := w.SetPrivacy(context.Background(), &workerv1.SetPrivacyRequest{SetDefaultDisappearing: true, DefaultDisappearingSeconds: 5}); err == nil {
		t.Fatal("invalid timer must be refused")
	}
}

// Our own starred messages are indexed with sender "0" (direct chats and groups).
func TestStarOwnMessageIndex(t *testing.T) {
	w := newMsgAdapter(t)
	var idx [][]string
	w.acct.sendAppState = func(_ context.Context, p appstate.PatchInfo) error {
		idx = append(idx, p.Mutations[0].Index)
		return nil
	}
	w.acct.chatSettings = func(context.Context, types.JID) (types.LocalChatSettings, error) {
		return types.LocalChatSettings{}, nil
	}
	for _, chat := range []string{"5511888887777@s.whatsapp.net", "120363@g.us"} {
		if err := w.ChatAction(context.Background(), &workerv1.ChatActionRequest{Chat: chat, Action: "star",
			Message: &workerv1.MessageRef{Id: "OWN", FromMe: true}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range idx {
		if i[3] != "1" || i[4] != "0" {
			t.Fatalf("index = %v", i)
		}
	}
}
