package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
)

const (
	maxContacts     = 10
	maxVCardBytes   = 8 << 10
	maxNameBytes    = 256
	maxPollOptions  = 12
	maxPollOption   = 100
	maxPollName     = 255
	maxMentions     = 50
	maxDelayMS      = 15000
	maxQuoteText    = 1024
	maxStickerBytes = 1 << 20
)

type LocationInput struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Name      string   `json:"name,omitempty"`
	Address   string   `json:"address,omitempty"`
	URL       string   `json:"url,omitempty"`
}

// ContactInput is one contact card: a ready vCard, or a name and phone to build one.
type ContactInput struct {
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	VCard string `json:"vcard,omitempty"`
}

type PollInput struct {
	Name    string   `json:"name"`
	Options []string `json:"options"`
	// MaxSelections is how many options a voter may pick (default 1; 0: any number).
	MaxSelections *int `json:"max_selections,omitempty"`
}

type PinInput struct {
	Unpin    bool   `json:"unpin,omitempty"`
	Duration string `json:"duration,omitempty"` // 24h | 7d | 30d (default 7d)
}

type ForwardInput struct {
	MessageID string `json:"message_id"`     // LiteZap id, or WhatsApp id with chat
	Chat      string `json:"chat,omitempty"` // chat of a WhatsApp id
}

// pinDurations are the durations WhatsApp offers when pinning.
var pinDurations = map[string]uint32{"24h": 86400, "7d": 7 * 86400, "30d": 30 * 86400}

func validTarget(t string) bool {
	return t == "edit" || t == "revoke" || t == "pin" || t == "event_response" || t == "poll_vote"
}

// findInChat finds a logged message of chat by LiteZap id or WhatsApp id.
func (s *Messaging) findInChat(ctx context.Context, instanceID, chat, id string) (store.Message, error) {
	m, err := s.Store.GetMessage(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.ChatJID != chat) {
		m, err = s.Store.MessageByWAID(ctx, instanceID, chat, id)
	}
	return m, err
}

// resolveChat fills an empty recipient of an edit, revoke or pin from its target's
// LiteZap id.
func (s *Messaging) resolveChat(ctx context.Context, instanceID string, in SendInput) (string, error) {
	if strings.TrimSpace(in.To) == "" && validTarget(in.Type) && in.MessageID != "" {
		m, err := s.Store.GetMessage(ctx, instanceID, in.MessageID)
		if errors.Is(err, store.ErrNotFound) {
			return "", apierr.Validation("to is required unless message_id is a LiteZap message id")
		}
		if err != nil {
			return "", err
		}
		return m.ChatJID, nil
	}
	return ChatJID(in.To)
}

// common validates the fields every new message may carry: reply_to, mentions, delay_ms
// and typing_ms.
func (s *Messaging) common(ctx context.Context, instanceID, chat string, in SendInput, p *queue.Payload) error {
	if in.DelayMS < 0 || in.DelayMS > maxDelayMS || in.TypingMS < 0 || in.TypingMS > maxDelayMS {
		return apierr.Validation("delay_ms and typing_ms must be between 0 and 15000")
	}
	p.DelayMS, p.TypingMS = in.DelayMS, in.TypingMS
	updates := validTarget(in.Type) || in.Type == "reaction"
	if updates && (in.ReplyTo != "" || len(in.Mentions) > 0) {
		return apierr.Validation("reply_to and mentions are not allowed for " + in.Type)
	}
	meta := store.MessageMeta{}
	if in.ReplyTo != "" {
		q, err := s.findInChat(ctx, instanceID, chat, in.ReplyTo)
		if errors.Is(err, store.ErrNotFound) {
			return apierr.Validation("reply_to is not a message of this chat")
		}
		if err != nil {
			return err
		}
		if q.WAMessageID == "" || q.Status == "pending" {
			return apierr.Validation("reply_to must be a message that was already sent or received")
		}
		p.Quote = &queue.QuoteTarget{WAMessageID: q.WAMessageID, FromMe: q.FromMe, Sender: q.SenderJID, Text: clip(q.Text, maxQuoteText)}
		meta.ReplyTo = q.WAMessageID
	}
	if len(in.Mentions) > maxMentions {
		return apierr.Validation("mentions is limited to 50 entries")
	}
	for _, m := range in.Mentions {
		if m == "all" {
			if !strings.HasSuffix(chat, "@g.us") {
				return apierr.Validation(`mentions "all" only works in groups`)
			}
			p.MentionAll = true
			continue
		}
		j, err := ChatJID(m)
		if err != nil || strings.HasSuffix(j, "@g.us") {
			return apierr.Validation("mentions must be phone numbers or user JIDs: " + m)
		}
		meta.Mentions = append(meta.Mentions, j)
	}
	if meta.ReplyTo != "" {
		metaOf(p).ReplyTo = meta.ReplyTo
	}
	if len(meta.Mentions) > 0 {
		metaOf(p).Mentions = meta.Mentions
	}
	return nil
}

// meta returns the payload's meta, creating it.
func metaOf(p *queue.Payload) *store.MessageMeta {
	if p.Meta == nil {
		p.Meta = &store.MessageMeta{}
	}
	return p.Meta
}

func (s *Messaging) composeText(ctx context.Context, in SendInput, p *queue.Payload) error {
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > maxTextBytes {
		return apierr.Validation("text is required (up to 64KB)")
	}
	p.Text = in.Text
	lp, info, err := s.linkPreview(ctx, in.LinkPreview, in.Text)
	if err != nil {
		return err
	}
	if lp != nil {
		p.LinkPreview = lp
		metaOf(p).LinkPreview = info
	}
	return nil
}

func composeLocation(in SendInput, p *queue.Payload) error {
	l := in.Location
	if l == nil || l.Latitude == nil || l.Longitude == nil {
		return apierr.Validation("location.latitude and location.longitude are required")
	}
	lat, lng := *l.Latitude, *l.Longitude
	if math.IsNaN(lat) || math.IsNaN(lng) || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return apierr.Validation("location.latitude must be in [-90, 90] and longitude in [-180, 180]")
	}
	if len(l.Name) > maxNameBytes || len(l.Address) > maxNameBytes {
		return apierr.Validation("location.name and location.address are limited to 256 bytes")
	}
	if l.URL != "" && !validPreviewURL(l.URL) {
		return apierr.Validation("location.url must be an absolute http(s) URL")
	}
	metaOf(p).Location = &store.Location{Latitude: lat, Longitude: lng, Name: l.Name, Address: l.Address, URL: l.URL}
	p.Text = strings.TrimSpace(fmt.Sprintf("%f,%f %s", lat, lng, l.Name))
	return nil
}

// vcardEscape escapes a vCard text value and drops line breaks.
func vcardEscape(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`).Replace(s)
}

func composeContacts(in SendInput, p *queue.Payload) error {
	if len(in.Contacts) == 0 || len(in.Contacts) > maxContacts {
		return apierr.Validation("contacts needs 1 to 10 entries")
	}
	var cards []store.ContactCard
	for i, c := range in.Contacts {
		name := strings.TrimSpace(c.Name)
		if name == "" || len(name) > maxNameBytes {
			return apierr.Validation(fmt.Sprintf("contacts[%d].name is required (up to 256 bytes)", i))
		}
		card := c.VCard
		switch {
		case card != "":
			if len(card) > maxVCardBytes || !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(card)), "BEGIN:VCARD") {
				return apierr.Validation(fmt.Sprintf("contacts[%d].vcard must be a vCard (BEGIN:VCARD, up to 8KB)", i))
			}
		case c.Phone != "":
			d := digitsOnly(c.Phone)
			if len(d) < 8 || len(d) > 15 {
				return apierr.Validation(fmt.Sprintf("contacts[%d].phone must have 8 to 15 digits", i))
			}
			n := vcardEscape(name)
			card = "BEGIN:VCARD\nVERSION:3.0\nN:;" + n + ";;;\nFN:" + n + "\nTEL;type=CELL;type=VOICE;waid=" + d + ":+" + d + "\nEND:VCARD"
		default:
			return apierr.Validation(fmt.Sprintf("contacts[%d] needs a phone or a vcard", i))
		}
		cards = append(cards, store.ContactCard{DisplayName: name, VCard: card})
	}
	metaOf(p).Contacts = cards
	p.Text = cards[0].DisplayName
	if len(cards) > 1 {
		p.Text = fmt.Sprintf("%d contacts", len(cards))
	}
	return nil
}

func composePoll(in SendInput, p *queue.Payload) error {
	pl := in.Poll
	if pl == nil || strings.TrimSpace(pl.Name) == "" || len(pl.Name) > maxPollName {
		return apierr.Validation("poll.name is required (up to 255 bytes)")
	}
	if len(pl.Options) < 2 || len(pl.Options) > maxPollOptions {
		return apierr.Validation("poll.options needs 2 to 12 options")
	}
	seen := map[string]bool{}
	for _, o := range pl.Options {
		if strings.TrimSpace(o) == "" || len(o) > maxPollOption || seen[o] {
			return apierr.Validation("poll.options must be distinct, non-empty and up to 100 bytes each")
		}
		seen[o] = true
	}
	sel := 1
	if pl.MaxSelections != nil {
		sel = *pl.MaxSelections
	}
	if sel < 0 || sel > len(pl.Options) {
		return apierr.Validation("poll.max_selections must be between 0 (any) and the number of options")
	}
	metaOf(p).Poll = &store.Poll{Name: pl.Name, Options: pl.Options, Selectable: sel}
	p.Text = pl.Name
	return nil
}

// composeTarget validates an edit, revoke or pin of a logged message.
func (s *Messaging) composeTarget(ctx context.Context, instanceID, chat string, in SendInput, p *queue.Payload) error {
	if in.MessageID == "" {
		return apierr.Validation("message_id is required for " + in.Type)
	}
	t, err := s.findInChat(ctx, instanceID, chat, in.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		return apierr.Validation("message_id is not a message of this chat")
	}
	if err != nil {
		return err
	}
	if t.Status == "pending" || t.WAMessageID == "" {
		return apierr.Validation("the message has not been sent yet; cancel it from the queue instead")
	}
	if !t.RevokedAt.IsZero() {
		return apierr.Validation("the message was deleted")
	}
	p.Target = &queue.Target{MessageID: t.ID, WAMessageID: t.WAMessageID, FromMe: t.FromMe, SentAt: t.Timestamp}
	if strings.HasSuffix(chat, "@g.us") {
		p.Target.Sender = t.SenderJID
	}
	now := time.Now()
	switch in.Type {
	case "edit":
		if !t.FromMe {
			return apierr.Validation("only your own messages can be edited")
		}
		if in.Event != nil || t.Type == "event" {
			// An event stays editable (and cancelable) until it happens.
			if in.Event == nil || t.Type != "event" {
				return apierr.Validation("use PATCH /messages/{id} to edit an event")
			}
			c, err := eventCard(*in.Event)
			if err != nil {
				return err
			}
			metaOf(p).Event = c
			p.Text = c.Name
			return nil
		}
		if t.Type != "text" {
			return apierr.Validation("only text messages can be edited")
		}
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > maxTextBytes {
			return apierr.Validation("text (the new text) is required (up to 64KB)")
		}
		if now.Sub(t.Timestamp) > queue.EditWindow {
			return apierr.New(http.StatusConflict, "edit_window_expired", "messages can only be edited for 15 minutes", false)
		}
		p.Text = in.Text
	case "revoke":
		if !t.FromMe && !strings.HasSuffix(chat, "@g.us") {
			return apierr.Validation("only your own messages can be deleted in a direct chat (in groups, admins can delete others')")
		}
		if !t.FromMe && t.SenderJID == "" {
			return apierr.Validation("the author of the message is unknown")
		}
		if now.Sub(t.Timestamp) > queue.RevokeWindow {
			return apierr.New(http.StatusConflict, "revoke_window_expired", "messages can only be deleted for everyone for 48 hours", false)
		}
	case "event_response":
		if in.eventResponse == nil {
			return apierr.Validation("use POST /messages/{id}/event-response to answer an event")
		}
		if t.Type != "event" {
			return apierr.Validation("the message is not an event")
		}
		if strings.HasSuffix(chat, "@g.us") && !t.FromMe && t.SenderJID == "" {
			return apierr.Validation("the author of the event is unknown")
		}
		p.EventResponse, p.ExtraGuests = in.eventResponse.Response, in.eventResponse.ExtraGuests
	case "poll_vote":
		if in.pollVote == nil {
			return apierr.Validation("use POST /messages/{id}/poll-vote to vote on a poll")
		}
		if t.Type != "poll" {
			return apierr.Validation("the message is not a poll")
		}
		if strings.HasSuffix(chat, "@g.us") && !t.FromMe && t.SenderJID == "" {
			return apierr.Validation("the author of the poll is unknown")
		}
		p.PollVote = in.pollVote.Options
	case "pin":
		pin := in.Pin
		if pin == nil {
			pin = &PinInput{}
		}
		p.Unpin = pin.Unpin
		if !pin.Unpin {
			d := pin.Duration
			if d == "" {
				d = "7d"
			}
			secs, ok := pinDurations[d]
			if !ok {
				return apierr.Validation("pin.duration must be 24h, 7d or 30d")
			}
			p.PinSeconds = secs
		}
		if strings.HasSuffix(chat, "@g.us") && !t.FromMe && t.SenderJID == "" {
			return apierr.Validation("the author of the message is unknown")
		}
	}
	return nil
}

// composeForward forwards a logged message: its text, location or contacts, or a received
// attachment (sent again from WhatsApp's servers, without a new upload).
func (s *Messaging) composeForward(ctx context.Context, instanceID string, in SendInput, p *queue.Payload) error {
	f := in.Forward
	if f == nil || f.MessageID == "" {
		return apierr.Validation("forward.message_id is required")
	}
	src, err := s.Store.GetMessage(ctx, instanceID, f.MessageID)
	if errors.Is(err, store.ErrNotFound) && f.Chat != "" {
		var chat string
		if chat, err = ChatJID(f.Chat); err != nil {
			return err
		}
		src, err = s.Store.MessageByWAID(ctx, instanceID, chat, f.MessageID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return apierr.Validation("forward.message_id is not in the message log (pass forward.chat with a WhatsApp id)")
	}
	if err != nil {
		return err
	}
	if !src.RevokedAt.IsZero() || src.Status == "pending" {
		return apierr.Validation("that message cannot be forwarded (deleted or not sent yet)")
	}
	meta := store.ParseMeta(src.MetaJSON)
	fwd := store.MessageMeta{Forwarded: true}
	switch src.Type {
	case "text":
		p.Type, p.Text = "text", src.Text
	case "location":
		if meta.Location == nil {
			return apierr.Validation("the location of that message is unknown")
		}
		p.Type, p.Text, fwd.Location = "location", src.Text, meta.Location
	case "contact":
		if len(meta.Contacts) == 0 {
			return apierr.Validation("the contacts of that message are unknown")
		}
		p.Type, p.Text, fwd.Contacts = "contact", src.Text, meta.Contacts
	case "image", "video", "gif", "ptv", "audio", "document", "sticker":
		var info store.MediaInfo
		if src.MediaJSON == "" || json.Unmarshal([]byte(src.MediaJSON), &info) != nil || info.ID == "" {
			return apierr.Validation("only received attachments can be forwarded: LiteZap does not keep the files it sends")
		}
		if info.ViewOnce {
			return apierr.Validation("view-once media cannot be forwarded")
		}
		md, err := s.Store.GetMedia(ctx, instanceID, info.ID)
		if err != nil {
			return apierr.Validation("the attachment of that message is no longer available")
		}
		p.Type, p.Text, p.ForwardKind, p.ForwardRef = "forward", src.Text, md.Kind, md.Ref
		p.Mime, p.Size, p.FileName, p.PTT = md.Mime, md.Size, md.FileName, info.PTT
	default:
		return apierr.Validation("messages of type " + src.Type + " cannot be forwarded")
	}
	p.Meta = &fwd
	return nil
}
