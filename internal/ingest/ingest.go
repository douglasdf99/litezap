// Package ingest writes worker events (messages, receipts, history sync) into the message
// log (spec 4.2) and publishes the matching events (spec 5.2) once they are stored.
package ingest

import (
	"context"
	"encoding/json"
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
	// Events, when set, receives message.received/sent/reaction for new messages and
	// message.status for receipts that raised a status.
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
		st := r.Type
		if st == "played" {
			st = "read"
		}
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
	if md := m.Media; md != nil {
		info := store.MediaInfo{ID: secure.NewID("med_"), Kind: md.Kind, Mime: md.Mime, Size: int64(md.Size),
			FileName: md.FileName, Seconds: int(md.Seconds), PTT: md.Ptt}
		b, _ := json.Marshal(info)
		rec.MediaJSON = string(b)
		rec.Media = &store.Media{ID: info.ID, Kind: md.Kind, Mime: md.Mime, Size: info.Size, FileName: md.FileName, Ref: md.Ref}
	}
	return rec
}

func chatUpdate(m *workerv1.Message, live bool) store.ChatUpdate {
	cu := store.ChatUpdate{IsGroup: m.IsGroup, Inbound: !m.FromMe}
	if !m.IsGroup && !m.FromMe {
		cu.Name = m.PushName // in groups the push name is the sender's, not the group's
	}
	cu.CountUnread = live && !m.FromMe && m.Type != "reaction"
	return cu
}

func (in *Ingestor) message(ctx context.Context, instanceID string, m *workerv1.Message) error {
	if m.Chat == "" || m.Id == "" {
		return nil
	}
	source := "contact"
	if m.FromMe {
		// from_me events come from the account's other devices. Our own API sends are not
		// echoed, but check anyway: a from_me message the API did not send means a human
		// answered from the phone (the agent hook pauses on it, marco 5).
		own, err := in.Store.IsOwnSent(ctx, instanceID, m.Id)
		if err != nil {
			return err
		}
		source = "device"
		if own {
			source = "api"
		}
	}
	rec := record(m, source)
	isNew, err := in.Store.InsertMessage(ctx, instanceID, rec, chatUpdate(m, true))
	if err != nil || !isNew || in.Events == nil {
		return err
	}
	typ := events.TypeMessageReceived
	switch {
	case m.FromMe:
		typ = events.TypeMessageSent
	case m.Type == "reaction":
		typ = events.TypeMessageReaction
	}
	in.Events.Publish(instanceID, typ, eventMessage(instanceID, m, rec))
	return nil
}

// eventMessage is the event data of a logged message.
func eventMessage(instanceID string, m *workerv1.Message, rec store.Message) events.Message {
	e := events.Message{
		ID: rec.ID, WAMessageID: rec.WAMessageID, Chat: rec.ChatJID, Sender: rec.SenderJID, FromMe: rec.FromMe,
		IsGroup: m.IsGroup, PushName: m.PushName, Source: rec.Source, Type: rec.Type, Text: rec.Text,
		ReactionTo: rec.ReactionTo, Timestamp: rec.Timestamp,
	}
	if md := rec.Media; md != nil {
		e.Media = &events.Media{ID: md.ID, Kind: md.Kind, Mime: md.Mime, Size: md.Size, FileName: md.FileName,
			Seconds: int(m.Media.GetSeconds()), PTT: m.Media.GetPtt(),
			URL: "/v1/instances/" + instanceID + "/media/" + md.ID}
	}
	return e
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
