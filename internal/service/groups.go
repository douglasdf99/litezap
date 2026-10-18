package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GroupWorkers gives the Groups client of a running worker (manager.Manager).
type GroupWorkers interface {
	Groups(instanceID string) (workerv1.GroupsClient, error)
}

// Groups manages WhatsApp groups and communities (marco 8). Management calls are
// synchronous (rpcTimeout). The actions that look like spam to WhatsApp — adding
// participants, creating groups and joining by link — go through per-instance quotas
// (settings.groups) and are refused while the operator suspends sending; participants to
// add must be known to the instance (see checkAddable).
type Groups struct {
	Store        *store.Store
	Workers      GroupWorkers
	Resolver     *lookup.Resolver
	Sending      *Sending       // nil: no operator suspension
	Fetcher      *media.Fetcher // photo URLs; nil: only base64
	Defaults     settings.Settings
	DefaultProxy string
	// Now is the clock of the quotas (tests); nil means time.Now.
	Now func() time.Time

	quota groupQuota
}

// Limits of one request.
const (
	maxAddBatch       = 10 // participants added per request
	maxParticipantOps = 50 // participants removed/promoted/demoted/approved per request
	maxCreateMembers  = 10 // initial participants of a new group (also adds)
	maxGroupName      = 100
	maxGroupDesc      = 2048
	maxGroupPhoto     = 2 << 20
)

// ParticipantView is a member of a group.
type ParticipantView struct {
	JID        string `json:"jid"`
	LID        string `json:"lid,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Admin      bool   `json:"admin"`
	SuperAdmin bool   `json:"super_admin,omitempty"`
}

// GroupSettings are a group's switches.
type GroupSettings struct {
	Announce         bool   `json:"announce"`           // only admins send messages
	Locked           bool   `json:"locked"`             // only admins edit the group info
	ApprovalRequired bool   `json:"approval_required"`  // admins approve join requests
	AdminsAddMembers bool   `json:"admins_add_members"` // only admins add members
	EphemeralSeconds uint32 `json:"ephemeral_seconds"`  // disappearing messages (0: off)
}

// GroupView is a group's metadata.
type GroupView struct {
	JID               string            `json:"jid"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	Owner             string            `json:"owner,omitempty"`
	CreatedAt         *time.Time        `json:"created_at,omitempty"`
	Community         bool              `json:"community,omitempty"`
	Parent            string            `json:"parent,omitempty"`
	AnnouncementGroup bool              `json:"announcement_group,omitempty"`
	ParticipantCount  int               `json:"participant_count"`
	LIDAddressing     bool              `json:"lid_addressing,omitempty"`
	Suspended         bool              `json:"suspended,omitempty"`
	Settings          GroupSettings     `json:"settings"`
	Participants      []ParticipantView `json:"participants,omitempty"`
}

// ParticipantResultView is the outcome for one requested participant.
type ParticipantResultView struct {
	Participant     string     `json:"participant"`
	JID             string     `json:"jid,omitempty"`
	Status          string     `json:"status"`
	Code            int        `json:"code,omitempty"`
	InviteCode      string     `json:"invite_code,omitempty"`
	InviteExpiresAt *time.Time `json:"invite_expires_at,omitempty"`
}

// JoinRequestView is a pending request to join a group.
type JoinRequestView struct {
	JID         string     `json:"jid"`
	LID         string     `json:"lid,omitempty"`
	RequestedAt *time.Time `json:"requested_at,omitempty"`
}

// SubGroupView is a group of a community.
type SubGroupView struct {
	JID               string `json:"jid"`
	Name              string `json:"name"`
	AnnouncementGroup bool   `json:"announcement_group,omitempty"`
}

// GroupSettingsInput sets switches; nil fields are left as they are.
type GroupSettingsInput struct {
	Announce         *bool   `json:"announce,omitempty"`
	Locked           *bool   `json:"locked,omitempty"`
	ApprovalRequired *bool   `json:"approval_required,omitempty"`
	AdminsAddMembers *bool   `json:"admins_add_members,omitempty"`
	EphemeralSeconds *uint32 `json:"ephemeral_seconds,omitempty"`
}

// CreateGroupInput creates a group (or a community, or a group inside one).
type CreateGroupInput struct {
	Name         string              `json:"name"`
	Participants []string            `json:"participants,omitempty"`
	Community    bool                `json:"-"` // set by the communities route
	Parent       string              `json:"parent,omitempty"`
	Description  string              `json:"description,omitempty"`
	Settings     *GroupSettingsInput `json:"settings,omitempty"`
}

// CreateGroupResult is the new group and the outcome per initial participant.
type CreateGroupResult struct {
	Group   GroupView               `json:"group"`
	Results []ParticipantResultView `json:"results"`
	// Warning: a step after creation failed (description); the group exists.
	Warning string `json:"warning,omitempty"`
}

// UpdateGroupInput changes name, description and settings; nil fields stay.
type UpdateGroupInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	GroupSettingsInput
}

// JoinResult is the outcome of joining by invite link.
type JoinResult struct {
	JID     string `json:"jid"`
	Pending bool   `json:"pending"` // the group requires approval: a request was sent
}

func (g *Groups) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// groupErr renders a Groups RPC failure (codes in groups.proto) as an API error.
func groupErr(err error) error {
	switch {
	case errors.Is(err, manager.ErrNoGroups):
		return apierr.New(http.StatusNotImplemented, "not_implemented", "this worker does not support groups", false)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() == codes.OK {
		return mapErr(err)
	}
	code, detail, _ := strings.Cut(st.Message(), ": ")
	switch {
	case st.Code() == codes.NotFound && code == "group_not_found":
		return apierr.New(http.StatusNotFound, "group_not_found", "group not found (or the instance is not in it)", false)
	case st.Code() == codes.NotFound && code == "invite_revoked":
		return apierr.New(http.StatusGone, "invite_revoked", "the invite link was revoked", false)
	case st.Code() == codes.InvalidArgument && code == "invite_invalid":
		return apierr.New(http.StatusBadRequest, "invite_invalid", "the invite link is not valid", false)
	case st.Code() == codes.PermissionDenied:
		return apierr.New(http.StatusForbidden, "group_forbidden", redact(detail), false)
	case st.Code() == codes.ResourceExhausted:
		return apierr.New(http.StatusTooManyRequests, "rate_limited", "WhatsApp rate limit reached; slow down", true)
	case st.Code() == codes.Unimplemented:
		return apierr.New(http.StatusNotImplemented, "not_implemented", "this worker does not support groups", false)
	}
	return mapErr(err)
}

func (g *Groups) client(ctx context.Context, instanceID string) (workerv1.GroupsClient, error) {
	if _, err := g.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	cli, err := g.Workers.Groups(instanceID)
	if err != nil {
		return nil, groupErr(err)
	}
	return cli, nil
}

func (g *Groups) checkSending(instanceID string) error {
	if g.Sending == nil {
		return nil
	}
	return g.Sending.Check(instanceID)
}

func (g *Groups) limits(ctx context.Context, instanceID string) (settings.Groups, error) {
	in, err := g.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return settings.Groups{}, mapErr(err)
	}
	set, err := settings.Effective(g.Defaults, g.DefaultProxy, in.SettingsJSON)
	if err != nil {
		return settings.Groups{}, err
	}
	return set.Groups.Limits(), nil
}

// GroupJID normalizes a group id: "<id>@g.us" or the bare id.
func GroupJID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	user, server, ok := strings.Cut(raw, "@")
	if ok && server != "g.us" {
		return "", apierr.Validation("a group JID ends with @g.us")
	}
	if user == "" || strings.Trim(user, "0123456789-") != "" {
		return "", apierr.Validation("invalid group JID")
	}
	return user + "@g.us", nil
}

// participantJIDs normalizes participants (phone numbers or contact JIDs), dropping
// duplicates.
func participantJIDs(raws []string, maxN int) ([]string, error) {
	if len(raws) == 0 {
		return nil, apierr.Validation("participants must list at least one phone number or JID")
	}
	if len(raws) > maxN {
		return nil, apierr.Validation("too many participants in one request")
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range raws {
		jid, err := ChatJID(raw)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(jid, "@g.us") {
			return nil, apierr.Validation("a group cannot be a participant")
		}
		if !seen[jid] {
			seen[jid] = true
			out = append(out, jid)
		}
	}
	return out, nil
}

func ms(v int64) *time.Time {
	if v <= 0 {
		return nil
	}
	t := time.UnixMilli(v).UTC()
	return &t
}

func phoneOf(jid string) string {
	if user, server, ok := strings.Cut(jid, "@"); ok && server == "s.whatsapp.net" {
		return user
	}
	return ""
}

func groupView(gi *workerv1.GroupInfo, withParticipants bool) GroupView {
	v := GroupView{
		JID: gi.Jid, Name: gi.Name, Description: gi.Description, Owner: gi.Owner, CreatedAt: ms(gi.CreatedMs),
		Community: gi.Community, Parent: gi.Parent, AnnouncementGroup: gi.AnnouncementGroup,
		ParticipantCount: int(gi.ParticipantCount), LIDAddressing: gi.LidAddressing, Suspended: gi.Suspended,
		Settings: GroupSettings{Announce: gi.Announce, Locked: gi.Locked, ApprovalRequired: gi.ApprovalRequired,
			AdminsAddMembers: gi.AdminsAddMembers, EphemeralSeconds: gi.EphemeralSeconds},
	}
	if withParticipants {
		v.Participants = []ParticipantView{}
		for _, p := range gi.Participants {
			v.Participants = append(v.Participants, ParticipantView{JID: p.Jid, LID: p.Lid, Phone: phoneOf(p.Jid), Admin: p.Admin, SuperAdmin: p.SuperAdmin})
		}
	}
	return v
}

func resultViews(rs []*workerv1.ParticipantResult) []ParticipantResultView {
	out := make([]ParticipantResultView, 0, len(rs))
	for _, r := range rs {
		out = append(out, ParticipantResultView{Participant: r.Participant, JID: r.Jid, Status: r.Status,
			Code: int(r.Code), InviteCode: r.InviteCode, InviteExpiresAt: ms(r.InviteExpiresMs)})
	}
	return out
}

func groupCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, rpcTimeout)
}

// List returns the groups the instance is in (communities only with communities=true),
// without participants.
func (g *Groups) List(ctx context.Context, instanceID string, communities bool) ([]GroupView, error) {
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	res, err := cli.ListGroups(c, &workerv1.ListGroupsRequest{})
	if err != nil {
		return nil, groupErr(err)
	}
	out := []GroupView{}
	for _, gi := range res.Groups {
		if communities && !gi.Community {
			continue
		}
		out = append(out, groupView(gi, false))
	}
	return out, nil
}

// Get returns a group's metadata with its participants.
func (g *Groups) Get(ctx context.Context, instanceID, groupRaw string) (GroupView, error) {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return GroupView{}, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return GroupView{}, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	gi, err := cli.GetGroup(c, &workerv1.GetGroupRequest{Jid: jid})
	if err != nil {
		return GroupView{}, groupErr(err)
	}
	return groupView(gi, true), nil
}

func validEphemeral(s uint32) bool {
	switch s {
	case 0, 86400, 7 * 86400, 90 * 86400:
		return true
	}
	return false
}

func validateSettings(in *GroupSettingsInput) error {
	if in != nil && in.EphemeralSeconds != nil && !validEphemeral(*in.EphemeralSeconds) {
		return apierr.Validation("ephemeral_seconds must be 0, 86400 (24h), 604800 (7 days) or 7776000 (90 days)")
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return apierr.Validation("name is required")
	}
	if utf8.RuneCountInString(name) > maxGroupName {
		return apierr.Validation("name is limited to 100 characters")
	}
	return nil
}

// addable is the outcome of checkAddable: the JIDs to send, and results for the rest.
type addable struct {
	send    []string // participant JIDs to send to WhatsApp
	request []string // the participant as requested, for each send entry
	skipped []ParticipantResultView
}

// checkAddable keeps only participants the instance knows: a chat in its log, or (phone
// numbers) a WhatsApp account confirmed by the number lookup, which spends the
// check-numbers quota. Adding strangers in bulk is the fastest way to get a number
// banned; LIDs without a chat cannot be checked and are refused.
func (g *Groups) checkAddable(ctx context.Context, instanceID string, jids []string) (addable, error) {
	var a addable
	var lookupPhones []string
	for _, jid := range jids {
		if _, err := g.Store.GetChat(ctx, instanceID, jid); err == nil {
			a.send, a.request = append(a.send, jid), append(a.request, jid)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return a, err
		}
		if strings.HasSuffix(jid, "@lid") {
			a.skipped = append(a.skipped, ParticipantResultView{Participant: jid, Status: "unknown_contact"})
			continue
		}
		lookupPhones = append(lookupPhones, phoneOf(jid))
	}
	if len(lookupPhones) == 0 {
		return a, nil
	}
	if g.Resolver == nil {
		for _, p := range lookupPhones {
			a.skipped = append(a.skipped, ParticipantResultView{Participant: p + "@s.whatsapp.net", Status: "unknown_contact"})
		}
		return a, nil
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	res, err := g.Resolver.Resolve(c, instanceID, lookupPhones, lookup.ForCheck)
	if errors.Is(err, lookup.ErrRateLimited) {
		return a, apierr.New(http.StatusTooManyRequests, "rate_limited",
			"number lookup quota reached while checking participants: "+err.Error(), true)
	}
	if err != nil {
		return a, mapErr(err)
	}
	for _, r := range res {
		requested := r.Phone + "@s.whatsapp.net"
		if !r.Found {
			a.skipped = append(a.skipped, ParticipantResultView{Participant: requested, Status: "not_on_whatsapp"})
			continue
		}
		jid := r.JID
		if jid == "" {
			jid = requested
		}
		a.send, a.request = append(a.send, jid), append(a.request, requested)
	}
	return a, nil
}

// mergeResults returns one result per requested participant, in order: the skipped
// ones from a, the rest from the worker (which echoes the JID it was sent).
func mergeResults(order []string, a addable, sent []*workerv1.ParticipantResult) []ParticipantResultView {
	reqOf := map[string]string{}
	for i, s := range a.send {
		reqOf[s] = a.request[i]
	}
	byReq := map[string]ParticipantResultView{}
	for _, s := range a.skipped {
		byReq[s.Participant] = s
	}
	for _, r := range resultViews(sent) {
		if req, ok := reqOf[r.Participant]; ok {
			r.Participant = req
		}
		byReq[r.Participant] = r
	}
	out := make([]ParticipantResultView, 0, len(order))
	for _, p := range order {
		if r, ok := byReq[p]; ok {
			out = append(out, r)
		} else {
			out = append(out, ParticipantResultView{Participant: p, Status: "unknown"})
		}
	}
	return out
}

// Create creates a group (Community: a community) with optional initial participants.
// The creation counts against max_creates_per_day and the participants against the add
// quotas; participants follow the same rules as Participants("add").
func (g *Groups) Create(ctx context.Context, instanceID string, in CreateGroupInput) (CreateGroupResult, error) {
	if err := validateName(in.Name); err != nil {
		return CreateGroupResult{}, err
	}
	if err := validateSettings(in.Settings); err != nil {
		return CreateGroupResult{}, err
	}
	if utf8.RuneCountInString(in.Description) > maxGroupDesc {
		return CreateGroupResult{}, apierr.Validation("description is limited to 2048 characters")
	}
	var parent string
	if in.Parent != "" {
		if in.Community {
			return CreateGroupResult{}, apierr.Validation("a community cannot have a parent")
		}
		p, err := GroupJID(in.Parent)
		if err != nil {
			return CreateGroupResult{}, err
		}
		parent = p
	}
	var jids []string
	if len(in.Participants) > 0 {
		var err error
		if jids, err = participantJIDs(in.Participants, maxCreateMembers); err != nil {
			return CreateGroupResult{}, err
		}
	}
	if err := g.checkSending(instanceID); err != nil {
		return CreateGroupResult{}, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return CreateGroupResult{}, err
	}
	lim, err := g.limits(ctx, instanceID)
	if err != nil {
		return CreateGroupResult{}, err
	}
	var a addable
	if len(jids) > 0 {
		if a, err = g.checkAddable(ctx, instanceID, jids); err != nil {
			return CreateGroupResult{}, err
		}
	}
	res, err := g.quota.reserve(ctx, g.Store, instanceID, g.now(), lim, quotaCreate, 1, len(a.send))
	if err != nil {
		return CreateGroupResult{}, err
	}
	req := &workerv1.CreateGroupRequest{Name: in.Name, Participants: a.send, Community: in.Community, Parent: parent}
	if s := in.Settings; s != nil {
		req.Announce, req.Locked = s.Announce != nil && *s.Announce, s.Locked != nil && *s.Locked
		req.ApprovalRequired = s.ApprovalRequired != nil && *s.ApprovalRequired
		req.AdminsAddMembers = s.AdminsAddMembers != nil && *s.AdminsAddMembers
		if s.EphemeralSeconds != nil {
			req.EphemeralSeconds = *s.EphemeralSeconds
		}
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	created, err := cli.CreateGroup(c, req)
	if err != nil {
		g.quota.refund(ctx, g.Store, instanceID, res)
		return CreateGroupResult{}, groupErr(err)
	}
	out := CreateGroupResult{Group: groupView(created.Group, true), Results: mergeResults(jids, a, created.Results)}
	if in.Description != "" {
		d := in.Description
		if _, err := cli.UpdateGroup(c, &workerv1.UpdateGroupRequest{Jid: created.Group.Jid, Description: &d}); err != nil {
			out.Warning = "group created, but setting the description failed: " + groupErr(err).Error()
		} else {
			out.Group.Description = d
		}
	}
	return out, nil
}

// Update changes the group's name, description and settings (applied in that order; on
// a failure the earlier changes stay) and returns the group as it is afterwards.
func (g *Groups) Update(ctx context.Context, instanceID, groupRaw string, in UpdateGroupInput) (GroupView, error) {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return GroupView{}, err
	}
	if in.Name != nil {
		if err := validateName(*in.Name); err != nil {
			return GroupView{}, err
		}
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > maxGroupDesc {
		return GroupView{}, apierr.Validation("description is limited to 2048 characters")
	}
	if err := validateSettings(&in.GroupSettingsInput); err != nil {
		return GroupView{}, err
	}
	s := in.GroupSettingsInput
	req := &workerv1.UpdateGroupRequest{Jid: jid, Name: in.Name, Description: in.Description, Announce: s.Announce,
		Locked: s.Locked, ApprovalRequired: s.ApprovalRequired, AdminsAddMembers: s.AdminsAddMembers, EphemeralSeconds: s.EphemeralSeconds}
	if req.Name == nil && req.Description == nil && req.Announce == nil && req.Locked == nil && req.ApprovalRequired == nil &&
		req.AdminsAddMembers == nil && req.EphemeralSeconds == nil {
		return GroupView{}, apierr.Validation("nothing to update")
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return GroupView{}, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	if _, err := cli.UpdateGroup(c, req); err != nil {
		return GroupView{}, groupErr(err)
	}
	gi, err := cli.GetGroup(c, &workerv1.GetGroupRequest{Jid: jid})
	if err != nil {
		return GroupView{}, groupErr(err)
	}
	return groupView(gi, true), nil
}

// PhotoInput is a JPEG given inline (base64) or by a public URL.
type PhotoInput struct {
	Base64 string `json:"base64,omitempty"`
	URL    string `json:"url,omitempty"`
}

// SetPhoto sets the group's photo (JPEG, up to 2MB); a nil input removes it.
func (g *Groups) SetPhoto(ctx context.Context, instanceID, groupRaw string, in *PhotoInput) error {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return err
	}
	var data []byte
	if in != nil {
		switch {
		case in.Base64 != "" && in.URL != "":
			return apierr.Validation("give base64 or url, not both")
		case in.Base64 != "":
			raw := in.Base64
			if _, after, ok := strings.Cut(raw, ","); ok && strings.HasPrefix(raw, "data:") {
				raw = after
			}
			if data, err = base64.StdEncoding.DecodeString(raw); err != nil {
				return apierr.Validation("base64 is not valid")
			}
		case in.URL != "":
			if g.Fetcher == nil {
				return apierr.Validation("photo URLs are not supported here; send base64")
			}
			c, cancel := groupCtx(ctx)
			defer cancel()
			if data, _, err = g.Fetcher.Fetch(c, in.URL, maxGroupPhoto); err != nil {
				if errors.Is(err, media.ErrTooLarge) {
					return apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", "the photo is limited to 2MB", false)
				}
				return apierr.New(http.StatusBadRequest, "media_fetch_failed", "could not download the photo", false)
			}
		default:
			return apierr.Validation("base64 or url is required")
		}
		if len(data) > maxGroupPhoto {
			return apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", "the photo is limited to 2MB", false)
		}
		if !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
			return apierr.Validation("the photo must be a JPEG image")
		}
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	if _, err := cli.SetGroupPhoto(c, &workerv1.SetGroupPhotoRequest{Jid: jid, Jpeg: data}); err != nil {
		return groupErr(err)
	}
	return nil
}

// Participants adds, removes, promotes or demotes participants. The answer has one
// result per requested participant; one failing never fails the batch. Adds are capped
// per request, paced and counted against the hourly and daily quotas, and only reach
// participants the instance knows (checkAddable).
func (g *Groups) Participants(ctx context.Context, instanceID, groupRaw, action string, participants []string) ([]ParticipantResultView, error) {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return nil, err
	}
	maxN := maxParticipantOps
	switch action {
	case "add":
		maxN = maxAddBatch
	case "remove", "promote", "demote":
	default:
		return nil, apierr.Validation("action must be add, remove, promote or demote")
	}
	jids, err := participantJIDs(participants, maxN)
	if err != nil {
		return nil, err
	}
	if action == "add" {
		if err := g.checkSending(instanceID); err != nil {
			return nil, err
		}
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	a := addable{send: jids, request: jids}
	var res reservation
	if action == "add" {
		lim, err := g.limits(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		if a, err = g.checkAddable(ctx, instanceID, jids); err != nil {
			return nil, err
		}
		if len(a.send) == 0 {
			return mergeResults(jids, a, nil), nil
		}
		if res, err = g.quota.reserve(ctx, g.Store, instanceID, g.now(), lim, quotaAdd, 0, len(a.send)); err != nil {
			return nil, err
		}
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	out, err := cli.UpdateParticipants(c, &workerv1.UpdateParticipantsRequest{Jid: jid, Action: action, Participants: a.send})
	if err != nil {
		g.quota.refund(ctx, g.Store, instanceID, res)
		return nil, groupErr(err)
	}
	return mergeResults(jids, a, out.Results), nil
}

// JoinRequests lists the pending requests to join a group (approval mode; admins only).
func (g *Groups) JoinRequests(ctx context.Context, instanceID, groupRaw string) ([]JoinRequestView, error) {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return nil, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	res, err := cli.ListJoinRequests(c, &workerv1.ListJoinRequestsRequest{Jid: jid})
	if err != nil {
		return nil, groupErr(err)
	}
	out := []JoinRequestView{}
	for _, r := range res.Requests {
		out = append(out, JoinRequestView{JID: r.Jid, LID: r.Lid, RequestedAt: ms(r.RequestedMs)})
	}
	return out, nil
}

// DecideJoinRequests approves or rejects join requests, with one result per participant.
func (g *Groups) DecideJoinRequests(ctx context.Context, instanceID, groupRaw string, approve bool, participants []string) ([]ParticipantResultView, error) {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return nil, err
	}
	jids, err := participantJIDs(participants, maxParticipantOps)
	if err != nil {
		return nil, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	res, err := cli.UpdateJoinRequests(c, &workerv1.UpdateJoinRequestsRequest{Jid: jid, Approve: approve, Participants: jids})
	if err != nil {
		return nil, groupErr(err)
	}
	return mergeResults(jids, addable{send: jids, request: jids}, res.Results), nil
}

// InviteLink returns the group's invite link; reset revokes it and creates a new one.
func (g *Groups) InviteLink(ctx context.Context, instanceID, groupRaw string, reset bool) (string, error) {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return "", err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return "", err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	res, err := cli.InviteLink(c, &workerv1.InviteLinkRequest{Jid: jid, Reset_: reset})
	if err != nil {
		return "", groupErr(err)
	}
	return res.Link, nil
}

func inviteInput(code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 200 {
		return "", apierr.Validation("code (invite code or https://chat.whatsapp.com/… link) is required")
	}
	return code, nil
}

// PreviewInvite returns the metadata of the group behind an invite link without joining.
func (g *Groups) PreviewInvite(ctx context.Context, instanceID, code string) (GroupView, error) {
	code, err := inviteInput(code)
	if err != nil {
		return GroupView{}, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return GroupView{}, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	gi, err := cli.PreviewInvite(c, &workerv1.PreviewInviteRequest{Code: code})
	if err != nil {
		return GroupView{}, groupErr(err)
	}
	return groupView(gi, false), nil
}

// JoinInvite joins a group by invite link (max_joins_per_day). With approval mode on, a
// join request is sent instead (pending).
func (g *Groups) JoinInvite(ctx context.Context, instanceID, code string) (JoinResult, error) {
	code, err := inviteInput(code)
	if err != nil {
		return JoinResult{}, err
	}
	if err := g.checkSending(instanceID); err != nil {
		return JoinResult{}, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return JoinResult{}, err
	}
	lim, err := g.limits(ctx, instanceID)
	if err != nil {
		return JoinResult{}, err
	}
	res, err := g.quota.reserve(ctx, g.Store, instanceID, g.now(), lim, quotaJoin, 1, 0)
	if err != nil {
		return JoinResult{}, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	out, err := cli.JoinInvite(c, &workerv1.JoinInviteRequest{Code: code})
	if err != nil {
		g.quota.refund(ctx, g.Store, instanceID, res)
		return JoinResult{}, groupErr(err)
	}
	return JoinResult{JID: out.Jid, Pending: out.Pending}, nil
}

// Leave leaves the group.
func (g *Groups) Leave(ctx context.Context, instanceID, groupRaw string) error {
	jid, err := GroupJID(groupRaw)
	if err != nil {
		return err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	if _, err := cli.LeaveGroup(c, &workerv1.LeaveGroupRequest{Jid: jid}); err != nil {
		return groupErr(err)
	}
	return nil
}

// Link adds an existing group to a community (unlink removes it).
func (g *Groups) Link(ctx context.Context, instanceID, communityRaw, groupRaw string, unlink bool) error {
	community, err := GroupJID(communityRaw)
	if err != nil {
		return err
	}
	group, err := GroupJID(groupRaw)
	if err != nil {
		return err
	}
	if community == group {
		return apierr.Validation("a community cannot contain itself")
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	if _, err := cli.LinkGroup(c, &workerv1.LinkGroupRequest{Community: community, Group: group, Unlink: unlink}); err != nil {
		return groupErr(err)
	}
	return nil
}

// SubGroups lists a community's groups (the announcement group is flagged).
func (g *Groups) SubGroups(ctx context.Context, instanceID, communityRaw string) ([]SubGroupView, error) {
	community, err := GroupJID(communityRaw)
	if err != nil {
		return nil, err
	}
	cli, err := g.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, cancel := groupCtx(ctx)
	defer cancel()
	res, err := cli.ListSubGroups(c, &workerv1.ListSubGroupsRequest{Community: community})
	if err != nil {
		return nil, groupErr(err)
	}
	out := []SubGroupView{}
	for _, s := range res.Groups {
		out = append(out, SubGroupView{JID: s.Jid, Name: s.Name, AnnouncementGroup: s.AnnouncementGroup})
	}
	return out, nil
}
