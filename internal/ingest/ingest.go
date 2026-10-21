// Package ingest writes worker events (messages, receipts, history sync) into the message
// log (spec 4.2) and publishes the matching events (spec 5.2) once they are stored.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

// Ingestor is a manager.Observer.
type Ingestor struct {
	Store *store.Store
	Log   *slog.Logger
	// Events, when set, receives message.received/sent/reaction/button_reply for new
	// messages and message.status for receipts that raised a status.
	Events events.Publisher
	// History, when set, aggregates history chunks into one history.synced per sync.
	History *events.HistoryTracker
}

func (in *Ingestor) StateChanged(string, instance.State, string) {}

func (in *Ingestor) WorkerEvent(instanceID string, ev *workerv1.Event) {
	ctx := context.Background()
	var err error
	switch {
	case ev.GetMessage() != nil:
		err = in.message(ctx, instanceID, ev.GetMessage())
	case ev.GetReceipt() != nil:
		r := ev.GetReceipt()
		st := r.Type // delivered | read | played (voice notes and videos)
		var changed []store.ReceiptChange
		changed, err = in.Store.ApplyReceipt(ctx, instanceID, r.MessageIds, st)
		if in.Events != nil {
			for _, c := range changed {
				in.Events.Publish(instanceID, events.TypeMessageStatus, events.MessageStatus{
					ID: c.ID, WAMessageID: c.WAMessageID, Chat: c.ChatJID, Status: st})
			}
		}
	case ev.GetHistory() != nil:
		err = in.history(ctx, instanceID, ev.GetHistory())
	}
	if err != nil {
		in.Log.Error("ingest worker event", "instance_id", instanceID, "err", err)
	}
}

// record converts a worker message into a log entry.
func record(m *workerv1.Message, source string) store.Message {
	rec := store.Message{
		ID: secure.NewID("msg_"), WAMessageID: m.Id, ChatJID: m.Chat, SenderJID: m.Sender,
		FromMe: m.FromMe, Source: source, Type: m.Type, Text: m.Text, ReactionTo: m.ReactionTo,
		Status: "received", Timestamp: time.UnixMilli(m.TimestampMs).UTC(),
	}
	if m.FromMe {
		rec.Status = "sent"
	}
	rec.MetaJSON = meta(m).JSON()
	if md := m.Media; md != nil {
		info := store.MediaInfo{ID: secure.NewID("med_"), Kind: md.Kind, Mime: md.Mime, Size: int64(md.Size),
			FileName: md.FileName, Seconds: int(md.Seconds), PTT: md.Ptt, ViewOnce: md.ViewOnce}
		b, _ := json.Marshal(info)
		rec.MediaJSON = string(b)
		rec.Media = &store.Media{ID: info.ID, Kind: md.Kind, Mime: md.Mime, Size: info.Size, FileName: md.FileName, Ref: md.Ref}
	}
	return rec
}

// meta converts the structured parts of a worker message.
func meta(m *workerv1.Message) store.MessageMeta {
	mm := store.MessageMeta{ReplyTo: m.QuotedId, Mentions: m.Mentions, Forwarded: m.Forwarded}
	if l := m.Location; l != nil {
		mm.Location = &store.Location{Latitude: l.Latitude, Longitude: l.Longitude, Name: l.Name, Address: l.Address, URL: l.Url}
	}
	for _, c := range m.Contacts {
		mm.Contacts = append(mm.Contacts, store.ContactCard{DisplayName: c.DisplayName, VCard: c.Vcard})
	}
	if p := m.Poll; p != nil {
		mm.Poll = &store.Poll{Name: p.Name, Options: p.Options, Selectable: int(p.Selectable)}
	}
	if b := m.ButtonReply; b != nil {
		mm.ButtonReply = &store.ButtonReply{Kind: b.Kind, ID: b.Id, Title: b.Title, Description: b.Description}
	}
	if p := m.Product; p != nil {
		mm.Product = &store.ProductCard{ProductID: p.ProductId, Title: p.Title, Description: p.Description,
			PriceAmount1000: p.PriceAmount_1000, Currency: p.Currency, RetailerID: p.RetailerId, URL: p.Url,
			BusinessOwner: p.BusinessOwnerJid, Body: p.Body, Footer: p.Footer}
	}
	if o := m.Order; o != nil { // the token is not part of the logged message
		mm.Order = &store.OrderCard{OrderID: o.OrderId, ItemCount: int(o.ItemCount), TotalAmount1000: o.TotalAmount_1000,
			Currency: o.Currency, Seller: o.SellerJid, Title: o.Title, Status: o.Status}
	}
	return mm
}

func chatUpdate(m *workerv1.Message, live bool) store.ChatUpdate {
	cu := store.ChatUpdate{IsGroup: m.IsGroup, Inbound: !m.FromMe}
	if !m.IsGroup && !m.FromMe {
		cu.Name = m.PushName // in groups the push name is the sender's, not the group's
	}
	cu.CountUnread = live && !m.FromMe && m.Type != "reaction"
	return cu
}

// source says who wrote a message: a contact, the API, or the account's other devices.
func (in *Ingestor) source(ctx context.Context, instanceID string, m *workerv1.Message) (string, error) {
	if !m.FromMe {
		return "contact", nil
	}
	// from_me events come from the account's other devices. Our own API sends are not
	// echoed, but check anyway: a from_me message the API did not send means a human
	// answered from the phone (the agent hook pauses on it, marco 5).
	own, err := in.Store.IsOwnSent(ctx, instanceID, m.Id)
	if err != nil || !own {
		return "device", err
	}
	return "api", nil
}

func (in *Ingestor) message(ctx context.Context, instanceID string, m *workerv1.Message) error {
	if m.Chat == "" || m.Id == "" {
		return nil
	}
	switch m.Type {
	case "edit", "revoke":
		return in.update(ctx, instanceID, m)
	case "poll_vote":
		return in.pollVote(ctx, instanceID, m)
	}
	source, err := in.source(ctx, instanceID, m)
	if err != nil {
		return err
	}
	rec := record(m, source)
	isNew, err := in.Store.InsertMessage(ctx, instanceID, rec, chatUpdate(m, true))
	if err != nil || !isNew {
		return err
	}
	var orderEv *events.OrderReceived
	if o := m.Order; o != nil && o.OrderId != "" {
		if orderEv, err = in.order(ctx, instanceID, m, rec); err != nil {
			return err
		}
	}
	if in.Events == nil {
		return nil
	}
	if orderEv != nil { // after the message itself
		defer in.Events.Publish(instanceID, events.TypeOrderReceived, *orderEv)
	}
	typ := events.TypeMessageReceived
	switch {
	case m.FromMe:
		typ = events.TypeMessageSent
	case m.Type == "reaction":
		typ = events.TypeMessageReaction
	case m.Type == "button_reply":
		typ = events.TypeMessageButton
	}
	in.Events.Publish(instanceID, typ, eventMessage(instanceID, m, rec))
	return nil
}

// order stores a received catalog order (with its token, kept server-side) and returns
// the order.received to publish for orders customers placed (not the account's own).
func (in *Ingestor) order(ctx context.Context, instanceID string, m *workerv1.Message, rec store.Message) (*events.OrderReceived, error) {
	o := m.Order
	if len(o.OrderId) > 64 || len(o.Token) > 4096 {
		return nil, nil // not a plausible order: logged as a message only
	}
	so := store.Order{OrderID: o.OrderId, MessageID: rec.ID, WAMessageID: rec.WAMessageID, ChatJID: rec.ChatJID,
		SellerJID: o.SellerJid, Token: o.Token, ItemCount: int(o.ItemCount), Total1000: o.TotalAmount_1000,
		Currency: o.Currency, Title: o.Title, Note: o.Message, Status: o.Status, FromMe: m.FromMe, ReceivedAt: rec.Timestamp}
	created, err := in.Store.UpsertOrder(ctx, instanceID, so)
	if err != nil || !created || m.FromMe {
		return nil, err
	}
	return &events.OrderReceived{
		OrderID: so.OrderID, MessageID: so.MessageID, WAMessageID: so.WAMessageID, Chat: so.ChatJID, Seller: so.SellerJID,
		ItemCount: so.ItemCount, TotalAmount1000: so.Total1000, Total: float64(so.Total1000) / 1000, Currency: so.Currency,
		Title: so.Title, Note: so.Note, Status: so.Status, PushName: m.PushName, Timestamp: rec.Timestamp}, nil
}

// eventMessage is the event data of a logged message.
func eventMessage(instanceID string, m *workerv1.Message, rec store.Message) events.Message {
	e := events.Message{
		ID: rec.ID, WAMessageID: rec.WAMessageID, Chat: rec.ChatJID, Sender: rec.SenderJID, FromMe: rec.FromMe,
		IsGroup: m.IsGroup, PushName: m.PushName, Source: rec.Source, Type: rec.Type, Text: rec.Text,
		ReactionTo: rec.ReactionTo, Timestamp: rec.Timestamp, MessageMeta: store.ParseMeta(rec.MetaJSON),
	}
	if md := rec.Media; md != nil {
		e.Media = &events.Media{ID: md.ID, Kind: md.Kind, Mime: md.Mime, Size: md.Size, FileName: md.FileName,
			Seconds: int(m.Media.GetSeconds()), PTT: m.Media.GetPtt(),
			URL: "/v1/instances/" + instanceID + "/media/" + md.ID}
	}
	return e
}

// update applies a received edit or revoke to the logged message and publishes
// message.edited / message.revoked (also when the message is not in the log).
func (in *Ingestor) update(ctx context.Context, instanceID string, m *workerv1.Message) error {
	if m.TargetId == "" {
		return nil
	}
	source, err := in.source(ctx, instanceID, m)
	if err != nil {
		return err
	}
	at := time.UnixMilli(m.TimestampMs).UTC()
	typ := events.TypeMessageEdited
	if m.Type == "revoke" {
		typ = events.TypeMessageRevoked
	}
	// Anyone who knows a message id can send an edit or revoke of it: only its author may
	// edit it, and only its author or a group admin may delete it (as WhatsApp clients do).
	rec, err := in.Store.MessageByWAID(ctx, instanceID, m.Chat, m.TargetId)
	switch {
	case errors.Is(err, store.ErrNotFound): // not in the log: the event still goes out
	case err != nil:
		return err
	case !mayChange(m, rec):
		in.Log.Warn("ignored "+m.Type+" of a message by someone else", "instance_id", instanceID, "chat", m.Chat, "target", m.TargetId)
		return nil
	case m.Type == "edit":
		rec, err = in.Store.EditMessage(ctx, instanceID, m.Chat, m.TargetId, m.Text, at)
	default:
		rec, err = in.Store.RevokeMessage(ctx, instanceID, m.Chat, m.TargetId, at)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if in.Events != nil {
		u := events.MessageUpdate{ID: rec.ID, WAMessageID: m.TargetId, Chat: m.Chat, Sender: m.Sender,
			FromMe: m.FromMe, Source: source, Timestamp: at}
		if m.Type == "edit" {
			u.Text = m.Text
		}
		in.Events.Publish(instanceID, typ, u)
	}
	return nil
}

// mayChange reports whether the update m may change the logged message target.
func mayChange(m *workerv1.Message, target store.Message) bool {
	sameAuthor := target.FromMe == m.FromMe && (m.FromMe || target.SenderJID == m.Sender)
	return sameAuthor || (m.Type == "revoke" && m.IsGroup && m.AdminRevoke)
}

// pollVote publishes message.poll_vote with the options matched by their SHA-256 against
// the poll in the log. Votes are not logged as messages.
func (in *Ingestor) pollVote(ctx context.Context, instanceID string, m *workerv1.Message) error {
	if m.TargetId == "" || in.Events == nil {
		return nil
	}
	v := events.PollVote{PollWAMessageID: m.TargetId, Chat: m.Chat, Voter: m.Sender, PushName: m.PushName,
		FromMe: m.FromMe, SelectedOptions: []string{}, Timestamp: time.UnixMilli(m.TimestampMs).UTC()}
	byHash := map[[32]byte]string{}
	if poll, err := in.Store.MessageByWAID(ctx, instanceID, m.Chat, m.TargetId); err == nil {
		v.PollID = poll.ID
		if p := store.ParseMeta(poll.MetaJSON).Poll; p != nil {
			for _, o := range p.Options {
				byHash[sha256.Sum256([]byte(o))] = o
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	for _, h := range m.PollHashes {
		var k [32]byte
		if len(h) == len(k) {
			copy(k[:], h)
			if o, ok := byHash[k]; ok {
				v.SelectedOptions = append(v.SelectedOptions, o)
				continue
			}
		}
		v.UnknownOptions++
	}
	in.Events.Publish(instanceID, events.TypeMessagePollVote, v)
	return nil
}

func (in *Ingestor) history(ctx context.Context, instanceID string, h *workerv1.HistoryChunk) error {
	msgs := make([]store.Message, 0, len(h.Messages))
	ups := make([]store.ChatUpdate, 0, len(h.Messages))
	for _, m := range h.Messages {
		if m.Chat == "" || m.Id == "" {
			continue
		}
		msgs = append(msgs, record(m, "history"))
		cu := chatUpdate(m, false)
		cu.Name, cu.IsGroup = h.Name, h.IsGroup // the conversation name is the chat's name
		ups = append(ups, cu)
	}
	n, err := in.Store.InsertMessages(ctx, instanceID, msgs, ups)
	if err == nil {
		in.Log.Info("history synced", "instance_id", instanceID, "chat_messages", len(msgs), "new", n)
		if in.History != nil {
			in.History.Add(instanceID, len(msgs), n)
		}
	}
	return err
}
