package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// historyChunkSize bounds the messages of one HistoryChunk event (and so the supervisor's
// write transaction for it).
const historyChunkSize = 250

var _ Messenger = (*Whatsmeow)(nil)

// grpcErr wraps a failure as a status whose message starts with a stable code.
func grpcErr(c codes.Code, code, msg string) error {
	return status.Error(c, code+": "+msg)
}

// sendErr maps whatsmeow send/upload errors onto the codes documented in worker.proto.
func sendErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrNotConnected):
		return grpcErr(codes.Unavailable, "instance_not_connected", "websocket not connected")
	case errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	case errors.Is(err, whatsmeow.ErrMessageTimedOut), errors.Is(err, context.DeadlineExceeded):
		return grpcErr(codes.DeadlineExceeded, "worker_timeout", err.Error())
	case errors.Is(err, whatsmeow.ErrRecipientADJID), errors.Is(err, whatsmeow.ErrUnknownServer):
		return grpcErr(codes.InvalidArgument, "invalid_number", err.Error())
	case errors.Is(err, whatsmeow.ErrNoPermissionInGroup):
		return grpcErr(codes.PermissionDenied, "forbidden", err.Error())
	case strings.Contains(err.Error(), "no LID found"):
		// whatsmeow cannot resolve a phone number that has no WhatsApp account.
		return grpcErr(codes.InvalidArgument, "invalid_number", "recipient is not on WhatsApp")
	}
	return grpcErr(codes.Unknown, "worker_error", err.Error())
}

func mediaType(kind string) (whatsmeow.MediaType, bool) {
	switch kind {
	case "image":
		return whatsmeow.MediaImage, true
	case "video":
		return whatsmeow.MediaVideo, true
	case "audio":
		return whatsmeow.MediaAudio, true
	case "document":
		return whatsmeow.MediaDocument, true
	}
	return "", false
}

// buildMedia builds the message for an uploaded attachment.
func buildMedia(req *workerv1.SendRequest, up whatsmeow.UploadResponse) *waE2E.Message {
	var caption *string
	if req.Text != "" {
		caption = proto.String(req.Text)
	}
	switch req.Type {
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption: caption, Mimetype: proto.String(req.Mime),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	case "video", "gif", "ptv":
		v := &waE2E.VideoMessage{
			Caption: caption, Mimetype: proto.String(req.Mime),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}
		switch req.Type {
		case "gif":
			v.GifPlayback = proto.Bool(true)
		case "ptv":
			v.Caption = nil // round videos have no caption
			return &waE2E.Message{PtvMessage: v}
		}
		return &waE2E.Message{VideoMessage: v}
	case "sticker":
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			Mimetype: proto.String("image/webp"), IsAnimated: proto.Bool(req.Animated),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	case "audio":
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			Mimetype: proto.String(req.Mime), PTT: proto.Bool(req.Ptt),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	default: // document
		name := req.FileName
		if name == "" {
			name = "file"
		}
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Caption: caption, Mimetype: proto.String(req.Mime), FileName: proto.String(name), Title: proto.String(name),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	}
}

// dataPath checks that p is a file directly inside <dataDir>/<area>/<instance>: Send only
// reads from "outbox" and DownloadMedia only writes to "media", each for this instance.
// The supervisor passes paths; the worker must not touch anything else (other instances,
// litezap.db, media_key, its own session).
func (w *Whatsmeow) dataPath(p, area string) (string, error) {
	if w.dataDir == "" || w.instanceID == "" || p == "" || !filepath.IsAbs(p) {
		return "", grpcErr(codes.InvalidArgument, "validation_failed", "media path must be absolute")
	}
	p = filepath.Clean(p)
	if filepath.Dir(p) != filepath.Join(w.dataDir, area, w.instanceID) {
		return "", grpcErr(codes.InvalidArgument, "validation_failed", "media path outside the instance's "+area+" directory")
	}
	return p, nil
}

func (w *Whatsmeow) Send(ctx context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	to, err := types.ParseJID(req.To)
	if err != nil || to.User == "" {
		return nil, grpcErr(codes.InvalidArgument, "invalid_number", "invalid recipient JID")
	}
	msg, err := w.build(ctx, to, req)
	if err != nil {
		return nil, err
	}
	resp, err := w.cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: types.MessageID(req.MessageId)})
	if err != nil {
		return nil, sendErr(err)
	}
	return &workerv1.SendResponse{MessageId: string(resp.ID), TimestampMs: resp.Timestamp.UnixMilli()}, nil
}

func (w *Whatsmeow) SendPresence(ctx context.Context, req *workerv1.SendPresenceRequest) error {
	to, err := types.ParseJID(req.To)
	if err != nil {
		return grpcErr(codes.InvalidArgument, "validation_failed", "invalid JID")
	}
	state, media := types.ChatPresenceComposing, types.ChatPresenceMediaText
	switch req.State {
	case "recording":
		media = types.ChatPresenceMediaAudio
	case "paused":
		state = types.ChatPresencePaused
	}
	if err := w.cli.SendChatPresence(ctx, to, state, media); err != nil {
		return sendErr(err)
	}
	return nil
}

func (w *Whatsmeow) MarkRead(ctx context.Context, req *workerv1.MarkReadRequest) error {
	chat, err := types.ParseJID(req.Chat)
	if err != nil {
		return grpcErr(codes.InvalidArgument, "validation_failed", "invalid chat JID")
	}
	sender := types.EmptyJID
	if req.Sender != "" {
		if sender, err = types.ParseJID(req.Sender); err != nil {
			return grpcErr(codes.InvalidArgument, "validation_failed", "invalid sender JID")
		}
	}
	ids := make([]types.MessageID, len(req.MessageIds))
	for i, id := range req.MessageIds {
		ids[i] = types.MessageID(id)
	}
	sender = w.groupParticipant(ctx, chat, sender)
	if err := w.cli.MarkRead(ctx, ids, time.Now(), chat, sender); err != nil {
		return sendErr(err)
	}
	return nil
}

// groupParticipant converts a phone-number participant to its LID when chat is a group
// that addresses members by LID: the key of a reaction and the read receipt must name the
// participant the way the group does. Anything it cannot determine (lookup failure, no
// mapping) leaves the participant as given.
func (w *Whatsmeow) groupParticipant(ctx context.Context, chat, p types.JID) types.JID {
	if chat.Server != types.GroupServer || p.Server != types.DefaultUserServer {
		return p
	}
	if !w.groupUsesLID(ctx, chat) {
		return p
	}
	lidFor := w.lidFor
	if lidFor == nil {
		if w.cli.Store.LIDs == nil {
			return p
		}
		lidFor = w.cli.Store.LIDs.GetLIDForPN
	}
	if lid, err := lidFor(ctx, p.ToNonAD()); err == nil && !lid.IsEmpty() {
		return lid.ToNonAD()
	}
	return p
}

// groupUsesLID reports whether the group addresses its members by LID. The answer is
// cached for the life of the worker (a group's addressing mode does not change); failed
// lookups are not cached.
func (w *Whatsmeow) groupUsesLID(ctx context.Context, group types.JID) bool {
	w.groupMu.Lock()
	lid, ok := w.groupLID[group]
	w.groupMu.Unlock()
	if ok {
		return lid
	}
	info := w.groupInfo
	if info == nil {
		info = w.cli.GetGroupInfo
	}
	gi, err := info(ctx, group)
	if err != nil || gi == nil {
		return false
	}
	lid = gi.AddressingMode == types.AddressingModeLID
	w.groupMu.Lock()
	if w.groupLID == nil {
		w.groupLID = map[types.JID]bool{}
	}
	w.groupLID[group] = lid
	w.groupMu.Unlock()
	return lid
}

// mediaRef serializes the attachment sub-message: it carries everything needed to download it later.
func mediaRef(m proto.Message) []byte {
	b, _ := proto.Marshal(m)
	return b
}

// downloadable rebuilds the attachment sub-message from a mediaRef.
func downloadable(kind string, ref []byte) (whatsmeow.DownloadableMessage, error) {
	var m interface {
		proto.Message
		whatsmeow.DownloadableMessage
	}
	switch kind {
	case "image":
		m = &waE2E.ImageMessage{}
	case "video":
		m = &waE2E.VideoMessage{}
	case "audio":
		m = &waE2E.AudioMessage{}
	case "document":
		m = &waE2E.DocumentMessage{}
	case "sticker":
		m = &waE2E.StickerMessage{}
	default:
		return nil, fmt.Errorf("unknown media kind %q", kind)
	}
	if err := proto.Unmarshal(ref, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (w *Whatsmeow) DownloadMedia(ctx context.Context, kind string, ref []byte, dest string) (int64, error) {
	msg, err := downloadable(kind, ref)
	if err != nil {
		return 0, grpcErr(codes.InvalidArgument, "validation_failed", err.Error())
	}
	path, err := w.dataPath(dest, "media")
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, grpcErr(codes.Unknown, "worker_error", "cannot create media file")
	}
	err = w.cli.DownloadToFile(ctx, msg, f)
	var size int64
	if fi, serr := f.Stat(); serr == nil {
		size = fi.Size()
	}
	f.Close()
	if err != nil {
		os.Remove(path)
		return 0, downloadErr(err)
	}
	return size, nil
}

// downloadErr maps a download failure: only 403/404/410 (and a missing URL) mean the
// media is gone for good; other HTTP statuses (5xx, 429) are worth retrying later.
func downloadErr(err error) error {
	var httpErr whatsmeow.DownloadHTTPError
	switch {
	case errors.As(err, &httpErr):
		switch httpErr.StatusCode {
		case 403, 404, 410:
			return grpcErr(codes.NotFound, "media_unavailable", err.Error())
		}
		return grpcErr(codes.Unavailable, "media_download_failed", err.Error())
	case errors.Is(err, whatsmeow.ErrNoURLPresent):
		return grpcErr(codes.NotFound, "media_unavailable", err.Error())
	}
	return grpcErr(codes.Unknown, "media_download_failed", err.Error())
}

func contactOf(jid types.JID, c types.ContactInfo) *workerv1.Contact {
	name := c.FullName
	if name == "" {
		name = c.FirstName
	}
	return &workerv1.Contact{Jid: jid.String(), Name: name, PushName: c.PushName, BusinessName: c.BusinessName}
}

func (w *Whatsmeow) Contacts(ctx context.Context) ([]*workerv1.Contact, error) {
	all, err := w.cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, grpcErr(codes.Unknown, "worker_error", err.Error())
	}
	out := make([]*workerv1.Contact, 0, len(all))
	for jid, c := range all {
		out = append(out, contactOf(jid, c))
	}
	return out, nil
}

func (w *Whatsmeow) Contact(ctx context.Context, raw string) (*workerv1.Contact, bool, error) {
	jid, err := types.ParseJID(raw)
	if err != nil {
		return nil, false, grpcErr(codes.InvalidArgument, "validation_failed", "invalid JID")
	}
	c, err := w.cli.Store.Contacts.GetContact(ctx, jid)
	if err != nil {
		return nil, false, grpcErr(codes.Unknown, "worker_error", err.Error())
	}
	return contactOf(jid, c), c.Found, nil
}

func (w *Whatsmeow) CheckNumbers(ctx context.Context, phones []string) ([]*workerv1.NumberStatus, error) {
	query := make([]string, len(phones))
	for i, p := range phones {
		query[i] = "+" + p // IsOnWhatsApp wants international format with "+"
	}
	res, err := w.cli.IsOnWhatsApp(ctx, query)
	if err != nil {
		return nil, sendErr(err)
	}
	byPhone := map[string]types.IsOnWhatsAppResponse{}
	for _, r := range res {
		byPhone[strings.TrimPrefix(r.Query, "+")] = r
	}
	out := make([]*workerv1.NumberStatus, 0, len(phones))
	for _, p := range phones {
		ns := &workerv1.NumberStatus{Phone: p}
		if r, ok := byPhone[p]; ok && r.IsIn {
			ns.Exists = true
			jid := r.JID
			if r.PhoneNumber.Server == types.DefaultUserServer {
				jid = r.PhoneNumber // prefer the phone JID over a LID
			}
			ns.Jid = jid.ToNonAD().String()
		}
		out = append(out, ns)
	}
	return out, nil
}

// pollCreation returns the poll of any poll creation version.
func pollCreation(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{m.GetPollCreationMessage(), m.GetPollCreationMessageV2(),
		m.GetPollCreationMessageV3(), m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6()} {
		if p != nil {
			return p
		}
	}
	return nil
}

// content extracts the LiteZap type, text, media and reaction target of a message.
// ok=false means the message carries nothing to log (protocol messages, revokes, edits...).
func content(m *waE2E.Message) (typ, text string, media *workerv1.Media, reactionTo string, ok bool) {
	switch {
	case m == nil:
		return "", "", nil, "", false
	case m.GetConversation() != "":
		return "text", m.GetConversation(), nil, "", true
	case m.GetExtendedTextMessage() != nil:
		return "text", m.GetExtendedTextMessage().GetText(), nil, "", true
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return "image", x.GetCaption(), &workerv1.Media{Kind: "image", Mime: x.GetMimetype(), Size: x.GetFileLength(), Ref: mediaRef(x)}, "", true
	case m.GetPtvMessage() != nil:
		x := m.GetPtvMessage()
		return "ptv", "", &workerv1.Media{Kind: "video", Mime: x.GetMimetype(), Size: x.GetFileLength(), Seconds: x.GetSeconds(), Ref: mediaRef(x)}, "", true
	case m.GetVideoMessage() != nil:
		x := m.GetVideoMessage()
		typ := "video"
		if x.GetGifPlayback() {
			typ = "gif"
		}
		return typ, x.GetCaption(), &workerv1.Media{Kind: "video", Mime: x.GetMimetype(), Size: x.GetFileLength(), Seconds: x.GetSeconds(), Ref: mediaRef(x)}, "", true
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		return "audio", "", &workerv1.Media{Kind: "audio", Mime: x.GetMimetype(), Size: x.GetFileLength(), Seconds: x.GetSeconds(), Ptt: x.GetPTT(), Ref: mediaRef(x)}, "", true
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		return "document", x.GetCaption(), &workerv1.Media{Kind: "document", Mime: x.GetMimetype(), Size: x.GetFileLength(), FileName: x.GetFileName(), Ref: mediaRef(x)}, "", true
	case m.GetStickerMessage() != nil:
		x := m.GetStickerMessage()
		return "sticker", "", &workerv1.Media{Kind: "sticker", Mime: x.GetMimetype(), Size: x.GetFileLength(), Ref: mediaRef(x)}, "", true
	case m.GetLocationMessage() != nil:
		x := m.GetLocationMessage()
		return "location", strings.TrimSpace(fmt.Sprintf("%f,%f %s", x.GetDegreesLatitude(), x.GetDegreesLongitude(), x.GetName())), nil, "", true
	case m.GetContactMessage() != nil:
		return "contact", m.GetContactMessage().GetDisplayName(), nil, "", true
	case m.GetContactsArrayMessage() != nil:
		return "contact", m.GetContactsArrayMessage().GetDisplayName(), nil, "", true
	case pollCreation(m) != nil:
		return "poll", pollCreation(m).GetName(), nil, "", true
	case m.GetReactionMessage() != nil:
		x := m.GetReactionMessage()
		return "reaction", x.GetText(), nil, x.GetKey().GetID(), true
	case m.GetProtocolMessage() != nil, m.GetSenderKeyDistributionMessage() != nil,
		m.GetPollUpdateMessage() != nil, m.GetPinInChatMessage() != nil, m.GetKeepInChatMessage() != nil,
		m.GetSecretEncryptedMessage() != nil:
		// Updates of other messages (edits, revokes and votes are reported by toUpdate).
		return "", "", nil, "", false
	}
	return "unknown", "", nil, "", true
}

// contextOf returns the ContextInfo of a message's content, if any.
func contextOf(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetPtvMessage() != nil:
		return m.GetPtvMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetLocationMessage() != nil:
		return m.GetLocationMessage().GetContextInfo()
	case m.GetContactMessage() != nil:
		return m.GetContactMessage().GetContextInfo()
	case m.GetContactsArrayMessage() != nil:
		return m.GetContactsArrayMessage().GetContextInfo()
	case pollCreation(m) != nil:
		return pollCreation(m).GetContextInfo()
	}
	return nil
}

// details fills the structured parts of a message: reply, mentions, location, contacts, poll.
func details(m *waE2E.Message, out *workerv1.Message) {
	if ci := contextOf(m); ci != nil {
		out.QuotedId = ci.GetStanzaID()
		out.Mentions = ci.GetMentionedJID()
		out.Forwarded = ci.GetIsForwarded()
	}
	if x := m.GetLocationMessage(); x != nil {
		out.Location = &workerv1.Location{Latitude: x.GetDegreesLatitude(), Longitude: x.GetDegreesLongitude(),
			Name: x.GetName(), Address: x.GetAddress(), Url: x.GetURL()}
	}
	if x := m.GetContactMessage(); x != nil {
		out.Contacts = []*workerv1.ContactCard{{DisplayName: x.GetDisplayName(), Vcard: x.GetVcard()}}
	}
	if x := m.GetContactsArrayMessage(); x != nil {
		for _, c := range x.GetContacts() {
			out.Contacts = append(out.Contacts, &workerv1.ContactCard{DisplayName: c.GetDisplayName(), Vcard: c.GetVcard()})
		}
	}
	if p := pollCreation(m); p != nil {
		poll := &workerv1.Poll{Name: p.GetName(), Selectable: p.GetSelectableOptionsCount()}
		for _, o := range p.GetOptions() {
			poll.Options = append(poll.Options, o.GetOptionName())
		}
		out.Poll = poll
	}
}

// lidLookupTimeout bounds each LID store lookup. History sync can block for a long time on
// backpressure, so no overall deadline covers a whole blob.
const lidLookupTimeout = 2 * time.Second

// lidMap maps LID users to phone-number JIDs learned from a history sync blob.
type lidMap map[string]types.JID

// add records lid -> pn; both may be full JIDs or bare user parts.
func (m lidMap) add(lid, pn string) {
	if lid == "" || pn == "" {
		return
	}
	l, err1 := parseJIDOr(lid, types.HiddenUserServer)
	p, err2 := parseJIDOr(pn, types.DefaultUserServer)
	if err1 != nil || err2 != nil || l.Server != types.HiddenUserServer || p.Server != types.DefaultUserServer {
		return
	}
	m[l.User] = p.ToNonAD()
}

func parseJIDOr(s, server string) (types.JID, error) {
	if !strings.Contains(s, "@") {
		return types.NewJID(s, server), nil
	}
	return types.ParseJID(s)
}

// phoneJID returns the phone-number JID for a LID when it is known (the blob's mappings,
// alt, or the LID store), so a contact keeps one chat whatever addressing WhatsApp uses.
func (w *Whatsmeow) phoneJID(jid, alt types.JID, known lidMap) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid.ToNonAD()
	}
	if pn, ok := known[jid.User]; ok {
		return pn
	}
	if alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if w.cli.Store.LIDs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), lidLookupTimeout)
		defer cancel()
		if pn, err := w.cli.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid.ToNonAD()
}

// addressing returns the chat and sender of a message, as phone-number JIDs when known.
func (w *Whatsmeow) addressing(info types.MessageInfo, known lidMap) (chat, sender types.JID) {
	chat = info.Chat.ToNonAD()
	if !info.IsGroup {
		alt := info.SenderAlt
		if info.IsFromMe {
			alt = info.RecipientAlt
		}
		chat = w.phoneJID(info.Chat, alt, known)
	}
	return chat, w.phoneJID(info.Sender, info.SenderAlt, known)
}

// outOfScope: status updates, broadcast lists and channels are not handled.
func outOfScope(info types.MessageInfo) bool {
	return info.Chat.Server == types.BroadcastServer || info.Chat.Server == types.NewsletterServer
}

// toMessage normalizes a whatsmeow message; nil means it is not logged.
func (w *Whatsmeow) toMessage(info types.MessageInfo, m *waE2E.Message, known lidMap) *workerv1.Message {
	if outOfScope(info) {
		return nil
	}
	typ, text, media, reactionTo, ok := content(m)
	if !ok {
		return nil
	}
	chat, sender := w.addressing(info, known)
	out := &workerv1.Message{
		Id: string(info.ID), Chat: chat.String(), Sender: sender.String(),
		FromMe: info.IsFromMe, IsGroup: info.IsGroup, TimestampMs: info.Timestamp.UnixMilli(),
		PushName: info.PushName, Type: typ, Text: text, Media: media, ReactionTo: reactionTo,
	}
	details(m, out)
	return out
}

// editText is the new text carried by an edit (nil content: not an edit we can show).
func editText(m *waE2E.Message) (string, bool) {
	if pm := m.GetProtocolMessage(); pm != nil && pm.GetEditedMessage() != nil {
		m = pm.GetEditedMessage()
	}
	_, t, _, _, ok := content(m)
	return t, ok
}

// toUpdate reports an edit, revoke or poll vote of the message target (nil: not one).
func (w *Whatsmeow) toUpdate(e *events.Message) *workerv1.Message {
	m := e.Message
	if outOfScope(e.Info) || m == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lidLookupTimeout)
	defer cancel()
	u := &workerv1.Message{}
	switch {
	case m.GetProtocolMessage() != nil:
		pm := m.GetProtocolMessage()
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			u.Type, u.TargetId = "revoke", pm.GetKey().GetID()
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			t, ok := editText(pm.GetEditedMessage())
			if !ok {
				return nil
			}
			u.Type, u.TargetId, u.Text = "edit", pm.GetKey().GetID(), t
		default:
			return nil
		}
	case m.GetSecretEncryptedMessage() != nil:
		sm := m.GetSecretEncryptedMessage()
		if sm.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
			return nil
		}
		decrypt := w.decryptSecret
		if decrypt == nil {
			decrypt = w.cli.DecryptSecretEncryptedMessage
		}
		dm, err := decrypt(ctx, e)
		if err != nil {
			w.log.Warn("cannot decrypt message edit", "id", e.Info.ID, "err", err)
			return nil
		}
		t, ok := editText(dm)
		if !ok {
			return nil
		}
		u.Type, u.TargetId, u.Text = "edit", sm.GetTargetMessageKey().GetID(), t
	case m.GetPollUpdateMessage() != nil:
		decrypt := w.decryptPoll
		if decrypt == nil {
			decrypt = w.cli.DecryptPollVote
		}
		vote, err := decrypt(ctx, e)
		if err != nil {
			// Votes on polls created before pairing cannot be decrypted (no secret).
			w.log.Warn("cannot decrypt poll vote", "id", e.Info.ID, "err", err)
			return nil
		}
		u.Type, u.TargetId = "poll_vote", m.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()
		u.PollHashes = vote.GetSelectedOptions() // empty: the voter removed their vote
	default:
		return nil
	}
	if u.TargetId == "" {
		return nil
	}
	chat, sender := w.addressing(e.Info, nil)
	u.Id, u.Chat, u.Sender, u.FromMe, u.IsGroup = string(e.Info.ID), chat.String(), sender.String(), e.Info.IsFromMe, e.Info.IsGroup
	u.TimestampMs, u.PushName = e.Info.Timestamp.UnixMilli(), e.Info.PushName
	return u
}

func receiptType(t types.ReceiptType) (string, bool) {
	switch t {
	case types.ReceiptTypeDelivered, types.ReceiptTypeInactive:
		return "delivered", true
	case types.ReceiptTypeRead:
		return "read", true
	case types.ReceiptTypePlayed:
		return "played", true
	}
	return "", false
}

// onData handles the message-related whatsmeow events; it reports whether evt was one.
func (w *Whatsmeow) onData(evt any) bool {
	switch e := evt.(type) {
	case *events.Message:
		if u := w.toUpdate(e); u != nil {
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Message{Message: u}})
		} else if m := w.toMessage(e.Info, e.Message, nil); m != nil {
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Message{Message: m}})
		}
	case *events.Receipt:
		typ, ok := receiptType(e.Type)
		if !ok || e.IsFromMe { // our own devices' read receipts are not delivery reports
			return true
		}
		ids := make([]string, len(e.MessageIDs))
		for i, id := range e.MessageIDs {
			ids[i] = string(id)
		}
		w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: &workerv1.Receipt{
			MessageIds: ids, Chat: e.Chat.ToNonAD().String(), Type: typ, TimestampMs: e.Timestamp.UnixMilli(),
		}}})
	case *events.HistorySync:
		w.onHistory(e)
	default:
		return false
	}
	return true
}

// onHistory turns a history blob into HistoryChunk events. Sink.Data may block for a long
// time (backpressure), so nothing here has an overall deadline. LIDs are resolved from the
// blob's own mappings first: whatsmeow stores them from a goroutine that can still be
// running, so the LID store alone is not reliable yet.
func (w *Whatsmeow) onHistory(e *events.HistorySync) {
	known := lidMap{}
	for _, m := range e.Data.GetPhoneNumberToLidMappings() {
		known.add(m.GetLidJID(), m.GetPnJID())
	}
	for _, conv := range e.Data.GetConversations() {
		known.add(conv.GetLidJID(), conv.GetPnJID())
		if id := conv.GetID(); strings.HasSuffix(id, "@"+types.HiddenUserServer) {
			known.add(id, conv.GetPnJID())
		}
	}
	for _, conv := range e.Data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		name := conv.GetName()
		if name == "" {
			name = conv.GetDisplayName()
		}
		var msgs []*workerv1.Message
		flush := func() {
			if len(msgs) == 0 {
				return
			}
			chat := msgs[0].Chat
			w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{
				Chat: chat, Name: name, IsGroup: chatJID.Server == types.GroupServer, Messages: msgs,
			}}})
			msgs = nil
		}
		for _, hm := range conv.GetMessages() {
			evt, err := w.cli.ParseWebMessage(chatJID, hm.GetMessage())
			if err != nil {
				continue
			}
			if m := w.toMessage(evt.Info, evt.Message, known); m != nil {
				msgs = append(msgs, m)
			}
			if len(msgs) >= historyChunkSize {
				flush()
			}
		}
		flush()
	}
}
