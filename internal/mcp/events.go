package mcp

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// EventBackend is the optional WhatsApp events part of a Backend (MSG-24): editing and
// answering events (creating one is a normal send).
type EventBackend interface {
	EditEvent(ctx context.Context, id, msgID string, p service.EventPatch, wait time.Duration) (service.MessageView, error)
	RespondEvent(ctx context.Context, id, msgID string, in service.EventResponseInput, wait time.Duration) (service.MessageView, error)
	// VotePoll votes on a poll (MSG-23), encrypted with the poll's key like an answer.
	VotePoll(ctx context.Context, id, msgID string, in service.PollVoteInput, wait time.Duration) (service.MessageView, error)
}

// EventService is the subset of service.Messaging used by Local for events.
type EventService interface {
	EditEvent(ctx context.Context, instanceID, id string, p service.EventPatch, wait time.Duration) (service.MessageView, error)
	RespondEvent(ctx context.Context, instanceID, id string, in service.EventResponseInput, wait time.Duration) (service.MessageView, error)
	VotePoll(ctx context.Context, instanceID, id string, in service.PollVoteInput, wait time.Duration) (service.MessageView, error)
}

var (
	_ EventBackend = (*Local)(nil)
	_ EventBackend = (*REST)(nil)
)

func (l *Local) events(id string) (EventService, error) {
	if err := l.allow(id); err != nil {
		return nil, err
	}
	s, ok := l.Messaging.(EventService)
	if !ok {
		return nil, apierr.New(http.StatusNotImplemented, "not_implemented", "not available on this server", false)
	}
	return s, nil
}

func (l *Local) EditEvent(ctx context.Context, id, msgID string, p service.EventPatch, wait time.Duration) (service.MessageView, error) {
	s, err := l.events(id)
	if err != nil {
		return service.MessageView{}, err
	}
	return s.EditEvent(ctx, id, msgID, p, wait)
}

func (l *Local) RespondEvent(ctx context.Context, id, msgID string, in service.EventResponseInput, wait time.Duration) (service.MessageView, error) {
	s, err := l.events(id)
	if err != nil {
		return service.MessageView{}, err
	}
	return s.RespondEvent(ctx, id, msgID, in, wait)
}

func waitQuery(wait time.Duration) url.Values {
	if wait <= 0 {
		return nil
	}
	return url.Values{"wait": {"true"}, "timeout": {strconv.Itoa(int(wait/time.Second)) + "s"}}
}

func (r *REST) EditEvent(ctx context.Context, id, msgID string, p service.EventPatch, wait time.Duration) (service.MessageView, error) {
	var v service.MessageView
	body := map[string]any{"event": p}
	err := r.call(ctx, restTimeout+wait, "PATCH", instPath(id, "messages", url.PathEscape(msgID)), waitQuery(wait), body, &v)
	return v, err
}

func (r *REST) RespondEvent(ctx context.Context, id, msgID string, in service.EventResponseInput, wait time.Duration) (service.MessageView, error) {
	var v service.MessageView
	err := r.call(ctx, restTimeout+wait, "POST", instPath(id, "messages", url.PathEscape(msgID), "event-response"), waitQuery(wait), in, &v)
	return v, err
}

func (l *Local) VotePoll(ctx context.Context, id, msgID string, in service.PollVoteInput, wait time.Duration) (service.MessageView, error) {
	s, err := l.events(id)
	if err != nil {
		return service.MessageView{}, err
	}
	return s.VotePoll(ctx, id, msgID, in, wait)
}

func (r *REST) VotePoll(ctx context.Context, id, msgID string, in service.PollVoteInput, wait time.Duration) (service.MessageView, error) {
	var v service.MessageView
	err := r.call(ctx, restTimeout+wait, "POST", instPath(id, "messages", url.PathEscape(msgID), "poll-vote"), waitQuery(wait), in, &v)
	return v, err
}

type votePollInput struct {
	InstanceID     string   `json:"instance_id,omitempty" jsonschema:"Instance of the poll"`
	MessageID      string   `json:"message_id" jsonschema:"LiteZap id (msg_…) of the poll, from get_messages."`
	Options        []string `json:"options" jsonschema:"Names of the chosen options, exactly as in poll.options; an empty list removes the vote."`
	Wait           *bool    `json:"wait,omitempty" jsonschema:"Wait for the outcome before returning (default true)."`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"How long to wait, in seconds (default 30, max 55)."`
}

type eventLocationInput struct {
	Name      string  `json:"name,omitempty" jsonschema:"Place name."`
	Address   string  `json:"address,omitempty" jsonschema:"Address."`
	Latitude  float64 `json:"latitude,omitempty" jsonschema:"Latitude (optional)."`
	Longitude float64 `json:"longitude,omitempty" jsonschema:"Longitude (optional)."`
}

func (l *eventLocationInput) loc() *service.EventLocation {
	if l == nil {
		return nil
	}
	return &service.EventLocation{Name: l.Name, Address: l.Address, Latitude: l.Latitude, Longitude: l.Longitude}
}

type sendEventInput struct {
	InstanceID         string              `json:"instance_id,omitempty" jsonschema:"Instance to send from"`
	To                 string              `json:"to" jsonschema:"Chat: group jid, or a phone number with country code."`
	Name               string              `json:"name" jsonschema:"Event name."`
	Description        string              `json:"description,omitempty" jsonschema:"Details shown under the name."`
	StartTime          string              `json:"start_time" jsonschema:"When it starts (RFC 3339 with offset, e.g. 2026-11-05T19:00:00-03:00)."`
	EndTime            string              `json:"end_time,omitempty" jsonschema:"When it ends (RFC 3339, optional)."`
	Location           *eventLocationInput `json:"location,omitempty" jsonschema:"Where it happens (optional)."`
	JoinLink           string              `json:"join_link,omitempty" jsonschema:"A WhatsApp call link (https://call.whatsapp.com/...), optional."`
	ExtraGuestsAllowed bool                `json:"extra_guests_allowed,omitempty" jsonschema:"Let people bring guests."`
	ClientMessageID    string              `json:"client_message_id,omitempty" jsonschema:"Your idempotency key (up to 128 chars)."`
	Wait               *bool               `json:"wait,omitempty" jsonschema:"Wait for the outcome before returning (default true)."`
	TimeoutSeconds     int                 `json:"timeout_seconds,omitempty" jsonschema:"How long to wait, in seconds (default 30, max 55)."`
}

type updateEventInput struct {
	InstanceID         string              `json:"instance_id,omitempty" jsonschema:"Instance of the event"`
	MessageID          string              `json:"message_id" jsonschema:"LiteZap id (msg_…) of one of your events."`
	Name               *string             `json:"name,omitempty" jsonschema:"New name."`
	Description        *string             `json:"description,omitempty" jsonschema:"New description (empty clears it)."`
	StartTime          *string             `json:"start_time,omitempty" jsonschema:"New start (RFC 3339)."`
	EndTime            *string             `json:"end_time,omitempty" jsonschema:"New end (RFC 3339; empty clears it)."`
	Location           *eventLocationInput `json:"location,omitempty" jsonschema:"New place (all fields empty: remove it)."`
	JoinLink           *string             `json:"join_link,omitempty" jsonschema:"New call link (empty clears it)."`
	ExtraGuestsAllowed *bool               `json:"extra_guests_allowed,omitempty" jsonschema:"Allow guests."`
	Canceled           *bool               `json:"canceled,omitempty" jsonschema:"true cancels the event for everyone; false restores it."`
	Wait               *bool               `json:"wait,omitempty" jsonschema:"Wait for the outcome before returning (default true)."`
	TimeoutSeconds     int                 `json:"timeout_seconds,omitempty" jsonschema:"How long to wait, in seconds (default 30, max 55)."`
}

type respondEventInput struct {
	InstanceID     string `json:"instance_id,omitempty" jsonschema:"Instance of the event"`
	MessageID      string `json:"message_id" jsonschema:"LiteZap id (msg_…) of the event, from get_messages."`
	Response       string `json:"response" jsonschema:"going, not_going or maybe."`
	ExtraGuests    int    `json:"extra_guests,omitempty" jsonschema:"Guests you bring (only with going, when the event allows guests)."`
	Wait           *bool  `json:"wait,omitempty" jsonschema:"Wait for the outcome before returning (default true)."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"How long to wait, in seconds (default 30, max 55)."`
}

// registerEvents adds the WhatsApp event tools (MSG-24).
func (t *tools) registerEvents(s *sdk.Server) {
	instID := func(p *jsonschema.Schema) { p.Description = instanceIDDoc }
	waitProps := map[string]func(*jsonschema.Schema){
		"instance_id": instID, "wait": boolDefault(true), "timeout_seconds": intRange(30, 1, maxWaitSeconds),
	}
	add(t, s, &sdk.Tool{
		Name:  "send_event",
		Title: "Send a WhatsApp event",
		Description: "Sends an event invitation (name, date, place, optional call link) to a real person or group; everyone in the chat can answer going / not going / maybe. " +
			"Only send events the user explicitly asked for, to the chat they named, with the details they gave (confirm date and time zone). Same anti-ban queue as send_message.",
		InputSchema: schema[sendEventInput](waitProps),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, in sendEventInput) (*sdk.CallToolResult, sendOutput, error) {
		return t.send(ctx, in.InstanceID, service.SendInput{To: in.To, Type: "event", ClientMessageID: in.ClientMessageID,
			Event: &service.EventInput{Name: in.Name, Description: in.Description, StartTime: in.StartTime, EndTime: in.EndTime,
				Location: in.Location.loc(), JoinLink: in.JoinLink, ExtraGuestsAllowed: in.ExtraGuestsAllowed}}, waitOf(in.Wait, in.TimeoutSeconds))
	})

	eb, ok := t.b.(EventBackend)
	if !ok {
		return
	}
	add(t, s, &sdk.Tool{
		Name:  "update_event",
		Title: "Edit or cancel a WhatsApp event",
		Description: "Changes one of the user's events (name, description, time, place, call link) or cancels it (canceled: true) for everyone in the chat. Absent fields keep their value. " +
			"Only when the user explicitly asks, for the event they identified: every participant sees the change. Goes through the send queue.",
		InputSchema: schema[updateEventInput](waitProps),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, in updateEventInput) (*sdk.CallToolResult, sendOutput, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, sendOutput{}, err
		}
		p := service.EventPatch{Name: in.Name, Description: in.Description, StartTime: in.StartTime, EndTime: in.EndTime,
			JoinLink: in.JoinLink, ExtraGuestsAllowed: in.ExtraGuestsAllowed, Canceled: in.Canceled}
		if in.Location != nil {
			p.Location = in.Location.loc()
		}
		v, err := eb.EditEvent(ctx, id, in.MessageID, p, waitOf(in.Wait, in.TimeoutSeconds))
		return nil, queuedOutput(v), err
	})
	add(t, s, &sdk.Tool{
		Name:  "respond_event",
		Title: "Answer a WhatsApp event",
		Description: "Answers an event of a chat (going, not_going or maybe) as the user's number; the event's author and the chat see the answer. " +
			"Only answer when the user tells you their answer. Events received before this number was paired cannot be answered (event_secret_missing).",
		InputSchema: schema[respondEventInput](map[string]func(*jsonschema.Schema){
			"instance_id": instID, "wait": boolDefault(true), "timeout_seconds": intRange(30, 1, maxWaitSeconds),
			"response":     func(p *jsonschema.Schema) { p.Enum = []any{"going", "not_going", "maybe"} },
			"extra_guests": intRange(0, 0, 20),
		}),
		Annotations: &sdk.ToolAnnotations{IdempotentHint: true, DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, in respondEventInput) (*sdk.CallToolResult, sendOutput, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, sendOutput{}, err
		}
		v, err := eb.RespondEvent(ctx, id, in.MessageID, service.EventResponseInput{Response: in.Response, ExtraGuests: in.ExtraGuests},
			waitOf(in.Wait, in.TimeoutSeconds))
		return nil, queuedOutput(v), err
	})
	add(t, s, &sdk.Tool{
		Name:  "vote_poll",
		Title: "Vote on a WhatsApp poll",
		Description: "Votes on a poll of a chat as the user's number (replacing their previous vote; an empty options list removes it); everyone in the chat sees the vote. " +
			"Only vote when the user tells you their choice. get_messages shows each poll's options, poll.votes and poll.results. Polls received before this number was paired cannot be voted on (poll_secret_missing).",
		InputSchema: schema[votePollInput](map[string]func(*jsonschema.Schema){
			"instance_id": instID, "wait": boolDefault(true), "timeout_seconds": intRange(30, 1, maxWaitSeconds),
		}),
		Annotations: &sdk.ToolAnnotations{IdempotentHint: true, DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, in votePollInput) (*sdk.CallToolResult, sendOutput, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, sendOutput{}, err
		}
		v, err := eb.VotePoll(ctx, id, in.MessageID, service.PollVoteInput{Options: in.Options}, waitOf(in.Wait, in.TimeoutSeconds))
		return nil, queuedOutput(v), err
	})
}

// queuedOutput describes a queued change like a send.
func queuedOutput(v service.MessageView) sendOutput {
	out := sendOutput{Message: v}
	switch v.Status {
	case "sent", "delivered", "read", "played":
		out.Sent, out.Note = true, "Sent."
	case "failed":
		out.Note = "Failed: " + v.Error
	default:
		out.Note = "Queued by the anti-ban queue: it will be sent automatically. Do not repeat it; follow it with get_message_status."
	}
	return out
}
