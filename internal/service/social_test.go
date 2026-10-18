package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

type socialEnv struct {
	*msgEnv
	s   *Social
	obs *SocialEvents
	pub *anyPub
	now time.Time
	mu  sync.Mutex
}

// anyPub records every published event.
type anyPub struct {
	mu   sync.Mutex
	typs []string
	data []any
}

func (p *anyPub) Publish(_, typ string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.typs, p.data = append(p.typs, typ), append(p.data, data)
}

func (p *anyPub) of(typ string) []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for i, t := range p.typs {
		if t == typ {
			out = append(out, p.data[i])
		}
	}
	return out
}

func newSocialEnv(t *testing.T) *socialEnv {
	e := &socialEnv{msgEnv: newMsgEnv(t), pub: &anyPub{}, now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	e.s = &Social{Store: e.st, Workers: e.svc.Manager, Messaging: e.svc, Fetcher: e.svc.Fetcher, Events: e.pub,
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }}
	e.obs = &SocialEvents{Store: e.st, Events: e.pub}
	return e
}

func (e *socialEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }
func (e *socialEnv) wa() *workertest.FakeWA  { return e.sp.Last("i1").WA }

func TestPostStatusGoesThroughTheQueue(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	v, err := e.s.PostStatus(ctx, "i1", StatusInput{Type: "text", Text: "Bom dia!", BackgroundColor: "#112233", Font: 2}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Chat != store.StatusChat {
		t.Fatalf("post = %+v, %v", v, err)
	}
	r := e.lastSent()
	if r.To != "status@broadcast" || r.Type != "text" || r.Text != "Bom dia!" || r.StatusStyle.GetBackgroundArgb() != 0xFF112233 ||
		r.StatusStyle.GetFont() != 2 {
		t.Fatalf("request = %+v", r)
	}
	img := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	if v, err := e.s.PostStatus(ctx, "i1", StatusInput{Type: "image", Caption: "foto", Media: &MediaInput{Base64: img, Mime: "image/png"}}, 2*time.Second); err != nil || v.Status != "sent" {
		t.Fatalf("image = %+v, %v", v, err)
	}
	for _, bad := range []StatusInput{{Type: "poll"}, {Type: "image", Text: "x"}, {Type: "text", Text: "x", BackgroundColor: "red"},
		{Type: "text", Text: "x", Font: 12}, {Type: "video", Font: 1}} {
		if _, err := e.s.PostStatus(ctx, "i1", bad, 0); apiCode(err) != "validation_failed" {
			t.Fatalf("%+v = %v", bad, err)
		}
	}
	// Our status stays in the log, out of the chat list.
	page, err := e.s.StatusUpdates(ctx, "i1", 10, "")
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("statuses = %+v, %v", page, err)
	}
	chats, _ := e.svc.Chats(ctx, "i1", 10, "")
	if len(chats.Chats) != 0 {
		t.Fatalf("chats = %+v", chats.Chats)
	}
	// Delete our text status for everyone.
	if v, err := e.s.DeleteStatus(ctx, "i1", page.Messages[1].ID, 2*time.Second); err != nil || v.Type != "revoke" {
		t.Fatalf("delete = %+v, %v", v, err)
	}
	if r := e.lastSent(); r.To != "status@broadcast" || r.Type != "revoke" || r.TargetId == "" || !r.TargetFromMe {
		t.Fatalf("revoke = %+v", r)
	}
}

func TestStatusDailyCap(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	for i := range maxStatusPerDay {
		if _, err := e.s.PostStatus(ctx, "i1", StatusInput{Type: "text", Text: fmt.Sprint("s", i)}, 0); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := e.s.PostStatus(ctx, "i1", StatusInput{Type: "text", Text: "one more"}, 0); apiCode(err) != "rate_limited" {
		t.Fatalf("31st status = %v", err)
	}
}

func TestReceivedStatusAndReply(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Status{Status: &workerv1.Message{Id: "ST1", Chat: "status@broadcast",
		Sender: peer, Type: "image", Text: "praia", TimestampMs: time.Now().UnixMilli(), PushName: "Ana",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Ref: []byte("ref")}}}})
	// A duplicate (redelivery) is not published twice.
	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Status{Status: &workerv1.Message{Id: "ST1", Chat: "status@broadcast",
		Sender: peer, Type: "image", TimestampMs: time.Now().UnixMilli()}}})
	got := e.pub.of(events.TypeStatusReceived)
	if len(got) != 1 {
		t.Fatalf("status.received = %v", got)
	}
	m := got[0].(events.Message)
	if m.Chat != "status@broadcast" || m.Sender != peer || m.Media == nil || m.Media.URL == "" || m.PushName != "Ana" {
		t.Fatalf("event = %+v", m)
	}
	v, err := e.s.ReplyStatus(ctx, "i1", m.ID, StatusReplyInput{Text: "Que lindo!"}, 2*time.Second)
	if err != nil || v.Chat != peer || v.Status != "sent" {
		t.Fatalf("reply = %+v, %v", v, err)
	}
	r := e.lastSent()
	if r.To != peer || r.Quote.GetMessageId() != "ST1" || r.Quote.GetRemoteJid() != "status@broadcast" || r.Quote.GetSender() != peer {
		t.Fatalf("reply request = %+v", r)
	}
	if _, err := e.s.ReplyStatus(ctx, "i1", "nope", StatusReplyInput{Text: "x"}, 0); apiCode(err) != "not_found" {
		t.Fatalf("unknown status = %v", err)
	}
	if !events.OptIn(events.TypeStatusReceived) || !events.OptIn(events.TypeChannelMessage) || events.OptIn(events.TypeLabelUpdated) {
		t.Fatal("status.received and channel.message must be opt-in")
	}
}

func TestChannels(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	own := &workerv1.Channel{Jid: "120363000000000001@newsletter", Name: "Minha loja", Role: "owner", InviteCode: "OWN"}
	other := &workerv1.Channel{Jid: "120363000000000002@newsletter", Name: "Notícias", InviteCode: "NEWS", Subscribers: 1000}
	e.wa().SetChannel(own, false)
	e.wa().SetChannel(other, false)

	if v, err := e.s.Channel(ctx, "i1", "", "https://whatsapp.com/channel/NEWS"); err != nil || v.JID != other.Jid || v.InviteLink != "https://whatsapp.com/channel/NEWS" {
		t.Fatalf("by invite = %+v, %v", v, err)
	}
	if _, err := e.s.Channel(ctx, "i1", "123@g.us", ""); apiCode(err) != "validation_failed" {
		t.Fatalf("group JID = %v", err)
	}
	for i := 0; i < followsPerMinute; i++ {
		if err := e.s.ChannelAction(ctx, "i1", "120363000000000002", "follow"); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := e.s.ChannelAction(ctx, "i1", other.Jid, "unfollow"); apiCode(err) != "rate_limited" {
		t.Fatalf("4th follow in a minute = %v", err)
	}
	if err := e.s.ChannelAction(ctx, "i1", other.Jid, "mute"); err != nil {
		t.Fatalf("mute is not rate limited: %v", err)
	}
	e.advance(time.Minute)
	if err := e.s.ChannelAction(ctx, "i1", other.Jid, "unfollow"); err != nil {
		t.Fatal(err)
	}
	list, err := e.s.Channels(ctx, "i1")
	if err != nil || len(list) != 1 || list[0].JID != own.Jid {
		t.Fatalf("followed = %+v, %v", list, err)
	}

	// Publishing needs ownership; it goes through the queue.
	if _, err := e.s.PublishChannel(ctx, "i1", other.Jid, ChannelPostInput{Type: "text", Text: "x"}, 0); apiCode(err) != "forbidden" {
		t.Fatalf("post as subscriber = %v", err)
	}
	v, err := e.s.PublishChannel(ctx, "i1", own.Jid, ChannelPostInput{Type: "text", Text: "Promoção!"}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Chat != own.Jid {
		t.Fatalf("post = %+v, %v", v, err)
	}
	if r := e.lastSent(); r.To != own.Jid || r.Text != "Promoção!" {
		t.Fatalf("post request = %+v", r)
	}
	if _, err := e.s.PublishChannel(ctx, "i1", own.Jid, ChannelPostInput{Type: "poll"}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("poll = %v", err)
	}

	e.wa().SetChannelPosts(own.Jid, []*workerv1.ChannelPost{
		{Channel: own.Jid, ServerId: 12, Views: 40, Reactions: map[string]int64{"❤️": 3}, Message: &workerv1.Message{Id: "A", Type: "text", Text: "oi"}},
		{Channel: own.Jid, ServerId: 11, Message: &workerv1.Message{Id: "B", Type: "image", Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg"}}},
	})
	posts, err := e.s.ChannelMessages(ctx, "i1", own.Jid, 0, 0)
	if err != nil || len(posts) != 2 || posts[0].ServerID != 12 || posts[0].Reactions["❤️"] != 3 || posts[1].Media == nil {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	if err := e.s.ChannelReact(ctx, "i1", own.Jid, 12, "🔥"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ChannelReact(ctx, "i1", own.Jid, 0, "🔥"); apiCode(err) != "validation_failed" {
		t.Fatalf("react without server id = %v", err)
	}

	for i := 0; i < createsPerDay; i++ {
		e.advance(time.Minute)
		if _, err := e.s.CreateChannel(ctx, "i1", CreateChannelInput{Name: fmt.Sprint("Canal ", i)}); err != nil {
			t.Fatal(i, err)
		}
	}
	e.advance(time.Minute)
	if _, err := e.s.CreateChannel(ctx, "i1", CreateChannelInput{Name: "Mais um"}); apiCode(err) != "rate_limited" {
		t.Fatalf("3rd channel in a day = %v", err)
	}

	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_ChannelPost{ChannelPost: &workerv1.ChannelPost{Channel: other.Jid, ServerId: 5,
		Message: &workerv1.Message{Id: "C", Type: "text", Text: "manchete", TimestampMs: time.Now().UnixMilli()}}}})
	if got := e.pub.of(events.TypeChannelMessage); len(got) != 1 || got[0].(events.ChannelMessage).Text != "manchete" {
		t.Fatalf("channel.message = %v", got)
	}
}

func TestLabelsNeedBusinessAndSync(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	if _, err := e.s.Labels(ctx, "i1"); apiCode(err) != "business_account_required" {
		t.Fatalf("personal account = %v", err)
	}
	if _, err := e.s.BusinessProfile(ctx, "i1"); apiCode(err) != "business_account_required" {
		t.Fatalf("personal business profile = %v", err)
	}
	e.s.business = nil // drop the cached answer
	e.wa().SetProfile(&workerv1.GetProfileResponse{Jid: "5511999999999@s.whatsapp.net", BusinessName: "Loja X"})
	e.wa().SetBusiness(true, &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net", Email: "oi@loja.x",
		Categories: []string{"Varejo"}, BusinessName: "Loja X"})
	if bp, err := e.s.BusinessProfile(ctx, "i1"); err != nil || bp.Email != "oi@loja.x" || bp.Categories[0] != "Varejo" {
		t.Fatalf("business profile = %+v, %v", bp, err)
	}

	// The phone's labels arrive through the app state (full sync: stored, not published).
	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Label{Label: &workerv1.LabelUpdate{Kind: "edit", LabelId: "1", Name: "Novo cliente", Color: 1, FullSync: true}}})
	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Label{Label: &workerv1.LabelUpdate{Kind: "edit", LabelId: "4", Name: "Pago", Color: 4, FullSync: true}}})
	if n := len(e.pub.of(events.TypeLabelUpdated)); n != 0 {
		t.Fatalf("full sync published %d events", n)
	}
	name, color := "VIP", 7
	l, err := e.s.CreateLabel(ctx, "i1", LabelInput{Name: &name, Color: &color})
	if err != nil || l.ID != "5" || l.Name != "VIP" {
		t.Fatalf("create = %+v, %v", l, err)
	}
	if _, err := e.s.CreateLabel(ctx, "i1", LabelInput{Name: &name, Color: func() *int { c := 25; return &c }()}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad color = %v", err)
	}
	e.received(&workerv1.Message{Id: "IN1", Type: "text", Text: "oi"})
	if l, err = e.s.LabelChat(ctx, "i1", "5", peer, true); err != nil || len(l.Chats) != 1 || l.Chats[0] != peer {
		t.Fatalf("label chat = %+v, %v", l, err)
	}
	if _, err := e.s.LabelChat(ctx, "i1", "99", peer, true); apiCode(err) != "not_found" {
		t.Fatalf("unknown label = %v", err)
	}
	acts := e.wa().LabelActions()
	if len(acts) != 2 || acts[0].Kind != "edit" || acts[0].LabelId != "5" || acts[1].Kind != "chat" || acts[1].Chat != peer || !acts[1].Labeled {
		t.Fatalf("label actions = %+v", acts)
	}
	// The app state echo of our own change publishes nothing more.
	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Label{Label: &workerv1.LabelUpdate{Kind: "chat", LabelId: "5", Chat: peer, Labeled: true}}})
	evs := e.pub.of(events.TypeLabelUpdated)
	if len(evs) != 2 || evs[0].(events.LabelUpdated).Source != "api" || evs[1].(events.LabelUpdated).Kind != "chat" {
		t.Fatalf("label.updated = %+v", evs)
	}
	// A change on the phone is published as source device.
	e.obs.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Label{Label: &workerv1.LabelUpdate{Kind: "edit", LabelId: "4", Deleted: true, Name: "Pago", Color: 4}}})
	if evs = e.pub.of(events.TypeLabelUpdated); len(evs) != 3 || evs[2].(events.LabelUpdated).Source != "device" || !evs[2].(events.LabelUpdated).Deleted {
		t.Fatalf("device change = %+v", evs)
	}
	list, err := e.s.Labels(ctx, "i1")
	if err != nil || len(list) != 2 || list[0].ID != "1" || list[1].ID != "5" {
		t.Fatalf("labels = %+v, %v", list, err)
	}
	newName := "Cliente VIP"
	if l, err := e.s.UpdateLabel(ctx, "i1", "5", LabelInput{Name: &newName}); err != nil || l.Name != newName || l.Color != 7 {
		t.Fatalf("update = %+v, %v", l, err)
	}
	m, _ := e.st.MessageByWAID(ctx, "i1", peer, "IN1")
	if err := e.s.LabelMessage(ctx, "i1", "5", m.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.s.DeleteLabel(ctx, "i1", "5"); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.s.Labels(ctx, "i1"); len(list) != 1 {
		t.Fatalf("after delete = %+v", list)
	}
}

func TestSocialRespectsSendingSuspension(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	e.s.Sending = &Sending{Store: e.st}
	if err := e.s.Sending.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Sending.SetInstance(ctx, "i1", true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.PostStatus(ctx, "i1", StatusInput{Type: "text", Text: "x"}, 0); err == nil {
		t.Fatal("status while suspended")
	}
	if err := e.s.ChannelAction(ctx, "i1", "120363000000000002@newsletter", "follow"); err == nil {
		t.Fatal("follow while suspended")
	}
}
