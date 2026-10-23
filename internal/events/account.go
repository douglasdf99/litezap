package events

import (
	"strconv"
	"time"
)

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
	// | cleared | starred | disappearing.
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
	MessageID   string `json:"message_id,omitempty"`
	WAMessageID string `json:"wa_message_id,omitempty"`
	Starred     *bool  `json:"starred,omitempty"`
	// Disappearing is the chat's disappearing messages timer when known: off | 24h | 7d |
	// 90d (another value as "<seconds>s"), with DisappearingSeconds (0: off).
	Disappearing        string    `json:"disappearing,omitempty"`
	DisappearingSeconds *uint32   `json:"disappearing_seconds,omitempty"`
	Source              string    `json:"source"` // api | device | contact (disappearing changed by the contact)
	Timestamp           time.Time `json:"timestamp"`
}

// DisappearingLabel names a disappearing messages timer: off, 24h, 7d, 90d or "<n>s".
func DisappearingLabel(seconds uint32) string {
	switch seconds {
	case 0:
		return "off"
	case 86400:
		return "24h"
	case 7 * 86400:
		return "7d"
	case 90 * 86400:
		return "90d"
	}
	return strconv.FormatUint(uint64(seconds), 10) + "s"
}

// DisappearingSeconds parses a label of DisappearingLabel (off, 24h, 7d, 90d).
func DisappearingSeconds(label string) (uint32, bool) {
	switch label {
	case "off":
		return 0, true
	case "24h":
		return 86400, true
	case "7d":
		return 7 * 86400, true
	case "90d":
		return 90 * 86400, true
	}
	return 0, false
}

func (c ChatUpdated) EventAttrs() Attrs { return Attrs{Chat: c.Chat} }
