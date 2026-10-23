package worker

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// maxMentionAll bounds "mention everyone": larger groups are refused (mass pings look like
// spam and each mention notifies the member).
const maxMentionAll = 256

func (w *Whatsmeow) uploadFile(ctx context.Context, f io.Reader, mt whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	if w.upload != nil {
		return w.upload(ctx, f, mt)
	}
	// Streams the file (encrypted into a temporary file) instead of loading it in memory.
	return w.cli.UploadReader(ctx, f, nil, mt)
}

func (w *Whatsmeow) groupInfoOf(ctx context.Context, group types.JID) (*types.GroupInfo, error) {
	if w.groupInfo != nil {
		return w.groupInfo(ctx, group)
	}
	return w.cli.GetGroupInfo(ctx, group)
}

// ownJIDs returns the account's phone JID and LID (either may be empty).
func (w *Whatsmeow) ownJIDs() (pn, lid types.JID) {
	if w.cli == nil || w.cli.Store == nil {
		return
	}
	if w.cli.Store.ID != nil {
		pn = w.cli.Store.ID.ToNonAD()
	}
	return pn, w.cli.Store.LID.ToNonAD()
}

func parseOptJID(s string) (types.JID, error) {
	if s == "" {
		return types.EmptyJID, nil
	}
	return types.ParseJID(s)
}

func invalid(msg string) error { return grpcErr(codes.InvalidArgument, "validation_failed", msg) }

// build turns a SendRequest into the WhatsApp message to send (uploading media when needed).
func (w *Whatsmeow) build(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	var ci *waE2E.ContextInfo
	if req.Type == "disappearing" {
		return buildDisappearing(to, req)
	}
	if req.Type == "event_response" {
		return w.buildEventResponse(ctx, to, req)
	}
	if req.Type == "edit" && req.Event != nil {
		return w.buildEventEdit(ctx, to, req)
	}
	if req.Type != "edit" && req.Type != "revoke" && req.Type != "pin" {
		var renames map[string]string
		var err error
		if ci, renames, err = w.contextInfo(ctx, to, req); err != nil {
			return nil, err
		}
		if len(renames) > 0 {
			// A mention mapped to a LID must appear in the text as "@<lid user>": clients
			// highlight a mention by matching the @token with the JID.
			req = proto.Clone(req).(*workerv1.SendRequest)
			for pn, lid := range renames {
				req.Text = strings.ReplaceAll(req.Text, "@"+pn, "@"+lid)
			}
		}
	}
	var msg *waE2E.Message
	var err error
	switch req.Type {
	case "text":
		msg = w.buildText(req)
	case "reaction":
		msg, err = w.buildReaction(ctx, to, req)
	case "location":
		l := req.Location
		if l == nil {
			return nil, invalid("location is required")
		}
		lm := &waE2E.LocationMessage{DegreesLatitude: proto.Float64(l.Latitude), DegreesLongitude: proto.Float64(l.Longitude)}
		if l.Name != "" {
			lm.Name = proto.String(l.Name)
		}
		if l.Address != "" {
			lm.Address = proto.String(l.Address)
		}
		if l.Url != "" {
			lm.URL = proto.String(l.Url)
		}
		msg = &waE2E.Message{LocationMessage: lm}
	case "contact":
		msg, err = buildContacts(req.Contacts)
	case "poll":
		p := req.Poll
		if p == nil || p.Name == "" || len(p.Options) < 2 {
			return nil, invalid("poll needs a name and at least 2 options")
		}
		// BuildPollCreation adds the message secret; SendMessage stores it, so the votes on
		// this poll can be decrypted later.
		msg = w.cli.BuildPollCreation(p.Name, p.Options, int(p.Selectable))
	case "edit":
		if req.TargetId == "" || req.Text == "" {
			return nil, invalid("edit needs the target message and the new text")
		}
		return w.cli.BuildEdit(to, types.MessageID(req.TargetId), &waE2E.Message{Conversation: proto.String(req.Text)}), nil
	case "revoke":
		return w.buildRevoke(ctx, to, req)
	case "pin":
		return w.buildPin(ctx, to, req)
	case "forward":
		msg, err = buildForward(req.ForwardKind, req.ForwardRef)
	case "product":
		msg, err = w.buildProduct(ctx, req)
	case "event":
		msg, err = buildEvent(req)
	default:
		msg, err = w.buildUpload(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if ci != nil {
		if !setContextInfo(msg, ci) {
			return nil, invalid("reply_to, mentions and forwarding are not supported for " + req.Type)
		}
	}
	return msg, nil
}

// buildText uses ExtendedTextMessage when the text carries a preview, a reply or mentions.
func (w *Whatsmeow) buildText(req *workerv1.SendRequest) *waE2E.Message {
	lp := req.LinkPreview
	if lp == nil && req.Quote == nil && len(req.Mentions) == 0 && !req.MentionAll && !req.Forwarded {
		return &waE2E.Message{Conversation: proto.String(req.Text)}
	}
	ext := &waE2E.ExtendedTextMessage{Text: proto.String(req.Text)}
	if lp != nil && lp.Url != "" {
		ext.MatchedText = proto.String(lp.Url)
		ext.PreviewType = waE2E.ExtendedTextMessage_NONE.Enum()
		if lp.Title != "" {
			ext.Title = proto.String(lp.Title)
		}
		if lp.Description != "" {
			ext.Description = proto.String(lp.Description)
		}
		if len(lp.Thumbnail) > 0 {
			ext.JPEGThumbnail = lp.Thumbnail
		}
	}
	return &waE2E.Message{ExtendedTextMessage: ext}
}

func (w *Whatsmeow) buildReaction(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	sender := to // reacting to a message the contact sent in a direct chat
	if req.ReactionFromMe {
		sender = types.EmptyJID
	} else if req.ReactionSender != "" {
		var err error
		if sender, err = types.ParseJID(req.ReactionSender); err != nil {
			return nil, invalid("invalid reaction sender")
		}
	} else if to.Server == types.GroupServer {
		// The key of a group reaction names the author of the target message.
		return nil, invalid("reaction_sender is required in groups")
	}
	sender = w.groupParticipant(ctx, to, sender)
	return w.cli.BuildReaction(to, sender, types.MessageID(req.ReactionMessageId), req.Text), nil
}

// targetSender is the author named in the key of a revoke or pin: empty for our own
// messages, the contact in a direct chat, the participant (as the group addresses it) in a
// group.
func (w *Whatsmeow) targetSender(ctx context.Context, to types.JID, req *workerv1.SendRequest) (types.JID, error) {
	if req.TargetFromMe {
		return types.EmptyJID, nil
	}
	if to.Server != types.GroupServer {
		return to, nil
	}
	sender, err := parseOptJID(req.TargetSender)
	if err != nil || sender.IsEmpty() {
		return types.EmptyJID, invalid("the author of the target message is required in groups")
	}
	return w.groupParticipant(ctx, to, sender), nil
}

func (w *Whatsmeow) buildRevoke(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	if req.TargetId == "" {
		return nil, invalid("revoke needs the target message")
	}
	if !req.TargetFromMe {
		if to.Server != types.GroupServer {
			return nil, invalid("only your own messages can be deleted in a direct chat")
		}
		// Deleting someone else's message needs admin rights: check before sending (the
		// server would silently ignore it).
		if ok, err := w.isGroupAdmin(ctx, to); err != nil {
			return nil, sendErr(err)
		} else if !ok {
			return nil, grpcErr(codes.PermissionDenied, "forbidden", "the instance is not an admin of this group")
		}
	}
	sender, err := w.targetSender(ctx, to, req)
	if err != nil {
		return nil, err
	}
	return w.cli.BuildRevoke(to, sender, types.MessageID(req.TargetId)), nil
}

func (w *Whatsmeow) isGroupAdmin(ctx context.Context, group types.JID) (bool, error) {
	gi, err := w.groupInfoOf(ctx, group)
	if err != nil {
		return false, err
	}
	pn, lid := w.ownJIDs()
	for _, p := range gi.Participants {
		for _, j := range []types.JID{p.JID, p.PhoneNumber, p.LID} {
			if !j.IsEmpty() && ((!pn.IsEmpty() && j.User == pn.User) || (!lid.IsEmpty() && j.User == lid.User)) {
				return p.IsAdmin || p.IsSuperAdmin, nil
			}
		}
	}
	return false, nil
}

// buildPin builds a PinInChatMessage (no whatsmeow helper; SendMessage tags the node).
func (w *Whatsmeow) buildPin(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	if req.TargetId == "" {
		return nil, invalid("pin needs the target message")
	}
	sender, err := w.targetSender(ctx, to, req)
	if err != nil {
		return nil, err
	}
	typ := waE2E.PinInChatMessage_PIN_FOR_ALL
	if req.Unpin {
		typ = waE2E.PinInChatMessage_UNPIN_FOR_ALL
	}
	msg := &waE2E.Message{PinInChatMessage: &waE2E.PinInChatMessage{
		Key:               w.cli.BuildMessageKey(to, sender, types.MessageID(req.TargetId)),
		Type:              typ.Enum(),
		SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
	}}
	if !req.Unpin {
		secs := req.PinSeconds
		if secs == 0 {
			secs = 7 * 24 * 3600
		}
		msg.MessageContextInfo = &waE2E.MessageContextInfo{
			MessageAddOnDurationInSecs: proto.Uint32(secs),
			MessageAddOnExpiryType:     waE2E.MessageContextInfo_STATIC.Enum(),
		}
	}
	return msg, nil
}

func buildContacts(cards []*workerv1.ContactCard) (*waE2E.Message, error) {
	switch {
	case len(cards) == 0:
		return nil, invalid("contacts are required")
	case len(cards) == 1:
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
			DisplayName: proto.String(cards[0].DisplayName), Vcard: proto.String(cards[0].Vcard)}}, nil
	}
	list := make([]*waE2E.ContactMessage, len(cards))
	for i, c := range cards {
		list[i] = &waE2E.ContactMessage{DisplayName: proto.String(c.DisplayName), Vcard: proto.String(c.Vcard)}
	}
	return &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{
		DisplayName: proto.String(cards[0].DisplayName), Contacts: list}}, nil
}

// buildForward rebuilds a received attachment from its ref: the same encrypted file on
// WhatsApp's servers is sent again, without downloading or uploading it.
func buildForward(kind string, ref []byte) (*waE2E.Message, error) {
	if len(ref) == 0 {
		return nil, invalid("forward needs the original media")
	}
	m, err := downloadable(kind, ref)
	if err != nil {
		return nil, invalid("unreadable media reference")
	}
	if v, ok := m.(interface{ GetViewOnce() bool }); ok && v.GetViewOnce() {
		return nil, invalid("view-once media cannot be forwarded")
	}
	switch x := m.(type) {
	case *waE2E.ImageMessage:
		x.ContextInfo = nil
		return &waE2E.Message{ImageMessage: x}, nil
	case *waE2E.VideoMessage:
		x.ContextInfo = nil
		return &waE2E.Message{VideoMessage: x}, nil
	case *waE2E.AudioMessage:
		x.ContextInfo = nil
		return &waE2E.Message{AudioMessage: x}, nil
	case *waE2E.DocumentMessage:
		x.ContextInfo = nil
		return &waE2E.Message{DocumentMessage: x}, nil
	case *waE2E.StickerMessage:
		x.ContextInfo = nil
		return &waE2E.Message{StickerMessage: x}, nil
	}
	return nil, invalid("unsupported media kind " + kind)
}

// uploadType maps a send type to the media type of its upload.
func uploadType(typ string) (whatsmeow.MediaType, bool) {
	switch typ {
	case "gif", "ptv":
		return whatsmeow.MediaVideo, true
	case "sticker":
		return whatsmeow.MediaImage, true
	}
	return mediaType(typ)
}

func (w *Whatsmeow) buildUpload(ctx context.Context, req *workerv1.SendRequest) (*waE2E.Message, error) {
	mt, ok := uploadType(req.Type)
	if !ok {
		return nil, invalid("unsupported message type " + req.Type)
	}
	path, err := w.dataPath(req.MediaPath, "outbox")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, invalid("media file is missing")
	}
	var dims [2]uint32
	if req.Type == "sticker" {
		dims = webpSize(f)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, invalid("media file is unreadable")
		}
	}
	up, err := w.uploadFile(ctx, f, mt)
	f.Close()
	if err != nil {
		return nil, grpcErr(codes.Unavailable, "media_upload_failed", err.Error())
	}
	msg := buildMedia(req, up)
	if s := msg.GetStickerMessage(); s != nil && dims[0] > 0 {
		s.Width, s.Height = proto.Uint32(dims[0]), proto.Uint32(dims[1])
	}
	return msg, nil
}

// contextInfo builds the reply, mention and forwarding context of a message (nil: none).
// renames maps the phone users of mentions that the group addresses by LID to their LID
// users, so the text can be fixed to match.
func (w *Whatsmeow) contextInfo(ctx context.Context, to types.JID, req *workerv1.SendRequest) (ci *waE2E.ContextInfo, renames map[string]string, err error) {
	if req.Quote == nil && len(req.Mentions) == 0 && !req.MentionAll && !req.Forwarded {
		return nil, nil, nil
	}
	ci = &waE2E.ContextInfo{}
	if q := req.Quote; q != nil && q.MessageId != "" {
		ci.StanzaID = proto.String(q.MessageId)
		ci.QuotedMessage = &waE2E.Message{Conversation: proto.String(q.Text)}
		var author types.JID
		switch {
		case q.RemoteJid != "":
			// A message of another chat (a contact's status): its author must be given.
			a, err := parseOptJID(q.Sender)
			if err != nil || a.IsEmpty() {
				return nil, nil, invalid("the author of the quoted message is required")
			}
			ci.RemoteJID = proto.String(q.RemoteJid)
			author = a
		case q.FromMe:
			pn, lid := w.ownJIDs()
			author = pn
			if to.Server == types.GroupServer && !lid.IsEmpty() && w.groupUsesLID(ctx, to) {
				author = lid
			}
		case to.Server != types.GroupServer:
			author = to
		default:
			a, err := parseOptJID(q.Sender)
			if err != nil || a.IsEmpty() {
				return nil, nil, invalid("the author of the quoted message is required in groups")
			}
			author = w.groupParticipant(ctx, to, a)
		}
		if !author.IsEmpty() {
			ci.Participant = proto.String(author.ToNonAD().String())
		}
	}
	seen := map[string]bool{}
	addMention := func(j types.JID) {
		s := j.ToNonAD().String()
		if !seen[s] {
			seen[s] = true
			ci.MentionedJID = append(ci.MentionedJID, s)
		}
	}
	for _, m := range req.Mentions {
		j, err := types.ParseJID(m)
		if err != nil || j.User == "" {
			return nil, nil, invalid("invalid mention " + m)
		}
		mapped := w.groupParticipant(ctx, to, j)
		if mapped.User != j.User {
			if renames == nil {
				renames = map[string]string{}
			}
			renames[j.User] = mapped.User
		}
		addMention(mapped)
	}
	if req.MentionAll {
		if to.Server != types.GroupServer {
			return nil, nil, invalid("mentions \"all\" only works in groups")
		}
		gi, err := w.groupInfoOf(ctx, to)
		if err != nil {
			return nil, nil, sendErr(err)
		}
		if len(gi.Participants) > maxMentionAll {
			return nil, nil, invalid("mentions \"all\" is limited to groups of up to 256 participants")
		}
		pn, lid := w.ownJIDs()
		for _, p := range gi.Participants {
			if (!pn.IsEmpty() && p.JID.User == pn.User) || (!lid.IsEmpty() && p.JID.User == lid.User) {
				continue
			}
			addMention(p.JID)
		}
	}
	if req.Forwarded {
		ci.IsForwarded = proto.Bool(true)
		ci.ForwardingScore = proto.Uint32(1)
	}
	return ci, renames, nil
}

// setContextInfo attaches ci to the content of msg; false when the type has no context.
func setContextInfo(msg *waE2E.Message, ci *waE2E.ContextInfo) bool {
	switch {
	case msg.ExtendedTextMessage != nil:
		msg.ExtendedTextMessage.ContextInfo = ci
	case msg.ImageMessage != nil:
		msg.ImageMessage.ContextInfo = ci
	case msg.VideoMessage != nil:
		msg.VideoMessage.ContextInfo = ci
	case msg.PtvMessage != nil:
		msg.PtvMessage.ContextInfo = ci
	case msg.AudioMessage != nil:
		msg.AudioMessage.ContextInfo = ci
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.ContextInfo = ci
	case msg.StickerMessage != nil:
		msg.StickerMessage.ContextInfo = ci
	case msg.LocationMessage != nil:
		msg.LocationMessage.ContextInfo = ci
	case msg.ContactMessage != nil:
		msg.ContactMessage.ContextInfo = ci
	case msg.ContactsArrayMessage != nil:
		msg.ContactsArrayMessage.ContextInfo = ci
	case msg.PollCreationMessage != nil:
		msg.PollCreationMessage.ContextInfo = ci
	case msg.ProductMessage != nil:
		msg.ProductMessage.ContextInfo = ci
	case msg.EventMessage != nil:
		msg.EventMessage.ContextInfo = ci
	default:
		return false
	}
	return true
}

// webpSize reads the canvas size of a WebP file (VP8X, VP8 or VP8L); zero when unknown.
func webpSize(r io.Reader) [2]uint32 {
	var b [30]byte
	if n, _ := io.ReadFull(r, b[:]); n < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return [2]uint32{}
	}
	le24 := func(p []byte) uint32 { return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 }
	switch string(b[12:16]) {
	case "VP8X":
		return [2]uint32{le24(b[24:27]) + 1, le24(b[27:30]) + 1}
	case "VP8 ":
		if b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
			return [2]uint32{(uint32(b[26]) | uint32(b[27])<<8) & 0x3fff, (uint32(b[28]) | uint32(b[29])<<8) & 0x3fff}
		}
	case "VP8L":
		if b[20] == 0x2f {
			bits := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
			return [2]uint32{bits&0x3fff + 1, (bits>>14)&0x3fff + 1}
		}
	}
	return [2]uint32{}
}
