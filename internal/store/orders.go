package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Order is an order received from the WhatsApp catalog (marco 12). Token authorizes
// reading the details and is never exposed by the API.
type Order struct {
	OrderID     string
	MessageID   string // LiteZap id of the logged order message
	WAMessageID string
	ChatJID     string
	SellerJID   string
	Token       string
	ItemCount   int
	Total1000   int64
	Currency    string
	Title       string
	Note        string
	Status      string
	FromMe      bool
	ReceivedAt  time.Time
}

// UpsertOrder stores an order; a repeated order id keeps the first message's ids and
// updates the rest. created reports whether it was new.
func (s *Store) UpsertOrder(ctx context.Context, instanceID string, o Order) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE instance_id = ? AND order_id = ?`,
		instanceID, o.OrderID).Scan(&n); err != nil {
		return false, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO orders (instance_id, order_id, message_id, wa_message_id, chat_jid, seller_jid,
		token, item_count, total_1000, currency, title, note, status, from_me, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, order_id) DO UPDATE SET token = excluded.token, item_count = excluded.item_count,
		total_1000 = excluded.total_1000, currency = excluded.currency, title = excluded.title, note = excluded.note,
		status = excluded.status`,
		instanceID, o.OrderID, o.MessageID, o.WAMessageID, o.ChatJID, o.SellerJID, o.Token, o.ItemCount, o.Total1000,
		o.Currency, o.Title, o.Note, o.Status, o.FromMe, fmtTS(o.ReceivedAt))
	return n == 0, err
}

const orderCols = `order_id, message_id, wa_message_id, chat_jid, seller_jid, token, item_count, total_1000, currency, title,
	note, status, from_me, received_at`

func scanOrder(sc interface{ Scan(...any) error }) (Order, error) {
	var o Order
	var ts string
	err := sc.Scan(&o.OrderID, &o.MessageID, &o.WAMessageID, &o.ChatJID, &o.SellerJID, &o.Token, &o.ItemCount, &o.Total1000,
		&o.Currency, &o.Title, &o.Note, &o.Status, &o.FromMe, &ts)
	o.ReceivedAt = parseTS(ts)
	return o, err
}

// GetOrder returns a stored order (ErrNotFound when unknown).
func (s *Store) GetOrder(ctx context.Context, instanceID, orderID string) (Order, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE instance_id = ? AND order_id = ?`,
		instanceID, orderID))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	return o, err
}

// RecentOrders lists the newest orders first.
func (s *Store) RecentOrders(ctx context.Context, instanceID string, limit int) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+orderCols+` FROM orders WHERE instance_id = ?
		ORDER BY received_at DESC, order_id DESC LIMIT ?`, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
