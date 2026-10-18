package events

import (
	"strings"
	"time"
)

// Marco 7 event types: button replies, presence and calls. message.status also gains the
// status "played" (voice notes and videos played by the recipient).
const (
	TypeMessageButton = "message.button_reply"
	TypeChatPresence  = "chat.presence"
	TypeCallReceived  = "call.received"
	TypeCallAccepted  = "call.accepted"
	TypeCallEnded     = "call.ended"
)

// Registered here (not in the Types literal) to keep this addition self-contained.
func init() {
	Types = append(Types, TypeMessageButton, TypeChatPresence, TypeCallReceived, TypeCallAccepted, TypeCallEnded)
}

// optIn types are never matched by "*" (nor by an empty filter): they are high volume and
// reveal contacts' activity, so a consumer must name them ("chat.presence" or "chat.*").
var optIn = map[string]bool{TypeChatPresence: true}

// OptIn reports whether typ must be named explicitly to be received.
func OptIn(typ string) bool { return optIn[typ] }

// ChatPresence is the data of chat.presence: a contact typing, recording or stopping in a
// chat, or a subscribed contact going online/offline (only while presence.online is on).
type ChatPresence struct {
	Chat    string `json:"chat"`
	Sender  string `json:"sender"` // who is typing (the participant, in groups)
	IsGroup bool   `json:"is_group,omitempty"`
	State   string `json:"state"` // composing | recording | paused | online | offline
	// LastSeen (offline): when the contact was last online, if they show it.
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// Call is the data of call.received, call.accepted and call.ended.
type Call struct {
	CallID  string `json:"call_id"`
	Chat    string `json:"chat"`   // the caller's chat (direct) or the group (group calls)
	Caller  string `json:"caller"` // who started the call
	IsGroup bool   `json:"is_group,omitempty"`
	Media   string `json:"media,omitempty"` // audio | video
	// call.received: the instance rejected it automatically (calls.auto_reject).
	Rejected bool `json:"rejected,omitempty"`
	// call.ended: why — caller_hangup (rang unanswered until the caller gave up),
	// accepted_elsewhere (answered on another device of the account), ended (after being
	// answered), rejected (calls.auto_reject), rejected_by_peer, timeout (no signal for 2
	// min) or another reason sent by WhatsApp — and missed = rang out unanswered and was
	// not rejected.
	Reason    string    `json:"reason,omitempty"`
	Missed    bool      `json:"missed,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

func isGroupJID(chat string) bool { return strings.HasSuffix(chat, "@g.us") }

// Filterable fields (webhook filters, filters.go). from_me only concerns message events.

func (m Message) EventAttrs() Attrs {
	return Attrs{Chat: m.Chat, MessageType: m.Type, HasFromMe: true, FromMe: m.FromMe}
}
func (u MessageUpdate) EventAttrs() Attrs {
	return Attrs{Chat: u.Chat, HasFromMe: true, FromMe: u.FromMe}
}
func (v PollVote) EventAttrs() Attrs { return Attrs{Chat: v.Chat, HasFromMe: true, FromMe: v.FromMe} }

// Status events are always about the instance's own messages: from_me does not apply
// (filters.from_me=false must not hide every status).
func (s MessageStatus) EventAttrs() Attrs { return Attrs{Chat: s.Chat} }
func (p ChatPresence) EventAttrs() Attrs  { return Attrs{Chat: p.Chat} }
func (c Call) EventAttrs() Attrs          { return Attrs{Chat: c.Chat} }
