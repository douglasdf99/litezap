package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// fakeGroups records calls and answers from scripted values.
type fakeGroups struct {
	groupAPI // unimplemented methods panic
	info     *types.GroupInfo
	created  whatsmeow.ReqCreateGroup
	updates  []string
	sent     []types.JID
	action   whatsmeow.ParticipantChange
	answers  []types.GroupParticipant
	err      error
	failAt   string
	joined   string
	requests []types.GroupParticipantRequest
	reqSent  []types.JID
}

func (f *fakeGroups) GetGroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func (f *fakeGroups) CreateGroup(_ context.Context, req whatsmeow.ReqCreateGroup) (*types.GroupInfo, error) {
	f.created = req
	return f.info, f.err
}

func (f *fakeGroups) rec(name string) error {
	if f.failAt == name {
		return whatsmeow.ErrIQForbidden
	}
	f.updates = append(f.updates, name)
	return nil
}

func (f *fakeGroups) SetGroupName(context.Context, types.JID, string) error { return f.rec("name") }
func (f *fakeGroups) SetGroupTopic(context.Context, types.JID, string, string, string) error {
	return f.rec("description")
}
func (f *fakeGroups) SetGroupAnnounce(context.Context, types.JID, bool) error {
	return f.rec("announce")
}
func (f *fakeGroups) SetGroupLocked(context.Context, types.JID, bool) error { return f.rec("locked") }
func (f *fakeGroups) SetDisappearingTimer(_ context.Context, _ types.JID, d time.Duration, _ time.Time) error {
	return f.rec("ephemeral:" + d.String())
}

func (f *fakeGroups) UpdateGroupParticipants(_ context.Context, _ types.JID, changes []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error) {
	f.sent, f.action = changes, action
	return f.answers, f.err
}

func (f *fakeGroups) GetGroupRequestParticipants(context.Context, types.JID) ([]types.GroupParticipantRequest, error) {
	return f.requests, nil
}

func (f *fakeGroups) UpdateGroupRequestParticipants(_ context.Context, _ types.JID, changes []types.JID, _ whatsmeow.ParticipantRequestChange) ([]types.GroupParticipant, error) {
	f.reqSent = changes
	return nil, nil
}

func (f *fakeGroups) GetGroupInfoFromLink(_ context.Context, code string) (*types.GroupInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func (f *fakeGroups) JoinGroupWithLink(_ context.Context, code string) (types.JID, error) {
	f.joined = code
	return f.info.JID, nil
}

func newGroupAdapter(t *testing.T, f *fakeGroups) *Whatsmeow {
	t.Helper()
	w := newMsgAdapter(t)
	w.gapi = f
	return w
}

var (
	grp   = types.NewJID("120363000000000001", types.GroupServer)
	pnA   = types.NewJID("5511888887777", types.DefaultUserServer)
	lidA  = types.NewJID("111111", types.HiddenUserServer)
	pnB   = types.NewJID("5511777776666", types.DefaultUserServer)
	ownPN = types.NewJID("5511999999999", types.DefaultUserServer)
)

func TestGetGroupReportsPhoneNumbersAndSettings(t *testing.T) {
	f := &fakeGroups{info: &types.GroupInfo{
		JID: grp, OwnerJID: lidA, OwnerPN: pnA, AddressingMode: types.AddressingModeLID,
		GroupName:      types.GroupName{Name: "Equipe"},
		GroupTopic:     types.GroupTopic{Topic: "desc"},
		GroupAnnounce:  types.GroupAnnounce{IsAnnounce: true},
		GroupEphemeral: types.GroupEphemeral{IsEphemeral: true, DisappearingTimer: 86400},
		MemberAddMode:  types.GroupMemberAddModeAdmin,
		Participants: []types.GroupParticipant{
			{JID: lidA, LID: lidA, PhoneNumber: pnA, IsAdmin: true, IsSuperAdmin: true},
			{JID: pnB},
		},
	}}
	w := newGroupAdapter(t, f)
	gi, err := w.GetGroup(context.Background(), grp.String())
	if err != nil {
		t.Fatal(err)
	}
	if gi.Name != "Equipe" || gi.Description != "desc" || !gi.Announce || gi.EphemeralSeconds != 86400 ||
		!gi.AdminsAddMembers || !gi.LidAddressing || gi.Owner != pnA.String() || gi.ParticipantCount != 2 {
		t.Fatalf("info = %+v", gi)
	}
	p := gi.Participants[0]
	if p.Jid != pnA.String() || p.Lid != lidA.String() || !p.Admin || !p.SuperAdmin {
		t.Fatalf("participant = %+v", p)
	}
	if _, err := w.GetGroup(context.Background(), pnA.String()); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("non-group jid = %v", err)
	}
	f.err = whatsmeow.ErrIQNotFound
	if _, err := w.GetGroup(context.Background(), grp.String()); status.Code(err) != codes.NotFound {
		t.Fatalf("missing = %v", err)
	}
}

func TestUpdateParticipantsResultsPerParticipant(t *testing.T) {
	f := &fakeGroups{answers: []types.GroupParticipant{
		{JID: pnA},
		{JID: lidA, PhoneNumber: pnB, Error: 403, AddRequest: &types.GroupParticipantAddRequest{Code: "INV", Expiration: time.Unix(1_900_000_000, 0)}},
	}}
	w := newGroupAdapter(t, f)
	third := "5511666665555@s.whatsapp.net"
	res, err := w.UpdateParticipants(context.Background(), grp.String(), "add", []string{pnA.String(), pnB.String(), third})
	if err != nil {
		t.Fatal(err)
	}
	if f.action != whatsmeow.ParticipantChangeAdd || len(f.sent) != 3 {
		t.Fatalf("sent %v %v", f.action, f.sent)
	}
	if res[0].Status != "ok" || res[1].Status != "privacy_invite_required" || res[1].InviteCode != "INV" ||
		res[1].Code != 403 || res[2].Status != "unknown" {
		t.Fatalf("results = %+v", res)
	}
	if _, err := w.UpdateParticipants(context.Background(), grp.String(), "kick", []string{pnA.String()}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad action = %v", err)
	}
	if _, err := w.UpdateParticipants(context.Background(), grp.String(), "add", []string{grp.String()}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("group as participant = %v", err)
	}
}

// Members of a LID group are removed by their LID.
func TestRemoveUsesLIDInLIDGroups(t *testing.T) {
	f := &fakeGroups{answers: []types.GroupParticipant{{JID: lidA, PhoneNumber: pnA, Error: 404}}}
	w := newGroupAdapter(t, f)
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{AddressingMode: types.AddressingModeLID}, nil
	}
	w.lidFor = func(_ context.Context, pn types.JID) (types.JID, error) { return lidA, nil }
	res, err := w.UpdateParticipants(context.Background(), grp.String(), "remove", []string{pnA.String()})
	if err != nil {
		t.Fatal(err)
	}
	if f.sent[0] != lidA {
		t.Fatalf("sent = %v", f.sent)
	}
	if res[0].Status != "not_participant" || res[0].Jid != pnA.String() {
		t.Fatalf("result = %+v", res[0])
	}
}

func TestUpdateGroupStopsAtFirstFailure(t *testing.T) {
	f := &fakeGroups{failAt: "locked"}
	w := newGroupAdapter(t, f)
	applied, err := w.UpdateGroup(context.Background(), &workerv1.UpdateGroupRequest{
		Jid: grp.String(), Name: proto.String("x"), Announce: proto.Bool(true), Locked: proto.Bool(true), EphemeralSeconds: proto.Uint32(0),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v", err)
	}
	if len(applied) != 2 || applied[0] != "name" || applied[1] != "announce" {
		t.Fatalf("applied = %v", applied)
	}
	f.failAt = ""
	f.updates = nil
	if _, err := w.UpdateGroup(context.Background(), &workerv1.UpdateGroupRequest{Jid: grp.String(), EphemeralSeconds: proto.Uint32(604800)}); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || f.updates[0] != "ephemeral:168h0m0s" {
		t.Fatalf("updates = %v", f.updates)
	}
}

func TestCreateGroupRequest(t *testing.T) {
	f := &fakeGroups{info: &types.GroupInfo{JID: grp, GroupName: types.GroupName{Name: "Nova"},
		Participants: []types.GroupParticipant{{JID: ownPN, IsSuperAdmin: true, IsAdmin: true}, {JID: pnA, Error: 408}}}}
	w := newGroupAdapter(t, f)
	res, err := w.CreateGroup(context.Background(), &workerv1.CreateGroupRequest{
		Name: "Nova", Participants: []string{pnA.String()}, Announce: true, EphemeralSeconds: 86400, AdminsAddMembers: true,
		Parent: "120363000000000009@g.us",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := f.created
	if c.Name != "Nova" || len(c.Participants) != 1 || !c.IsAnnounce || !c.IsEphemeral || c.DisappearingTimer != 86400 ||
		c.MemberAddMode != types.GroupMemberAddModeAdmin || c.LinkedParentJID.User != "120363000000000009" {
		t.Fatalf("request = %+v", c)
	}
	if res.Group.Jid != grp.String() || res.Results[0].Status != "recently_left" {
		t.Fatalf("response = %+v", res)
	}
}

func TestJoinInviteReportsPending(t *testing.T) {
	f := &fakeGroups{info: &types.GroupInfo{JID: grp, GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{IsJoinApprovalRequired: true}}}
	w := newGroupAdapter(t, f)
	jid, pending, err := w.JoinInvite(context.Background(), "https://chat.whatsapp.com/AbCdEf123?x=1")
	if err != nil || jid != grp.String() || !pending || f.joined != "AbCdEf123" {
		t.Fatalf("join = %q %v %v (code %q)", jid, pending, err, f.joined)
	}
	if _, _, err := w.JoinInvite(context.Background(), "https://chat.whatsapp.com/a/b"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad link = %v", err)
	}
	f.err = whatsmeow.ErrInviteLinkRevoked
	if _, err := w.PreviewInvite(context.Background(), "AbC"); status.Code(err) != codes.NotFound {
		t.Fatalf("revoked = %v", err)
	}
}

func TestGroupErrMapping(t *testing.T) {
	cases := map[error]codes.Code{
		whatsmeow.ErrNotInGroup:                  codes.PermissionDenied,
		whatsmeow.ErrGroupInviteLinkUnauthorized: codes.PermissionDenied,
		whatsmeow.ErrIQNotAcceptable:             codes.InvalidArgument,
		whatsmeow.ErrIQRateOverLimit:             codes.ResourceExhausted,
		whatsmeow.ErrNotConnected:                codes.Unavailable,
		errors.New("boom"):                       codes.Unknown,
	}
	for in, want := range cases {
		if got := status.Code(groupErr(in)); got != want {
			t.Errorf("groupErr(%v) = %v, want %v", in, got, want)
		}
	}
}

func groupEvents(r *dataRec) []*workerv1.GroupUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*workerv1.GroupUpdate
	for _, e := range r.evs {
		if g := e.GetGroup(); g != nil {
			out = append(out, g)
		}
	}
	return out
}

func TestGroupEvents(t *testing.T) {
	w := newMsgAdapter(t)
	r := w.sink.(*dataRec)
	ts := time.Unix(1_800_000_000, 0)
	w.handle(&events.GroupInfo{
		JID: grp, Sender: &lidA, SenderPN: &pnA, Timestamp: ts,
		Name: &types.GroupName{Name: "Novo nome"}, Join: []types.JID{pnB}, Promote: []types.JID{pnB},
		Topic:     &types.GroupTopic{Topic: "old", TopicDeleted: true},
		Ephemeral: &types.GroupEphemeral{IsEphemeral: false},
		UnknownChanges: []*waBinary.Node{
			{Tag: "created_membership_requests", Content: []waBinary.Node{{Tag: "requested_user", Attrs: waBinary.Attrs{"jid": pnB}}}},
			{Tag: "revoked_membership_requests", Content: []waBinary.Node{{Tag: "requested_user", Attrs: waBinary.Attrs{"jid": pnA}}}},
			{Tag: "mystery"},
		},
		MembershipApprovalMode: &types.GroupMembershipApprovalMode{IsJoinApprovalRequired: true},
		Suspended:              true,
	})
	w.handle(&events.JoinedGroup{Reason: "invite", GroupInfo: types.GroupInfo{JID: grp, GroupName: types.GroupName{Name: "G"}}})
	w.handle(&events.Picture{JID: grp, Author: pnA, Remove: true})
	w.handle(&events.Picture{JID: pnA}) // a contact's photo: not a group event
	got := groupEvents(r)
	if len(got) != 3 {
		t.Fatalf("events = %v", got)
	}
	u := got[0]
	if u.Actor != pnA.String() || u.GetName() != "Novo nome" || u.Description == nil || u.GetDescription() != "" ||
		u.EphemeralSeconds == nil || u.GetEphemeralSeconds() != 0 || len(u.Joined) != 1 || u.Joined[0] != pnB.String() ||
		len(u.Promoted) != 1 || u.TimestampMs != ts.UnixMilli() || len(u.JoinRequests) != 1 || u.JoinRequests[0] != pnB.String() ||
		len(u.Unknown) != 1 || u.Unknown[0] != "mystery" || u.Announce != nil || !u.ApprovalModeChanged || u.ApprovalRequired != nil ||
		len(u.JoinRequestsRevoked) != 1 || u.JoinRequestsRevoked[0] != pnA.String() || !u.Suspended {
		t.Fatalf("update = %+v", u)
	}
	if !got[1].JoinedSelf || got[1].Info.GetName() != "G" || got[1].JoinReason != "invite" {
		t.Fatalf("joined = %+v", got[1])
	}
	if !got[2].PhotoRemoved || got[2].Actor != pnA.String() {
		t.Fatalf("photo = %+v", got[2])
	}
}

// Members are named as the server's participant list has them, even without a LID
// mapping in the local store; requesters as the pending request list has them.
func TestMembersAndRequestersResolvedFromServerLists(t *testing.T) {
	f := &fakeGroups{info: &types.GroupInfo{JID: grp, AddressingMode: types.AddressingModeLID,
		Participants: []types.GroupParticipant{{JID: lidA, LID: lidA, PhoneNumber: pnA}}},
		requests: []types.GroupParticipantRequest{{JID: lidA}}}
	w := newGroupAdapter(t, f)
	if _, err := w.UpdateParticipants(context.Background(), grp.String(), "demote", []string{pnA.String(), pnB.String()}); err != nil {
		t.Fatal(err)
	}
	if f.sent[0] != lidA || f.sent[1] != pnB {
		t.Fatalf("sent = %v", f.sent)
	}
	// The request list names lidA; asking by its LID works.
	if _, err := w.UpdateJoinRequests(context.Background(), grp.String(), true, []string{lidA.String()}); err != nil {
		t.Fatal(err)
	}
	if f.reqSent[0] != lidA {
		t.Fatalf("approve sent = %v", f.reqSent)
	}
}

// A description update keeps the group error codes (whatsmeow would wrap them).
func TestDescriptionKeepsErrorCode(t *testing.T) {
	f := &fakeGroups{err: whatsmeow.ErrIQForbidden}
	w := newGroupAdapter(t, f)
	_, err := w.UpdateGroup(context.Background(), &workerv1.UpdateGroupRequest{Jid: grp.String(), Description: proto.String("x")})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v", err)
	}
}

// An unpaired session answers group calls at once instead of waiting for the timeout.
func TestGroupsFailFastWhenUnpaired(t *testing.T) {
	w := NewWhatsmeow(whatsmeow.NewClient(&store.Device{}, nil), slog.New(slog.DiscardHandler))
	g := NewGroupsServer(NewServer(w))
	if _, err := g.ListGroups(context.Background(), &workerv1.ListGroupsRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err = %v", err)
	}
}
