package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GroupAdmin is implemented by WAClients that manage groups and communities (marco 8).
// Like Realtime it is optional: without it the Groups RPCs answer Unimplemented.
type GroupAdmin interface {
	ListGroups(ctx context.Context) ([]*workerv1.GroupInfo, error)
	GetGroup(ctx context.Context, jid string) (*workerv1.GroupInfo, error)
	CreateGroup(ctx context.Context, req *workerv1.CreateGroupRequest) (*workerv1.CreateGroupResponse, error)
	UpdateGroup(ctx context.Context, req *workerv1.UpdateGroupRequest) ([]string, error)
	SetGroupPhoto(ctx context.Context, jid string, jpeg []byte) (string, error)
	UpdateParticipants(ctx context.Context, jid, action string, participants []string) ([]*workerv1.ParticipantResult, error)
	ListJoinRequests(ctx context.Context, jid string) ([]*workerv1.JoinRequest, error)
	UpdateJoinRequests(ctx context.Context, jid string, approve bool, participants []string) ([]*workerv1.ParticipantResult, error)
	InviteLink(ctx context.Context, jid string, reset bool) (string, error)
	PreviewInvite(ctx context.Context, code string) (*workerv1.GroupInfo, error)
	JoinInvite(ctx context.Context, code string) (jid string, pending bool, err error)
	LeaveGroup(ctx context.Context, jid string) error
	LinkGroup(ctx context.Context, community, group string, unlink bool) error
	ListSubGroups(ctx context.Context, community string) ([]*workerv1.SubGroup, error)
}

var _ GroupAdmin = (*Whatsmeow)(nil)

// GroupsServer serves the Groups gRPC service over a Server's WAClient.
type GroupsServer struct {
	workerv1.UnimplementedGroupsServer
	s *Server
}

// NewGroupsServer returns the Groups service of s (register it next to s).
func NewGroupsServer(s *Server) *GroupsServer { return &GroupsServer{s: s} }

func (g *GroupsServer) admin() (GroupAdmin, error) {
	a, ok := g.s.wa.(GroupAdmin)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "groups not supported by this worker")
	}
	return a, nil
}

func (g *GroupsServer) ListGroups(ctx context.Context, _ *workerv1.ListGroupsRequest) (*workerv1.ListGroupsResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	list, err := a.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	return &workerv1.ListGroupsResponse{Groups: list}, nil
}

func (g *GroupsServer) GetGroup(ctx context.Context, req *workerv1.GetGroupRequest) (*workerv1.GroupInfo, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	return a.GetGroup(ctx, req.Jid)
}

func (g *GroupsServer) CreateGroup(ctx context.Context, req *workerv1.CreateGroupRequest) (*workerv1.CreateGroupResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	return a.CreateGroup(ctx, req)
}

func (g *GroupsServer) UpdateGroup(ctx context.Context, req *workerv1.UpdateGroupRequest) (*workerv1.UpdateGroupResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	applied, err := a.UpdateGroup(ctx, req)
	if err != nil {
		return nil, err
	}
	return &workerv1.UpdateGroupResponse{Applied: applied}, nil
}

func (g *GroupsServer) SetGroupPhoto(ctx context.Context, req *workerv1.SetGroupPhotoRequest) (*workerv1.SetGroupPhotoResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	id, err := a.SetGroupPhoto(ctx, req.Jid, req.Jpeg)
	if err != nil {
		return nil, err
	}
	return &workerv1.SetGroupPhotoResponse{PictureId: id}, nil
}

func (g *GroupsServer) UpdateParticipants(ctx context.Context, req *workerv1.UpdateParticipantsRequest) (*workerv1.UpdateParticipantsResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	res, err := a.UpdateParticipants(ctx, req.Jid, req.Action, req.Participants)
	if err != nil {
		return nil, err
	}
	return &workerv1.UpdateParticipantsResponse{Results: res}, nil
}

func (g *GroupsServer) ListJoinRequests(ctx context.Context, req *workerv1.ListJoinRequestsRequest) (*workerv1.ListJoinRequestsResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	list, err := a.ListJoinRequests(ctx, req.Jid)
	if err != nil {
		return nil, err
	}
	return &workerv1.ListJoinRequestsResponse{Requests: list}, nil
}

func (g *GroupsServer) UpdateJoinRequests(ctx context.Context, req *workerv1.UpdateJoinRequestsRequest) (*workerv1.UpdateParticipantsResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	res, err := a.UpdateJoinRequests(ctx, req.Jid, req.Approve, req.Participants)
	if err != nil {
		return nil, err
	}
	return &workerv1.UpdateParticipantsResponse{Results: res}, nil
}

func (g *GroupsServer) InviteLink(ctx context.Context, req *workerv1.InviteLinkRequest) (*workerv1.InviteLinkResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	link, err := a.InviteLink(ctx, req.Jid, req.Reset_)
	if err != nil {
		return nil, err
	}
	return &workerv1.InviteLinkResponse{Link: link}, nil
}

func (g *GroupsServer) PreviewInvite(ctx context.Context, req *workerv1.PreviewInviteRequest) (*workerv1.GroupInfo, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	return a.PreviewInvite(ctx, req.Code)
}

func (g *GroupsServer) JoinInvite(ctx context.Context, req *workerv1.JoinInviteRequest) (*workerv1.JoinInviteResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	jid, pending, err := a.JoinInvite(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	return &workerv1.JoinInviteResponse{Jid: jid, Pending: pending}, nil
}

func (g *GroupsServer) LeaveGroup(ctx context.Context, req *workerv1.LeaveGroupRequest) (*workerv1.LeaveGroupResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	if err := a.LeaveGroup(ctx, req.Jid); err != nil {
		return nil, err
	}
	return &workerv1.LeaveGroupResponse{}, nil
}

func (g *GroupsServer) LinkGroup(ctx context.Context, req *workerv1.LinkGroupRequest) (*workerv1.LinkGroupResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	if err := a.LinkGroup(ctx, req.Community, req.Group, req.Unlink); err != nil {
		return nil, err
	}
	return &workerv1.LinkGroupResponse{}, nil
}

func (g *GroupsServer) ListSubGroups(ctx context.Context, req *workerv1.ListSubGroupsRequest) (*workerv1.ListSubGroupsResponse, error) {
	a, err := g.admin()
	if err != nil {
		return nil, err
	}
	list, err := a.ListSubGroups(ctx, req.Community)
	if err != nil {
		return nil, err
	}
	return &workerv1.ListSubGroupsResponse{Groups: list}, nil
}

// groupAPI is the part of *whatsmeow.Client used for groups; tests replace it.
type groupAPI interface {
	CreateGroup(ctx context.Context, req whatsmeow.ReqCreateGroup) (*types.GroupInfo, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	SetGroupTopic(ctx context.Context, jid types.JID, previousID, newID, topic string) error
	SetGroupAnnounce(ctx context.Context, jid types.JID, announce bool) error
	SetGroupLocked(ctx context.Context, jid types.JID, locked bool) error
	SetGroupJoinApprovalMode(ctx context.Context, jid types.JID, mode bool) error
	SetGroupMemberAddMode(ctx context.Context, jid types.JID, mode types.GroupMemberAddMode) error
	SetDisappearingTimer(ctx context.Context, chat types.JID, timer time.Duration, settingTS time.Time) error
	SetGroupPhoto(ctx context.Context, jid types.JID, avatar []byte) (string, error)
	UpdateGroupParticipants(ctx context.Context, jid types.JID, changes []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error)
	GetGroupRequestParticipants(ctx context.Context, jid types.JID) ([]types.GroupParticipantRequest, error)
	UpdateGroupRequestParticipants(ctx context.Context, jid types.JID, changes []types.JID, action whatsmeow.ParticipantRequestChange) ([]types.GroupParticipant, error)
	GetGroupInviteLink(ctx context.Context, jid types.JID, reset bool) (string, error)
	GetGroupInfoFromLink(ctx context.Context, code string) (*types.GroupInfo, error)
	JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
	LeaveGroup(ctx context.Context, jid types.JID) error
	LinkGroup(ctx context.Context, parent, child types.JID) error
	UnlinkGroup(ctx context.Context, parent, child types.JID) error
	GetSubGroups(ctx context.Context, community types.JID) ([]*types.GroupLinkTarget, error)
}

var _ groupAPI = (*whatsmeow.Client)(nil)

func (w *Whatsmeow) groups() groupAPI {
	if w.gapi != nil {
		return w.gapi
	}
	return w.cli
}

// groupErr maps whatsmeow group errors onto the codes documented in groups.proto.
func groupErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrGroupNotFound), errors.Is(err, whatsmeow.ErrIQNotFound):
		return grpcErr(codes.NotFound, "group_not_found", "group not found")
	case errors.Is(err, whatsmeow.ErrNotInGroup), errors.Is(err, whatsmeow.ErrGroupInviteLinkUnauthorized),
		errors.Is(err, whatsmeow.ErrIQForbidden), errors.Is(err, whatsmeow.ErrIQNotAuthorized),
		errors.Is(err, whatsmeow.ErrIQNotAllowed):
		return grpcErr(codes.PermissionDenied, "forbidden", "not allowed: the account must be a member (or an admin) of the group")
	case errors.Is(err, whatsmeow.ErrInviteLinkRevoked), errors.Is(err, whatsmeow.ErrIQGone):
		return grpcErr(codes.NotFound, "invite_revoked", "the invite link was revoked")
	case errors.Is(err, whatsmeow.ErrInviteLinkInvalid):
		return grpcErr(codes.InvalidArgument, "invite_invalid", "the invite link is not valid")
	case errors.Is(err, whatsmeow.ErrInvalidImageFormat):
		return grpcErr(codes.InvalidArgument, "validation_failed", "the photo must be a JPEG image")
	case errors.Is(err, whatsmeow.ErrInvalidDisappearingTimer):
		return grpcErr(codes.InvalidArgument, "validation_failed", "unsupported disappearing messages timer")
	case errors.Is(err, whatsmeow.ErrIQNotAcceptable), errors.Is(err, whatsmeow.ErrIQBadRequest):
		return grpcErr(codes.InvalidArgument, "not_acceptable", "WhatsApp refused the request (value not accepted)")
	case errors.Is(err, whatsmeow.ErrIQLocked):
		return grpcErr(codes.PermissionDenied, "forbidden", "WhatsApp refused: the group is locked")
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit), errors.Is(err, whatsmeow.ErrIQResourceLimit):
		return grpcErr(codes.ResourceExhausted, "rate_limited", "WhatsApp rate limit reached; slow down")
	}
	return sendErr(err)
}

func groupJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil || jid.User == "" || jid.Server != types.GroupServer {
		return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "a group JID (…@g.us) is required")
	}
	return jid.ToNonAD(), nil
}

func participantJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
		return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "participants must be contact JIDs (…@s.whatsapp.net or …@lid)")
	}
	return jid.ToNonAD(), nil
}

// participantView returns the JID to report for a participant (phone number when known)
// and its LID.
func (w *Whatsmeow) participantView(p types.GroupParticipant) (jid, lid string) {
	pn := p.PhoneNumber
	if pn.IsEmpty() {
		pn = w.phoneJID(p.JID, types.EmptyJID, nil)
	}
	if !p.LID.IsEmpty() {
		lid = p.LID.ToNonAD().String()
	} else if p.JID.Server == types.HiddenUserServer {
		lid = p.JID.ToNonAD().String()
	}
	return pn.ToNonAD().String(), lid
}

// toGroupInfo converts group metadata with its participants.
func (w *Whatsmeow) toGroupInfo(gi *types.GroupInfo) *workerv1.GroupInfo {
	return w.convertGroup(gi, true)
}

// convertGroup converts group metadata; without participants it only counts them (each
// participant may cost a LID lookup).
func (w *Whatsmeow) convertGroup(gi *types.GroupInfo, withParticipants bool) *workerv1.GroupInfo {
	if gi == nil {
		return nil
	}
	out := &workerv1.GroupInfo{
		Jid: gi.JID.ToNonAD().String(), Name: gi.Name, Description: gi.Topic,
		Announce: gi.IsAnnounce, Locked: gi.IsLocked, ApprovalRequired: gi.IsJoinApprovalRequired,
		AdminsAddMembers: gi.MemberAddMode == types.GroupMemberAddModeAdmin,
		Community:        gi.IsParent, AnnouncementGroup: gi.IsDefaultSubGroup,
		ParticipantCount: int32(gi.ParticipantCount), LidAddressing: gi.AddressingMode == types.AddressingModeLID,
		Suspended: gi.Suspended,
	}
	if gi.IsEphemeral {
		out.EphemeralSeconds = gi.DisappearingTimer
	}
	if !gi.GroupCreated.IsZero() {
		out.CreatedMs = gi.GroupCreated.UnixMilli()
	}
	if !gi.NameSetAt.IsZero() {
		out.NameSetMs = gi.NameSetAt.UnixMilli()
	}
	if !gi.OwnerPN.IsEmpty() {
		out.Owner = gi.OwnerPN.ToNonAD().String()
	} else if !gi.OwnerJID.IsEmpty() {
		out.Owner = w.phoneJID(gi.OwnerJID, types.EmptyJID, nil).String()
	}
	if !gi.LinkedParentJID.IsEmpty() {
		out.Parent = gi.LinkedParentJID.ToNonAD().String()
	}
	if withParticipants {
		for _, p := range gi.Participants {
			jid, lid := w.participantView(p)
			out.Participants = append(out.Participants, &workerv1.GroupParticipant{Jid: jid, Lid: lid, Admin: p.IsAdmin, SuperAdmin: p.IsSuperAdmin})
		}
	}
	if out.ParticipantCount == 0 {
		out.ParticipantCount = int32(len(gi.Participants))
	}
	return out
}

func (w *Whatsmeow) ListGroups(ctx context.Context) ([]*workerv1.GroupInfo, error) {
	list, err := w.groups().GetJoinedGroups(ctx)
	if err != nil {
		return nil, groupErr(err)
	}
	out := make([]*workerv1.GroupInfo, 0, len(list))
	for _, gi := range list {
		if gi != nil && !gi.JID.IsEmpty() {
			out = append(out, w.convertGroup(gi, false))
		}
	}
	return out, nil
}

func (w *Whatsmeow) GetGroup(ctx context.Context, raw string) (*workerv1.GroupInfo, error) {
	jid, err := groupJID(raw)
	if err != nil {
		return nil, err
	}
	gi, err := w.groups().GetGroupInfo(ctx, jid)
	if err != nil {
		return nil, groupErr(err)
	}
	return w.toGroupInfo(gi), nil
}

// participantStatus names a participant outcome by WhatsApp's code. The codes are those
// WhatsApp clients document for group participant changes (best effort: unknown codes
// are reported as error_<code>).
func participantStatus(code int, action string) string {
	switch code {
	case 0, 200:
		return "ok"
	case 403:
		return "privacy_invite_required"
	case 408:
		return "recently_left"
	case 409:
		return "already_participant"
	case 404:
		if action == "add" {
			return "not_on_whatsapp"
		}
		return "not_participant"
	case 500:
		return "group_full"
	}
	return fmt.Sprintf("error_%d", code)
}

// matchResults pairs WhatsApp's per-participant answers with the requested participants
// (by phone number or LID); a requested participant without an answer is "unknown".
func (w *Whatsmeow) matchResults(requested []string, sent []types.JID, answers []types.GroupParticipant, action string) []*workerv1.ParticipantResult {
	idx := map[string]int{}
	out := make([]*workerv1.ParticipantResult, len(requested))
	for i, raw := range requested {
		out[i] = &workerv1.ParticipantResult{Participant: raw, Status: "unknown"}
		for _, j := range []types.JID{sent[i], w.phoneJID(sent[i], types.EmptyJID, nil)} {
			if !j.IsEmpty() {
				idx[j.ToNonAD().String()] = i
			}
		}
		if req, err := types.ParseJID(raw); err == nil {
			idx[req.ToNonAD().String()] = i
		}
	}
	for _, a := range answers {
		i, ok := -1, false
		for _, j := range []types.JID{a.JID, a.PhoneNumber, a.LID} {
			if j.IsEmpty() {
				continue
			}
			if i, ok = idx[j.ToNonAD().String()]; ok {
				break
			}
		}
		if !ok {
			continue
		}
		r := out[i]
		r.Jid, _ = w.participantView(a)
		r.Code = int32(a.Error)
		r.Status = participantStatus(a.Error, action)
		if a.AddRequest != nil {
			r.InviteCode = a.AddRequest.Code
			if !a.AddRequest.Expiration.IsZero() {
				r.InviteExpiresMs = a.AddRequest.Expiration.UnixMilli()
			}
		}
	}
	return out
}

func parseParticipants(raws []string) ([]types.JID, error) {
	if len(raws) == 0 {
		return nil, grpcErr(codes.InvalidArgument, "validation_failed", "at least one participant is required")
	}
	out := make([]types.JID, len(raws))
	for i, raw := range raws {
		j, err := participantJID(raw)
		if err != nil {
			return nil, err
		}
		out[i] = j
	}
	return out, nil
}

func (w *Whatsmeow) CreateGroup(ctx context.Context, req *workerv1.CreateGroupRequest) (*workerv1.CreateGroupResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, grpcErr(codes.InvalidArgument, "validation_failed", "name is required")
	}
	var parts []types.JID
	if len(req.Participants) > 0 {
		var err error
		if parts, err = parseParticipants(req.Participants); err != nil {
			return nil, err
		}
	}
	r := whatsmeow.ReqCreateGroup{Name: req.Name, Participants: parts}
	r.IsAnnounce, r.IsLocked, r.IsJoinApprovalRequired = req.Announce, req.Locked, req.ApprovalRequired
	if req.EphemeralSeconds > 0 {
		r.IsEphemeral, r.DisappearingTimer = true, req.EphemeralSeconds
	}
	if req.AdminsAddMembers {
		r.MemberAddMode = types.GroupMemberAddModeAdmin
	}
	r.IsParent = req.Community
	if req.Parent != "" {
		parent, err := groupJID(req.Parent)
		if err != nil {
			return nil, err
		}
		r.LinkedParentJID = parent
	}
	gi, err := w.groups().CreateGroup(ctx, r)
	if err != nil {
		return nil, groupErr(err)
	}
	return &workerv1.CreateGroupResponse{
		Group: w.toGroupInfo(gi), Results: w.matchResults(req.Participants, parts, gi.Participants, "add"),
	}, nil
}

func (w *Whatsmeow) UpdateGroup(ctx context.Context, req *workerv1.UpdateGroupRequest) ([]string, error) {
	jid, err := groupJID(req.Jid)
	if err != nil {
		return nil, err
	}
	g := w.groups()
	var applied []string
	step := func(name string, fn func() error) error {
		if err := fn(); err != nil {
			return groupErr(err)
		}
		applied = append(applied, name)
		return nil
	}
	steps := []struct {
		name string
		set  bool
		fn   func() error
	}{
		{"name", req.Name != nil, func() error { return g.SetGroupName(ctx, jid, req.GetName()) }},
		{"description", req.Description != nil, func() error {
			// SetGroupTopic would look the previous topic up itself, wrapping its error
			// with %v (errors.Is would no longer see not-found / forbidden).
			gi, err := g.GetGroupInfo(ctx, jid)
			if err != nil {
				return err
			}
			prev := ""
			if gi != nil {
				prev = gi.TopicID
			}
			return g.SetGroupTopic(ctx, jid, prev, "", req.GetDescription())
		}},
		{"announce", req.Announce != nil, func() error { return g.SetGroupAnnounce(ctx, jid, req.GetAnnounce()) }},
		{"locked", req.Locked != nil, func() error { return g.SetGroupLocked(ctx, jid, req.GetLocked()) }},
		{"approval_required", req.ApprovalRequired != nil, func() error { return g.SetGroupJoinApprovalMode(ctx, jid, req.GetApprovalRequired()) }},
		{"admins_add_members", req.AdminsAddMembers != nil, func() error {
			mode := types.GroupMemberAddModeAllMember
			if req.GetAdminsAddMembers() {
				mode = types.GroupMemberAddModeAdmin
			}
			return g.SetGroupMemberAddMode(ctx, jid, mode)
		}},
		{"ephemeral_seconds", req.EphemeralSeconds != nil, func() error {
			return g.SetDisappearingTimer(ctx, jid, time.Duration(req.GetEphemeralSeconds())*time.Second, time.Time{})
		}},
	}
	for _, s := range steps {
		if !s.set {
			continue
		}
		if err := step(s.name, s.fn); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

func (w *Whatsmeow) SetGroupPhoto(ctx context.Context, raw string, jpeg []byte) (string, error) {
	jid, err := groupJID(raw)
	if err != nil {
		return "", err
	}
	if len(jpeg) == 0 {
		jpeg = nil // removes the photo
	}
	id, err := w.groups().SetGroupPhoto(ctx, jid, jpeg)
	if err != nil {
		return "", groupErr(err)
	}
	return id, nil
}

func (w *Whatsmeow) UpdateParticipants(ctx context.Context, raw, action string, participants []string) ([]*workerv1.ParticipantResult, error) {
	jid, err := groupJID(raw)
	if err != nil {
		return nil, err
	}
	var change whatsmeow.ParticipantChange
	switch action {
	case "add":
		change = whatsmeow.ParticipantChangeAdd
	case "remove":
		change = whatsmeow.ParticipantChangeRemove
	case "promote":
		change = whatsmeow.ParticipantChangePromote
	case "demote":
		change = whatsmeow.ParticipantChangeDemote
	default:
		return nil, grpcErr(codes.InvalidArgument, "validation_failed", "action must be add, remove, promote or demote")
	}
	parts, err := parseParticipants(participants)
	if err != nil {
		return nil, err
	}
	if change != whatsmeow.ParticipantChangeAdd {
		parts = w.memberJIDs(ctx, jid, parts)
	}
	answers, err := w.groups().UpdateGroupParticipants(ctx, jid, parts, change)
	if err != nil {
		return nil, groupErr(err)
	}
	return w.matchResults(participants, parts, answers, action), nil
}

// memberJIDs names existing members exactly as the group lists them (phone number or
// LID, from the server's participant list); one the list does not have falls back to
// the LID store conversion.
func (w *Whatsmeow) memberJIDs(ctx context.Context, group types.JID, parts []types.JID) []types.JID {
	out := make([]types.JID, len(parts))
	gi, err := w.groups().GetGroupInfo(ctx, group)
	for i, p := range parts {
		out[i] = p
		found := false
		if err == nil && gi != nil {
			for _, m := range gi.Participants {
				if sameUser(p, m.JID) || sameUser(p, m.PhoneNumber) || sameUser(p, m.LID) {
					out[i], found = m.JID.ToNonAD(), true
					break
				}
			}
		}
		if !found {
			out[i] = w.groupParticipant(ctx, group, p)
		}
	}
	return out
}

// requesterJIDs names join requesters exactly as the pending request list does.
func (w *Whatsmeow) requesterJIDs(ctx context.Context, group types.JID, parts []types.JID) []types.JID {
	out := make([]types.JID, len(parts))
	reqs, err := w.groups().GetGroupRequestParticipants(ctx, group)
	for i, p := range parts {
		out[i] = p
		if err != nil {
			continue
		}
		for _, r := range reqs {
			if sameUser(p, r.JID) || sameUser(p, w.phoneJID(r.JID, types.EmptyJID, nil)) {
				out[i] = r.JID.ToNonAD()
				break
			}
		}
	}
	return out
}

func sameUser(a, b types.JID) bool {
	return !a.IsEmpty() && !b.IsEmpty() && a.User == b.User && a.Server == b.Server
}

func (w *Whatsmeow) ListJoinRequests(ctx context.Context, raw string) ([]*workerv1.JoinRequest, error) {
	jid, err := groupJID(raw)
	if err != nil {
		return nil, err
	}
	list, err := w.groups().GetGroupRequestParticipants(ctx, jid)
	if err != nil {
		return nil, groupErr(err)
	}
	out := make([]*workerv1.JoinRequest, 0, len(list))
	for _, r := range list {
		jr := &workerv1.JoinRequest{Jid: w.phoneJID(r.JID, types.EmptyJID, nil).String()}
		if r.JID.Server == types.HiddenUserServer {
			jr.Lid = r.JID.ToNonAD().String()
		}
		if !r.RequestedAt.IsZero() {
			jr.RequestedMs = r.RequestedAt.UnixMilli()
		}
		out = append(out, jr)
	}
	return out, nil
}

func (w *Whatsmeow) UpdateJoinRequests(ctx context.Context, raw string, approve bool, participants []string) ([]*workerv1.ParticipantResult, error) {
	jid, err := groupJID(raw)
	if err != nil {
		return nil, err
	}
	parts, err := parseParticipants(participants)
	if err != nil {
		return nil, err
	}
	parts = w.requesterJIDs(ctx, jid, parts)
	action, name := whatsmeow.ParticipantChangeReject, "reject"
	if approve {
		action, name = whatsmeow.ParticipantChangeApprove, "approve"
	}
	answers, err := w.groups().UpdateGroupRequestParticipants(ctx, jid, parts, action)
	if err != nil {
		return nil, groupErr(err)
	}
	return w.matchResults(participants, parts, answers, name), nil
}

func (w *Whatsmeow) InviteLink(ctx context.Context, raw string, reset bool) (string, error) {
	jid, err := groupJID(raw)
	if err != nil {
		return "", err
	}
	link, err := w.groups().GetGroupInviteLink(ctx, jid, reset)
	if err != nil {
		return "", groupErr(err)
	}
	return link, nil
}

// inviteCode accepts a bare code or a chat.whatsapp.com link.
func inviteCode(raw string) (string, error) {
	code := strings.TrimSpace(raw)
	for _, p := range []string{"https://chat.whatsapp.com/invite/", "https://chat.whatsapp.com/", "http://chat.whatsapp.com/", "chat.whatsapp.com/"} {
		code = strings.TrimPrefix(code, p)
	}
	code, _, _ = strings.Cut(code, "?")
	if code == "" || len(code) > 64 || strings.ContainsAny(code, "/ ") {
		return "", grpcErr(codes.InvalidArgument, "invite_invalid", "the invite link is not valid")
	}
	return code, nil
}

func (w *Whatsmeow) PreviewInvite(ctx context.Context, raw string) (*workerv1.GroupInfo, error) {
	code, err := inviteCode(raw)
	if err != nil {
		return nil, err
	}
	gi, err := w.groups().GetGroupInfoFromLink(ctx, code)
	if err != nil {
		return nil, groupErr(err)
	}
	return w.toGroupInfo(gi), nil
}

func (w *Whatsmeow) JoinInvite(ctx context.Context, raw string) (string, bool, error) {
	code, err := inviteCode(raw)
	if err != nil {
		return "", false, err
	}
	g := w.groups()
	// The preview tells whether joining needs an admin's approval (the join answer only
	// names the group either way).
	gi, err := g.GetGroupInfoFromLink(ctx, code)
	if err != nil {
		return "", false, groupErr(err)
	}
	jid, err := g.JoinGroupWithLink(ctx, code)
	if err != nil {
		return "", false, groupErr(err)
	}
	if jid.IsEmpty() {
		jid = gi.JID
	}
	return jid.ToNonAD().String(), gi.IsJoinApprovalRequired, nil
}

func (w *Whatsmeow) LeaveGroup(ctx context.Context, raw string) error {
	jid, err := groupJID(raw)
	if err != nil {
		return err
	}
	if err := w.groups().LeaveGroup(ctx, jid); err != nil {
		return groupErr(err)
	}
	return nil
}

func (w *Whatsmeow) LinkGroup(ctx context.Context, community, group string, unlink bool) error {
	parent, err := groupJID(community)
	if err != nil {
		return err
	}
	child, err := groupJID(group)
	if err != nil {
		return err
	}
	if unlink {
		err = w.groups().UnlinkGroup(ctx, parent, child)
	} else {
		err = w.groups().LinkGroup(ctx, parent, child)
	}
	if err != nil {
		return groupErr(err)
	}
	return nil
}

func (w *Whatsmeow) ListSubGroups(ctx context.Context, community string) ([]*workerv1.SubGroup, error) {
	parent, err := groupJID(community)
	if err != nil {
		return nil, err
	}
	list, err := w.groups().GetSubGroups(ctx, parent)
	if err != nil {
		return nil, groupErr(err)
	}
	out := make([]*workerv1.SubGroup, 0, len(list))
	for _, g := range list {
		if g == nil {
			continue
		}
		out = append(out, &workerv1.SubGroup{Jid: g.JID.ToNonAD().String(), Name: g.Name, AnnouncementGroup: g.IsDefaultSubGroup})
	}
	return out, nil
}

// ---- events ----

func (w *Whatsmeow) jids(list []types.JID) []string {
	out := make([]string, 0, len(list))
	for _, j := range list {
		out = append(out, w.phoneJID(j, types.EmptyJID, nil).String())
	}
	return out
}

func (w *Whatsmeow) actor(sender, senderPN *types.JID) string {
	if senderPN != nil && !senderPN.IsEmpty() {
		return senderPN.ToNonAD().String()
	}
	if sender != nil && !sender.IsEmpty() {
		return w.phoneJID(*sender, types.EmptyJID, nil).String()
	}
	return ""
}

// joinRequesters collects the JIDs named in a join request notification (unknown to
// whatsmeow): the child's own jid attribute and its children's.
func (w *Whatsmeow) joinRequesters(n *waBinary.Node) []string {
	var out []string
	add := func(m *waBinary.Node) {
		if j, ok := m.Attrs["jid"].(types.JID); ok && !j.IsEmpty() && j.Server != types.GroupServer {
			out = append(out, w.phoneJID(j, types.EmptyJID, nil).String())
		}
	}
	add(n)
	for _, c := range n.GetChildren() {
		add(&c)
	}
	return out
}

func (w *Whatsmeow) toGroupUpdate(e *events.GroupInfo) *workerv1.GroupUpdate {
	u := &workerv1.GroupUpdate{
		Group: e.JID.ToNonAD().String(), Actor: w.actor(e.Sender, e.SenderPN),
		Joined: w.jids(e.Join), Left: w.jids(e.Leave), Promoted: w.jids(e.Promote), Demoted: w.jids(e.Demote),
		JoinReason: e.JoinReason, InviteLinkReset: e.NewInviteLink != nil,
	}
	if !e.Timestamp.IsZero() {
		u.TimestampMs = e.Timestamp.UnixMilli()
	}
	if e.Name != nil {
		u.Name = &e.Name.Name
	}
	if e.Topic != nil {
		t := e.Topic.Topic
		if e.Topic.TopicDeleted {
			t = ""
		}
		u.Description = &t
	}
	if e.Announce != nil {
		u.Announce = &e.Announce.IsAnnounce
	}
	if e.Locked != nil {
		u.Locked = &e.Locked.IsLocked
	}
	// whatsmeow reports any approval mode change as "required" (it drops the state).
	u.ApprovalModeChanged = e.MembershipApprovalMode != nil
	u.Suspended, u.Unsuspended = e.Suspended, e.Unsuspended
	if e.Ephemeral != nil {
		s := uint32(0)
		if e.Ephemeral.IsEphemeral {
			s = e.Ephemeral.DisappearingTimer
		}
		u.EphemeralSeconds = &s
	}
	if e.Delete != nil && e.Delete.Deleted {
		u.Deleted = true
	}
	if e.Link != nil {
		u.LinkedGroup = e.Link.Group.JID.ToNonAD().String()
	}
	if e.Unlink != nil {
		u.UnlinkedGroup = e.Unlink.Group.JID.ToNonAD().String()
	}
	for _, n := range e.UnknownChanges {
		if n == nil {
			continue
		}
		// Not parsed by whatsmeow; tag names as other WhatsApp clients document them.
		switch n.Tag {
		case "created_membership_requests":
			reqs := w.joinRequesters(n)
			if len(reqs) == 0 && u.Actor != "" {
				reqs = []string{u.Actor}
			}
			u.JoinRequests = append(u.JoinRequests, reqs...)
		case "revoked_membership_requests":
			u.JoinRequestsRevoked = append(u.JoinRequestsRevoked, w.joinRequesters(n)...)
		default:
			u.Unknown = append(u.Unknown, n.Tag)
		}
	}
	return u
}

// onGroup handles group events; it reports whether evt was one.
func (w *Whatsmeow) onGroup(evt any) bool {
	var u *workerv1.GroupUpdate
	switch e := evt.(type) {
	case *events.GroupInfo:
		if e.JID.IsEmpty() {
			return true
		}
		u = w.toGroupUpdate(e)
	case *events.JoinedGroup:
		if e.JID.IsEmpty() {
			return true
		}
		u = &workerv1.GroupUpdate{Group: e.JID.ToNonAD().String(), Actor: w.actor(e.Sender, e.SenderPN),
			JoinedSelf: true, Info: w.convertGroup(&e.GroupInfo, false), JoinReason: e.Reason, TimestampMs: time.Now().UnixMilli()}
	case *events.Picture:
		if e.JID.Server != types.GroupServer {
			return false
		}
		u = &workerv1.GroupUpdate{Group: e.JID.ToNonAD().String(), PhotoChanged: !e.Remove, PhotoRemoved: e.Remove}
		if !e.Author.IsEmpty() {
			u.Actor = w.phoneJID(e.Author, types.EmptyJID, nil).String()
		}
		if !e.Timestamp.IsZero() {
			u.TimestampMs = e.Timestamp.UnixMilli()
		}
	default:
		return false
	}
	if u.TimestampMs == 0 {
		u.TimestampMs = time.Now().UnixMilli()
	}
	w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Group{Group: u}})
	return true
}
