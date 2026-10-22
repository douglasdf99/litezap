package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/menu"
	"github.com/douglasdf99/litezap/internal/store"
)

// ---- orders built in a chat (cart) ----
//
// An order draft is priced by the menu's engine (menu.PriceOrder: options, sale prices,
// availability, hidden items), stored in LiteZap and sent to the chat as a summary
// through the send queue. The Atendente IA's agent.order_draft persists the same kind
// of draft (source "attendant"). Nothing is written to WhatsApp or Meta besides the
// summary message.

// Order draft sources.
const (
	OrderSourceAPI       = "api"
	OrderSourceMCP       = "mcp"
	OrderSourceDashboard = "dashboard"
	OrderSourceAgent     = "agent"
	OrderSourceAttendant = "attendant"
)

// OrderDraftInput builds a cart: lines with item_id (or name), quantity and the chosen
// option choices by name. dry_run prices it without saving.
type OrderDraftInput struct {
	// Chat is the customer's chat: a phone number with country code or a JID.
	Chat         string                `json:"chat,omitempty"`
	Items        []menu.OrderLineInput `json:"items"`
	CustomerName string                `json:"customer_name,omitempty"`
	Address      string                `json:"address,omitempty"`
	Notes        string                `json:"notes,omitempty"`
	DryRun       bool                  `json:"dry_run,omitempty"`
	// Source records who built it (api by default; set by the MCP tools and dashboard).
	Source string `json:"source,omitempty"`
}

// OrderDraftView is a priced cart, saved (id) or previewed.
type OrderDraftView struct {
	ID            string           `json:"id,omitempty"`
	Chat          string           `json:"chat,omitempty"`
	CustomerName  string           `json:"customer_name,omitempty"`
	Address       string           `json:"address,omitempty"`
	Notes         string           `json:"notes,omitempty"`
	Items         []menu.OrderLine `json:"items"`
	SubtotalCents int64            `json:"subtotal_cents"`
	TotalCents    int64            `json:"total_cents"`
	TotalText     string           `json:"total_text"`
	SavingsCents  int64            `json:"savings_cents"`
	Currency      string           `json:"currency"`
	// Valid is false when a line has a problem (Problems says which).
	Valid    bool     `json:"valid"`
	Problems []string `json:"problems"`
	// Status: draft, sent (the summary went to the chat), confirmed or cancelled.
	Status    string `json:"status,omitempty"`
	Source    string `json:"source,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	// Summary is the WhatsApp text send_order_summary sends.
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

func draftView(d store.OrderDraftRow) OrderDraftView {
	r := menu.FromDraft(d)
	v := resultView(r, menu.OrderInfo{CustomerName: d.CustomerName, Address: d.Address, Notes: d.Notes})
	v.ID, v.Chat, v.CustomerName, v.Address, v.Notes = d.ID, d.Chat, d.CustomerName, d.Address, d.Notes
	v.Status, v.Source, v.MessageID, v.CreatedAt, v.UpdatedAt = d.Status, d.Source, d.MessageID, d.CreatedAt, d.UpdatedAt
	return v
}

func resultView(r menu.OrderResult, info menu.OrderInfo) OrderDraftView {
	return OrderDraftView{Items: r.Lines, SubtotalCents: r.SubtotalCents, TotalCents: r.TotalCents, TotalText: menu.FormatPrice(r.TotalCents, r.Currency),
		SavingsCents: r.SavingsCents, Currency: r.Currency, Valid: r.Valid, Problems: r.Problems, Summary: menu.OrderSummary(r, info)}
}

var orderSources = []string{OrderSourceAPI, OrderSourceMCP, OrderSourceDashboard, OrderSourceAgent}

// CreateOrderDraft prices a cart from the menu and saves it (status draft). A cart with
// a problem (an item out of the menu or hidden, sold out, an unknown or missing
// required option) is refused with the problems listed, and the preview returned.
func (m *Menu) CreateOrderDraft(ctx context.Context, instanceID string, in OrderDraftInput) (OrderDraftView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return OrderDraftView{}, err
	}
	if len(in.Items) == 0 || len(in.Items) > menu.MaxOrderLines {
		return OrderDraftView{}, apierr.Validation(fmt.Sprintf("items lists 1 to %d lines (item_id, quantity, options)", menu.MaxOrderLines))
	}
	chat := ""
	if strings.TrimSpace(in.Chat) != "" {
		c, err := ChatJID(in.Chat)
		if err != nil {
			return OrderDraftView{}, apierr.Validation("chat is a phone number with country code or a WhatsApp JID")
		}
		chat = c
	}
	for _, f := range []struct {
		v    string
		n    int
		name string
	}{{in.CustomerName, 200, "customer_name"}, {in.Address, 500, "address"}, {in.Notes, menu.MaxOrderNotes, "notes"}} {
		if utf8.RuneCountInString(f.v) > f.n {
			return OrderDraftView{}, apierr.Validation(fmt.Sprintf("%s is limited to %d characters", f.name, f.n))
		}
	}
	source := in.Source
	if source == "" {
		source = OrderSourceAPI
	}
	if !containsStr(orderSources, source) {
		return OrderDraftView{}, apierr.Validation("source is one of " + strings.Join(orderSources, ", "))
	}
	for i := range in.Items {
		in.Items[i].FallbackUnitCents = 0 // never priced by the caller
	}
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return OrderDraftView{}, err
	}
	info := menu.OrderInfo{CustomerName: in.CustomerName, Address: in.Address, Notes: in.Notes}
	r := menu.PriceOrder(items, in.Items, menu.Today())
	v := resultView(r, info)
	v.Chat, v.CustomerName, v.Address, v.Notes = chat, menu.CleanLine(in.CustomerName), menu.CleanLine(in.Address), menu.CleanText(in.Notes)
	if in.DryRun {
		return v, nil
	}
	if !r.Valid {
		return v, apierr.Validation("nothing saved: " + strings.Join(r.Problems, "; "))
	}
	d, err := m.Store.CreateOrderDraft(ctx, instanceID, menu.DraftRow(r, chat, source, info))
	if err != nil {
		return OrderDraftView{}, err
	}
	return draftView(d), nil
}

// OrderDraftQuery filters the drafts.
type OrderDraftQuery struct {
	Status string `json:"status,omitempty"`
	Chat   string `json:"chat,omitempty"`
	Source string `json:"source,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// OrderDraftList is a page of drafts, newest first.
type OrderDraftList struct {
	Drafts []OrderDraftView `json:"drafts"`
}

var orderStatuses = []string{store.OrderDraft, store.OrderSent, store.OrderConfirmed, store.OrderCancelled}

// ListOrderDrafts lists the drafts, newest first.
func (m *Menu) ListOrderDrafts(ctx context.Context, instanceID string, q OrderDraftQuery) (OrderDraftList, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return OrderDraftList{}, err
	}
	if q.Status != "" && !containsStr(orderStatuses, q.Status) {
		return OrderDraftList{}, apierr.Validation("status is draft, sent, confirmed or cancelled")
	}
	if q.Limit < 0 || q.Limit > 200 {
		return OrderDraftList{}, apierr.Validation("limit is 1 to 200")
	}
	chat := ""
	if strings.TrimSpace(q.Chat) != "" {
		c, err := ChatJID(q.Chat)
		if err != nil {
			return OrderDraftList{}, apierr.Validation("chat is a phone number with country code or a WhatsApp JID")
		}
		chat = c
	}
	rows, err := m.Store.ListOrderDrafts(ctx, instanceID, store.OrderDraftFilter{Status: q.Status, Chat: chat, Source: q.Source, Limit: q.Limit})
	if err != nil {
		return OrderDraftList{}, err
	}
	out := OrderDraftList{Drafts: []OrderDraftView{}}
	for _, d := range rows {
		out.Drafts = append(out.Drafts, draftView(d))
	}
	return out, nil
}

// OrderDraft returns one draft.
func (m *Menu) OrderDraft(ctx context.Context, instanceID, id string) (OrderDraftView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return OrderDraftView{}, err
	}
	d, err := m.Store.GetOrderDraft(ctx, instanceID, id)
	if err != nil {
		return OrderDraftView{}, menuErr(err, "order draft")
	}
	return draftView(d), nil
}

// OrderStatusInput changes a draft's status.
type OrderStatusInput struct {
	Status string `json:"status"`
}

// SetOrderStatus moves a draft to sent, confirmed or cancelled (or back to draft from
// sent). A cancelled draft is final and a confirmed one can only be cancelled.
func (m *Menu) SetOrderStatus(ctx context.Context, instanceID, id string, in OrderStatusInput) (OrderDraftView, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return OrderDraftView{}, err
	}
	if !containsStr(orderStatuses, in.Status) {
		return OrderDraftView{}, apierr.Validation("status is draft, sent, confirmed or cancelled")
	}
	d, err := m.Store.SetOrderDraftStatus(ctx, instanceID, id, in.Status, "")
	if err != nil {
		return OrderDraftView{}, menuErr(err, "order draft")
	}
	return draftView(d), nil
}

// SendOrderInput sends a draft's summary to its chat (or to "to").
type SendOrderInput struct {
	To string `json:"to,omitempty"`
	// Intro is an optional line before the summary (up to 500 characters).
	Intro           string `json:"intro,omitempty"`
	ReplyTo         string `json:"reply_to,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	ExpiresAfter    string `json:"expires_after,omitempty"`
}

// SendOrderResult is the queued summary and the draft (now sent).
type SendOrderResult struct {
	Message MessageView    `json:"message"`
	Draft   OrderDraftView `json:"draft"`
}

// SendOrderSummary queues a draft's summary through the send queue (one text) and marks
// the draft sent. A cancelled or invalid draft is refused.
func (m *Menu) SendOrderSummary(ctx context.Context, instanceID, id string, in SendOrderInput, wait time.Duration) (SendOrderResult, error) {
	if _, err := m.instance(ctx, instanceID); err != nil {
		return SendOrderResult{}, err
	}
	if m.Sender == nil {
		return SendOrderResult{}, apierr.New(http.StatusNotImplemented, "not_implemented", "sending is not available", false)
	}
	if utf8.RuneCountInString(in.Intro) > 500 {
		return SendOrderResult{}, apierr.Validation("intro is limited to 500 characters")
	}
	d, err := m.Store.GetOrderDraft(ctx, instanceID, id)
	if err != nil {
		return SendOrderResult{}, menuErr(err, "order draft")
	}
	switch {
	case d.Status == store.OrderCancelled:
		return SendOrderResult{}, apierr.Validation("this order draft is cancelled")
	case !d.Valid:
		return SendOrderResult{}, apierr.Validation("this order draft has problems (items out of the menu, sold out or missing options): build a new one")
	}
	// The summary goes out only while it still matches the menu: an item hidden, sold
	// out, removed or repriced since the draft was built makes it stale.
	items, err := m.Store.ListMenuItems(ctx, instanceID)
	if err != nil {
		return SendOrderResult{}, err
	}
	stored := menu.FromDraft(d)
	now := menu.PriceOrder(items, menu.Inputs(stored.Lines), menu.Today())
	switch {
	case !now.Valid:
		return SendOrderResult{}, apierr.Validation("the menu changed since this order was built (" + strings.Join(now.Problems, "; ") +
			"): build the order again")
	case now.TotalCents != stored.TotalCents:
		return SendOrderResult{}, apierr.Validation(fmt.Sprintf("the prices changed since this order was built (total was %s, now %s): build the order again",
			menu.FormatPrice(stored.TotalCents, stored.Currency), menu.FormatPrice(now.TotalCents, now.Currency)))
	}
	to := strings.TrimSpace(in.To)
	if to == "" {
		to = d.Chat
	}
	if to == "" {
		return SendOrderResult{}, apierr.Validation("to is required: this draft has no chat")
	}
	v := draftView(d)
	text := v.Summary
	if intro := menu.CleanText(in.Intro); intro != "" {
		text = intro + "\n\n" + text
	}
	if in.ClientMessageID == "" { // a retry of the same summary never sends twice; a changed one is a new message
		sum := sha256.Sum256([]byte(to + "\x00" + text))
		in.ClientMessageID = "order:" + d.ID + ":" + hex.EncodeToString(sum[:8])
	}
	msg, err := m.Sender.Send(ctx, instanceID, SendInput{To: to, Type: "text", Text: text, ReplyTo: in.ReplyTo,
		ClientMessageID: in.ClientMessageID, ExpiresAfter: in.ExpiresAfter}, wait)
	if err != nil {
		return SendOrderResult{}, err
	}
	if d.Status == store.OrderDraft {
		if nd, err := m.Store.SetOrderDraftStatus(ctx, instanceID, id, store.OrderSent, msg.ID); err == nil {
			d = nd
		}
	}
	return SendOrderResult{Message: msg, Draft: draftView(d)}, nil
}
