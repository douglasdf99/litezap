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

// Caps on stored orders (an order message is written by the customer: a flood of fake
// orders must not fill the table). Counted over the last 24 hours of storage time.
const (
	MaxOrdersPerChatDay     = 20
	MaxOrdersPerInstanceDay = 1000
)

// ErrOrderCap means an order was not stored because a cap was reached.
var ErrOrderCap = errors.New("order cap reached")

// UpsertOrder stores an order. created reports whether this call inserted it (decided
// by the insert itself, so concurrent copies of one order publish once). A repeated
// order id only updates the row when it comes from the same chat: another chat can never
// take over an order (and its token). New orders over the caps return ErrOrderCap.
func (s *Store) UpsertOrder(ctx context.Context, instanceID string, o Order) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	upd, err := tx.ExecContext(ctx, `UPDATE orders SET token = ?, item_count = ?, total_1000 = ?, currency = ?, title = ?,
		note = ?, status = ? WHERE instance_id = ? AND order_id = ? AND chat_jid = ?`,
		o.Token, o.ItemCount, o.Total1000, o.Currency, o.Title, o.Note, o.Status, instanceID, o.OrderID, o.ChatJID)
	if err != nil {
		return false, err
	}
	if n, _ := upd.RowsAffected(); n > 0 {
		return false, tx.Commit()
	}
	since := fmtTS(time.Now().Add(-24 * time.Hour))
	var perChat, perInstance int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE instance_id = ? AND chat_jid = ? AND stored_at >= ?`,
		instanceID, o.ChatJID, since).Scan(&perChat); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE instance_id = ? AND stored_at >= ?`,
		instanceID, since).Scan(&perInstance); err != nil {
		return false, err
	}
	if perChat >= MaxOrdersPerChatDay || perInstance >= MaxOrdersPerInstanceDay {
		return false, ErrOrderCap
	}
	ins, err := tx.ExecContext(ctx, `INSERT INTO orders (instance_id, order_id, message_id, wa_message_id, chat_jid, seller_jid,
		token, item_count, total_1000, currency, title, note, status, from_me, received_at, stored_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (instance_id, order_id) DO NOTHING`,
		instanceID, o.OrderID, o.MessageID, o.WAMessageID, o.ChatJID, o.SellerJID, o.Token, o.ItemCount, o.Total1000,
		o.Currency, o.Title, o.Note, o.Status, o.FromMe, fmtTS(o.ReceivedAt), now())
	if err != nil {
		return false, err
	}
	n, err := ins.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
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
