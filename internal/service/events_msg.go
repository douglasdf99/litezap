package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
)

// WhatsApp events (MSG-24): created with type "event" in POST /messages, edited (or
// canceled) with PATCH /messages/{id}, answered with POST /messages/{id}/event-response.

// EventInput describes an event. Times are RFC 3339.
type EventInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Location is a place (name and/or address, coordinates optional).
	Location *EventLocation `json:"location,omitempty"`
	// JoinLink is a WhatsApp call link (https://call.whatsapp.com/...).
	JoinLink           string `json:"join_link,omitempty"`
	StartTime          string `json:"start_time"`
	EndTime            string `json:"end_time,omitempty"`
	ExtraGuestsAllowed bool   `json:"extra_guests_allowed,omitempty"`
	Canceled           bool   `json:"canceled,omitempty"`
}

// EventLocation is where an event happens.
type EventLocation struct {
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
}

// EventPatch changes an event; absent fields keep their value (an empty string clears
// description, location name/address or join_link).
type EventPatch struct {
	Name               *string        `json:"name,omitempty"`
	Description        *string        `json:"description,omitempty"`
	Location           *EventLocation `json:"location,omitempty"`
	JoinLink           *string        `json:"join_link,omitempty"`
	StartTime          *string        `json:"start_time,omitempty"`
	EndTime            *string        `json:"end_time,omitempty"`
	ExtraGuestsAllowed *bool          `json:"extra_guests_allowed,omitempty"`
	Canceled           *bool          `json:"canceled,omitempty"`
	ClientMessageID    string         `json:"client_message_id,omitempty"`
}

// EventResponseInput answers an event.
type EventResponseInput struct {
	Response        string `json:"response"` // going | not_going | maybe
	ExtraGuests     int    `json:"extra_guests,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
}

const (
	maxEventName        = 256
	maxEventDescription = 2048
	maxEventExtraGuests = 20
)

func eventTime(name, raw string, required bool) (time.Time, error) {
	if raw == "" {
		if required {
			return time.Time{}, apierr.Validation("event." + name + " is required (RFC 3339, e.g. 2026-11-05T19:00:00-03:00)")
		}
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, apierr.Validation("event." + name + " must be an RFC 3339 time, e.g. 2026-11-05T19:00:00-03:00")
	}
	return t.UTC().Truncate(time.Second), nil
}

// eventCard validates an event and converts it to the logged form.
func eventCard(in EventInput) (*store.EventCard, error) {
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return nil, apierr.Validation("event.name is required")
	case utf8.RuneCountInString(name) > maxEventName:
		return nil, apierr.Validation("event.name is limited to 256 characters")
	case utf8.RuneCountInString(in.Description) > maxEventDescription:
		return nil, apierr.Validation("event.description is limited to 2048 characters")
	}
	start, err := eventTime("start_time", in.StartTime, true)
	if err != nil {
		return nil, err
	}
	c := &store.EventCard{Name: name, Description: in.Description, StartTime: start, Canceled: in.Canceled,
		ExtraGuestsAllowed: in.ExtraGuestsAllowed}
	if end, err := eventTime("end_time", in.EndTime, false); err != nil {
		return nil, err
	} else if !end.IsZero() {
		if end.Before(start) {
			return nil, apierr.Validation("event.end_time must be after start_time")
		}
		c.EndTime = &end
	}
	if l := in.Location; l != nil {
		if l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 {
			return nil, apierr.Validation("event.location has invalid coordinates")
		}
		if l.Name == "" && l.Address == "" && l.Latitude == 0 && l.Longitude == 0 {
			return nil, apierr.Validation("event.location needs a name, an address or coordinates")
		}
		c.Location = &store.Location{Name: l.Name, Address: l.Address, Latitude: l.Latitude, Longitude: l.Longitude}
	}
	if in.JoinLink != "" {
		u, err := url.Parse(in.JoinLink)
		if err != nil || u.Scheme != "https" || u.Host != "call.whatsapp.com" || len(in.JoinLink) > 512 {
			return nil, apierr.Validation("event.join_link must be a WhatsApp call link (https://call.whatsapp.com/...)")
		}
		c.JoinLink = in.JoinLink
	}
	return c, nil
}

// composeEvent prepares a new event.
func composeEvent(in SendInput, p *queue.Payload) error {
	if in.Event == nil {
		return apierr.Validation("event is required for type event")
	}
	if in.Event.Canceled {
		return apierr.Validation("a new event cannot be canceled")
	}
	c, err := eventCard(*in.Event)
	if err != nil {
		return err
	}
	if c.StartTime.Before(time.Now().Add(-time.Minute)) {
		return apierr.Validation("event.start_time is in the past")
	}
	metaOf(p).Event = c
	p.Text = c.Name
	return nil
}

// eventTarget loads a logged event by LiteZap or WhatsApp id.
func (s *Messaging) eventTarget(ctx context.Context, instanceID, id string) (store.Message, store.EventCard, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return store.Message{}, store.EventCard{}, mapErr(err)
	}
	m, err := s.Store.GetMessage(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return m, store.EventCard{}, notFound("message")
	}
	if err != nil {
		return m, store.EventCard{}, err
	}
	ev := store.ParseMeta(m.MetaJSON).Event
	if m.Type != "event" || ev == nil {
		return m, store.EventCard{}, apierr.Validation("the message is not an event")
	}
	if !m.RevokedAt.IsZero() {
		return m, store.EventCard{}, apierr.Validation("the event was deleted")
	}
	if m.Status == "pending" || m.WAMessageID == "" {
		return m, store.EventCard{}, apierr.Validation("the event has not been sent yet")
	}
	return m, *ev, nil
}

func fmtEventTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// EditEvent changes (or cancels) one of our events. Everyone in the chat sees the change;
// it goes through the send queue like an edit.
func (s *Messaging) EditEvent(ctx context.Context, instanceID, id string, p EventPatch, wait time.Duration) (MessageView, error) {
	m, cur, err := s.eventTarget(ctx, instanceID, id)
	if err != nil {
		return MessageView{}, err
	}
	if !m.FromMe {
		return MessageView{}, apierr.Validation("only your own events can be edited")
	}
	in := EventInput{Name: cur.Name, Description: cur.Description, JoinLink: cur.JoinLink, StartTime: fmtEventTime(cur.StartTime),
		ExtraGuestsAllowed: cur.ExtraGuestsAllowed, Canceled: cur.Canceled}
	if cur.EndTime != nil {
		in.EndTime = fmtEventTime(*cur.EndTime)
	}
	if l := cur.Location; l != nil {
		in.Location = &EventLocation{Name: l.Name, Address: l.Address, Latitude: l.Latitude, Longitude: l.Longitude}
	}
	changed := false
	set := func(dst *string, v *string) {
		if v != nil {
			*dst, changed = *v, true
		}
	}
	set(&in.Name, p.Name)
	set(&in.Description, p.Description)
	set(&in.JoinLink, p.JoinLink)
	set(&in.StartTime, p.StartTime)
	set(&in.EndTime, p.EndTime)
	if p.Location != nil {
		in.Location, changed = p.Location, true
		if *p.Location == (EventLocation{}) {
			in.Location = nil
		}
	}
	if p.ExtraGuestsAllowed != nil {
		in.ExtraGuestsAllowed, changed = *p.ExtraGuestsAllowed, true
	}
	if p.Canceled != nil {
		in.Canceled, changed = *p.Canceled, true
	}
	if !changed {
		return MessageView{}, apierr.Validation("nothing to change: set a field of the event (or canceled)")
	}
	if cur.Canceled && in.Canceled && p.Canceled == nil {
		return MessageView{}, apierr.Validation("the event is canceled; send canceled: false to restore it")
	}
	ev := in
	return s.send(ctx, instanceID, SendInput{Type: "edit", MessageID: m.ID, Event: &ev, ClientMessageID: p.ClientMessageID}, m.ChatJID, wait)
}

var eventAnswers = map[string]bool{"going": true, "not_going": true, "maybe": true}

// RespondEvent answers an event of the chat (ours or a contact's): going, not_going or
// maybe. It goes through the send queue; the answer is encrypted with the event's key,
// so events received before this number was paired cannot be answered.
func (s *Messaging) RespondEvent(ctx context.Context, instanceID, id string, in EventResponseInput, wait time.Duration) (MessageView, error) {
	if !eventAnswers[in.Response] {
		return MessageView{}, apierr.Validation("response must be going, not_going or maybe")
	}
	if in.ExtraGuests < 0 || in.ExtraGuests > maxEventExtraGuests {
		return MessageView{}, apierr.Validation("extra_guests must be between 0 and 20")
	}
	m, ev, err := s.eventTarget(ctx, instanceID, id)
	if err != nil {
		return MessageView{}, err
	}
	if in.ExtraGuests > 0 && (!ev.ExtraGuestsAllowed || in.Response != "going") {
		return MessageView{}, apierr.Validation("extra_guests needs response going and an event that allows guests")
	}
	if ev.Canceled {
		return MessageView{}, apierr.New(http.StatusConflict, "event_canceled", "the event was canceled", false)
	}
	r := in
	return s.send(ctx, instanceID, SendInput{Type: "event_response", MessageID: m.ID, ClientMessageID: in.ClientMessageID,
		eventResponse: &r}, m.ChatJID, wait)
}
