package workertest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var _ worker.SocialClient = (*FakeWA)(nil)

// fakeSocial is the in-memory marco 10 state of a FakeWA.
type fakeSocial struct {
	mu       sync.Mutex
	privacy  *workerv1.StatusPrivacy
	channels map[string]*workerv1.Channel // every known channel
	followed map[string]bool
	posts    map[string][]*workerv1.ChannelPost
	labels   []*workerv1.LabelActionRequest
	business bool
	profile  *workerv1.BusinessProfileInfo
	bizSets  []*workerv1.SetBusinessProfileRequest
	// Marco 12: order details by id, and the tokens Order received.
	orders      map[string]*workerv1.OrderDetails
	orderTokens []string
	calls       []string
	err         error
	next        int
	onResync    func()
}

func (s *fakeSocial) init() {
	if s.channels == nil {
		s.channels = map[string]*workerv1.Channel{}
		s.followed = map[string]bool{}
		s.posts = map[string][]*workerv1.ChannelPost{}
	}
}

// slock records the call and returns the scripted error, if any; the caller unlocks.
func (f *FakeWA) slock(call string) error {
	f.soc.mu.Lock()
	f.soc.init()
	f.soc.calls = append(f.soc.calls, call)
	return f.soc.err
}

// SetChannel puts (or replaces) a channel; followed says whether the account follows it.
func (f *FakeWA) SetChannel(c *workerv1.Channel, followed bool) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.init()
	f.soc.channels[c.Jid] = proto.Clone(c).(*workerv1.Channel)
	f.soc.followed[c.Jid] = followed
}

// SetChannelPosts scripts the messages of a channel (newest first).
func (f *FakeWA) SetChannelPosts(jid string, posts []*workerv1.ChannelPost) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.init()
	f.soc.posts[jid] = posts
}

// SetBusiness makes the fake account a WhatsApp Business account with this profile.
func (f *FakeWA) SetBusiness(on bool, p *workerv1.BusinessProfileInfo) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.business, f.soc.profile = on, p
}

// SetStatusPrivacy scripts GetStatusPrivacy.
func (f *FakeWA) SetStatusPrivacy(p *workerv1.StatusPrivacy) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.privacy = p
}

// SetSocialError makes every Social call fail with err (nil restores success).
func (f *FakeWA) SetSocialError(err error) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.err = err
}

// SocialCalls returns the Social calls made so far ("follow <jid>", "react <jid> <id> <emoji>"...).
func (f *FakeWA) SocialCalls() []string {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	return append([]string(nil), f.soc.calls...)
}

// LabelActions returns the LabelAction requests received.
func (f *FakeWA) LabelActions() []*workerv1.LabelActionRequest {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	return append([]*workerv1.LabelActionRequest(nil), f.soc.labels...)
}

func (f *FakeWA) StatusPrivacy(context.Context) (*workerv1.StatusPrivacy, error) {
	err := f.slock("status_privacy")
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if f.soc.privacy == nil {
		return &workerv1.StatusPrivacy{Type: "contacts"}, nil
	}
	return proto.Clone(f.soc.privacy).(*workerv1.StatusPrivacy), nil
}

func (f *FakeWA) Channels(context.Context) ([]*workerv1.Channel, error) {
	err := f.slock("channels")
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []*workerv1.Channel
	for jid, c := range f.soc.channels {
		if f.soc.followed[jid] || c.Role == "owner" || c.Role == "admin" {
			out = append(out, proto.Clone(c).(*workerv1.Channel))
		}
	}
	return out, nil
}

func (f *FakeWA) Channel(_ context.Context, jid, invite string) (*workerv1.Channel, error) {
	err := f.slock("channel " + jid + invite)
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	code := strings.TrimPrefix(invite, "https://whatsapp.com/channel/")
	for _, c := range f.soc.channels {
		if (jid != "" && c.Jid == jid) || (code != "" && c.InviteCode == code) {
			out := proto.Clone(c).(*workerv1.Channel)
			if invite != "" { // read by invite: no viewer metadata
				out.Role, out.Muted = "", false
			}
			return out, nil
		}
	}
	return nil, status.Error(codes.NotFound, "not_found: channel not found")
}

func (f *FakeWA) CreateChannel(_ context.Context, req *workerv1.CreateChannelRequest) (*workerv1.Channel, error) {
	err := f.slock("create " + req.Name)
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	f.soc.next++
	c := &workerv1.Channel{Jid: fmt.Sprintf("1203630000000%05d@newsletter", f.soc.next), Name: req.Name,
		Description: req.Description, Role: "owner", State: "active", InviteCode: fmt.Sprintf("INV%d", f.soc.next)}
	f.soc.channels[c.Jid] = c
	return proto.Clone(c).(*workerv1.Channel), nil
}

func (f *FakeWA) ChannelAction(_ context.Context, jid, action string) error {
	err := f.slock(action + " " + jid)
	defer f.soc.mu.Unlock()
	if err != nil {
		return err
	}
	c, ok := f.soc.channels[jid]
	if !ok {
		return status.Error(codes.NotFound, "not_found: channel not found")
	}
	switch action {
	case "follow", "unfollow":
		f.soc.followed[jid] = action == "follow"
		if action == "follow" && c.Role == "" {
			c.Role = "subscriber"
		}
	case "mute", "unmute":
		c.Muted = action == "mute"
	default:
		return status.Error(codes.InvalidArgument, "validation_failed: unknown action")
	}
	return nil
}

func (f *FakeWA) ChannelMessages(_ context.Context, jid string, count int, before int64) ([]*workerv1.ChannelPost, error) {
	err := f.slock("messages " + jid)
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []*workerv1.ChannelPost
	for _, p := range f.soc.posts[jid] {
		if before > 0 && p.ServerId >= before {
			continue
		}
		if count > 0 && len(out) >= count {
			break
		}
		out = append(out, proto.Clone(p).(*workerv1.ChannelPost))
	}
	return out, nil
}

func (f *FakeWA) ChannelReact(_ context.Context, jid string, serverID int64, emoji string) error {
	err := f.slock(fmt.Sprintf("react %s %d %s", jid, serverID, emoji))
	defer f.soc.mu.Unlock()
	return err
}

func (f *FakeWA) LabelAction(_ context.Context, req *workerv1.LabelActionRequest) (string, error) {
	err := f.slock("label " + req.Kind + " " + req.LabelId)
	defer f.soc.mu.Unlock()
	if err != nil {
		return "", err
	}
	if !f.soc.business {
		return "", status.Error(codes.FailedPrecondition, "business_account_required: labels need a WhatsApp Business account")
	}
	f.soc.labels = append(f.soc.labels, proto.Clone(req).(*workerv1.LabelActionRequest))
	if req.Kind == "resync" && f.soc.onResync != nil {
		f.soc.onResync() // synchronously, under the fake's lock: fn must not call the fake
	}
	chat := req.Chat
	if strings.HasSuffix(chat, "@lid") { // the fake maps every LID to one phone number
		chat = "5511977776666@s.whatsapp.net"
	}
	return chat, nil
}

// OnLabelResync runs fn when a label resync is requested.
func (f *FakeWA) OnLabelResync(fn func()) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.onResync = fn
}

func (f *FakeWA) BusinessProfile(_ context.Context, jid string) (*workerv1.BusinessProfileInfo, error) {
	err := f.slock("business " + jid)
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if jid == "" && !f.soc.business {
		return nil, status.Error(codes.FailedPrecondition, "business_account_required: not a WhatsApp Business account")
	}
	if f.soc.profile == nil {
		return nil, status.Error(codes.NotFound, "not_found: no business profile")
	}
	p := proto.Clone(f.soc.profile).(*workerv1.BusinessProfileInfo)
	if jid != "" {
		p.Jid = jid
	}
	return p, nil
}

// SetBusinessProfile applies the delta to the fake profile (category names are the ids).
func (f *FakeWA) SetBusinessProfile(_ context.Context, req *workerv1.SetBusinessProfileRequest) (*workerv1.BusinessProfileInfo, error) {
	err := f.slock("business_set")
	defer f.soc.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !f.soc.business {
		return nil, status.Error(codes.FailedPrecondition, "business_account_required: not a WhatsApp Business account")
	}
	f.soc.bizSets = append(f.soc.bizSets, proto.Clone(req).(*workerv1.SetBusinessProfileRequest))
	p := f.soc.profile
	if p == nil {
		p = &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net"}
		f.soc.profile = p
	}
	if req.Description != nil {
		p.Description = *req.Description
	}
	if req.Address != nil {
		p.Address = *req.Address
	}
	if req.Email != nil {
		p.Email = *req.Email
	}
	if req.SetWebsites {
		p.Websites = append([]string(nil), req.Websites...)
	}
	if req.SetLocation {
		p.HasLocation, p.Latitude, p.Longitude = true, req.Latitude, req.Longitude
	}
	if req.SetCategories {
		p.Categories, p.CategoryList = nil, nil
		for _, id := range req.CategoryIds {
			p.Categories = append(p.Categories, "cat "+id)
			p.CategoryList = append(p.CategoryList, &workerv1.BusinessCategory{Id: id, Name: "cat " + id})
		}
	}
	if req.Hours != nil {
		p.Timezone, p.Hours = req.Hours.Timezone, req.Hours.Days
	}
	if req.ClearHours {
		p.Timezone, p.Hours = "", nil
	}
	return proto.Clone(p).(*workerv1.BusinessProfileInfo), nil
}

// SetOrder makes Order answer id with d (nil: the order IQ fails, as when WhatsApp no
// longer answers it).
func (f *FakeWA) SetOrder(id string, d *workerv1.OrderDetails) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	if f.soc.orders == nil {
		f.soc.orders = map[string]*workerv1.OrderDetails{}
	}
	f.soc.orders[id] = d
}

// Order answers the orders set with SetOrder; others time out like the legacy IQ.
func (f *FakeWA) Order(_ context.Context, id, token string) (*workerv1.OrderDetails, error) {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	f.soc.orderTokens = append(f.soc.orderTokens, token)
	if d := f.soc.orders[id]; d != nil {
		return proto.Clone(d).(*workerv1.OrderDetails), nil
	}
	return nil, status.Error(codes.DeadlineExceeded, "worker_timeout: WhatsApp did not answer the order query")
}

// OrderTokens returns the tokens Order received.
func (f *FakeWA) OrderTokens() []string {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	return append([]string(nil), f.soc.orderTokens...)
}

// BusinessSets returns the SetBusinessProfile requests received so far.
func (f *FakeWA) BusinessSets() []*workerv1.SetBusinessProfileRequest {
	f.soc.mu.Lock()
	defer f.soc.mu.Unlock()
	return append([]*workerv1.SetBusinessProfileRequest(nil), f.soc.bizSets...)
}

// Social is the Social client of the fake worker (marco 10).
func (f *Fake) Social() workerv1.SocialClient { return workerv1.NewSocialClient(f.cc) }

// EmitStatus simulates a contact's status post.
func (f *Fake) EmitStatus(m *workerv1.Message) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Status{Status: m}})
}

// EmitChannelPost simulates a message in a followed channel.
func (f *Fake) EmitChannelPost(p *workerv1.ChannelPost) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_ChannelPost{ChannelPost: p}})
}

// EmitLabel simulates a label change from the app state.
func (f *Fake) EmitLabel(u *workerv1.LabelUpdate) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Label{Label: u}})
}
