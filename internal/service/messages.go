package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
)

// Messaging holds the message use cases: send and settings here; the log, contacts and
// media in inbox.go.
type Messaging struct {
	Store        *store.Store
	Manager      *manager.Manager
	Queues       *queue.Queues
	Fetcher      *media.Fetcher
	Signer       *media.Signer
	DataDir      string
	Defaults     settings.Settings
	DefaultProxy string
	// MediaRetention is how long a downloaded attachment is kept (spec 4.5: 7 days).
	MediaRetention time.Duration
	// Resolver caches and rate-limits phone lookups (check-numbers).
	Resolver *lookup.Resolver
	// Catalog reads WhatsApp catalogs (marco 12): type product sends and the catalog
	// routes. nil: not available.
	Catalog *CatalogReader
	// SignedURLs enables ?signed=1; off when the API is exposed without a public_url
	// (the URLs would point at an address third parties cannot use).
	SignedURLs bool

	// PreviewFetcher fetches link previews (default: a fetcher that never reaches private
	// addresses). Tests replace it.
	PreviewFetcher *media.Fetcher
	previewOnce    sync.Once

	mediaOnce  sync.Once
	mediaSlots chan struct{}

	flightMu sync.Mutex
	flights  map[string]*mediaFlight // first downloads in progress (inbox.go)

	settingsMu    sync.Mutex
	settingsLocks map[string]*sync.Mutex // per instance: read-merge-write of the overrides

	// OnSettingsChanged, when set, is called after a settings update is saved (marco 7:
	// presence.online applies right away). It must not block.
	OnSettingsChanged func(instanceID string, before, after settings.Settings)
	presenceQuota     presenceQuota // presence subscriptions (presence.go)
}

// settingsLock serializes UpdateSettings per instance so concurrent patches never lose
// each other's fields.
func (s *Messaging) settingsLock(instanceID string) *sync.Mutex {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if s.settingsLocks == nil {
		s.settingsLocks = map[string]*sync.Mutex{}
	}
	mu := s.settingsLocks[instanceID]
	if mu == nil {
		mu = &sync.Mutex{}
		s.settingsLocks[instanceID] = mu
	}
	return mu
}

// mediaFlight is one media download in progress; concurrent callers wait for it.
type mediaFlight struct {
	done chan struct{}
	file MediaFile
	err  error
}

// MediaFile is a received attachment available on disk.
type MediaFile struct {
	Path     string
	Mime     string
	FileName string
}

const (
	maxWait          = 2 * time.Minute
	rpcTimeout       = 30 * time.Second
	maxTextBytes     = 65536
	maxEmojiBytes    = 64
	maxFileNameBytes = 255
	// maxMediaJobs bounds concurrent media downloads/decodes (each holds the file in memory).
	maxMediaJobs   = 3
	defaultExpires = 24 * time.Hour
	maxExpires     = 7 * 24 * time.Hour
)

// acquireMedia waits for one of the maxMediaJobs slots.
func (s *Messaging) acquireMedia(ctx context.Context) (func(), error) {
	s.mediaOnce.Do(func() { s.mediaSlots = make(chan struct{}, maxMediaJobs) })
	select {
	case s.mediaSlots <- struct{}{}:
		return func() { <-s.mediaSlots }, nil
	case <-ctx.Done():
		return nil, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many media sends in progress", true)
	}
}

func notFound(what string) *apierr.Error {
	return apierr.New(http.StatusNotFound, "not_found", what+" not found", false)
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// ChatJID normalizes a recipient: a phone number (10 to 15 digits, separators allowed) or a
// user/group JID. The device part of a JID is dropped.
func ChatJID(to string) (string, error) {
	to = strings.TrimSpace(to)
	if user, server, ok := strings.Cut(to, "@"); ok {
		user, _, _ = strings.Cut(user, ":")
		switch server {
		case "s.whatsapp.net", "g.us", "lid":
		default:
			return "", apierr.Validation("unsupported JID server " + server)
		}
		if user == "" {
			return "", apierr.Validation("JID without user part")
		}
		return user + "@" + server, nil
	}
	d := digitsOnly(to)
	if len(d) < 10 || len(d) > 15 {
		return "", apierr.Validation("phone must have 10 to 15 digits including country code")
	}
	return d + "@s.whatsapp.net", nil
}

// newWAMessageID mimics whatsmeow.GenerateMessageID ("3EB0" + 18 hex): the id is chosen
// before the first attempt and reused on every retry.
func newWAMessageID() string {
	b := make([]byte, 9)
	rand.Read(b)
	return "3EB0" + strings.ToUpper(hex.EncodeToString(b))
}

type MediaInput struct {
	URL      string `json:"url,omitempty"`
	Base64   string `json:"base64,omitempty"`
	Mime     string `json:"mime,omitempty"`
	FileName string `json:"file_name,omitempty"`
	PTT      bool   `json:"ptt,omitempty"` // audio as voice note
}

type ReactionInput struct {
	MessageID string `json:"message_id"` // LiteZap id or WhatsApp id of a message in the chat
	Emoji     string `json:"emoji"`      // "" removes the reaction
}

// SendInput is the body of POST /messages.
type SendInput struct {
	To       string         `json:"to"`
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`    // text; new text of an edit
	Caption  string         `json:"caption,omitempty"` // image, video, gif, document
	Media    *MediaInput    `json:"media,omitempty"`
	Reaction *ReactionInput `json:"reaction,omitempty"`
	Location *LocationInput `json:"location,omitempty"`
	Contacts []ContactInput `json:"contacts,omitempty"`
	Poll     *PollInput     `json:"poll,omitempty"`
	// MessageID is the logged message an edit, revoke or pin changes (LiteZap or WhatsApp id).
	MessageID string        `json:"message_id,omitempty"`
	Pin       *PinInput     `json:"pin,omitempty"`
	Forward   *ForwardInput `json:"forward,omitempty"`
	// ReplyTo quotes a message of the chat (LiteZap or WhatsApp id).
	ReplyTo string `json:"reply_to,omitempty"`
	// Mentions are phone numbers or user JIDs (the text should contain "@<number>"), or
	// ["all"] in a group.
	Mentions    []string          `json:"mentions,omitempty"`
	LinkPreview *LinkPreviewInput `json:"link_preview,omitempty"`
	// Type product (marco 12): a product of a WhatsApp catalog (catalog_owner: a phone
	// number; default the instance's own), with an optional caption (body) and footer.
	ProductID    string `json:"product_id,omitempty"`
	CatalogOwner string `json:"catalog_owner,omitempty"`
	Body         string `json:"body,omitempty"`
	Footer       string `json:"footer,omitempty"`
	// Types menu_item and menu (the Cardápio, LiteZap's own catalog): an item (its photo
	// with the caption, or a text) or the menu text, optionally one category.
	MenuItemID     string `json:"menu_item_id,omitempty"`
	MenuCategoryID string `json:"menu_category_id,omitempty"`
	// MenuImageID sends that photo of the item instead of its main photo, and
	// MenuCaption chooses its text: "full" (default: name, price, description, options),
	// "short" (name and price) or "none" (the photo alone, e.g. the rest of an album).
	MenuImageID string `json:"menu_image_id,omitempty"`
	MenuCaption string `json:"menu_caption,omitempty"`
	// DelayMS waits before sending and TypingMS shows "typing..." for that long (0-15000).
	// They never shorten the anti-ban pacing.
	DelayMS         int    `json:"delay_ms,omitempty"`
	TypingMS        int    `json:"typing_ms,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	// ExpiresAfter is how long the message may wait in the queue ("90m"; default 24h, max
	// 7 days); not sent by then, it fails with "expired".
	ExpiresAfter string `json:"expires_after,omitempty"`

	// Marco 10 (set by service.Social only): the look of a text status, and a reply to a
	// contact's status (quoted from status@broadcast).
	statusStyle *queue.StatusStyle
	statusQuote *queue.QuoteTarget
}

// requestHash fingerprints a send request (without its client id) to detect a
// client_message_id reused for a different message.
func requestHash(in SendInput) string {
	in.ClientMessageID = ""
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type MediaView struct {
	store.MediaInfo
	URL string `json:"url,omitempty"` // API path to download a received attachment
}

// MessageView is a message of the log or an item of the send queue.
type MessageView struct {
	ID              string     `json:"id"`
	ClientMessageID string     `json:"client_message_id,omitempty"`
	WAMessageID     string     `json:"wa_message_id,omitempty"`
	Chat            string     `json:"chat"`
	Sender          string     `json:"sender,omitempty"`
	FromMe          bool       `json:"from_me"`
	Source          string     `json:"source,omitempty"`
	Type            string     `json:"type"`
	Text            string     `json:"text,omitempty"`
	Media           *MediaView `json:"media,omitempty"`
	ReactionTo      string     `json:"reaction_to,omitempty"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts,omitempty"`
	Error           string     `json:"error,omitempty"`
	Timestamp       time.Time  `json:"timestamp"`
	// Target is the WhatsApp id of the message an edit, revoke or pin changes.
	Target    string     `json:"target,omitempty"`
	EditedAt  *time.Time `json:"edited_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	store.MessageMeta
}

func messageView(instanceID string, m store.Message) MessageView {
	v := MessageView{
		ID: m.ID, WAMessageID: m.WAMessageID, Chat: m.ChatJID, Sender: m.SenderJID, FromMe: m.FromMe,
		Source: m.Source, Type: m.Type, Text: m.Text, ReactionTo: m.ReactionTo, Status: m.Status, Timestamp: m.Timestamp,
		MessageMeta: store.ParseMeta(m.MetaJSON),
	}
	if !m.EditedAt.IsZero() {
		v.EditedAt = &m.EditedAt
	}
	if !m.RevokedAt.IsZero() {
		v.RevokedAt = &m.RevokedAt
	}
	if m.MediaJSON != "" {
		var info store.MediaInfo
		if json.Unmarshal([]byte(m.MediaJSON), &info) == nil {
			v.Media = &MediaView{MediaInfo: info}
			if info.ID != "" {
				v.Media.URL = "/v1/instances/" + instanceID + "/media/" + info.ID
			}
		}
	}
	return v
}

func outboundView(o store.Outbound, logged *store.Message) MessageView {
	var p queue.Payload
	_ = json.Unmarshal([]byte(o.Payload), &p)
	v := MessageView{
		ID: o.ID, ClientMessageID: o.ClientMessageID, Chat: o.ChatJID, FromMe: true, Source: "api",
		Type: p.Type, Text: p.Text, Status: o.Status, Attempts: o.Attempts, Timestamp: o.CreatedAt,
	}
	if p.Reaction != nil {
		v.ReactionTo = p.Reaction.WAMessageID
	}
	if p.Type == "forward" && p.ForwardKind != "" {
		v.Type = p.ForwardKind
	}
	if p.Mime != "" {
		kind := v.Type
		if kind == "product" {
			kind = "image" // the product's image
		}
		if kind == "gif" || kind == "ptv" {
			kind = "video"
		}
		v.Media = &MediaView{MediaInfo: store.MediaInfo{Kind: kind, Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT}}
	}
	if p.Meta != nil {
		v.MessageMeta = *p.Meta
	}
	if p.Target != nil {
		v.Target = p.Target.WAMessageID
	}
	if o.Status != store.OutSent {
		v.Error = o.LastError
	}
	if o.Status == store.OutSent {
		v.WAMessageID, v.Timestamp = o.WAMessageID, o.SentAt
		if logged != nil {
			v.Status = logged.Status // delivered / read from receipts
			v.Text = logged.Text     // edits and revokes change the logged text
			if !logged.EditedAt.IsZero() {
				v.EditedAt = &logged.EditedAt
			}
			if !logged.RevokedAt.IsZero() {
				v.RevokedAt = &logged.RevokedAt
			}
		}
	}
	return v
}

// failedErr turns the last_error of a failed item ("code: message") into an API error.
func failedErr(lastErr string) error {
	code, msg, _ := strings.Cut(lastErr, ": ")
	switch code {
	case "invalid_number":
		return apierr.New(http.StatusBadRequest, "invalid_number", msg, false)
	case "validation_failed":
		return apierr.Validation(msg)
	case "forbidden":
		return apierr.New(http.StatusForbidden, "forbidden", msg, false)
	case "logged_out":
		return apierr.New(http.StatusConflict, "instance_not_connected", "instance is logged out", false)
	case "worker_timeout":
		return apierr.New(http.StatusGatewayTimeout, "worker_timeout", msg, true)
	case "expired":
		return apierr.New(http.StatusGone, "message_expired", "message not sent before expires_after", false)
	case "canceled":
		return apierr.New(http.StatusConflict, "message_canceled", "message was removed from the queue before it was sent", false)
	case "edit_window_expired", "revoke_window_expired":
		return apierr.New(http.StatusConflict, code, msg, false)
	}
	return apierr.New(http.StatusBadGateway, "worker_error", "send failed: "+redact(lastErr), true)
}

// Send validates and enqueues a message (spec 4.1). With wait > 0 it waits for the
// outcome: the returned view says sent, queued/in_flight (timeout: still queued) or the
// error says why it failed.
func (s *Messaging) Send(ctx context.Context, instanceID string, in SendInput, wait time.Duration) (MessageView, error) {
	return s.send(ctx, instanceID, in, "", wait)
}

// send is Send; a non-empty chat is the recipient as is (status@broadcast or a channel,
// marco 10), bypassing the recipient validation of in.To.
func (s *Messaging) send(ctx context.Context, instanceID string, in SendInput, chat string, wait time.Duration) (MessageView, error) {
	if chat != "" {
		in.To = chat // part of the request hash
	}
	inst, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return MessageView{}, mapErr(err)
	}
	switch inst.State {
	case instance.Banned:
		return MessageView{}, apierr.New(http.StatusConflict, "instance_banned", "instance is banned; delete it", false)
	case instance.LoggedOut:
		return MessageView{}, apierr.New(http.StatusConflict, "instance_not_connected", "instance is logged out; pair it again", false)
	}
	if len(in.ClientMessageID) > 128 {
		return MessageView{}, apierr.Validation("client_message_id is limited to 128 characters")
	}
	hash := requestHash(in)
	if in.statusStyle != nil || in.statusQuote != nil { // unexported: not in requestHash
		b, _ := json.Marshal([]any{hash, in.statusStyle, in.statusQuote})
		sum := sha256.Sum256(b)
		hash = hex.EncodeToString(sum[:])
	}
	if in.ClientMessageID != "" { // idempotency before any download
		if o, err := s.Store.OutboundByClientID(ctx, instanceID, in.ClientMessageID); err == nil {
			if o.RequestHash != hash {
				return MessageView{}, apierr.New(http.StatusConflict, "conflict",
					"client_message_id was already used for a different message", false)
			}
			return s.finish(ctx, instanceID, o, wait)
		} else if !errors.Is(err, store.ErrNotFound) {
			return MessageView{}, err
		}
	}
	expires := defaultExpires
	if in.ExpiresAfter != "" {
		d, err := time.ParseDuration(in.ExpiresAfter)
		if err != nil || d < time.Minute || d > maxExpires {
			return MessageView{}, apierr.Validation("expires_after must be a duration between 1m and 168h")
		}
		expires = d
	}
	if chat == "" {
		if chat, err = s.resolveChat(ctx, instanceID, in); err != nil {
			return MessageView{}, err
		}
		// An edit/revoke/pin resolved from a status or channel post: those have their own
		// routes (the worker would refuse it after the queue's pacing).
		if chat == store.StatusChat || strings.HasSuffix(chat, "@newsletter") {
			return MessageView{}, apierr.Validation("status and channel posts are managed by the /status and /channels routes")
		}
	}
	id := secure.NewID("msg_")
	p, err := s.payload(ctx, instanceID, id, chat, in)
	if err != nil {
		return MessageView{}, err
	}
	b, _ := json.Marshal(p)
	o, created, err := s.Store.Enqueue(ctx, store.Outbound{
		ID: id, InstanceID: instanceID, ClientMessageID: in.ClientMessageID, ChatJID: chat,
		WAMessageID: newWAMessageID(), Payload: string(b), RequestHash: hash,
		ExpiresAt: time.Now().Add(expires),
	})
	if err != nil || !created { // !created: a concurrent request with the same client id won
		if p.MediaFile != "" {
			_ = os.Remove(p.MediaFile)
		}
		if err != nil {
			return MessageView{}, err
		}
		if o.RequestHash != hash {
			return MessageView{}, apierr.New(http.StatusConflict, "conflict",
				"client_message_id was already used for a different message", false)
		}
	}
	s.Queues.Wake(instanceID)
	return s.finish(ctx, instanceID, o, wait)
}

// Wait waits up to wait for a queued message to be sent or to fail (the second half of
// ?wait=true, after the request body has been released).
func (s *Messaging) Wait(ctx context.Context, instanceID, id string, wait time.Duration) (MessageView, error) {
	o, err := s.Store.GetOutbound(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("message")
	}
	if err != nil {
		return MessageView{}, err
	}
	return s.finish(ctx, instanceID, o, wait)
}

func (s *Messaging) finish(ctx context.Context, instanceID string, o store.Outbound, wait time.Duration) (MessageView, error) {
	if wait > 0 {
		wctx, cancel := context.WithTimeout(ctx, min(wait, maxWait))
		var err error
		o, err = s.Queues.Wait(wctx, instanceID, o.ID)
		cancel()
		if err != nil {
			return MessageView{}, mapErr(err) // ErrNotFound: the instance was deleted meanwhile
		}
	}
	if o.Status == store.OutFailed {
		return MessageView{}, failedErr(o.LastError)
	}
	var logged *store.Message
	if o.Status == store.OutSent {
		if m, err := s.Store.GetMessage(ctx, instanceID, o.ID); err == nil {
			logged = &m
		}
	}
	return outboundView(o, logged), nil
}

// payload validates the input and stages its attachment under data/outbox.
func (s *Messaging) payload(ctx context.Context, instanceID, id, chat string, in SendInput) (queue.Payload, error) {
	p := queue.Payload{Type: in.Type}
	if lp := in.LinkPreview; lp != nil && lp.Mode != "off" && in.Type != "text" {
		return p, apierr.Validation("link_preview only applies to type text")
	}
	if err := s.common(ctx, instanceID, chat, in, &p); err != nil {
		return p, err
	}
	if in.Type != "product" && (in.ProductID != "" || in.CatalogOwner != "" || in.Body != "" || in.Footer != "") {
		return p, apierr.Validation("product_id, catalog_owner, body and footer only apply to type product")
	}
	if (in.Type != "menu_item" && (in.MenuItemID != "" || in.MenuImageID != "" || in.MenuCaption != "")) || (in.Type != "menu" && in.MenuCategoryID != "") {
		return p, apierr.Validation("menu_item_id, menu_image_id and menu_caption only apply to type menu_item, and menu_category_id to type menu")
	}
	p.StatusStyle = in.statusStyle
	if q := in.statusQuote; q != nil {
		p.Quote = q
		metaOf(&p).ReplyTo = q.WAMessageID
	}
	var err error
	switch in.Type {
	case "text":
		err = s.composeText(ctx, in, &p)
	case "reaction":
		err = s.composeReaction(ctx, instanceID, chat, in, &p)
	case "location":
		err = composeLocation(in, &p)
	case "contact":
		err = composeContacts(in, &p)
	case "poll":
		err = composePoll(in, &p)
	case "edit", "revoke", "pin":
		err = s.composeTarget(ctx, instanceID, chat, in, &p)
	case "forward":
		meta := p.Meta
		err = s.composeForward(ctx, instanceID, in, &p)
		if err == nil && meta != nil { // keep reply_to and mentions
			p.Meta.ReplyTo, p.Meta.Mentions = meta.ReplyTo, meta.Mentions
		}
	case "image", "video", "audio", "document", "sticker", "gif", "ptv":
		err = s.composeMedia(ctx, instanceID, id, in, &p)
	case "product":
		err = s.composeProduct(ctx, instanceID, id, in, &p)
	case "menu_item":
		err = s.composeMenuItem(ctx, instanceID, id, in, &p)
	case "menu":
		err = s.composeMenu(ctx, instanceID, in, &p)
	default:
		return p, apierr.Validation("type must be text, image, video, gif, ptv, audio, document, sticker, location, contact, poll, reaction, edit, revoke, pin, forward, product, menu_item or menu")
	}
	return p, err
}

func (s *Messaging) composeReaction(ctx context.Context, instanceID, chat string, in SendInput, p *queue.Payload) error {
	if in.Reaction == nil || in.Reaction.MessageID == "" {
		return apierr.Validation("reaction.message_id is required")
	}
	if len(in.Reaction.Emoji) > maxEmojiBytes {
		return apierr.Validation("reaction.emoji is limited to 64 bytes")
	}
	target, err := s.findInChat(ctx, instanceID, chat, in.Reaction.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		return apierr.Validation("reaction.message_id is not a message of this chat")
	}
	if err != nil {
		return err
	}
	p.Text = in.Reaction.Emoji
	p.Reaction = &queue.ReactionTarget{WAMessageID: target.WAMessageID, FromMe: target.FromMe}
	if strings.HasSuffix(chat, "@g.us") {
		p.Reaction.Sender = target.SenderJID
	}
	return nil
}

// composeMedia downloads or decodes the attachment and stages it under data/outbox.
func (s *Messaging) composeMedia(ctx context.Context, instanceID, id string, in SendInput, p *queue.Payload) error {
	m := in.Media
	if m == nil || (m.URL == "") == (m.Base64 == "") {
		return apierr.Validation("media needs exactly one of url or base64")
	}
	if len(in.Caption) > maxTextBytes {
		return apierr.Validation("caption is limited to 64KB")
	}
	if in.Caption != "" && (in.Type == "sticker" || in.Type == "ptv" || in.Type == "audio") {
		return apierr.Validation(in.Type + " messages have no caption")
	}
	if len(m.FileName) > maxFileNameBytes {
		return apierr.Validation("media.file_name is limited to 255 bytes")
	}
	release, err := s.acquireMedia(ctx)
	if err != nil {
		return err
	}
	defer release()
	// kind is the attachment check: gif and ptv are videos, a sticker is a WebP image.
	kind := in.Type
	switch kind {
	case "gif", "ptv":
		kind = "video"
	case "sticker":
		kind = "image"
	}
	limit := media.MaxSize(kind)
	if in.Type == "sticker" {
		limit = maxStickerBytes
	}
	var data []byte
	var remote string
	if m.URL != "" {
		data, remote, err = s.Fetcher.Fetch(ctx, m.URL, limit)
	} else {
		data, remote, err = media.DecodeBase64(m.Base64, limit)
	}
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", in.Type+" exceeds the size limit", false)
	case errors.Is(err, media.ErrBlocked):
		return apierr.Validation("media.url points to a private or local address")
	case errors.Is(err, media.ErrInvalidURL):
		return apierr.Validation(err.Error())
	case errors.Is(err, media.ErrFetch):
		return apierr.New(http.StatusUnprocessableEntity, "media_fetch_failed", "media.url could not be downloaded", true)
	case err != nil:
		return apierr.Validation(err.Error())
	}
	mime, err := media.ResolveMime(kind, m.Mime, remote, data)
	if err != nil {
		return apierr.Validation(err.Error())
	}
	switch {
	case in.Type == "sticker" && mime != "image/webp":
		return apierr.Validation("a sticker must be a WebP image")
	case in.Type == "gif" && mime != "video/mp4":
		return apierr.Validation("a gif must be an MP4 video (WhatsApp plays it in a loop)")
	}
	if in.Type == "sticker" {
		p.Animated = animatedWebP(data)
	}
	name := m.FileName
	if name == "" && in.Type == "document" && m.URL != "" {
		if u, err := url.Parse(m.URL); err == nil {
			if name = path.Base(u.Path); len(name) > maxFileNameBytes {
				name = "" // a name taken from the URL never exceeds the limit either
			}
		}
	}
	dir := filepath.Join(s.DataDir, "outbox", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, id)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	p.Text, p.MediaFile, p.Mime, p.FileName, p.Size, p.PTT = in.Caption, file, mime, name, int64(len(data)), m.PTT
	return nil
}

// animatedWebP reports whether a WebP has an animation chunk (in the first 64KB: it comes
// right after the VP8X header and an optional color profile).
func animatedWebP(data []byte) bool {
	head := data[:min(len(data), 64<<10)]
	return len(head) > 30 && string(head[12:16]) == "VP8X" && head[20]&0x02 != 0 && bytes.Contains(head, []byte("ANIM"))
}

// GetMessage returns a queued/sent item (by the id returned on send) or a logged message.
func (s *Messaging) GetMessage(ctx context.Context, instanceID, id string) (MessageView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessageView{}, mapErr(err)
	}
	o, err := s.Store.GetOutbound(ctx, instanceID, id)
	if err == nil {
		var logged *store.Message
		if m, err := s.Store.GetMessage(ctx, instanceID, id); err == nil {
			logged = &m
		}
		return outboundView(o, logged), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return MessageView{}, err
	}
	m, err := s.Store.GetMessage(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("message")
	}
	if err != nil {
		return MessageView{}, err
	}
	return messageView(instanceID, m), nil
}

func (s *Messaging) effective(in store.Instance) (settings.Settings, error) {
	return settings.Effective(s.Defaults, s.DefaultProxy, in.SettingsJSON)
}

func redacted(set settings.Settings) settings.Settings {
	set.ProxyURL = settings.RedactProxy(set.ProxyURL)
	return set
}

// Settings returns the instance's effective settings (proxy password hidden).
func (s *Messaging) Settings(ctx context.Context, instanceID string) (settings.Settings, error) {
	in, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return settings.Settings{}, mapErr(err)
	}
	set, err := s.effective(in)
	return redacted(set), err
}

// UpdateSettings merges patch into the instance's overrides. Changing the effective proxy
// restarts a running worker (the proxy is fixed for the whole session, spec 4.6).
func (s *Messaging) UpdateSettings(ctx context.Context, instanceID string, patch settings.Patch) (settings.Settings, error) {
	mu := s.settingsLock(instanceID)
	mu.Lock()
	defer mu.Unlock()
	in, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return settings.Settings{}, mapErr(err)
	}
	before, err := s.effective(in)
	if err != nil {
		return settings.Settings{}, err
	}
	stored, err := settings.ParsePatch(in.SettingsJSON)
	if err != nil {
		return settings.Settings{}, err
	}
	merged := stored.Merge(patch)
	b, _ := json.Marshal(merged)
	after, err := settings.Effective(s.Defaults, s.DefaultProxy, string(b))
	if err != nil {
		return settings.Settings{}, err
	}
	if err := after.Validate(); err != nil {
		return settings.Settings{}, apierr.Validation(err.Error())
	}
	if err := s.Store.SetInstanceSettings(ctx, instanceID, string(b)); err != nil {
		return settings.Settings{}, mapErr(err)
	}
	if after.ProxyURL != before.ProxyURL && s.Manager.Running(instanceID) {
		if err := s.Manager.Restart(context.WithoutCancel(ctx), instanceID); err != nil {
			return settings.Settings{}, mapErr(err)
		}
	}
	if s.OnSettingsChanged != nil {
		s.OnSettingsChanged(instanceID, before, after)
	}
	s.Queues.Wake(instanceID)
	return redacted(after), nil
}
