package events

import "time"

// TypeGroupUpdated is published for every change to a group the instance is in (marco 8):
// one event per kind of change.
const TypeGroupUpdated = "group.updated"

func init() { Types = append(Types, TypeGroupUpdated) }

// Kinds of group.updated.
const (
	GroupParticipantsAdded    = "participants_added"
	GroupParticipantsRemoved  = "participants_removed"
	GroupParticipantsPromoted = "participants_promoted"
	GroupParticipantsDemoted  = "participants_demoted"
	GroupSubject              = "subject"
	GroupDescription          = "description"
	GroupPhoto                = "photo"
	GroupSettings             = "settings"
	GroupInviteLinkReset      = "invite_link_reset"
	GroupJoinRequest          = "join_request"
	GroupJoinRequestRevoked   = "join_request_revoked"
	GroupJoined               = "joined"
	GroupDeleted              = "deleted"
	GroupCommunityLinked      = "community_linked"
	GroupCommunityUnlinked    = "community_unlinked"
	GroupSuspended            = "suspended"
	GroupUnsuspended          = "unsuspended"
)

// GroupSettingsChange carries the settings a "settings" change set (others are nil).
type GroupSettingsChange struct {
	Announce         *bool   `json:"announce,omitempty"`
	Locked           *bool   `json:"locked,omitempty"`
	ApprovalRequired *bool   `json:"approval_required,omitempty"`
	EphemeralSeconds *uint32 `json:"ephemeral_seconds,omitempty"`
	// ApprovalModeChanged: the approval mode changed and the new value could not be read.
	ApprovalModeChanged bool `json:"approval_mode_changed,omitempty"`
}

// GroupUpdate is the data of group.updated.
type GroupUpdate struct {
	Chat string `json:"chat"` // the group JID
	Kind string `json:"kind"`
	// Actor made the change (phone-number JID when known); empty when WhatsApp omits it.
	Actor string `json:"actor,omitempty"`
	// Participants affected (participants_*, join_request*), phone-number JIDs when known.
	Participants []string `json:"participants,omitempty"`
	// Reason: participants_added "invite" (joined by link); joined "invite" or "".
	Reason      string               `json:"reason,omitempty"`
	Name        *string              `json:"name,omitempty"`        // subject, joined
	Description *string              `json:"description,omitempty"` // description ("" removed)
	Removed     bool                 `json:"removed,omitempty"`     // photo removed
	Settings    *GroupSettingsChange `json:"settings,omitempty"`    // settings
	Group       string               `json:"group,omitempty"`       // community_linked/unlinked: the group
	Timestamp   time.Time            `json:"timestamp"`
}

func (g GroupUpdate) EventAttrs() Attrs { return Attrs{Chat: g.Chat} }
