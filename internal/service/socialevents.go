package service

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

// SocialEvents is a manager.Observer for the marco 10 worker events: contacts' status
// posts are logged in the status@broadcast chat (their media can be downloaded like any
// received attachment) and published as status.received; channel posts are published as
// channel.message (not logged); label changes update the label tables and publish
// label.updated (not for the full sync, nor for echoes of changes already applied).
type SocialEvents struct {
	Store  *store.Store
	Events events.Publisher
	Log    *slog.Logger
}

func (o *SocialEvents) StateChanged(string, instance.State, string) {}

func (o *SocialEvents) WorkerEvent(instanceID string, ev *workerv1.Event) {
	var err error
	switch {
	case ev.GetStatus() != nil:
		err = o.status(instanceID, ev.GetStatus())
	case ev.GetChannelPost() != nil:
		o.channelPost(instanceID, ev.GetChannelPost())
	case ev.GetLabel() != nil:
		err = o.label(instanceID, ev.GetLabel())
	default:
		return
	}
	if err != nil && o.Log != nil {
		o.Log.Error("social worker event", "instance_id", instanceID, "err", err)
	}
}

func (o *SocialEvents) publish(instanceID, typ string, data any) {
	if o.Events != nil {
		o.Events.Publish(instanceID, typ, data)
	}
}

func (o *SocialEvents) status(instanceID string, m *workerv1.Message) error {
	if m.Id == "" || m.Sender == "" {
		return nil
	}
	rec := store.Message{
		ID: secure.NewID("msg_"), WAMessageID: m.Id, ChatJID: store.StatusChat, SenderJID: m.Sender,
		Source: "contact", Type: m.Type, Text: m.Text, Status: "received", Timestamp: time.UnixMilli(m.TimestampMs).UTC(),
	}
	if md := m.Media; md != nil {
		info := store.MediaInfo{ID: secure.NewID("med_"), Kind: md.Kind, Mime: md.Mime, Size: int64(md.Size),
			FileName: md.FileName, Seconds: int(md.Seconds), PTT: md.Ptt}
		b, _ := json.Marshal(info)
		rec.MediaJSON = string(b)
		rec.Media = &store.Media{ID: info.ID, Kind: md.Kind, Mime: md.Mime, Size: info.Size, FileName: md.FileName, Ref: md.Ref}
	}
	// Not a conversation: no unread count, no inbound mark (the chat stays out of lists).
	isNew, err := o.Store.InsertMessage(context.Background(), instanceID, rec, store.ChatUpdate{IsGroup: true})
	if err != nil || !isNew {
		return err
	}
	e := events.Message{ID: rec.ID, WAMessageID: m.Id, Chat: store.StatusChat, Sender: m.Sender, PushName: m.PushName,
		Source: "contact", Type: m.Type, Text: m.Text, Timestamp: rec.Timestamp}
	if md := rec.Media; md != nil {
		e.Media = &events.Media{ID: md.ID, Kind: md.Kind, Mime: md.Mime, Size: md.Size, FileName: md.FileName,
			Seconds: int(m.Media.GetSeconds()), URL: "/v1/instances/" + instanceID + "/media/" + md.ID}
	}
	o.publish(instanceID, events.TypeStatusReceived, e)
	return nil
}

func (o *SocialEvents) channelPost(instanceID string, p *workerv1.ChannelPost) {
	m := p.GetMessage()
	e := events.ChannelMessage{Channel: p.Channel, ServerID: p.ServerId, WAMessageID: m.GetId(), Type: m.GetType(),
		Text: m.GetText(), Views: p.Views, Reactions: p.Reactions, Timestamp: time.UnixMilli(m.GetTimestampMs()).UTC()}
	if md := m.GetMedia(); md != nil {
		e.Media = &events.Media{Kind: md.Kind, Mime: md.Mime, Size: int64(md.Size), FileName: md.FileName, Seconds: int(md.Seconds)}
	}
	o.publish(instanceID, events.TypeChannelMessage, e)
}

func (o *SocialEvents) label(instanceID string, u *workerv1.LabelUpdate) error {
	ctx := context.Background()
	at := time.UnixMilli(u.TimestampMs).UTC()
	if u.TimestampMs == 0 {
		at = time.Now().UTC()
	}
	ev := events.LabelUpdated{Kind: u.Kind, LabelID: u.LabelId, Source: "device", Timestamp: at}
	var changed bool
	var err error
	switch u.Kind {
	case "edit":
		color := int(u.Color)
		changed, err = o.Store.UpsertLabel(ctx, instanceID, store.Label{ID: u.LabelId, Name: u.Name, Color: color,
			PredefinedID: int(u.PredefinedId), Deleted: u.Deleted})
		ev.Name, ev.Color, ev.Deleted = u.Name, &color, u.Deleted
	case "chat":
		if u.Chat == "" {
			return nil
		}
		labeled := u.Labeled
		changed, err = o.Store.SetLabelChat(ctx, instanceID, u.LabelId, u.Chat, labeled)
		ev.Chat, ev.Labeled = u.Chat, &labeled
	case "message":
		if u.Chat == "" || u.MessageId == "" {
			return nil
		}
		labeled := u.Labeled
		changed, err = o.Store.SetLabelMessage(ctx, instanceID, u.LabelId, u.Chat, u.MessageId, labeled)
		ev.Chat, ev.WAMessageID, ev.Labeled = u.Chat, u.MessageId, &labeled
	default:
		return nil
	}
	if err != nil || !changed || u.FullSync {
		return err
	}
	o.publish(instanceID, events.TypeLabelUpdated, ev)
	return nil
}
