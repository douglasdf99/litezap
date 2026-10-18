package events

import "time"

// Marco 10 event types: contacts' status posts, channel posts and label changes.
const (
	TypeStatusReceived = "status.received"
	TypeChannelMessage = "channel.message"
	TypeLabelUpdated   = "label.updated"
)

// Registered here (not in the Types literal) to keep this addition self-contained.
// status.received and channel.message are opt-in: high volume (every contact's story,
// every post of every followed channel), so "*" never matches them.
func init() {
	Types = append(Types, TypeStatusReceived, TypeChannelMessage, TypeLabelUpdated)
	optIn[TypeStatusReceived] = true
	optIn[TypeChannelMessage] = true
}

// ChannelMessage is the data of channel.message: a post in a followed channel. Channel
// media is not downloaded (media carries metadata only).
type ChannelMessage struct {
	Channel     string           `json:"channel"`
	ServerID    int64            `json:"server_id"` // reactions use it
	WAMessageID string           `json:"wa_message_id"`
	Type        string           `json:"type"`
	Text        string           `json:"text,omitempty"`
	Media       *Media           `json:"media,omitempty"`
	Views       int64            `json:"views,omitempty"`
	Reactions   map[string]int64 `json:"reactions,omitempty"`
	Timestamp   time.Time        `json:"timestamp"`
}

func (c ChannelMessage) EventAttrs() Attrs { return Attrs{Chat: c.Channel, MessageType: c.Type} }

// LabelUpdated is the data of label.updated. kind: edit (created, renamed, recolored or
// deleted: name, color, deleted) | chat (a chat labeled/unlabeled) | message.
type LabelUpdated struct {
	Kind        string    `json:"kind"`
	LabelID     string    `json:"label_id"`
	Name        string    `json:"name,omitempty"`
	Color       *int      `json:"color,omitempty"`
	Deleted     bool      `json:"deleted,omitempty"`
	Chat        string    `json:"chat,omitempty"`
	WAMessageID string    `json:"wa_message_id,omitempty"`
	Labeled     *bool     `json:"labeled,omitempty"`
	Source      string    `json:"source"` // api | device
	Timestamp   time.Time `json:"timestamp"`
}

func (l LabelUpdated) EventAttrs() Attrs { return Attrs{Chat: l.Chat} }
