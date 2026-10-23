package events

import "time"

// v0.1.1 (MSG-24): an answer to a WhatsApp event (going / not going / maybe).
const TypeMessageEventResponse = "message.event_response"

func init() { Types = append(Types, TypeMessageEventResponse) }

// EventResponse is the data of message.event_response: the responder's current answer.
// Responses are not logged as messages; the event's meta keeps the latest of each person.
type EventResponse struct {
	EventID          string    `json:"event_id,omitempty"` // LiteZap id of the event (empty if not in the log)
	EventWAMessageID string    `json:"event_wa_message_id"`
	Chat             string    `json:"chat"`
	Responder        string    `json:"responder"`
	PushName         string    `json:"push_name,omitempty"`
	FromMe           bool      `json:"from_me"`
	Response         string    `json:"response"` // going | not_going | maybe
	ExtraGuests      int       `json:"extra_guests,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
}

func (v EventResponse) EventAttrs() Attrs {
	return Attrs{Chat: v.Chat, MessageType: "event_response", HasFromMe: true, FromMe: v.FromMe}
}
