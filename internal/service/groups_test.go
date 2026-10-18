package service

import (
	"context"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type groupEnv struct {
	*msgEnv
	g   *Groups
	now time.Time
	mu  sync.Mutex
}

func newGroupEnv(t *testing.T) *groupEnv {
	e := &groupEnv{msgEnv: newMsgEnv(t), now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	e.g = &Groups{Store: e.st, Workers: e.svc.Manager, Resolver: e.svc.Resolver, Defaults: fast(),
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }}
	return e
}

func (e *groupEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }
func (e *groupEnv) wa() *workertest.FakeWA  { return e.sp.Last("i1").WA }

func (e *groupEnv) group(jid string, members ...string) {
	gi := &workerv1.GroupInfo{Jid: jid, Name: "G", Participants: []*workerv1.GroupParticipant{{Jid: "5511999999999@s.whatsapp.net", Admin: true, SuperAdmin: true}}}
	for _, m := range members {
		gi.Participants = append(gi.Participants, &workerv1.GroupParticipant{Jid: m})
	}
	e.wa().SetGroup(gi)
}

func statuses(rs []ParticipantResultView) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Participant+"="+r.Status)
	}
	return out
}

func TestCreateGroupChecksParticipants(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	e.received(&workerv1.Message{Id: "IN1", Type: "text", Text: "oi"}) // peer has a chat
	e.wa().SetUnregistered("5511900000000")
	e.wa().RefuseAdd("5511911111111@s.whatsapp.net", 403)
	ann := true
	res, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "Equipe", Description: "d",
		Participants: []string{peer, "5511900000000", "+55 11 91111-1111", "222@lid", peer},
		Settings:     &GroupSettingsInput{Announce: &ann}})
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(res.Results)
	want := []string{peer + "=ok", "5511900000000@s.whatsapp.net=not_on_whatsapp",
		"5511911111111@s.whatsapp.net=privacy_invite_required", "222@lid=unknown_contact"}
	if len(got) != len(want) {
		t.Fatalf("results = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("results = %v, want %v", got, want)
		}
	}
	if res.Results[2].InviteCode == "" || !res.Group.Settings.Announce || res.Group.Description != "d" || res.Warning != "" {
		t.Fatalf("create = %+v", res)
	}
	g := e.wa().Group(res.Group.JID)
	if g == nil || len(g.Participants) != 2 || g.Description != "d" {
		t.Fatalf("worker group = %+v", g)
	}
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: " "}); apiCode(err) != "validation_failed" {
		t.Fatalf("empty name = %v", err)
	}
	eph := uint32(5)
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "x", Settings: &GroupSettingsInput{EphemeralSeconds: &eph}}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad ephemeral = %v", err)
	}
}

func TestGroupCreateQuotaPersists(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	for i := range 3 {
		if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g"}); apiCode(err) != "rate_limited" {
		t.Fatalf("4th create = %v", err)
	}
	// A new service (restart) still sees today's creations.
	g2 := &Groups{Store: e.st, Workers: e.svc.Manager, Defaults: fast(), Now: e.g.Now}
	if _, err := g2.Create(ctx, "i1", CreateGroupInput{Name: "g"}); apiCode(err) != "rate_limited" {
		t.Fatalf("after restart = %v", err)
	}
	e.advance(25 * time.Hour)
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g"}); err != nil {
		t.Fatalf("next day = %v", err)
	}
	// A higher limit (within the ceiling) is honored.
	if _, err := e.svc.UpdateSettings(ctx, "i1", settingsPatchGroups(5)); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g"}); apiCode(err) != "rate_limited" {
		t.Fatalf("over raised limit = %v", err)
	}
}

func TestParticipantAddsArePacedAndCapped(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	const grp = "120363000000000001@g.us"
	e.group(grp)
	// Lookups have their own per-minute quota (real clock): out of the way here.
	e.g.Resolver = &lookup.Resolver{Store: e.st, Workers: e.svc.Manager, CheckPerMinute: 1000, CheckPerDay: 10000}
	phone := func(i int) string { return "55119800000" + string(rune('0'+i/10)) + string(rune('0'+i%10)) }
	batch := func(from, n int) []string {
		var out []string
		for i := range n {
			out = append(out, phone(from+i))
		}
		return out
	}
	res, err := e.g.Participants(ctx, "i1", grp, "add", batch(0, 4))
	if err != nil || len(res) != 4 || res[0].Status != "ok" {
		t.Fatalf("add = %v, %v", statuses(res), err)
	}
	// Paced: a second batch right away is refused, and nothing is sent.
	calls := len(e.wa().GroupCalls())
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(4, 1)); apiCode(err) != "rate_limited" {
		t.Fatalf("unpaced add = %v", err)
	}
	if len(e.wa().GroupCalls()) != calls {
		t.Fatal("a refused add reached the worker")
	}
	e.advance(11 * time.Second)
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(4, 6)); err != nil {
		t.Fatal(err)
	}
	e.advance(11 * time.Second)
	// 10 per hour by default.
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(10, 1)); apiCode(err) != "rate_limited" {
		t.Fatalf("over hourly limit = %v", err)
	}
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(20, 11)); apiCode(err) != "validation_failed" {
		t.Fatalf("batch over 10 = %v", err)
	}
	e.advance(time.Hour)
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(10, 1)); err != nil {
		t.Fatalf("next hour = %v", err)
	}
	// A request WhatsApp answered (unknown group) keeps its count and pacing.
	e.advance(11 * time.Second)
	if _, err := e.g.Participants(ctx, "i1", "120363000000000999@g.us", "add", batch(11, 1)); apiCode(err) != "group_not_found" {
		t.Fatalf("unknown group = %v", err)
	}
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(12, 1)); apiCode(err) != "rate_limited" {
		t.Fatalf("add right after a counted failure = %v", err)
	}
	// One that never reached WhatsApp (worker not connected) gives it back.
	e.advance(11 * time.Second)
	e.wa().SetGroupErr(status.Error(codes.Unavailable, "instance_not_connected: down"))
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(13, 1)); apiCode(err) != "instance_not_connected" {
		t.Fatalf("worker down = %v", err)
	}
	e.wa().SetGroupErr(nil)
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(13, 1)); err != nil {
		t.Fatalf("refunded add was paced or counted: %v", err)
	}
	// A timeout may have reached WhatsApp: counted.
	e.advance(11 * time.Second)
	e.wa().SetGroupErr(status.Error(codes.DeadlineExceeded, "worker_timeout: slow"))
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(14, 1)); apiCode(err) != "worker_timeout" {
		t.Fatalf("timeout = %v", err)
	}
	e.wa().SetGroupErr(nil)
	if _, err := e.g.Participants(ctx, "i1", grp, "add", batch(15, 1)); apiCode(err) != "rate_limited" {
		t.Fatalf("add right after a timeout = %v", err)
	}
}

// A create with participants is paced like an add, and quotas are checked before any
// number lookup is spent.
func TestCreateIsPacedAndQuotaCheckedBeforeLookups(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	res, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g", Participants: []string{"5511980000001"}})
	if err != nil || res.Results[0].Status != "ok" {
		t.Fatalf("create = %+v %v", res, err)
	}
	checks := e.wa().Checks()
	if _, err := e.g.Participants(ctx, "i1", res.Group.JID, "add", []string{"5511980000002"}); apiCode(err) != "rate_limited" {
		t.Fatalf("add right after create = %v", err)
	}
	if e.wa().Checks() != checks {
		t.Fatal("a refused add spent number lookups")
	}
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "c", Community: true, Participants: []string{peer}}); apiCode(err) != "validation_failed" {
		t.Fatalf("community with participants = %v", err)
	}
}

// Two inputs that resolve to the same account are sent once and both get its result.
func TestDuplicateResolvedParticipants(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	e.received(&workerv1.Message{Id: "IN1", Type: "text", Text: "oi"}) // peer has a chat
	e.wa().SetRegistered("551188887777", peer)                         // without the 9th digit
	res, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g", Participants: []string{peer, "551188887777"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 2 || res.Results[0].Status != "ok" || res.Results[1].Status != "ok" ||
		res.Results[1].Participant != "551188887777@s.whatsapp.net" {
		t.Fatalf("results = %+v", res.Results)
	}
	if g := e.wa().Group(res.Group.JID); len(g.Participants) != 2 {
		t.Fatalf("participants = %+v", g.Participants)
	}
}

func TestParticipantsRemovePromoteAndErrors(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	const grp = "120363000000000001@g.us"
	e.group(grp, peer)
	res, err := e.g.Participants(ctx, "i1", grp, "promote", []string{peer, "5511977776666"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != "ok" || res[1].Status != "not_participant" {
		t.Fatalf("promote = %v", statuses(res))
	}
	if g := e.wa().Group(grp); !g.Participants[1].Admin {
		t.Fatalf("not promoted: %+v", g.Participants)
	}
	if _, err := e.g.Participants(ctx, "i1", grp, "kick", []string{peer}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad action = %v", err)
	}
	if _, err := e.g.Participants(ctx, "i1", "not-a-group", "remove", []string{peer}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad group = %v", err)
	}
	if _, err := e.g.Participants(ctx, "i1", grp, "remove", []string{grp}); apiCode(err) != "validation_failed" {
		t.Fatalf("group as participant = %v", err)
	}
	if res, err := e.g.Participants(ctx, "i1", grp, "remove", []string{peer}); err != nil || res[0].Status != "ok" {
		t.Fatalf("remove = %v %v", res, err)
	}
	if _, err := e.g.Get(ctx, "missing", grp); apiCode(err) != "instance_not_found" {
		t.Fatalf("unknown instance = %v", err)
	}
}

func TestGroupUpdateInviteJoinLeaveAndCommunities(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	const grp = "120363000000000001@g.us"
	e.group(grp)
	name, locked, eph := "Novo", true, uint32(604800)
	v, err := e.g.Update(ctx, "i1", grp, UpdateGroupInput{Name: &name, GroupSettingsInput: GroupSettingsInput{Locked: &locked, EphemeralSeconds: &eph}})
	if err != nil || v.Name != "Novo" || !v.Settings.Locked || v.Settings.EphemeralSeconds != 604800 || len(v.Participants) != 1 {
		t.Fatalf("update = %+v, %v", v, err)
	}
	if _, err := e.g.Update(ctx, "i1", grp, UpdateGroupInput{}); apiCode(err) != "validation_failed" {
		t.Fatalf("empty update = %v", err)
	}
	link, err := e.g.InviteLink(ctx, "i1", grp, false)
	if err != nil || link == "" {
		t.Fatalf("link = %q %v", link, err)
	}
	if again, _ := e.g.InviteLink(ctx, "i1", grp, false); again != link {
		t.Fatalf("link changed without reset: %q", again)
	}
	reset, err := e.g.InviteLink(ctx, "i1", grp, true)
	if err != nil || reset == link {
		t.Fatalf("reset = %q %v", reset, err)
	}
	if _, err := e.g.PreviewInvite(ctx, "i1", link); apiCode(err) != "invite_revoked" {
		t.Fatalf("old link = %v", err)
	}
	other := &workerv1.GroupInfo{Jid: "120363000000000077@g.us", Name: "Fora", ParticipantCount: 3}
	e.wa().SetInvite("OTHER", other)
	pv, err := e.g.PreviewInvite(ctx, "i1", "https://chat.whatsapp.com/OTHER")
	if err != nil || pv.Name != "Fora" {
		t.Fatalf("preview = %+v %v", pv, err)
	}
	j, err := e.g.JoinInvite(ctx, "i1", "OTHER")
	if err != nil || j.JID != other.Jid || j.Pending {
		t.Fatalf("join = %+v %v", j, err)
	}
	if err := e.g.Leave(ctx, "i1", other.Jid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.Get(ctx, "i1", other.Jid); apiCode(err) != "group_not_found" {
		t.Fatalf("after leave = %v", err)
	}
	// Communities: create, link, list sub-groups, unlink.
	c, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "Comunidade", Community: true})
	if err != nil || !c.Group.Community {
		t.Fatalf("community = %+v %v", c, err)
	}
	if err := e.g.Link(ctx, "i1", c.Group.JID, grp, false); err != nil {
		t.Fatal(err)
	}
	subs, err := e.g.SubGroups(ctx, "i1", c.Group.JID)
	if err != nil || len(subs) != 1 || subs[0].JID != grp {
		t.Fatalf("subgroups = %+v %v", subs, err)
	}
	list, err := e.g.List(ctx, "i1", true)
	if err != nil || len(list) != 1 || list[0].JID != c.Group.JID || list[0].Participants != nil {
		t.Fatalf("communities = %+v %v", list, err)
	}
	if err := e.g.Link(ctx, "i1", c.Group.JID, grp, true); err != nil {
		t.Fatal(err)
	}
	if err := e.g.Link(ctx, "i1", grp, grp, false); apiCode(err) != "validation_failed" {
		t.Fatalf("self link = %v", err)
	}
	// Join requests.
	e.wa().SetJoinRequests(grp, &workerv1.JoinRequest{Jid: peer, RequestedMs: 1_800_000_000_000})
	reqs, err := e.g.JoinRequests(ctx, "i1", grp)
	if err != nil || len(reqs) != 1 || reqs[0].JID != peer || reqs[0].RequestedAt == nil {
		t.Fatalf("requests = %+v %v", reqs, err)
	}
	res, err := e.g.DecideJoinRequests(ctx, "i1", grp, true, []string{peer, "5511977776666"})
	if err != nil || res[0].Status != "ok" || res[1].Status != "not_participant" {
		t.Fatalf("approve = %v %v", statuses(res), err)
	}
}

func TestGroupJoinQuota(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	if _, err := e.svc.UpdateSettings(ctx, "i1", settingsPatchJoins(1)); err != nil {
		t.Fatal(err)
	}
	e.wa().SetInvite("A", &workerv1.GroupInfo{Jid: "120363000000000071@g.us"})
	e.wa().SetInvite("B", &workerv1.GroupInfo{Jid: "120363000000000072@g.us"})
	if _, err := e.g.JoinInvite(ctx, "i1", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.JoinInvite(ctx, "i1", "B"); apiCode(err) != "rate_limited" {
		t.Fatalf("2nd join = %v", err)
	}
}

func TestGroupsRespectSendingSuspension(t *testing.T) {
	e := newGroupEnv(t)
	ctx := context.Background()
	sending := &Sending{Store: e.st}
	if err := sending.SetInstance(ctx, "i1", true, "test"); err != nil {
		t.Fatal(err)
	}
	e.g.Sending = sending
	if _, err := e.g.Create(ctx, "i1", CreateGroupInput{Name: "g"}); apiCode(err) != "sending_suspended" {
		t.Fatalf("create while suspended = %v", err)
	}
	if _, err := e.g.Participants(ctx, "i1", "120363000000000001@g.us", "add", []string{peer}); apiCode(err) != "sending_suspended" {
		t.Fatalf("add while suspended = %v", err)
	}
	if _, err := e.g.List(ctx, "i1", false); err != nil { // reading still works
		t.Fatal(err)
	}
}

type pubRec struct {
	mu  sync.Mutex
	evs []events.GroupUpdate
}

func (p *pubRec) Publish(_, typ string, data any) {
	if typ != events.TypeGroupUpdated {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, data.(events.GroupUpdate))
}

func (p *pubRec) kinds() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.evs {
		out = append(out, e.Kind)
	}
	return out
}

func TestGroupEventsPublishAndNameChats(t *testing.T) {
	e := newGroupEnv(t)
	pub := &pubRec{}
	obs := &GroupEvents{Store: e.st, Workers: e.svc.Manager, Events: pub}
	t.Cleanup(obs.Close)
	const grp = "120363000000000001@g.us"
	e.group(grp)
	obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Group{Group: &workerv1.GroupUpdate{
		Group: grp, Actor: peer, TimestampMs: 1_800_000_000_000,
		Joined: []string{peer}, Left: []string{"5511977776666@s.whatsapp.net"}, Name: proto.String("Renomeado"),
		Announce: proto.Bool(true), PhotoRemoved: true, InviteLinkReset: true, JoinRequests: []string{peer},
		JoinRequestsRevoked: []string{peer}, Suspended: true,
	}}})
	want := []string{"participants_added", "participants_removed", "join_request", "join_request_revoked", "subject", "photo", "settings",
		"suspended", "invite_link_reset"}
	got := pub.kinds()
	if len(got) != len(want) {
		t.Fatalf("kinds = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
	pub.mu.Lock()
	first, settingsEv := pub.evs[0], pub.evs[6]
	pub.mu.Unlock()
	if first.Chat != grp || first.Actor != peer || first.Participants[0] != peer || first.Timestamp.UnixMilli() != 1_800_000_000_000 {
		t.Fatalf("added = %+v", first)
	}
	if settingsEv.Settings == nil || settingsEv.Settings.Announce == nil || !*settingsEv.Settings.Announce || settingsEv.Settings.Locked != nil {
		t.Fatalf("settings = %+v", settingsEv.Settings)
	}
	// A message in a group chat without a name looks the group up and names the chat.
	if _, err := e.st.InsertMessage(context.Background(), "i1", store.Message{ID: "m1", WAMessageID: "W1", ChatJID: grp,
		SenderJID: peer, Source: "contact", Type: "text", Status: "received", Timestamp: time.Now()},
		store.ChatUpdate{IsGroup: true}); err != nil {
		t.Fatal(err)
	}
	obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Message{Message: &workerv1.Message{Chat: grp, IsGroup: true}}})
	e.eventually("group chat named", func() bool {
		c, err := e.st.GetChat(context.Background(), "i1", grp)
		return err == nil && c.Name == "G"
	})
	// A subject change renames it.
	obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Group{Group: &workerv1.GroupUpdate{Group: grp, Name: proto.String("Outro")}}})
	if c, _ := e.st.GetChat(context.Background(), "i1", grp); c.Name != "Outro" {
		t.Fatalf("chat name = %q", c.Name)
	}
}

func settingsPatchGroups(creates int) settings.Patch {
	return settings.Patch{Groups: &settings.GroupsPatch{MaxCreatesPerDay: &creates}}
}

func settingsPatchJoins(joins int) settings.Patch {
	return settings.Patch{Groups: &settings.GroupsPatch{MaxJoinsPerDay: &joins}}
}

// An approval mode change is published with the value read from the group.
func TestApprovalModeChangeLooksTheGroupUp(t *testing.T) {
	e := newGroupEnv(t)
	pub := &pubRec{}
	obs := &GroupEvents{Store: e.st, Workers: e.svc.Manager, Events: pub}
	const grp = "120363000000000001@g.us"
	e.wa().SetGroup(&workerv1.GroupInfo{Jid: grp, Name: "G", ApprovalRequired: false})
	obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Group{Group: &workerv1.GroupUpdate{Group: grp, ApprovalModeChanged: true}}})
	obs.Close() // waits for the lookup
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.evs) != 1 || pub.evs[0].Kind != "settings" || pub.evs[0].Settings.ApprovalRequired == nil || *pub.evs[0].Settings.ApprovalRequired {
		t.Fatalf("events = %+v", pub.evs)
	}
}
