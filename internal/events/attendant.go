package events

const (
	// TypeAgentLead: the built-in attendant (SDR mode) qualified a lead (name, need).
	TypeAgentLead = "agent.lead"
	// TypeAgentAppointment: the built-in attendant collected a request for an appointment
	// (name, need, preferred time). No calendar is touched: route it with a webhook.
	TypeAgentAppointment = "agent.appointment_requested"
	// TypeAgentOrderDraft: the built-in attendant (order mode) built an order summary
	// (items, total, address) and asked the customer to confirm it.
	TypeAgentOrderDraft = "agent.order_draft"
)

// Registered here (not in the Types literal) to keep this addition self-contained.
func init() { Types = append(Types, TypeAgentLead, TypeAgentAppointment, TypeAgentOrderDraft) }
