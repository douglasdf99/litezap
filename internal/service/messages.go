package service

import (
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
	// SignedURLs enables ?signed=1; off when the API is exposed without a public_url
	// (the URLs would point at an address third parties cannot use).
	SignedURLs bool

	mediaOnce  sync.Once
	mediaSlots chan struct{}

	flightMu sync.Mutex
	flights  map[string]*mediaFlight // first downloads in progress (inbox.go)
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
	To              string         `json:"to"`
	Type            string         `json:"type"`
	Text            string         `json:"text,omitempty"`    // type text
	Caption         string         `json:"caption,omitempty"` // image, video, document
	Media           *MediaInput    `json:"media,omitempty"`
	Reaction        *ReactionInput `json:"reaction,omitempty"`
	ClientMessageID string         `json:"client_message_id,omitempty"`
	// ExpiresAfter is how long the message may wait in the queue ("90m"; default 24h, max
	// 7 days); not sent by then, it fails with "expired".
	ExpiresAfter string `json:"expires_after,omitempty"`
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
}

func messageView(instanceID string, m store.Message) MessageView {
	v := MessageView{
		ID: m.ID, WAMessageID: m.WAMessageID, Chat: m.ChatJID, Sender: m.SenderJID, FromMe: m.FromMe,
		Source: m.Source, Type: m.Type, Text: m.Text, ReactionTo: m.ReactionTo, Status: m.Status, Timestamp: m.Timestamp,
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
	if p.Mime != "" {
		v.Media = &MediaView{MediaInfo: store.MediaInfo{Kind: p.Type, Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT}}
	}
	if o.Status != store.OutSent {
		v.Error = o.LastError
	}
	if o.Status == store.OutSent {
		v.WAMessageID, v.Timestamp = o.WAMessageID, o.SentAt
		if logged != nil {
			v.Status = logged.Status // delivered / read from receipts
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
	}
	return apierr.New(http.StatusBadGateway, "worker_error", "send failed: "+redact(lastErr), true)
}

// Send validates and enqueues a message (spec 4.1). With wait > 0 it waits for the
// outcome: the returned view says sent, queued/in_flight (timeout: still queued) or the
// error says why it failed.
func (s *Messaging) Send(ctx context.Context, instanceID string, in SendInput, wait time.Duration) (MessageView, error) {
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
	chat, err := ChatJID(in.To)
	if err != nil {
		return MessageView{}, err
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
			return MessageView{}, err
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
	switch in.Type {
	case "text":
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > maxTextBytes {
			return p, apierr.Validation("text is required (up to 64KB)")
		}
		p.Text = in.Text
		return p, nil
	case "reaction":
		if in.Reaction == nil || in.Reaction.MessageID == "" {
			return p, apierr.Validation("reaction.message_id is required")
		}
		if len(in.Reaction.Emoji) > maxEmojiBytes {
			return p, apierr.Validation("reaction.emoji is limited to 64 bytes")
		}
		target, err := s.Store.GetMessage(ctx, instanceID, in.Reaction.MessageID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && target.ChatJID != chat) {
			target, err = s.Store.MessageByWAID(ctx, instanceID, chat, in.Reaction.MessageID)
		}
		if errors.Is(err, store.ErrNotFound) {
			return p, apierr.Validation("reaction.message_id is not a message of this chat")
		}
		if err != nil {
			return p, err
		}
		p.Text = in.Reaction.Emoji
		p.Reaction = &queue.ReactionTarget{WAMessageID: target.WAMessageID, FromMe: target.FromMe}
		if strings.HasSuffix(chat, "@g.us") {
			p.Reaction.Sender = target.SenderJID
		}
		return p, nil
	case "image", "video", "audio", "document":
	default:
		return p, apierr.Validation("type must be text, image, video, audio, document or reaction")
	}
	m := in.Media
	if m == nil || (m.URL == "") == (m.Base64 == "") {
		return p, apierr.Validation("media needs exactly one of url or base64")
	}
	if len(in.Caption) > maxTextBytes {
		return p, apierr.Validation("caption is limited to 64KB")
	}
	if len(m.FileName) > maxFileNameBytes {
		return p, apierr.Validation("media.file_name is limited to 255 bytes")
	}
	release, err := s.acquireMedia(ctx)
	if err != nil {
		return p, err
	}
	defer release()
	limit := media.MaxSize(in.Type)
	var data []byte
	var remote string
	if m.URL != "" {
		data, remote, err = s.Fetcher.Fetch(ctx, m.URL, limit)
	} else {
		data, remote, err = media.DecodeBase64(m.Base64, limit)
	}
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return p, apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", in.Type+" exceeds the size limit", false)
	case errors.Is(err, media.ErrBlocked):
		return p, apierr.Validation("media.url points to a private or local address")
	case errors.Is(err, media.ErrFetch):
		return p, apierr.New(http.StatusUnprocessableEntity, "media_fetch_failed", "media.url could not be downloaded", true)
	case err != nil:
		return p, apierr.Validation(err.Error())
	}
	mime, err := media.ResolveMime(in.Type, m.Mime, remote, data)
	if err != nil {
		return p, apierr.Validation(err.Error())
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
		return p, err
	}
	file := filepath.Join(dir, id)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return p, err
	}
	p.Text, p.MediaFile, p.Mime, p.FileName, p.Size, p.PTT = in.Caption, file, mime, name, int64(len(data)), m.PTT
	return p, nil
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
	s.Queues.Wake(instanceID)
	return redacted(after), nil
}
