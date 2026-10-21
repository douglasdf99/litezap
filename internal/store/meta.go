package store

import "encoding/json"

// MessageMeta is the structured content of a message, stored as messages.meta_json and
// shown (flattened) by the API and the events.
type MessageMeta struct {
	ReplyTo     string        `json:"reply_to,omitempty"` // WhatsApp id of the quoted message
	Mentions    []string      `json:"mentions,omitempty"` // mentioned JIDs
	Forwarded   bool          `json:"forwarded,omitempty"`
	Location    *Location     `json:"location,omitempty"`
	Contacts    []ContactCard `json:"contacts,omitempty"`
	Poll        *Poll         `json:"poll,omitempty"`
	LinkPreview *LinkPreview  `json:"link_preview,omitempty"`
	ButtonReply *ButtonReply  `json:"button_reply,omitempty"`
	Starred     bool          `json:"starred,omitempty"` // marco 9: starred on some device of the account
	// Marco 12: a catalog product card, or an order placed from a catalog (no token).
	Product *ProductCard `json:"product,omitempty"`
	Order   *OrderCard   `json:"order,omitempty"`
}

// ProductCard is the product of a product message (prices in thousandths of the unit).
type ProductCard struct {
	ProductID       string `json:"product_id"`
	Title           string `json:"title,omitempty"`
	Description     string `json:"description,omitempty"`
	PriceAmount1000 int64  `json:"price_amount_1000,omitempty"`
	Currency        string `json:"currency,omitempty"`
	RetailerID      string `json:"retailer_id,omitempty"`
	URL             string `json:"url,omitempty"`
	BusinessOwner   string `json:"business_owner_jid,omitempty"`
	Body            string `json:"body,omitempty"`
	Footer          string `json:"footer,omitempty"`
}

// OrderCard is an order message (the token stays in the orders table).
type OrderCard struct {
	OrderID         string `json:"order_id"`
	ItemCount       int    `json:"item_count"`
	TotalAmount1000 int64  `json:"total_amount_1000,omitempty"`
	Currency        string `json:"currency,omitempty"`
	Seller          string `json:"seller_jid,omitempty"`
	Title           string `json:"title,omitempty"`
	Status          string `json:"status,omitempty"`
}

// ButtonReply is a received answer to a button, list, template or interactive message.
type ButtonReply struct {
	Kind        string `json:"kind"`         // button | list | template | interactive
	ID          string `json:"id,omitempty"` // selected button/row id (interactive: flow name)
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"` // list row description; interactive: params JSON
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
	URL       string  `json:"url,omitempty"`
}

type ContactCard struct {
	DisplayName string `json:"display_name"`
	VCard       string `json:"vcard"`
}

type Poll struct {
	Name    string   `json:"name"`
	Options []string `json:"options"`
	// Selectable is how many options a voter may pick (0: any number).
	Selectable int `json:"selectable"`
}

// LinkPreview is the preview card attached to a text (the thumbnail is not kept).
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// Empty reports whether m carries nothing.
func (m MessageMeta) Empty() bool {
	return m.ReplyTo == "" && len(m.Mentions) == 0 && !m.Forwarded && m.Location == nil &&
		len(m.Contacts) == 0 && m.Poll == nil && m.LinkPreview == nil && m.ButtonReply == nil &&
		m.Product == nil && m.Order == nil
}

// JSON encodes m for meta_json ("" when empty).
func (m MessageMeta) JSON() string {
	if m.Empty() {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ParseMeta decodes meta_json (malformed or empty: no meta).
func ParseMeta(s string) MessageMeta {
	var m MessageMeta
	if s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}
