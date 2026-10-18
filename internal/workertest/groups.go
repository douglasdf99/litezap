package workertest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var _ worker.GroupAdmin = (*FakeWA)(nil)

// ownJID is the fake account's own JID (testJID without the device).
const ownJID = "5511999999999@s.whatsapp.net"

// fakeGroups is the in-memory group state of a FakeWA (marco 8).
type fakeGroups struct {
	mu       sync.Mutex
	groups   map[string]*workerv1.GroupInfo
	links    map[string]string // group -> invite code
	invites  map[string]string // invite code -> group (also groups we are not in)
	requests map[string][]*workerv1.JoinRequest
	refuse   map[string]int // participant -> WhatsApp error code on add
	photos   map[string][]byte
	calls    []string
	err      error
	next     int
}

func (g *fakeGroups) init() {
	if g.groups == nil {
		g.groups = map[string]*workerv1.GroupInfo{}
		g.links = map[string]string{}
		g.invites = map[string]string{}
		g.requests = map[string][]*workerv1.JoinRequest{}
		g.refuse = map[string]int{}
		g.photos = map[string][]byte{}
	}
}

// lock records the call and returns the scripted error, if any; the caller unlocks.
func (f *FakeWA) glock(call string) error {
	f.grp.mu.Lock()
	f.grp.init()
	f.grp.calls = append(f.grp.calls, call)
	return f.grp.err
}

// SetGroup puts (or replaces) a group the account is in.
func (f *FakeWA) SetGroup(g *workerv1.GroupInfo) {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	f.grp.init()
	f.grp.groups[g.Jid] = proto.Clone(g).(*workerv1.GroupInfo)
}

// Group returns a copy of a group's current state (nil when unknown).
func (f *FakeWA) Group(jid string) *workerv1.GroupInfo {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	f.grp.init()
	g, ok := f.grp.groups[jid]
	if !ok {
		return nil
	}
	return proto.Clone(g).(*workerv1.GroupInfo)
}

// SetInvite makes code an invite link to g (a group the account may not be in).
func (f *FakeWA) SetInvite(code string, g *workerv1.GroupInfo) {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	f.grp.init()
	f.grp.invites[code] = g.Jid
	if _, ok := f.grp.groups[g.Jid]; !ok {
		c := proto.Clone(g).(*workerv1.GroupInfo)
		c.Participants = slices.DeleteFunc(c.Participants, func(p *workerv1.GroupParticipant) bool { return p.Jid == ownJID })
		f.grp.groups["preview:"+g.Jid] = c
	}
}

// SetJoinRequests sets the pending join requests of a group.
func (f *FakeWA) SetJoinRequests(group string, reqs ...*workerv1.JoinRequest) {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	f.grp.init()
	f.grp.requests[group] = reqs
}

// RefuseAdd makes adding participant fail with WhatsApp's code (403: privacy).
func (f *FakeWA) RefuseAdd(participant string, code int) {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	f.grp.init()
	f.grp.refuse[participant] = code
}

// SetGroupErr makes every group call fail with err (nil restores normal answers).
func (f *FakeWA) SetGroupErr(err error) {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	f.grp.err = err
}

// GroupCalls returns the group calls made so far ("create", "add:<group>", ...).
func (f *FakeWA) GroupCalls() []string {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	return append([]string(nil), f.grp.calls...)
}

// GroupPhoto returns the last photo set for a group.
func (f *FakeWA) GroupPhoto(jid string) []byte {
	f.grp.mu.Lock()
	defer f.grp.mu.Unlock()
	return f.grp.photos[jid]
}

func (g *fakeGroups) member(jid string) (*workerv1.GroupInfo, error) {
	gi, ok := g.groups[jid]
	if !ok || !strings.HasSuffix(jid, "@g.us") {
		return nil, status.Error(codes.NotFound, "group_not_found: group not found")
	}
	return gi, nil
}

func findParticipant(gi *workerv1.GroupInfo, jid string) int {
	return slices.IndexFunc(gi.Participants, func(p *workerv1.GroupParticipant) bool { return p.Jid == jid || p.Lid == jid })
}

func (f *FakeWA) ListGroups(context.Context) ([]*workerv1.GroupInfo, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("list"); err != nil {
		return nil, err
	}
	var out []*workerv1.GroupInfo
	for k, g := range f.grp.groups {
		if strings.HasPrefix(k, "preview:") {
			continue
		}
		out = append(out, proto.Clone(g).(*workerv1.GroupInfo))
	}
	slices.SortFunc(out, func(a, b *workerv1.GroupInfo) int { return strings.Compare(a.Jid, b.Jid) })
	return out, nil
}

func (f *FakeWA) GetGroup(_ context.Context, jid string) (*workerv1.GroupInfo, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("get:" + jid); err != nil {
		return nil, err
	}
	gi, err := f.grp.member(jid)
	if err != nil {
		return nil, err
	}
	return proto.Clone(gi).(*workerv1.GroupInfo), nil
}

func (f *FakeWA) CreateGroup(_ context.Context, req *workerv1.CreateGroupRequest) (*workerv1.CreateGroupResponse, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("create"); err != nil {
		return nil, err
	}
	f.grp.next++
	gi := &workerv1.GroupInfo{
		Jid: fmt.Sprintf("12036300000000%04d@g.us", f.grp.next), Name: req.Name, Owner: ownJID,
		Announce: req.Announce, Locked: req.Locked, ApprovalRequired: req.ApprovalRequired,
		EphemeralSeconds: req.EphemeralSeconds, AdminsAddMembers: req.AdminsAddMembers,
		Community: req.Community, Parent: req.Parent,
		Participants: []*workerv1.GroupParticipant{{Jid: ownJID, Admin: true, SuperAdmin: true}},
	}
	res := f.addLocked(gi, req.Participants)
	gi.ParticipantCount = int32(len(gi.Participants))
	f.grp.groups[gi.Jid] = gi
	return &workerv1.CreateGroupResponse{Group: proto.Clone(gi).(*workerv1.GroupInfo), Results: res}, nil
}

func (f *FakeWA) addLocked(gi *workerv1.GroupInfo, participants []string) []*workerv1.ParticipantResult {
	var res []*workerv1.ParticipantResult
	for _, p := range participants {
		r := &workerv1.ParticipantResult{Participant: p, Jid: p, Status: "ok"}
		switch code := f.grp.refuse[p]; {
		case code == 403:
			r.Status, r.Code, r.InviteCode = "privacy_invite_required", 403, "INVITE-"+p
		case code != 0:
			r.Status, r.Code = fmt.Sprintf("error_%d", code), int32(code)
		case findParticipant(gi, p) >= 0:
			r.Status, r.Code = "already_participant", 409
		default:
			gi.Participants = append(gi.Participants, &workerv1.GroupParticipant{Jid: p})
		}
		res = append(res, r)
	}
	return res
}

func (f *FakeWA) UpdateGroup(_ context.Context, req *workerv1.UpdateGroupRequest) ([]string, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("update:" + req.Jid); err != nil {
		return nil, err
	}
	gi, err := f.grp.member(req.Jid)
	if err != nil {
		return nil, err
	}
	var applied []string
	if req.Name != nil {
		gi.Name, applied = req.GetName(), append(applied, "name")
	}
	if req.Description != nil {
		gi.Description, applied = req.GetDescription(), append(applied, "description")
	}
	if req.Announce != nil {
		gi.Announce, applied = req.GetAnnounce(), append(applied, "announce")
	}
	if req.Locked != nil {
		gi.Locked, applied = req.GetLocked(), append(applied, "locked")
	}
	if req.ApprovalRequired != nil {
		gi.ApprovalRequired, applied = req.GetApprovalRequired(), append(applied, "approval_required")
	}
	if req.AdminsAddMembers != nil {
		gi.AdminsAddMembers, applied = req.GetAdminsAddMembers(), append(applied, "admins_add_members")
	}
	if req.EphemeralSeconds != nil {
		gi.EphemeralSeconds, applied = req.GetEphemeralSeconds(), append(applied, "ephemeral_seconds")
	}
	return applied, nil
}

func (f *FakeWA) SetGroupPhoto(_ context.Context, jid string, jpeg []byte) (string, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("photo:" + jid); err != nil {
		return "", err
	}
	if _, err := f.grp.member(jid); err != nil {
		return "", err
	}
	f.grp.photos[jid] = jpeg
	if len(jpeg) == 0 {
		return "remove", nil
	}
	return "PIC1", nil
}

func (f *FakeWA) UpdateParticipants(_ context.Context, jid, action string, participants []string) ([]*workerv1.ParticipantResult, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock(action + ":" + jid); err != nil {
		return nil, err
	}
	gi, err := f.grp.member(jid)
	if err != nil {
		return nil, err
	}
	if action == "add" {
		res := f.addLocked(gi, participants)
		gi.ParticipantCount = int32(len(gi.Participants))
		return res, nil
	}
	var res []*workerv1.ParticipantResult
	for _, p := range participants {
		r := &workerv1.ParticipantResult{Participant: p, Jid: p, Status: "ok"}
		i := findParticipant(gi, p)
		if i < 0 {
			r.Status, r.Code = "not_participant", 404
			res = append(res, r)
			continue
		}
		switch action {
		case "remove":
			gi.Participants = slices.Delete(gi.Participants, i, i+1)
		case "promote":
			gi.Participants[i].Admin = true
		case "demote":
			gi.Participants[i].Admin = false
		}
		res = append(res, r)
	}
	gi.ParticipantCount = int32(len(gi.Participants))
	return res, nil
}

func (f *FakeWA) ListJoinRequests(_ context.Context, jid string) ([]*workerv1.JoinRequest, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("requests:" + jid); err != nil {
		return nil, err
	}
	if _, err := f.grp.member(jid); err != nil {
		return nil, err
	}
	return append([]*workerv1.JoinRequest(nil), f.grp.requests[jid]...), nil
}

func (f *FakeWA) UpdateJoinRequests(_ context.Context, jid string, approve bool, participants []string) ([]*workerv1.ParticipantResult, error) {
	verb := "reject"
	if approve {
		verb = "approve"
	}
	defer f.grp.mu.Unlock()
	if err := f.glock(verb + ":" + jid); err != nil {
		return nil, err
	}
	gi, err := f.grp.member(jid)
	if err != nil {
		return nil, err
	}
	var res []*workerv1.ParticipantResult
	for _, p := range participants {
		r := &workerv1.ParticipantResult{Participant: p, Jid: p, Status: "ok"}
		reqs := f.grp.requests[jid]
		i := slices.IndexFunc(reqs, func(q *workerv1.JoinRequest) bool { return q.Jid == p || q.Lid == p })
		if i < 0 {
			r.Status, r.Code = "not_participant", 404
		} else {
			f.grp.requests[jid] = slices.Delete(reqs, i, i+1)
			if approve {
				gi.Participants = append(gi.Participants, &workerv1.GroupParticipant{Jid: p})
			}
		}
		res = append(res, r)
	}
	return res, nil
}

func (f *FakeWA) InviteLink(_ context.Context, jid string, reset bool) (string, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("invite:" + jid); err != nil {
		return "", err
	}
	if _, err := f.grp.member(jid); err != nil {
		return "", err
	}
	code := f.grp.links[jid]
	if code == "" || reset {
		if code != "" {
			delete(f.grp.invites, code)
		}
		f.grp.next++
		code = fmt.Sprintf("CODE%d", f.grp.next)
		f.grp.links[jid] = code
		f.grp.invites[code] = jid
	}
	return "https://chat.whatsapp.com/" + code, nil
}

func fakeCode(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "https://chat.whatsapp.com/")
}

func (f *FakeWA) previewLocked(code string) (*workerv1.GroupInfo, error) {
	jid, ok := f.grp.invites[code]
	if !ok {
		return nil, status.Error(codes.NotFound, "invite_revoked: the invite link was revoked")
	}
	if gi, ok := f.grp.groups[jid]; ok {
		return gi, nil
	}
	return f.grp.groups["preview:"+jid], nil
}

func (f *FakeWA) PreviewInvite(_ context.Context, raw string) (*workerv1.GroupInfo, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("preview"); err != nil {
		return nil, err
	}
	gi, err := f.previewLocked(fakeCode(raw))
	if err != nil {
		return nil, err
	}
	return proto.Clone(gi).(*workerv1.GroupInfo), nil
}

func (f *FakeWA) JoinInvite(_ context.Context, raw string) (string, bool, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("join"); err != nil {
		return "", false, err
	}
	gi, err := f.previewLocked(fakeCode(raw))
	if err != nil {
		return "", false, err
	}
	if gi.ApprovalRequired {
		return gi.Jid, true, nil
	}
	if _, ok := f.grp.groups[gi.Jid]; !ok {
		c := proto.Clone(gi).(*workerv1.GroupInfo)
		c.Participants = append(c.Participants, &workerv1.GroupParticipant{Jid: ownJID})
		f.grp.groups[gi.Jid] = c
	}
	return gi.Jid, false, nil
}

func (f *FakeWA) LeaveGroup(_ context.Context, jid string) error {
	defer f.grp.mu.Unlock()
	if err := f.glock("leave:" + jid); err != nil {
		return err
	}
	if _, err := f.grp.member(jid); err != nil {
		return err
	}
	delete(f.grp.groups, jid)
	return nil
}

func (f *FakeWA) LinkGroup(_ context.Context, community, group string, unlink bool) error {
	verb := "link"
	if unlink {
		verb = "unlink"
	}
	defer f.grp.mu.Unlock()
	if err := f.glock(verb + ":" + community + ":" + group); err != nil {
		return err
	}
	c, err := f.grp.member(community)
	if err != nil {
		return err
	}
	if !c.Community {
		return status.Error(codes.InvalidArgument, "validation_failed: not a community")
	}
	g, err := f.grp.member(group)
	if err != nil {
		return err
	}
	if unlink {
		g.Parent = ""
	} else {
		g.Parent = community
	}
	return nil
}

func (f *FakeWA) ListSubGroups(_ context.Context, community string) ([]*workerv1.SubGroup, error) {
	defer f.grp.mu.Unlock()
	if err := f.glock("subgroups:" + community); err != nil {
		return nil, err
	}
	if _, err := f.grp.member(community); err != nil {
		return nil, err
	}
	var out []*workerv1.SubGroup
	for _, g := range f.grp.groups {
		if g.Parent == community {
			out = append(out, &workerv1.SubGroup{Jid: g.Jid, Name: g.Name, AnnouncementGroup: g.AnnouncementGroup})
		}
	}
	slices.SortFunc(out, func(a, b *workerv1.SubGroup) int { return strings.Compare(a.Jid, b.Jid) })
	return out, nil
}

// EmitGroupUpdate simulates a group notification from WhatsApp.
func (f *Fake) EmitGroupUpdate(u *workerv1.GroupUpdate) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Group{Group: u}})
}

// Groups is the fake's Groups client (spawner.GroupsHandle).
func (f *Fake) Groups() workerv1.GroupsClient { return workerv1.NewGroupsClient(f.cc) }
