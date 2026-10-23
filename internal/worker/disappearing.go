package worker

import (
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Disappearing messages (CHT-9). whatsmeow's SetDisappearingTimer sends the setting but
// does not mark later messages: every message sent in a chat with a timer must carry the
// timer in its ContextInfo (expiration and the time it was set), or it stays forever on
// the contact's phone while the chat shows a timer.

// validDisappearing reports whether s is a timer the official apps offer.
func validDisappearing(s uint32) bool {
	switch s {
	case 0, 86400, 7 * 86400, 90 * 86400:
		return true
	}
	return false
}

// buildDisappearing is the protocol message that changes a direct chat's timer (what
// SetDisappearingTimer sends), built here so it goes out with the queue's message id.
func buildDisappearing(to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	if to.Server != types.DefaultUserServer && to.Server != types.HiddenUserServer {
		return nil, invalid("disappearing messages of groups are changed through UpdateGroup")
	}
	if !validDisappearing(req.DisappearingSeconds) {
		return nil, invalid("disappearing timer must be 0, 86400, 604800 or 7776000 seconds")
	}
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:                      waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(),
		EphemeralExpiration:       proto.Uint32(req.DisappearingSeconds),
		EphemeralSettingTimestamp: proto.Int64(time.Now().Unix()),
	}}, nil
}

// applyEphemeral marks msg with the chat's disappearing timer. A plain text becomes an
// extended text (a Conversation has no ContextInfo). Types without a context (reactions,
// protocol messages) are left alone.
func applyEphemeral(msg *waE2E.Message, seconds uint32, settingTS int64) {
	if msg == nil || seconds == 0 {
		return
	}
	if msg.Conversation != nil {
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{Text: msg.Conversation}
		msg.Conversation = nil
	}
	ci := outgoingContext(msg)
	if ci == nil {
		return
	}
	ci.Expiration = proto.Uint32(seconds)
	if settingTS > 0 {
		ci.EphemeralSettingTimestamp = proto.Int64(settingTS)
	}
}

// outgoingContext returns the ContextInfo of a message's content, creating it when the
// content can carry one (nil otherwise).
func outgoingContext(msg *waE2E.Message) *waE2E.ContextInfo {
	ensure := func(ci **waE2E.ContextInfo) *waE2E.ContextInfo {
		if *ci == nil {
			*ci = &waE2E.ContextInfo{}
		}
		return *ci
	}
	switch {
	case msg.ExtendedTextMessage != nil:
		return ensure(&msg.ExtendedTextMessage.ContextInfo)
	case msg.ImageMessage != nil:
		return ensure(&msg.ImageMessage.ContextInfo)
	case msg.VideoMessage != nil:
		return ensure(&msg.VideoMessage.ContextInfo)
	case msg.PtvMessage != nil:
		return ensure(&msg.PtvMessage.ContextInfo)
	case msg.AudioMessage != nil:
		return ensure(&msg.AudioMessage.ContextInfo)
	case msg.DocumentMessage != nil:
		return ensure(&msg.DocumentMessage.ContextInfo)
	case msg.StickerMessage != nil:
		return ensure(&msg.StickerMessage.ContextInfo)
	case msg.LocationMessage != nil:
		return ensure(&msg.LocationMessage.ContextInfo)
	case msg.ContactMessage != nil:
		return ensure(&msg.ContactMessage.ContextInfo)
	case msg.ContactsArrayMessage != nil:
		return ensure(&msg.ContactsArrayMessage.ContextInfo)
	case msg.PollCreationMessage != nil:
		return ensure(&msg.PollCreationMessage.ContextInfo)
	case msg.ProductMessage != nil:
		return ensure(&msg.ProductMessage.ContextInfo)
	case msg.EventMessage != nil:
		return ensure(&msg.EventMessage.ContextInfo)
	}
	return nil
}

// disappearingUpdate turns a received EPHEMERAL_SETTING protocol message (the contact, or
// another device of the account, changed a direct chat's timer) into a ChatUpdate; nil
// when e is not one. Group timers arrive as group info changes instead.
func (w *Whatsmeow) disappearingUpdate(e *events.Message) *workerv1.ChatUpdate {
	pm := e.Message.GetProtocolMessage()
	if pm == nil || pm.GetType() != waE2E.ProtocolMessage_EPHEMERAL_SETTING || e.Info.IsGroup || outOfScope(e.Info) {
		return nil
	}
	chat, _ := w.addressing(e.Info, nil)
	ts := e.Info.Timestamp
	if s := pm.GetEphemeralSettingTimestamp(); s > 0 {
		ts = time.Unix(s, 0)
	}
	return &workerv1.ChatUpdate{Chat: chat.String(), Field: "disappearing", Value: pm.GetEphemeralExpiration() > 0,
		DisappearingSeconds: pm.GetEphemeralExpiration(), TimestampMs: ts.UnixMilli(), FromMe: e.Info.IsFromMe}
}
