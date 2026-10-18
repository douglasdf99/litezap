package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SocialWorkers gives the clients of a running worker (manager.Manager).
type SocialWorkers interface {
	Social(instanceID string) (workerv1.SocialClient, error)
	Client(instanceID string) (workerv1.WorkerClient, error)
}

// Social holds the marco 10 use cases: status (stories), channels (newsletters),
// WhatsApp Business labels and the business profile. Status posts and channel posts go
// through the send queue (pacing, send window, ramp, operator suspension); the other
// writes are synchronous and rate limited per instance.
type Social struct {
	Store     *store.Store
	Workers   SocialWorkers
	Messaging *Messaging // the send queue (status and channel posts, status replies)
	Sending   *Sending   // nil: no operator suspension
	Fetcher   *media.Fetcher
	Events    events.Publisher // label.updated for API changes (nil: not published)
	// Now is the clock of the quotas (tests); nil means time.Now.
	Now func() time.Time

	quota    slidingQuota // per instance and kind (follow, create, react)
	bizMu    sync.Mutex
	business map[string]bizEntry // instance -> whether it is a business account
	labelMu  sync.Mutex          // label creation picks the next id
}

type bizEntry struct {
	business bool
	at       time.Time
}

// Limits (anti-ban). Statuses reach every contact: at most maxStatusPerDay per rolling
// day, on top of the queue's pacing. Channel actions are counted per instance.
const (
	maxStatusPerDay      = 30
	followsPerMinute     = 3
	followsPerDay        = 30
	createsPerMinute     = 1
	createsPerDay        = 2
	reactionsPerMinute   = 30
	reactionsPerDay      = 500
	maxLabelName         = 100
	maxLabelColor        = 19 // WhatsApp's palette has 20 colors (0-19)
	maxChannelPicture    = maxPictureUpload
	businessCacheTTL     = 10 * time.Minute
	socialRPCTimeout     = 30 * time.Second
	defaultStatusBGColor = 0xFF1E6E4F
)

func (s *Social) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func socialCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, socialRPCTimeout)
}

// socialErr renders a Social RPC failure (codes in social.proto) as an API error.
func socialErr(err error) error {
	if errors.Is(err, manager.ErrNoSocial) {
		return apierr.New(http.StatusNotImplemented, "not_implemented", "this worker does not support status, channels and labels", false)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() == codes.OK {
		return mapErr(err)
	}
	code, detail, _ := strings.Cut(st.Message(), ": ")
	switch {
	case code == "business_account_required":
		return businessRequired()
	case code == "app_state_not_synced":
		return apierr.New(http.StatusConflict, "app_state_not_synced", detail, true)
	case st.Code() == codes.NotFound && code == "not_found":
		return apierr.New(http.StatusNotFound, "not_found", redact(detail), false)
	case st.Code() == codes.PermissionDenied:
		return apierr.New(http.StatusForbidden, "forbidden", redact(detail), false)
	case st.Code() == codes.ResourceExhausted:
		return apierr.New(http.StatusTooManyRequests, "rate_limited", "WhatsApp rate limit reached; slow down", true)
	case st.Code() == codes.InvalidArgument && code == "not_acceptable":
		return apierr.New(http.StatusBadRequest, "not_acceptable", redact(detail), false)
	case st.Code() == codes.Unimplemented:
		return apierr.New(http.StatusNotImplemented, "not_implemented", "this worker does not support status, channels and labels", false)
	}
	return mapErr(err)
}

func businessRequired() error {
	return apierr.New(http.StatusConflict, "business_account_required", "this needs a WhatsApp Business account", false)
}

func socialLimited(what string) error {
	return apierr.New(http.StatusTooManyRequests, "rate_limited", what+" limit reached for this instance; try again later", true)
}

func (s *Social) client(ctx context.Context, instanceID string) (workerv1.SocialClient, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	cli, err := s.Workers.Social(instanceID)
	if err != nil {
		return nil, socialErr(err)
	}
	return cli, nil
}

func (s *Social) checkSending(instanceID string) error {
	if s.Sending == nil {
		return nil
	}
	return s.Sending.Check(instanceID)
}

func (s *Social) take(kind, instanceID string, perMinute, perDay int) bool {
	return s.quota.take(kind+"\x00"+instanceID, s.now(), perMinute, perDay)
}

// ---- status ----

// StatusInput is the body of POST /status: a text, image or video story. It goes to the
// contacts allowed by the status privacy set on the phone.
type StatusInput struct {
	Type            string      `json:"type"` // text | image | video
	Text            string      `json:"text,omitempty"`
	Caption         string      `json:"caption,omitempty"`
	Media           *MediaInput `json:"media,omitempty"`
	BackgroundColor string      `json:"background_color,omitempty"` // text: "#RRGGBB"
	TextColor       string      `json:"text_color,omitempty"`       // text: "#RRGGBB"
	Font            int         `json:"font,omitempty"`             // text: 0-9 (WhatsApp's fonts)
	ClientMessageID string      `json:"client_message_id,omitempty"`
	ExpiresAfter    string      `json:"expires_after,omitempty"`
}

// parseColor reads "#RRGGBB" (or "RRGGBB") as an opaque ARGB color; "" is 0 (default).
func parseColor(c string) (uint32, error) {
	c = strings.TrimPrefix(strings.TrimSpace(c), "#")
	if c == "" {
		return 0, nil
	}
	if len(c) != 6 {
		return 0, apierr.Validation("colors are #RRGGBB")
	}
	v, err := strconv.ParseUint(c, 16, 32)
	if err != nil {
		return 0, apierr.Validation("colors are #RRGGBB")
	}
	return 0xFF000000 | uint32(v), nil
}

// PostStatus queues a status post (spec M10: paced like any send, at most 30 per day).
func (s *Social) PostStatus(ctx context.Context, instanceID string, in StatusInput, wait time.Duration) (MessageView, error) {
	if err := s.checkSending(instanceID); err != nil {
		return MessageView{}, err
	}
	if s.Messaging == nil {
		return MessageView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "status posting is not available", false)
	}
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessageView{}, mapErr(err)
	}
	send := SendInput{Type: in.Type, Text: in.Text, Caption: in.Caption, Media: in.Media,
		ClientMessageID: in.ClientMessageID, ExpiresAfter: in.ExpiresAfter}
	switch in.Type {
	case "text":
		if in.Media != nil || in.Caption != "" {
			return MessageView{}, apierr.Validation("a text status has no media or caption")
		}
		if utf8.RuneCountInString(in.Text) > 700 {
			return MessageView{}, apierr.Validation("a text status is limited to 700 characters")
		}
		bg, err := parseColor(in.BackgroundColor)
		if err != nil {
			return MessageView{}, err
		}
		fg, err := parseColor(in.TextColor)
		if err != nil {
			return MessageView{}, err
		}
		if in.Font < 0 || in.Font > 9 {
			return MessageView{}, apierr.Validation("font must be between 0 and 9")
		}
		if bg == 0 {
			bg = defaultStatusBGColor
		}
		send.statusStyle = &queue.StatusStyle{BackgroundARGB: bg, TextARGB: fg, Font: int32(in.Font)}
	case "image", "video":
		if in.Text != "" {
			return MessageView{}, apierr.Validation("use caption for the text of an image or video status")
		}
		if in.BackgroundColor != "" || in.TextColor != "" || in.Font != 0 {
			return MessageView{}, apierr.Validation("colors and font only apply to a text status")
		}
	default:
		return MessageView{}, apierr.Validation("type must be text, image or video")
	}
	// Idempotent retries return the existing item before the daily cap is checked.
	if in.ClientMessageID == "" || !s.knownClientID(ctx, instanceID, in.ClientMessageID) {
		n, err := s.Store.CountOutboundSince(ctx, instanceID, store.StatusChat, s.now().Add(-24*time.Hour))
		if err != nil {
			return MessageView{}, err
		}
		if n >= maxStatusPerDay {
			return MessageView{}, socialLimited("daily status (30 per day)")
		}
	}
	return s.Messaging.send(ctx, instanceID, send, store.StatusChat, wait)
}

func (s *Social) knownClientID(ctx context.Context, instanceID, id string) bool {
	_, err := s.Store.OutboundByClientID(ctx, instanceID, id)
	return err == nil
}

// DeleteStatus deletes one of our status posts for everyone (queued as a revoke).
func (s *Social) DeleteStatus(ctx context.Context, instanceID, msgID string, wait time.Duration) (MessageView, error) {
	if err := s.checkSending(instanceID); err != nil {
		return MessageView{}, err
	}
	if s.Messaging == nil {
		return MessageView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "status posting is not available", false)
	}
	m, err := s.Messaging.findInChat(ctx, instanceID, store.StatusChat, msgID)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("status")
	}
	if err != nil {
		return MessageView{}, err
	}
	if !m.FromMe {
		return MessageView{}, apierr.Validation("only your own status posts can be deleted")
	}
	return s.Messaging.send(ctx, instanceID, SendInput{Type: "revoke", MessageID: m.ID}, store.StatusChat, wait)
}

// StatusReplyInput is the body of POST /status/{msg_id}/reply.
type StatusReplyInput struct {
	Text            string `json:"text"`
	ClientMessageID string `json:"client_message_id,omitempty"`
}

// ReplyStatus answers a contact's status: a text to its author, quoting the status.
func (s *Social) ReplyStatus(ctx context.Context, instanceID, msgID string, in StatusReplyInput, wait time.Duration) (MessageView, error) {
	if err := s.checkSending(instanceID); err != nil {
		return MessageView{}, err
	}
	if s.Messaging == nil {
		return MessageView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "status replies are not available", false)
	}
	m, err := s.Messaging.findInChat(ctx, instanceID, store.StatusChat, msgID)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("status")
	}
	if err != nil {
		return MessageView{}, err
	}
	if m.FromMe || m.SenderJID == "" {
		return MessageView{}, apierr.Validation("only a contact's status can be answered")
	}
	q := &queue.QuoteTarget{WAMessageID: m.WAMessageID, Sender: m.SenderJID, Text: clip(m.Text, maxQuoteText), RemoteJID: store.StatusChat}
	if q.Text == "" && m.Type != "text" {
		q.Text = m.Type
	}
	send := SendInput{To: m.SenderJID, Type: "text", Text: in.Text, ClientMessageID: in.ClientMessageID, statusQuote: q}
	return s.Messaging.send(ctx, instanceID, send, "", wait)
}

// StatusUpdates lists the statuses in the log, newest first: contacts' posts (received
// while the instance was connected) and ours.
func (s *Social) StatusUpdates(ctx context.Context, instanceID string, limit int, cursor string) (MessagePage, error) {
	if s.Messaging == nil {
		return MessagePage{}, apierr.New(http.StatusNotImplemented, "not_implemented", "not available", false)
	}
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessagePage{}, mapErr(err)
	}
	return s.Messaging.chatMessages(ctx, instanceID, store.StatusChat, limit, cursor)
}

// StatusPrivacyView is who receives the account's status posts (changed on the phone).
type StatusPrivacyView struct {
	Type string   `json:"type"` // contacts | blacklist (contacts except list) | whitelist (only list)
	List []string `json:"list"`
}

func (s *Social) StatusPrivacy(ctx context.Context, instanceID string) (StatusPrivacyView, error) {
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return StatusPrivacyView{}, err
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	p, err := cli.GetStatusPrivacy(c, &workerv1.GetStatusPrivacyRequest{})
	if err != nil {
		return StatusPrivacyView{}, socialErr(err)
	}
	return StatusPrivacyView{Type: p.Type, List: nonNil(p.List)}, nil
}

// ---- channels ----

// ChannelView is a WhatsApp channel (newsletter).
type ChannelView struct {
	JID         string     `json:"jid"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	InviteCode  string     `json:"invite_code,omitempty"`
	InviteLink  string     `json:"invite_link,omitempty"`
	Subscribers int64      `json:"subscribers"`
	Verified    bool       `json:"verified,omitempty"`
	State       string     `json:"state,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	// Role is the account's role (owner | admin | subscriber | guest; "" unknown).
	Role       string `json:"role,omitempty"`
	Muted      bool   `json:"muted"`
	PictureURL string `json:"picture_url,omitempty"`
}

func channelView(c *workerv1.Channel) ChannelView {
	v := ChannelView{JID: c.Jid, Name: c.Name, Description: c.Description, InviteCode: c.InviteCode,
		Subscribers: c.Subscribers, Verified: c.Verified, State: c.State, CreatedAt: ms(c.CreatedMs),
		Role: c.Role, Muted: c.Muted, PictureURL: c.PictureUrl}
	if c.InviteCode != "" {
		v.InviteLink = "https://whatsapp.com/channel/" + c.InviteCode
	}
	return v
}

// ChannelJID normalizes a channel id: "<id>@newsletter" or the bare numeric id.
func ChannelJID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	user, server, ok := strings.Cut(raw, "@")
	if ok && server != "newsletter" {
		return "", apierr.Validation("a channel JID ends with @newsletter")
	}
	if user == "" || strings.Trim(user, "0123456789") != "" {
		return "", apierr.Validation("invalid channel id")
	}
	return user + "@newsletter", nil
}

func (s *Social) Channels(ctx context.Context, instanceID string) ([]ChannelView, error) {
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	resp, err := cli.ListChannels(c, &workerv1.ListChannelsRequest{})
	if err != nil {
		return nil, socialErr(err)
	}
	out := make([]ChannelView, 0, len(resp.Channels))
	for _, ch := range resp.Channels {
		out = append(out, channelView(ch))
	}
	return out, nil
}

// Channel reads a channel by JID, or by invite code/link when invite is set.
func (s *Social) Channel(ctx context.Context, instanceID, jidRaw, invite string) (ChannelView, error) {
	req := &workerv1.GetChannelRequest{}
	if invite != "" {
		code := strings.TrimSpace(invite)
		code = strings.TrimPrefix(strings.TrimPrefix(code, "https://"), "whatsapp.com/channel/")
		if code == "" || len(code) > 64 || strings.ContainsAny(code, "/?#@ ") {
			return ChannelView{}, apierr.Validation("invite must be a channel invite code or link")
		}
		req.Invite = code
	} else {
		jid, err := ChannelJID(jidRaw)
		if err != nil {
			return ChannelView{}, err
		}
		req.Jid = jid
	}
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return ChannelView{}, err
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	ch, err := cli.GetChannel(c, req)
	if err != nil {
		return ChannelView{}, socialErr(err)
	}
	return channelView(ch), nil
}

// CreateChannelInput creates a channel owned by the account.
type CreateChannelInput struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Picture     *PhotoInput `json:"picture,omitempty"`
}

func (s *Social) CreateChannel(ctx context.Context, instanceID string, in CreateChannelInput) (ChannelView, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(in.Description) > 2048 {
		return ChannelView{}, apierr.Validation("name is required (up to 100 characters; description up to 2048)")
	}
	if err := s.checkSending(instanceID); err != nil {
		return ChannelView{}, err
	}
	var pic []byte
	if in.Picture != nil {
		var err error
		if pic, err = s.photo(ctx, in.Picture); err != nil {
			return ChannelView{}, err
		}
	}
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return ChannelView{}, err
	}
	if !s.take("channel_create", instanceID, createsPerMinute, createsPerDay) {
		return ChannelView{}, socialLimited("channel creation (2 per day)")
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	ch, err := cli.CreateChannel(c, &workerv1.CreateChannelRequest{Name: name, Description: in.Description, Picture: pic})
	if err != nil {
		return ChannelView{}, socialErr(err)
	}
	return channelView(ch), nil
}

// photo decodes a picture input (base64 or URL) into a square JPEG.
func (s *Social) photo(ctx context.Context, in *PhotoInput) ([]byte, error) {
	var data []byte
	var err error
	switch {
	case in.Base64 != "" && in.URL != "":
		return nil, apierr.Validation("give base64 or url, not both")
	case in.Base64 != "":
		raw := in.Base64
		if _, after, ok := strings.Cut(raw, ","); ok && strings.HasPrefix(raw, "data:") {
			raw = after
		}
		if data, err = base64.StdEncoding.DecodeString(raw); err != nil {
			return nil, apierr.Validation("picture.base64 is not valid")
		}
	case in.URL != "":
		if s.Fetcher == nil {
			return nil, apierr.Validation("picture URLs are not supported here; send base64")
		}
		c, cancel := socialCtx(ctx)
		defer cancel()
		if data, _, err = s.Fetcher.Fetch(c, in.URL, maxChannelPicture); err != nil {
			if errors.Is(err, media.ErrTooLarge) {
				return nil, apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", "the picture is limited to 10MB", false)
			}
			return nil, apierr.New(http.StatusBadRequest, "media_fetch_failed", "could not download the picture", false)
		}
	default:
		return nil, apierr.Validation("picture needs base64 or url")
	}
	if len(data) > maxChannelPicture {
		return nil, apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", "the picture is limited to 10MB", false)
	}
	out, err := squareJPEG(data, profilePicSide)
	if err != nil {
		return nil, apierr.Validation("picture: " + err.Error())
	}
	return out, nil
}

// ChannelAction follows, unfollows, mutes or unmutes a channel. Follows and unfollows
// are rate limited (3 per minute, 30 per day).
func (s *Social) ChannelAction(ctx context.Context, instanceID, jidRaw, action string) error {
	jid, err := ChannelJID(jidRaw)
	if err != nil {
		return err
	}
	switch action {
	case "follow", "unfollow", "mute", "unmute":
	default:
		return apierr.Validation("action must be follow, unfollow, mute or unmute")
	}
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return err
	}
	if action == "follow" || action == "unfollow" {
		if err := s.checkSending(instanceID); err != nil {
			return err
		}
		if !s.take("channel_follow", instanceID, followsPerMinute, followsPerDay) {
			return socialLimited("channel follow (3 per minute, 30 per day)")
		}
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	if _, err := cli.ChannelAction(c, &workerv1.ChannelActionRequest{Jid: jid, Action: action}); err != nil {
		return socialErr(err)
	}
	return nil
}

// ChannelPostView is a message of a channel.
type ChannelPostView struct {
	ServerID    int64            `json:"server_id"`
	WAMessageID string           `json:"wa_message_id,omitempty"`
	Type        string           `json:"type"`
	Text        string           `json:"text,omitempty"`
	Media       *store.MediaInfo `json:"media,omitempty"`
	Views       int64            `json:"views"`
	Reactions   map[string]int64 `json:"reactions,omitempty"`
	Timestamp   time.Time        `json:"timestamp"`
}

func channelPostView(p *workerv1.ChannelPost) ChannelPostView {
	m := p.GetMessage()
	v := ChannelPostView{ServerID: p.ServerId, WAMessageID: m.GetId(), Type: m.GetType(), Text: m.GetText(),
		Views: p.Views, Reactions: p.Reactions}
	if m.GetTimestampMs() > 0 {
		v.Timestamp = time.UnixMilli(m.GetTimestampMs()).UTC()
	}
	if md := m.GetMedia(); md != nil {
		v.Media = &store.MediaInfo{Kind: md.Kind, Mime: md.Mime, Size: int64(md.Size), FileName: md.FileName, Seconds: int(md.Seconds)}
	}
	return v
}

// ChannelMessages reads a channel's latest messages (count 1-100, default 20), older than
// the server id before when set.
func (s *Social) ChannelMessages(ctx context.Context, instanceID, jidRaw string, count int, before int64) ([]ChannelPostView, error) {
	jid, err := ChannelJID(jidRaw)
	if err != nil {
		return nil, err
	}
	if count < 0 || count > 100 || before < 0 {
		return nil, apierr.Validation("count must be between 1 and 100")
	}
	if count == 0 {
		count = 20
	}
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	resp, err := cli.ChannelMessages(c, &workerv1.ChannelMessagesRequest{Jid: jid, Count: int32(count), Before: before})
	if err != nil {
		return nil, socialErr(err)
	}
	out := make([]ChannelPostView, 0, len(resp.Posts))
	for _, p := range resp.Posts {
		out = append(out, channelPostView(p))
	}
	return out, nil
}

// ChannelReact reacts to a channel message ("" removes the reaction); 30 per minute.
func (s *Social) ChannelReact(ctx context.Context, instanceID, jidRaw string, serverID int64, emoji string) error {
	jid, err := ChannelJID(jidRaw)
	if err != nil {
		return err
	}
	if serverID <= 0 {
		return apierr.Validation("server_id is required")
	}
	if utf8.RuneCountInString(emoji) > 10 {
		return apierr.Validation("emoji must be a single emoji")
	}
	if err := s.checkSending(instanceID); err != nil {
		return err
	}
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return err
	}
	if !s.take("channel_react", instanceID, reactionsPerMinute, reactionsPerDay) {
		return socialLimited("channel reaction (30 per minute)")
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	if _, err := cli.ChannelReact(c, &workerv1.ChannelReactRequest{Jid: jid, ServerId: serverID, Emoji: emoji}); err != nil {
		return socialErr(err)
	}
	return nil
}

// ChannelPostInput is the body of POST /channels/{jid}/messages.
type ChannelPostInput struct {
	Type            string      `json:"type"` // text | image | video | audio | document
	Text            string      `json:"text,omitempty"`
	Caption         string      `json:"caption,omitempty"`
	Media           *MediaInput `json:"media,omitempty"`
	ClientMessageID string      `json:"client_message_id,omitempty"`
	ExpiresAfter    string      `json:"expires_after,omitempty"`
}

// PublishChannel queues a post in a channel the account owns or administers.
func (s *Social) PublishChannel(ctx context.Context, instanceID, jidRaw string, in ChannelPostInput, wait time.Duration) (MessageView, error) {
	jid, err := ChannelJID(jidRaw)
	if err != nil {
		return MessageView{}, err
	}
	switch in.Type {
	case "text", "image", "video", "audio", "document":
	default:
		return MessageView{}, apierr.Validation("type must be text, image, video, audio or document")
	}
	if err := s.checkSending(instanceID); err != nil {
		return MessageView{}, err
	}
	if s.Messaging == nil {
		return MessageView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "channel posting is not available", false)
	}
	if in.ClientMessageID == "" || !s.knownClientID(ctx, instanceID, in.ClientMessageID) {
		ch, err := s.Channel(ctx, instanceID, jid, "")
		if err != nil {
			return MessageView{}, err
		}
		if ch.Role != "owner" && ch.Role != "admin" {
			return MessageView{}, apierr.New(http.StatusForbidden, "forbidden", "only the channel's owner or admins can post", false)
		}
	}
	send := SendInput{Type: in.Type, Text: in.Text, Caption: in.Caption, Media: in.Media,
		ClientMessageID: in.ClientMessageID, ExpiresAfter: in.ExpiresAfter}
	return s.Messaging.send(ctx, instanceID, send, jid, wait)
}

// ---- labels (WhatsApp Business) ----

// LabelView is a WhatsApp Business label.
type LabelView struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Color int      `json:"color"` // index in WhatsApp's palette (0-19)
	Chats []string `json:"chats"`
}

func labelView(l store.Label) LabelView {
	return LabelView{ID: l.ID, Name: l.Name, Color: l.Color, Chats: nonNil(l.Chats)}
}

// isBusiness reports whether the instance is a WhatsApp Business account (from the
// worker's profile, cached for a few minutes).
func (s *Social) isBusiness(ctx context.Context, instanceID string) (bool, error) {
	s.bizMu.Lock()
	e, ok := s.business[instanceID]
	s.bizMu.Unlock()
	if ok && s.now().Sub(e.at) < businessCacheTTL {
		return e.business, nil
	}
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return false, mapErr(err)
	}
	cli, err := s.Workers.Client(instanceID)
	if err != nil {
		return false, mapErr(err)
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	p, err := cli.GetProfile(c, &workerv1.GetProfileRequest{})
	if err != nil {
		return false, accountErr(err)
	}
	biz := p.BusinessName != ""
	s.bizMu.Lock()
	if s.business == nil {
		s.business = map[string]bizEntry{}
	}
	s.business[instanceID] = bizEntry{business: biz, at: s.now()}
	s.bizMu.Unlock()
	return biz, nil
}

func (s *Social) requireBusiness(ctx context.Context, instanceID string) error {
	biz, err := s.isBusiness(ctx, instanceID)
	if err != nil {
		return err
	}
	if !biz {
		return businessRequired()
	}
	return nil
}

// Labels lists the instance's labels (as synced from the phone), with their chats.
func (s *Social) Labels(ctx context.Context, instanceID string) ([]LabelView, error) {
	if err := s.requireBusiness(ctx, instanceID); err != nil {
		return nil, err
	}
	ls, err := s.Store.ListLabels(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	out := make([]LabelView, 0, len(ls))
	for _, l := range ls {
		out = append(out, labelView(l))
	}
	return out, nil
}

// LabelInput creates or changes a label (nil fields are kept on update).
type LabelInput struct {
	Name  *string `json:"name,omitempty"`
	Color *int    `json:"color,omitempty"`
}

func validLabel(name string, color int) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxLabelName {
		return apierr.Validation("name is required (up to 100 characters)")
	}
	if color < 0 || color > maxLabelColor {
		return apierr.Validation("color must be between 0 and 19")
	}
	return nil
}

func (s *Social) labelAction(ctx context.Context, instanceID string, req *workerv1.LabelActionRequest) error {
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return err
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	if _, err := cli.LabelAction(c, req); err != nil {
		if st, ok := status.FromError(err); ok && strings.HasPrefix(st.Message(), "business_account_required") {
			s.bizMu.Lock()
			delete(s.business, instanceID)
			s.bizMu.Unlock()
		}
		return socialErr(err)
	}
	return nil
}

func (s *Social) publishLabel(instanceID string, ev events.LabelUpdated) {
	if s.Events != nil {
		s.Events.Publish(instanceID, events.TypeLabelUpdated, ev)
	}
}

// CreateLabel creates a label (the phone gets it through the app state).
func (s *Social) CreateLabel(ctx context.Context, instanceID string, in LabelInput) (LabelView, error) {
	if in.Name == nil {
		return LabelView{}, apierr.Validation("name is required")
	}
	color := 0
	if in.Color != nil {
		color = *in.Color
	}
	if err := validLabel(*in.Name, color); err != nil {
		return LabelView{}, err
	}
	if err := s.requireBusiness(ctx, instanceID); err != nil {
		return LabelView{}, err
	}
	s.labelMu.Lock()
	defer s.labelMu.Unlock()
	id, err := s.Store.NextLabelID(ctx, instanceID)
	if err != nil {
		return LabelView{}, err
	}
	name := strings.TrimSpace(*in.Name)
	if err := s.labelAction(ctx, instanceID, &workerv1.LabelActionRequest{Kind: "edit", LabelId: id, Name: name, Color: int32(color)}); err != nil {
		return LabelView{}, err
	}
	l := store.Label{ID: id, Name: name, Color: color}
	if changed, err := s.Store.UpsertLabel(ctx, instanceID, l); err != nil {
		return LabelView{}, err
	} else if changed {
		s.publishLabel(instanceID, events.LabelUpdated{Kind: "edit", LabelID: id, Name: name, Color: &color, Source: "api", Timestamp: s.now().UTC()})
	}
	return labelView(l), nil
}

// UpdateLabel renames or recolors a label.
func (s *Social) UpdateLabel(ctx context.Context, instanceID, id string, in LabelInput) (LabelView, error) {
	if err := s.requireBusiness(ctx, instanceID); err != nil {
		return LabelView{}, err
	}
	l, err := s.Store.GetLabel(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return LabelView{}, notFound("label")
	}
	if err != nil {
		return LabelView{}, err
	}
	if in.Name != nil {
		l.Name = strings.TrimSpace(*in.Name)
	}
	if in.Color != nil {
		l.Color = *in.Color
	}
	if err := validLabel(l.Name, l.Color); err != nil {
		return LabelView{}, err
	}
	if err := s.labelAction(ctx, instanceID, &workerv1.LabelActionRequest{Kind: "edit", LabelId: id, Name: l.Name, Color: int32(l.Color)}); err != nil {
		return LabelView{}, err
	}
	color := l.Color
	if changed, err := s.Store.UpsertLabel(ctx, instanceID, store.Label{ID: id, Name: l.Name, Color: l.Color, PredefinedID: l.PredefinedID}); err != nil {
		return LabelView{}, err
	} else if changed {
		s.publishLabel(instanceID, events.LabelUpdated{Kind: "edit", LabelID: id, Name: l.Name, Color: &color, Source: "api", Timestamp: s.now().UTC()})
	}
	return labelView(l), nil
}

// DeleteLabel deletes a label (its chats lose it).
func (s *Social) DeleteLabel(ctx context.Context, instanceID, id string) error {
	if err := s.requireBusiness(ctx, instanceID); err != nil {
		return err
	}
	l, err := s.Store.GetLabel(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("label")
	}
	if err != nil {
		return err
	}
	if err := s.labelAction(ctx, instanceID, &workerv1.LabelActionRequest{Kind: "edit", LabelId: id, Name: l.Name, Color: int32(l.Color), Deleted: true}); err != nil {
		return err
	}
	if changed, err := s.Store.UpsertLabel(ctx, instanceID, store.Label{ID: id, Name: l.Name, Color: l.Color, Deleted: true}); err != nil {
		return err
	} else if changed {
		s.publishLabel(instanceID, events.LabelUpdated{Kind: "edit", LabelID: id, Name: l.Name, Deleted: true, Source: "api", Timestamp: s.now().UTC()})
	}
	return nil
}

// LabelChat adds (labeled) or removes a label of a chat.
func (s *Social) LabelChat(ctx context.Context, instanceID, id, chatRaw string, labeled bool) (LabelView, error) {
	chat, err := ChatJID(chatRaw)
	if err != nil {
		return LabelView{}, err
	}
	if err := s.requireBusiness(ctx, instanceID); err != nil {
		return LabelView{}, err
	}
	if _, err := s.Store.GetLabel(ctx, instanceID, id); errors.Is(err, store.ErrNotFound) {
		return LabelView{}, notFound("label")
	} else if err != nil {
		return LabelView{}, err
	}
	if err := s.labelAction(ctx, instanceID, &workerv1.LabelActionRequest{Kind: "chat", LabelId: id, Chat: chat, Labeled: labeled}); err != nil {
		return LabelView{}, err
	}
	if changed, err := s.Store.SetLabelChat(ctx, instanceID, id, chat, labeled); err != nil {
		return LabelView{}, err
	} else if changed {
		s.publishLabel(instanceID, events.LabelUpdated{Kind: "chat", LabelID: id, Chat: chat, Labeled: &labeled, Source: "api", Timestamp: s.now().UTC()})
	}
	l, err := s.Store.GetLabel(ctx, instanceID, id)
	if err != nil {
		return LabelView{}, err
	}
	return labelView(l), nil
}

// LabelMessage adds or removes a label of a logged message (LiteZap id).
func (s *Social) LabelMessage(ctx context.Context, instanceID, id, msgID string, labeled bool) error {
	if err := s.requireBusiness(ctx, instanceID); err != nil {
		return err
	}
	if _, err := s.Store.GetLabel(ctx, instanceID, id); errors.Is(err, store.ErrNotFound) {
		return notFound("label")
	} else if err != nil {
		return err
	}
	m, err := s.Store.GetMessage(ctx, instanceID, msgID)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("message")
	}
	if err != nil {
		return err
	}
	if m.WAMessageID == "" || m.Status == "pending" {
		return apierr.Validation("the message has not been sent yet")
	}
	if err := s.labelAction(ctx, instanceID, &workerv1.LabelActionRequest{Kind: "message", LabelId: id, Chat: m.ChatJID,
		MessageId: m.WAMessageID, Labeled: labeled}); err != nil {
		return err
	}
	if changed, err := s.Store.SetLabelMessage(ctx, instanceID, id, m.ChatJID, m.WAMessageID, labeled); err != nil {
		return err
	} else if changed {
		s.publishLabel(instanceID, events.LabelUpdated{Kind: "message", LabelID: id, Chat: m.ChatJID, WAMessageID: m.WAMessageID,
			Labeled: &labeled, Source: "api", Timestamp: s.now().UTC()})
	}
	return nil
}

// ---- business profile ----

// BusinessHoursView is one day of the business hours.
type BusinessHoursView struct {
	Day   string `json:"day"`
	Mode  string `json:"mode"`
	Open  string `json:"open,omitempty"`
	Close string `json:"close,omitempty"`
}

// BusinessAccountView is the account's business profile (read only).
type BusinessAccountView struct {
	JID          string              `json:"jid"`
	BusinessName string              `json:"business_name,omitempty"`
	Address      string              `json:"address,omitempty"`
	Email        string              `json:"email,omitempty"`
	Categories   []string            `json:"categories"`
	Timezone     string              `json:"timezone,omitempty"`
	Hours        []BusinessHoursView `json:"hours"`
	Options      map[string]string   `json:"options,omitempty"`
}

// BusinessProfile reads the account's own business profile (409 for personal accounts).
func (s *Social) BusinessProfile(ctx context.Context, instanceID string) (BusinessAccountView, error) {
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return BusinessAccountView{}, err
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	p, err := cli.GetBusinessProfile(c, &workerv1.GetBusinessProfileRequest{})
	if err != nil {
		return BusinessAccountView{}, socialErr(err)
	}
	v := BusinessAccountView{JID: p.Jid, BusinessName: p.BusinessName, Address: p.Address, Email: p.Email,
		Categories: nonNil(p.Categories), Timezone: p.Timezone, Options: p.Options, Hours: []BusinessHoursView{}}
	for _, h := range p.Hours {
		v.Hours = append(v.Hours, BusinessHoursView{Day: h.Day, Mode: h.Mode, Open: h.Open, Close: h.Close})
	}
	return v, nil
}
