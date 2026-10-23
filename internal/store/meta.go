package store

import (
	"context"
	"encoding/json"
	"time"
)

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
	// v0.1.1 (MSG-24): a WhatsApp event, with the responses seen so far.
	Event *EventCard `json:"event,omitempty"`
}

// EventCard is a WhatsApp event (an event message and its later edits).
type EventCard struct {
	Name               string     `json:"name"`
	Description        string     `json:"description,omitempty"`
	Location           *Location  `json:"location,omitempty"`
	JoinLink           string     `json:"join_link,omitempty"`
	StartTime          time.Time  `json:"start_time"`
	EndTime            *time.Time `json:"end_time,omitempty"`
	Canceled           bool       `json:"canceled,omitempty"`
	ExtraGuestsAllowed bool       `json:"extra_guests_allowed,omitempty"`
	// Responses maps who answered (JID) to their latest answer.
	Responses map[string]EventAnswer `json:"responses,omitempty"`
}

// EventAnswer is one person's answer to an event: going | not_going | maybe.
type EventAnswer struct {
	Response    string    `json:"response"`
	ExtraGuests int       `json:"extra_guests,omitempty"`
	At          time.Time `json:"at"`
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
		m.Product == nil && m.Order == nil && m.Event == nil && !m.Starred
}

// updateMeta rewrites the meta of a logged message in one transaction; change may also
// return a new text (nil: kept) and reports whether to write. ErrNotFound when the
// message is not in the log.
func (s *Store) updateMeta(ctx context.Context, instanceID, chatJID, waID string, change func(*Message, *MessageMeta) (*string, bool)) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	m, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ?`, instanceID, chatJID, waID))
	if err != nil {
		return m, err
	}
	meta := ParseMeta(m.MetaJSON)
	text, write := change(&m, &meta)
	if !write {
		return m, nil
	}
	if text != nil {
		m.Text = *text
	}
	m.MetaJSON = meta.JSON()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET meta_json = ?, text = ?, edited_at = ? WHERE instance_id = ? AND chat_jid = ? AND wa_message_id = ?`,
		m.MetaJSON, m.Text, fmtTS(m.EditedAt), instanceID, chatJID, waID); err != nil {
		return m, err
	}
	return m, tx.Commit()
}

// EditEvent replaces the details of a logged event (an edit or a cancellation), keeping
// the responses, and returns the updated message. ErrNotFound when it is not in the log
// or is not an event.
func (s *Store) EditEvent(ctx context.Context, instanceID, chatJID, waID string, card EventCard, at time.Time) (Message, error) {
	notEvent := false
	m, err := s.updateMeta(ctx, instanceID, chatJID, waID, func(m *Message, meta *MessageMeta) (*string, bool) {
		if m.Type != "event" || !m.RevokedAt.IsZero() {
			notEvent = true
			return nil, false
		}
		if meta.Event != nil {
			card.Responses = meta.Event.Responses
		}
		meta.Event = &card
		m.EditedAt = at
		return &card.Name, true
	})
	if err == nil && notEvent {
		return m, ErrNotFound
	}
	return m, err
}

// SetEventResponse records someone's answer to a logged event (the latest one wins) and
// returns the event. ErrNotFound when the event is not in the log.
func (s *Store) SetEventResponse(ctx context.Context, instanceID, chatJID, waID, who string, a EventAnswer) (Message, error) {
	notEvent := false
	m, err := s.updateMeta(ctx, instanceID, chatJID, waID, func(m *Message, meta *MessageMeta) (*string, bool) {
		if m.Type != "event" || meta.Event == nil {
			notEvent = true
			return nil, false
		}
		if old, ok := meta.Event.Responses[who]; ok && old.At.After(a.At) {
			return nil, false
		}
		if meta.Event.Responses == nil {
			meta.Event.Responses = map[string]EventAnswer{}
		}
		meta.Event.Responses[who] = a
		return nil, true
	})
	if err == nil && notEvent {
		return m, ErrNotFound
	}
	return m, err
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
