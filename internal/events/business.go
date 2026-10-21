package events

import "time"

// Marco 12 event types: the account's business profile changed through the API; an order
// arrived from the WhatsApp catalog.
const (
	TypeBusinessProfileUpdated = "business.profile_updated"
	TypeOrderReceived          = "order.received"
)

func init() { Types = append(Types, TypeBusinessProfileUpdated, TypeOrderReceived) }

// BusinessProfileUpdated is the data of business.profile_updated: which fields the API
// changed and the profile read back from WhatsApp afterwards.
type BusinessProfileUpdated struct {
	Fields    []string  `json:"fields"` // description | address | email | websites | location | categories | hours
	Cleared   []string  `json:"cleared,omitempty"`
	Profile   any       `json:"profile"`
	Source    string    `json:"source"` // api
	Timestamp time.Time `json:"timestamp"`
}

// OrderReceived is the data of order.received: an order a customer placed from the
// catalog (the message itself is also a message.received of type "order"). Amounts are in
// thousandths of the currency unit. The order token stays on the server: read the details
// with GET /v1/instances/{id}/orders/{order_id}.
type OrderReceived struct {
	OrderID         string    `json:"order_id"`
	MessageID       string    `json:"message_id"`
	WAMessageID     string    `json:"wa_message_id,omitempty"`
	Chat            string    `json:"chat"`
	Seller          string    `json:"seller,omitempty"`
	ItemCount       int       `json:"item_count"`
	TotalAmount1000 int64     `json:"total_amount_1000,omitempty"`
	Total           float64   `json:"total,omitempty"` // total_amount_1000 / 1000
	Currency        string    `json:"currency,omitempty"`
	Title           string    `json:"title,omitempty"`
	Note            string    `json:"note,omitempty"`
	Status          string    `json:"status,omitempty"` // inquiry | accepted | declined
	PushName        string    `json:"push_name,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
}
