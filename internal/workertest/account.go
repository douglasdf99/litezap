package workertest

import (
	"context"
	"fmt"
	"os"
	"sort"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var _ worker.Account = (*FakeWA)(nil)

// FakePicture is a contact's profile picture as the fake serves it.
type FakePicture struct {
	ID   string
	URL  string
	Data []byte
}

// fakeAccount is the marco 9 state of a FakeWA (guarded by FakeWA.msgMu).
type fakeAccount struct {
	actions  []*workerv1.ChatActionRequest
	settings map[string]*workerv1.GetChatSettingsResponse
	pictures map[string]FakePicture // "" is the account itself
	users    map[string]*workerv1.GetUserInfoResponse
	blocked  map[string]bool
	profile  workerv1.GetProfileResponse
	updates  []*workerv1.UpdateProfileRequest
	uploaded []byte // last profile picture read from picture_path
	privacy  map[string]string
	timer    uint32
	lookups  int // network lookups (pictures, user info)
	err      error
	echo     bool // ChatAction emits the matching ChatUpdate, like whatsmeow's echo
	// contactActs are the ContactAction requests (CON-2).
	contactActs []*workerv1.ContactActionRequest
}

// SetAccountErr makes every Account method fail with err (nil restores answers).
func (f *FakeWA) SetAccountErr(err error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.acct.err = err
}

// SetChatEcho makes ChatAction emit the ChatUpdate whatsmeow would echo back.
func (f *FakeWA) SetChatEcho(on bool) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.acct.echo = on
}

// ChatActions returns the ChatAction requests received so far.
func (f *FakeWA) ChatActions() []*workerv1.ChatActionRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.ChatActionRequest(nil), f.acct.actions...)
}

// SetChatSettings sets what GetChatSettings answers for chat.
func (f *FakeWA) SetChatSettings(chat string, s *workerv1.GetChatSettingsResponse) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.settings == nil {
		f.acct.settings = map[string]*workerv1.GetChatSettingsResponse{}
	}
	f.acct.settings[chat] = s
}

// SetPicture sets a contact's picture ("" for the account itself).
func (f *FakeWA) SetPicture(jid string, p FakePicture) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.pictures == nil {
		f.acct.pictures = map[string]FakePicture{}
	}
	f.acct.pictures[jid] = p
}

// SetUserInfo sets what GetUserInfo answers for jid.
func (f *FakeWA) SetUserInfo(jid string, u *workerv1.GetUserInfoResponse) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.users == nil {
		f.acct.users = map[string]*workerv1.GetUserInfoResponse{}
	}
	f.acct.users[jid] = u
}

// Lookups counts network lookups about contacts (pictures and user info).
func (f *FakeWA) Lookups() int {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.acct.lookups
}

// SetProfile sets the account profile GetProfile answers.
func (f *FakeWA) SetProfile(p *workerv1.GetProfileResponse) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.acct.profile = workerv1.GetProfileResponse{Jid: p.Jid, Lid: p.Lid, PushName: p.PushName,
		BusinessName: p.BusinessName, Platform: p.Platform, About: p.About, PictureId: p.PictureId}
}

// ProfileUpdates returns the UpdateProfile requests and the last uploaded picture.
func (f *FakeWA) ProfileUpdates() ([]*workerv1.UpdateProfileRequest, []byte) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.UpdateProfileRequest(nil), f.acct.updates...), f.acct.uploaded
}

// DefaultDisappearing is the last default disappearing timer set (seconds).
func (f *FakeWA) DefaultDisappearing() uint32 {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.acct.timer
}

// Blocked reports whether jid is in the fake's blocklist.
func (f *FakeWA) Blocked(jid string) bool {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.acct.blocked[jid]
}

func (f *FakeWA) ChatAction(_ context.Context, req *workerv1.ChatActionRequest) error {
	f.msgMu.Lock()
	if f.acct.err != nil {
		f.msgMu.Unlock()
		return f.acct.err
	}
	f.acct.actions = append(f.acct.actions, req)
	echo := f.acct.echo
	f.msgMu.Unlock()
	if !echo {
		return nil
	}
	u := &workerv1.ChatUpdate{Chat: req.Chat}
	switch req.Action {
	case "archive", "unarchive":
		u.Field, u.Value = "archived", req.Action == "archive"
	case "pin", "unpin":
		u.Field, u.Value = "pinned", req.Action == "pin"
	case "mute":
		u.Field, u.Value, u.MutedUntilMs = "muted", true, req.MuteUntilMs
		if u.MutedUntilMs == 0 {
			u.MutedUntilMs = -1
		}
	case "unmute":
		u.Field = "muted"
	case "read", "unread":
		u.Field, u.Value = "unread", req.Action == "unread"
	case "delete":
		u.Field, u.Value = "deleted", true
	case "clear":
		u.Field, u.Value = "cleared", true
	case "star", "unstar":
		u.Field, u.Value, u.MessageId, u.FromMe = "starred", req.Action == "star", req.Message.GetId(), req.Message.GetFromMe()
	}
	f.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_ChatUpdate{ChatUpdate: u}})
	return nil
}

// ContactActions returns the ContactAction requests received so far.
func (f *FakeWA) ContactActions() []*workerv1.ContactActionRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.ContactActionRequest(nil), f.acct.contactActs...)
}

// ContactAction saves or removes the contact's name in the fake address book (what the
// app state echo does on a real account).
func (f *FakeWA) ContactAction(_ context.Context, req *workerv1.ContactActionRequest) (*workerv1.ContactActionResponse, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	f.acct.contactActs = append(f.acct.contactActs, req)
	kept := f.contacts[:0:0]
	var found *workerv1.Contact
	for _, c := range f.contacts {
		if c.Jid == req.Jid {
			found = proto.Clone(c).(*workerv1.Contact)
			continue
		}
		kept = append(kept, c)
	}
	if found == nil {
		found = &workerv1.Contact{Jid: req.Jid}
	}
	switch req.Action {
	case "save":
		found.Name = req.FullName
		kept = append(kept, found)
	case "remove":
		if found.Name == "" { // like the worker: never remove an entry the address book lacks
			return nil, status.Error(codes.NotFound, "not_found: contato não está na agenda deste aparelho")
		}
		if found.PushName != "" || found.BusinessName != "" { // still known by its push name
			found.Name = ""
			kept = append(kept, found)
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "validation_failed: unknown contact action")
	}
	f.contacts = kept
	return &workerv1.ContactActionResponse{Jid: req.Jid}, nil
}

func (f *FakeWA) ChatSettings(_ context.Context, chat string) (*workerv1.GetChatSettingsResponse, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	if s, ok := f.acct.settings[chat]; ok {
		return proto.Clone(s).(*workerv1.GetChatSettingsResponse), nil
	}
	return &workerv1.GetChatSettingsResponse{}, nil
}

func (f *FakeWA) picture(jid string) (FakePicture, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return FakePicture{}, f.acct.err
	}
	f.acct.lookups++
	p, ok := f.acct.pictures[jid]
	if !ok {
		return p, status.Error(codes.NotFound, "not_found: no profile picture")
	}
	return p, nil
}

func (f *FakeWA) ProfilePicture(_ context.Context, jid string, preview bool) (*workerv1.GetProfilePictureResponse, error) {
	p, err := f.picture(jid)
	if err != nil {
		return nil, err
	}
	typ := "image"
	if preview {
		typ = "preview"
	}
	return &workerv1.GetProfilePictureResponse{Id: p.ID, Url: p.URL, Type: typ}, nil
}

func (f *FakeWA) DownloadProfilePicture(_ context.Context, jid string, _ bool, dest string) (*workerv1.DownloadProfilePictureResponse, error) {
	p, err := f.picture(jid)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(dest, p.Data, 0o600); err != nil {
		return nil, status.Error(codes.Unknown, "worker_error: "+err.Error())
	}
	return &workerv1.DownloadProfilePictureResponse{Id: p.ID, Size: int64(len(p.Data))}, nil
}

func (f *FakeWA) UserInfo(_ context.Context, jid string, business bool) (*workerv1.GetUserInfoResponse, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	f.acct.lookups++
	u, ok := f.acct.users[jid]
	if !ok {
		return nil, status.Error(codes.NotFound, "not_found: contact not found on WhatsApp")
	}
	out := proto.Clone(u).(*workerv1.GetUserInfoResponse)
	if !business {
		out.Business = nil
	}
	return out, nil
}

func (f *FakeWA) blockedList() []string {
	out := []string{}
	for j := range f.acct.blocked {
		out = append(out, j)
	}
	sort.Strings(out)
	return out
}

func (f *FakeWA) Blocklist(context.Context) ([]string, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	return f.blockedList(), nil
}

func (f *FakeWA) UpdateBlocklist(_ context.Context, jid string, block bool) ([]string, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	if f.acct.blocked == nil {
		f.acct.blocked = map[string]bool{}
	}
	if block {
		f.acct.blocked[jid] = true
	} else {
		delete(f.acct.blocked, jid)
	}
	return f.blockedList(), nil
}

func (f *FakeWA) Profile(context.Context) (*workerv1.GetProfileResponse, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	p := proto.Clone(&f.acct.profile).(*workerv1.GetProfileResponse)
	if p.Jid == "" {
		p.Jid = "5511999999999@s.whatsapp.net"
	}
	return p, nil
}

func (f *FakeWA) UpdateProfile(_ context.Context, req *workerv1.UpdateProfileRequest) (*workerv1.UpdateProfileResponse, error) {
	var data []byte
	if req.PicturePath != "" {
		var err error
		if data, err = os.ReadFile(req.PicturePath); err != nil {
			return nil, status.Error(codes.InvalidArgument, "validation_failed: cannot read the picture file")
		}
	}
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	f.acct.updates = append(f.acct.updates, req)
	out := &workerv1.UpdateProfileResponse{}
	if req.SetName {
		f.acct.profile.PushName = req.Name
	}
	if req.SetAbout {
		f.acct.profile.About = req.About
	}
	switch {
	case req.RemovePicture:
		f.acct.profile.PictureId = ""
	case data != nil:
		f.acct.uploaded = data
		f.acct.profile.PictureId = fmt.Sprintf("PIC%d", len(f.acct.updates))
		out.PictureId = f.acct.profile.PictureId
	}
	return out, nil
}

func (f *FakeWA) Privacy(context.Context) (map[string]string, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	return f.privacyCopy(), nil
}

// privacyCopy must be called with msgMu held.
func (f *FakeWA) privacyCopy() map[string]string {
	if f.acct.privacy == nil {
		f.acct.privacy = map[string]string{"last": "contacts", "online": "all", "profile": "all", "status": "contacts",
			"groupadd": "all", "readreceipts": "all", "calladd": "all", "messages": "all"}
	}
	out := map[string]string{}
	for k, v := range f.acct.privacy {
		out[k] = v
	}
	return out
}

func (f *FakeWA) SetPrivacy(_ context.Context, req *workerv1.SetPrivacyRequest) (map[string]string, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.acct.err != nil {
		return nil, f.acct.err
	}
	if req.SetDefaultDisappearing {
		f.acct.timer = req.DefaultDisappearingSeconds
	}
	f.privacyCopy()
	if req.Name != "" {
		if !worker.ValidPrivacy(req.Name, req.Value) {
			return nil, status.Error(codes.InvalidArgument, "validation_failed: invalid privacy setting or value")
		}
		f.acct.privacy[req.Name] = req.Value
	}
	return f.privacyCopy(), nil
}

// EmitChatUpdate simulates an app state change made on the phone.
func (f *Fake) EmitChatUpdate(u *workerv1.ChatUpdate) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_ChatUpdate{ChatUpdate: u}})
}
