package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// SocialClient is implemented by WAClients that handle status privacy, channels, labels
// and the business profile (marco 10). Optional: without it the Social RPCs answer
// Unimplemented.
type SocialClient interface {
	StatusPrivacy(ctx context.Context) (*workerv1.StatusPrivacy, error)
	Channels(ctx context.Context) ([]*workerv1.Channel, error)
	Channel(ctx context.Context, jid, invite string) (*workerv1.Channel, error)
	CreateChannel(ctx context.Context, req *workerv1.CreateChannelRequest) (*workerv1.Channel, error)
	ChannelAction(ctx context.Context, jid, action string) error
	ChannelMessages(ctx context.Context, jid string, count int, before int64) ([]*workerv1.ChannelPost, error)
	ChannelReact(ctx context.Context, jid string, serverID int64, emoji string) error
	LabelAction(ctx context.Context, req *workerv1.LabelActionRequest) error
	BusinessProfile(ctx context.Context, jid string) (*workerv1.BusinessProfileInfo, error)
}

var _ SocialClient = (*Whatsmeow)(nil)

// SocialServer serves the Social gRPC service over a Server's WAClient.
type SocialServer struct {
	workerv1.UnimplementedSocialServer
	s *Server
}

// NewSocialServer returns the Social service of s (register it next to s).
func NewSocialServer(s *Server) *SocialServer { return &SocialServer{s: s} }

func (g *SocialServer) client() (SocialClient, error) {
	c, ok := g.s.wa.(SocialClient)
	if !ok {
		return nil, grpcErr(codes.Unimplemented, "not_supported", "status, channels and labels not supported by this worker")
	}
	return c, nil
}

func (g *SocialServer) GetStatusPrivacy(ctx context.Context, _ *workerv1.GetStatusPrivacyRequest) (*workerv1.StatusPrivacy, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	return c.StatusPrivacy(ctx)
}

func (g *SocialServer) ListChannels(ctx context.Context, _ *workerv1.ListChannelsRequest) (*workerv1.ListChannelsResponse, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	list, err := c.Channels(ctx)
	if err != nil {
		return nil, err
	}
	return &workerv1.ListChannelsResponse{Channels: list}, nil
}

func (g *SocialServer) GetChannel(ctx context.Context, req *workerv1.GetChannelRequest) (*workerv1.Channel, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	return c.Channel(ctx, req.Jid, req.Invite)
}

func (g *SocialServer) CreateChannel(ctx context.Context, req *workerv1.CreateChannelRequest) (*workerv1.Channel, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	return c.CreateChannel(ctx, req)
}

func (g *SocialServer) ChannelAction(ctx context.Context, req *workerv1.ChannelActionRequest) (*workerv1.ChannelActionResponse, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	if err := c.ChannelAction(ctx, req.Jid, req.Action); err != nil {
		return nil, err
	}
	return &workerv1.ChannelActionResponse{}, nil
}

func (g *SocialServer) ChannelMessages(ctx context.Context, req *workerv1.ChannelMessagesRequest) (*workerv1.ChannelMessagesResponse, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	posts, err := c.ChannelMessages(ctx, req.Jid, int(req.Count), req.Before)
	if err != nil {
		return nil, err
	}
	return &workerv1.ChannelMessagesResponse{Posts: posts}, nil
}

func (g *SocialServer) ChannelReact(ctx context.Context, req *workerv1.ChannelReactRequest) (*workerv1.ChannelReactResponse, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	if err := c.ChannelReact(ctx, req.Jid, req.ServerId, req.Emoji); err != nil {
		return nil, err
	}
	return &workerv1.ChannelReactResponse{}, nil
}

func (g *SocialServer) LabelAction(ctx context.Context, req *workerv1.LabelActionRequest) (*workerv1.LabelActionResponse, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	if err := c.LabelAction(ctx, req); err != nil {
		return nil, err
	}
	return &workerv1.LabelActionResponse{}, nil
}

func (g *SocialServer) GetBusinessProfile(ctx context.Context, req *workerv1.GetBusinessProfileRequest) (*workerv1.BusinessProfileInfo, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}
	return c.BusinessProfile(ctx, req.Jid)
}

// socialHooks are the whatsmeow calls of the marco 10 methods; nil fields default to the
// client (tests replace them).
type socialHooks struct {
	statusPrivacy func(context.Context) ([]types.StatusPrivacy, error)
	subscribed    func(context.Context) ([]*types.NewsletterMetadata, error)
	channelInfo   func(context.Context, types.JID) (*types.NewsletterMetadata, error)
	channelInvite func(context.Context, string) (*types.NewsletterMetadata, error)
	createChannel func(context.Context, whatsmeow.CreateNewsletterParams) (*types.NewsletterMetadata, error)
	follow        func(context.Context, types.JID) error
	unfollow      func(context.Context, types.JID) error
	mute          func(context.Context, types.JID, bool) error
	channelPosts  func(context.Context, types.JID, *whatsmeow.GetNewsletterMessagesParams) ([]*types.NewsletterMessage, error)
	react         func(context.Context, types.JID, types.MessageServerID, string, types.MessageID) error
	uploadChannel func(context.Context, io.ReadSeeker, whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	bizProfile    func(context.Context, types.JID) (*types.BusinessProfile, error)
	// business reports whether the account is a WhatsApp Business account (the pairing
	// stored a business name).
	business func() bool
}

func (w *Whatsmeow) socialAPI() socialHooks {
	h := w.social
	if h.statusPrivacy == nil {
		h.statusPrivacy = w.cli.GetStatusPrivacy
	}
	if h.subscribed == nil {
		h.subscribed = w.cli.GetSubscribedNewsletters
	}
	if h.channelInfo == nil {
		h.channelInfo = w.cli.GetNewsletterInfo
	}
	if h.channelInvite == nil {
		h.channelInvite = w.cli.GetNewsletterInfoWithInvite
	}
	if h.createChannel == nil {
		h.createChannel = w.cli.CreateNewsletter
	}
	if h.follow == nil {
		h.follow = w.cli.FollowNewsletter
	}
	if h.unfollow == nil {
		h.unfollow = w.cli.UnfollowNewsletter
	}
	if h.mute == nil {
		h.mute = w.cli.NewsletterToggleMute
	}
	if h.channelPosts == nil {
		h.channelPosts = w.cli.GetNewsletterMessages
	}
	if h.react == nil {
		h.react = w.cli.NewsletterSendReaction
	}
	if h.uploadChannel == nil {
		h.uploadChannel = w.cli.UploadNewsletterReader
	}
	if h.bizProfile == nil {
		h.bizProfile = w.cli.GetBusinessProfile
	}
	if h.business == nil {
		h.business = func() bool { return w.cli.Store != nil && w.cli.Store.BusinessName != "" }
	}
	return h
}

// socialErr maps failures of the marco 10 calls onto worker.proto codes.
func socialErr(err error) error {
	var gql types.GraphQLErrors
	switch {
	case errors.Is(err, whatsmeow.ErrIQNotFound), errors.Is(err, whatsmeow.ErrIQGone):
		return grpcErr(codes.NotFound, "not_found", "not found")
	case errors.Is(err, whatsmeow.ErrIQForbidden), errors.Is(err, whatsmeow.ErrIQNotAuthorized),
		errors.Is(err, whatsmeow.ErrIQNotAllowed):
		return grpcErr(codes.PermissionDenied, "forbidden", "WhatsApp refused the request")
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit), errors.Is(err, whatsmeow.ErrIQResourceLimit):
		return grpcErr(codes.ResourceExhausted, "rate_limited", "WhatsApp rate limit reached; slow down")
	case errors.Is(err, whatsmeow.ErrIQBadRequest), errors.Is(err, whatsmeow.ErrIQNotAcceptable):
		return grpcErr(codes.InvalidArgument, "not_acceptable", "WhatsApp refused the request (value not accepted)")
	case errors.As(err, &gql):
		for _, e := range gql {
			switch e.Extensions.ErrorCode {
			case 404:
				return grpcErr(codes.NotFound, "not_found", "channel not found")
			case 401, 403:
				return grpcErr(codes.PermissionDenied, "forbidden", "WhatsApp refused the request")
			case 429:
				return grpcErr(codes.ResourceExhausted, "rate_limited", "WhatsApp rate limit reached; slow down")
			}
		}
		return grpcErr(codes.InvalidArgument, "not_acceptable", "WhatsApp refused the request: "+err.Error())
	case err != nil && strings.Contains(err.Error(), "no app state keys found"):
		return grpcErr(codes.FailedPrecondition, "app_state_not_synced",
			"label keys not synced from the phone yet; keep the phone online and try again shortly")
	}
	return sendErr(err)
}

func channelJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil || jid.User == "" || jid.Server != types.NewsletterServer {
		return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "a channel JID (…@newsletter) is required")
	}
	return jid, nil
}

func channelView(m *types.NewsletterMetadata) *workerv1.Channel {
	if m == nil {
		return nil
	}
	c := &workerv1.Channel{
		Jid: m.ID.String(), Name: m.ThreadMeta.Name.Text, Description: m.ThreadMeta.Description.Text,
		InviteCode: m.ThreadMeta.InviteCode, Subscribers: int64(m.ThreadMeta.SubscriberCount),
		Verified: m.ThreadMeta.VerificationState == types.NewsletterVerificationStateVerified,
		State:    string(m.State.Type),
	}
	if t := m.ThreadMeta.CreationTime.Time; !t.IsZero() {
		c.CreatedMs = t.UnixMilli()
	}
	if v := m.ViewerMeta; v != nil {
		c.Role, c.Muted = string(v.Role), v.Mute == types.NewsletterMuteOn
	}
	if p := m.ThreadMeta.Picture; p != nil && p.URL != "" {
		c.PictureUrl = p.URL
	} else if m.ThreadMeta.Preview.URL != "" {
		c.PictureUrl = m.ThreadMeta.Preview.URL
	}
	return c
}

func (w *Whatsmeow) StatusPrivacy(ctx context.Context) (*workerv1.StatusPrivacy, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	list, err := w.socialAPI().statusPrivacy(ctx)
	if err != nil {
		return nil, socialErr(err)
	}
	out := &workerv1.StatusPrivacy{Type: string(types.StatusPrivacyTypeContacts)}
	if len(list) > 0 { // the default setting is always first
		out.Type = string(list[0].Type)
		for _, j := range list[0].List {
			out.List = append(out.List, w.phoneJID(j, types.EmptyJID, nil).String())
		}
	}
	return out, nil
}

func (w *Whatsmeow) Channels(ctx context.Context) ([]*workerv1.Channel, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	list, err := w.socialAPI().subscribed(ctx)
	if err != nil {
		return nil, socialErr(err)
	}
	out := make([]*workerv1.Channel, 0, len(list))
	for _, m := range list {
		if c := channelView(m); c != nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func (w *Whatsmeow) Channel(ctx context.Context, jid, invite string) (*workerv1.Channel, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	if (jid == "") == (invite == "") {
		return nil, invalid("exactly one of jid or invite is required")
	}
	h := w.socialAPI()
	var m *types.NewsletterMetadata
	var err error
	if jid != "" {
		j, perr := channelJID(jid)
		if perr != nil {
			return nil, perr
		}
		m, err = h.channelInfo(ctx, j)
	} else {
		code := strings.TrimSpace(invite)
		code = strings.TrimPrefix(strings.TrimPrefix(code, "https://"), "whatsapp.com/channel/")
		if code == "" || strings.ContainsAny(code, "/?# ") {
			return nil, invalid("invalid channel invite")
		}
		m, err = h.channelInvite(ctx, code)
	}
	if err != nil {
		return nil, socialErr(err)
	}
	if m == nil {
		return nil, grpcErr(codes.NotFound, "not_found", "channel not found")
	}
	return channelView(m), nil
}

// Limits of a new channel, as in WhatsApp's apps.
const (
	maxChannelName = 100
	maxChannelDesc = 2048
)

func (w *Whatsmeow) CreateChannel(ctx context.Context, req *workerv1.CreateChannelRequest) (*workerv1.Channel, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > maxChannelName || len([]rune(req.Description)) > maxChannelDesc {
		return nil, invalid("channel name is required (up to 100 characters; description up to 2048)")
	}
	m, err := w.socialAPI().createChannel(ctx, whatsmeow.CreateNewsletterParams{Name: name, Description: req.Description, Picture: req.Picture})
	if err != nil {
		return nil, socialErr(err)
	}
	if m == nil {
		return nil, grpcErr(codes.Unknown, "worker_error", "WhatsApp did not return the new channel")
	}
	return channelView(m), nil
}

func (w *Whatsmeow) ChannelAction(ctx context.Context, raw, action string) error {
	if err := w.loggedIn(); err != nil {
		return err
	}
	jid, err := channelJID(raw)
	if err != nil {
		return err
	}
	h := w.socialAPI()
	switch action {
	case "follow":
		err = h.follow(ctx, jid)
	case "unfollow":
		err = h.unfollow(ctx, jid)
	case "mute", "unmute":
		err = h.mute(ctx, jid, action == "mute")
	default:
		return invalid("action must be follow, unfollow, mute or unmute")
	}
	if err != nil {
		return socialErr(err)
	}
	return nil
}

func (w *Whatsmeow) ChannelMessages(ctx context.Context, raw string, count int, before int64) ([]*workerv1.ChannelPost, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	jid, err := channelJID(raw)
	if err != nil {
		return nil, err
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	msgs, err := w.socialAPI().channelPosts(ctx, jid, &whatsmeow.GetNewsletterMessagesParams{Count: count, Before: types.MessageServerID(before)})
	if err != nil {
		return nil, socialErr(err)
	}
	out := make([]*workerv1.ChannelPost, 0, len(msgs))
	for _, m := range msgs {
		p := channelPost(jid, int64(m.MessageServerID), string(m.MessageID), m.Timestamp, m.Message)
		p.Views = int64(m.ViewsCount)
		if len(m.ReactionCounts) > 0 {
			p.Reactions = map[string]int64{}
			for k, v := range m.ReactionCounts {
				p.Reactions[k] = int64(v)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// channelPost describes a channel message; media of channels is not encrypted, so it
// carries no download ref.
func channelPost(jid types.JID, serverID int64, id string, ts time.Time, m *waE2E.Message) *workerv1.ChannelPost {
	msg := &workerv1.Message{Id: id, Chat: jid.String(), Sender: jid.String(), TimestampMs: ts.UnixMilli(), Type: "unknown"}
	if m != nil {
		if typ, text, media, reactionTo, ok := content(m); ok {
			msg.Type, msg.Text, msg.Media, msg.ReactionTo = typ, text, media, reactionTo
			if msg.Media != nil {
				msg.Media.Ref = nil
			}
			details(m, msg)
		}
	}
	return &workerv1.ChannelPost{Channel: jid.String(), ServerId: serverID, Message: msg}
}

func (w *Whatsmeow) ChannelReact(ctx context.Context, raw string, serverID int64, emoji string) error {
	if err := w.loggedIn(); err != nil {
		return err
	}
	jid, err := channelJID(raw)
	if err != nil {
		return err
	}
	if serverID <= 0 {
		return invalid("server_id is required")
	}
	if err := w.socialAPI().react(ctx, jid, types.MessageServerID(serverID), emoji, ""); err != nil {
		return socialErr(err)
	}
	return nil
}

func (w *Whatsmeow) LabelAction(ctx context.Context, req *workerv1.LabelActionRequest) error {
	if err := w.loggedIn(); err != nil {
		return err
	}
	if !w.socialAPI().business() {
		return grpcErr(codes.FailedPrecondition, "business_account_required", "labels need a WhatsApp Business account")
	}
	id := strings.TrimSpace(req.LabelId)
	if id == "" || strings.ContainsAny(id, " \x00") {
		return invalid("label_id is required")
	}
	var patch appstate.PatchInfo
	switch req.Kind {
	case "edit":
		if !req.Deleted && strings.TrimSpace(req.Name) == "" {
			return invalid("label name is required")
		}
		patch = appstate.BuildLabelEdit(id, req.Name, req.Color, req.Deleted)
	case "chat", "message":
		chat, err := parseChat(req.Chat)
		if err != nil {
			return err
		}
		target := w.appStateJID(ctx, chat)
		if req.Kind == "chat" {
			patch = appstate.BuildLabelChat(target, id, req.Labeled)
		} else {
			if req.MessageId == "" {
				return invalid("message_id is required")
			}
			patch = appstate.BuildLabelMessage(target, id, req.MessageId, req.Labeled)
		}
	default:
		return invalid("kind must be edit, chat or message")
	}
	if err := w.hooks().sendAppState(ctx, patch); err != nil {
		return socialErr(err)
	}
	return nil
}

func (w *Whatsmeow) BusinessProfile(ctx context.Context, raw string) (*workerv1.BusinessProfileInfo, error) {
	pn, _ := w.ownJIDs()
	if pn.IsEmpty() {
		return nil, grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	}
	target := pn
	own := raw == ""
	if !own {
		j, err := parseUser(raw)
		if err != nil {
			return nil, err
		}
		target = j
	} else if !w.socialAPI().business() {
		return nil, grpcErr(codes.FailedPrecondition, "business_account_required", "not a WhatsApp Business account")
	}
	bp, err := w.socialAPI().bizProfile(ctx, target)
	if err != nil {
		return nil, socialErr(err)
	}
	if bp == nil {
		return nil, grpcErr(codes.NotFound, "not_found", "no business profile")
	}
	out := &workerv1.BusinessProfileInfo{Jid: target.String(), Address: bp.Address, Email: bp.Email,
		Timezone: bp.BusinessHoursTimeZone, Options: bp.ProfileOptions}
	for _, c := range bp.Categories {
		out.Categories = append(out.Categories, c.Name)
	}
	for _, h := range bp.BusinessHours {
		out.Hours = append(out.Hours, &workerv1.BusinessHours{Day: h.DayOfWeek, Mode: h.Mode, Open: h.OpenTime, Close: h.CloseTime})
	}
	if own && w.cli.Store != nil {
		out.BusinessName = w.cli.Store.BusinessName
	}
	return out, nil
}

// ---- sending to status@broadcast and to channels (Worker.Send) ----

// statusTypes are what a status may be: WhatsApp's apps post text, photos and videos.
func statusType(typ string) bool { return typ == "text" || typ == "image" || typ == "video" }

// statusMessage gives a text status its look (background, text color, font).
func statusMessage(req *workerv1.SendRequest, msg *waE2E.Message) *waE2E.Message {
	if req.Type != "text" {
		return msg
	}
	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		ext = &waE2E.ExtendedTextMessage{Text: proto.String(req.Text)}
	}
	st := req.StatusStyle
	bg, fg, font := uint32(0xFF1E6E4F), uint32(0xFFFFFFFF), int32(0)
	if st != nil {
		if st.BackgroundArgb != 0 {
			bg = st.BackgroundArgb
		}
		if st.TextArgb != 0 {
			fg = st.TextArgb
		}
		font = st.Font
	}
	ext.BackgroundArgb, ext.TextArgb = proto.Uint32(bg), proto.Uint32(fg)
	ext.Font = waE2E.ExtendedTextMessage_FontType(font).Enum()
	return &waE2E.Message{ExtendedTextMessage: ext}
}

// buildChannel builds a channel post: channel media is uploaded without encryption and
// referenced by the upload handle (returned for SendRequestExtra.MediaHandle).
func (w *Whatsmeow) buildChannel(ctx context.Context, req *workerv1.SendRequest) (*waE2E.Message, string, error) {
	if req.Quote != nil || len(req.Mentions) > 0 || req.MentionAll {
		return nil, "", invalid("replies and mentions are not supported in channels")
	}
	switch req.Type {
	case "text":
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(req.Text)}}, "", nil
	case "image", "video", "document", "audio":
	default:
		return nil, "", invalid("channels accept text, image, video, audio and document posts")
	}
	mt, _ := mediaType(req.Type)
	path, err := w.dataPath(req.MediaPath, "outbox")
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", invalid("media file is missing")
	}
	up, err := w.socialAPI().uploadChannel(ctx, f, mt)
	f.Close()
	if err != nil {
		return nil, "", grpcErr(codes.Unavailable, "media_upload_failed", err.Error())
	}
	msg := buildMedia(req, up)
	// Channel media is plain: no key, no encrypted hash.
	switch {
	case msg.ImageMessage != nil:
		msg.ImageMessage.MediaKey, msg.ImageMessage.FileEncSHA256 = nil, nil
	case msg.VideoMessage != nil:
		msg.VideoMessage.MediaKey, msg.VideoMessage.FileEncSHA256 = nil, nil
	case msg.AudioMessage != nil:
		msg.AudioMessage.MediaKey, msg.AudioMessage.FileEncSHA256 = nil, nil
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.MediaKey, msg.DocumentMessage.FileEncSHA256 = nil, nil
	}
	return msg, up.Handle, nil
}

// buildSpecial builds a message to status@broadcast or to a channel; handled=false for
// any other recipient.
func (w *Whatsmeow) buildSpecial(ctx context.Context, to types.JID, req *workerv1.SendRequest) (msg *waE2E.Message, handle string, handled bool, err error) {
	switch {
	case to == types.StatusBroadcastJID:
		if req.Type == "revoke" {
			if !req.TargetFromMe || req.TargetId == "" {
				return nil, "", true, invalid("only our own status can be deleted")
			}
			own, _ := w.ownJIDs()
			return w.cli.BuildRevoke(to, own, types.MessageID(req.TargetId)), "", true, nil
		}
		if !statusType(req.Type) {
			return nil, "", true, invalid("a status is a text, image or video")
		}
		if req.Quote != nil || len(req.Mentions) > 0 || req.MentionAll {
			return nil, "", true, invalid("replies and mentions are not supported in a status")
		}
		m, err := w.build(ctx, to, req)
		if err != nil {
			return nil, "", true, err
		}
		return statusMessage(req, m), "", true, nil
	case to.Server == types.NewsletterServer:
		m, h, err := w.buildChannel(ctx, req)
		return m, h, true, err
	}
	return nil, "", false, nil
}

// ---- events ----

// onSocial turns status posts, channel posts and label changes into events; it reports
// whether evt was one of them. It runs before onData, which drops broadcast and channel
// messages.
func (w *Whatsmeow) onSocial(evt any) bool {
	switch e := evt.(type) {
	case *events.Message:
		switch {
		case e.Info.Chat == types.StatusBroadcastJID:
			if m := w.statusPost(e); m != nil {
				w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Status{Status: m}})
			}
			return true
		case e.Info.Chat.Server == types.NewsletterServer:
			if e.Message != nil && e.Message.GetProtocolMessage() == nil && e.Message.GetReactionMessage() == nil {
				p := channelPost(e.Info.Chat.ToNonAD(), int64(e.Info.ServerID), string(e.Info.ID), e.Info.Timestamp, e.Message)
				p.Message.PushName = e.Info.PushName
				w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_ChannelPost{ChannelPost: p}})
			}
			return true
		case e.Info.Chat.Server == types.BroadcastServer:
			return true // broadcast lists stay out of scope
		}
	case *events.LabelEdit:
		u := &workerv1.LabelUpdate{Kind: "edit", LabelId: e.LabelID, Name: e.Action.GetName(), Color: e.Action.GetColor(),
			Deleted: e.Action.GetDeleted(), PredefinedId: e.Action.GetPredefinedID(),
			TimestampMs: e.Timestamp.UnixMilli(), FullSync: e.FromFullSync}
		w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Label{Label: u}})
		return true
	case *events.LabelAssociationChat:
		if chat := w.chatUpdateJID(e.JID); chat != "" {
			u := &workerv1.LabelUpdate{Kind: "chat", LabelId: e.LabelID, Chat: chat, Labeled: e.Action.GetLabeled(),
				TimestampMs: e.Timestamp.UnixMilli(), FullSync: e.FromFullSync}
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Label{Label: u}})
		}
		return true
	case *events.LabelAssociationMessage:
		if chat := w.chatUpdateJID(e.JID); chat != "" {
			u := &workerv1.LabelUpdate{Kind: "message", LabelId: e.LabelID, Chat: chat, MessageId: e.MessageID,
				Labeled: e.Action.GetLabeled(), TimestampMs: e.Timestamp.UnixMilli(), FullSync: e.FromFullSync}
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Label{Label: u}})
		}
		return true
	}
	return false
}

// statusPost normalizes a contact's status post (nil: ours, a protocol message such as
// the author deleting it, or a type we do not show).
func (w *Whatsmeow) statusPost(e *events.Message) *workerv1.Message {
	if e.Info.IsFromMe || e.Message == nil || e.Message.GetProtocolMessage() != nil {
		return nil
	}
	typ, text, media, reactionTo, ok := content(e.Message)
	if !ok || typ == "reaction" || typ == "unknown" {
		return nil
	}
	sender := w.phoneJID(e.Info.Sender, e.Info.SenderAlt, nil)
	out := &workerv1.Message{
		Id: string(e.Info.ID), Chat: types.StatusBroadcastJID.String(), Sender: sender.String(),
		TimestampMs: e.Info.Timestamp.UnixMilli(), PushName: e.Info.PushName,
		Type: typ, Text: text, Media: media, ReactionTo: reactionTo,
	}
	details(e.Message, out)
	return out
}
