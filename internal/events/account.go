package events

import "time"

// Marco 9: chat state changes (archive, pin, mute, unread mark, delete, star), made
// through the API or on any device of the account.
const TypeChatUpdated = "chat.updated"

// Registered here (not in the Types literal) to keep this addition self-contained.
func init() { Types = append(Types, TypeChatUpdated) }

// ChatUpdated is the data of chat.updated: what changed and the chat's state after it.
type ChatUpdated struct {
	Chat    string `json:"chat"`
	IsGroup bool   `json:"is_group,omitempty"`
	// Changed lists the fields that changed: archived | pinned | muted | unread | deleted
	// | cleared | starred.
	Changed  []string `json:"changed"`
	Archived bool     `json:"archived"`
	Pinned   bool     `json:"pinned"`
	Muted    bool     `json:"muted"`
	// MutedUntil is set while muted with an end; muted without it means "until unmuted".
	MutedUntil *time.Time `json:"muted_until,omitempty"`
	Unread     bool       `json:"unread"` // marked as unread by hand
	Deleted    bool       `json:"deleted,omitempty"`
	Cleared    bool       `json:"cleared,omitempty"` // messages cleared on the phone (the log keeps them)
	// starred: the message and its new state.
	MessageID   string    `json:"message_id,omitempty"`
	WAMessageID string    `json:"wa_message_id,omitempty"`
	Starred     *bool     `json:"starred,omitempty"`
	Source      string    `json:"source"` // api | device
	Timestamp   time.Time `json:"timestamp"`
}

func (c ChatUpdated) EventAttrs() Attrs { return Attrs{Chat: c.Chat} }
