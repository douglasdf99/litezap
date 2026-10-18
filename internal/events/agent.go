package events

// Agent hook events (spec 4.3, marco 5).
const (
	// TypeAgentFailed: a hook call failed (timeout, HTTP or network error, invalid
	// response, reply refused by the queue) or the loop guard paused a chat. Nothing was
	// sent for that message (fail-closed).
	TypeAgentFailed = "agent.failed"
	// TypeAgentHandoff: the agent handed the chat over to a human (action handoff); the
	// hook stays off in that chat until it is re-enabled.
	TypeAgentHandoff = "agent.handoff"
)

// Registered here (not in the Types literal) to keep this addition self-contained.
func init() { Types = append(Types, TypeAgentFailed, TypeAgentHandoff) }

// AgentFailed is the data of agent.failed.
type AgentFailed struct {
	Chat        string `json:"chat"`
	MessageID   string `json:"message_id,omitempty"` // LiteZap id of the triggering message
	WAMessageID string `json:"wa_message_id,omitempty"`
	// Reason is a stable code: timeout | http_error | network_error | invalid_response |
	// send_failed | loop_guard.
	Reason     string `json:"reason"`
	Error      string `json:"error,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// AgentHandoff is the data of agent.handoff.
type AgentHandoff struct {
	Chat        string `json:"chat"`
	MessageID   string `json:"message_id,omitempty"`
	WAMessageID string `json:"wa_message_id,omitempty"`
	Note        string `json:"note,omitempty"`
}
