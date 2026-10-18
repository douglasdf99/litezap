package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// composeAdapter is a messaging adapter whose uploads succeed locally.
func composeAdapter(t *testing.T) (*Whatsmeow, string) {
	t.Helper()
	w := newMsgAdapter(t)
	w.dataDir, w.instanceID = t.TempDir(), "i1"
	dir := filepath.Join(w.dataDir, "outbox", "i1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.upload = func(_ context.Context, r io.Reader, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
		b, _ := io.ReadAll(r)
		return whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", FileLength: uint64(len(b))}, nil
	}
	return w, dir
}

func stage(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var dm = types.NewJID("5511888887777", types.DefaultUserServer)

func TestBuildNewTypes(t *testing.T) {
	w, dir := composeAdapter(t)
	ctx := context.Background()
	// VP8X header: canvas 512x512 (stored minus one).
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x10\x00\x00\x00\xff\x01\x00\xff\x01\x00")

	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "sticker", MediaPath: stage(t, dir, "s", webp), Animated: true})
	if err != nil || m.GetStickerMessage().GetMimetype() != "image/webp" || !m.GetStickerMessage().GetIsAnimated() ||
		m.GetStickerMessage().GetWidth() != 512 || m.GetStickerMessage().GetHeight() != 512 {
		t.Fatalf("sticker = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "gif", Mime: "video/mp4", Text: "hi", MediaPath: stage(t, dir, "g", []byte("mp4"))})
	if err != nil || !m.GetVideoMessage().GetGifPlayback() || m.GetVideoMessage().GetCaption() != "hi" {
		t.Fatalf("gif = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "ptv", Mime: "video/mp4", MediaPath: stage(t, dir, "p", []byte("mp4"))})
	if err != nil || m.GetPtvMessage() == nil || m.GetVideoMessage() != nil {
		t.Fatalf("ptv = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "location", Location: &workerv1.Location{Latitude: -23.5, Longitude: -46.6, Name: "SP"}})
	if err != nil || m.GetLocationMessage().GetDegreesLatitude() != -23.5 || m.GetLocationMessage().GetName() != "SP" {
		t.Fatalf("location = %v, %v", m, err)
	}
	one := []*workerv1.ContactCard{{DisplayName: "Ana", Vcard: "BEGIN:VCARD\nEND:VCARD"}}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "contact", Contacts: one})
	if err != nil || m.GetContactMessage().GetDisplayName() != "Ana" {
		t.Fatalf("contact = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "contact", Contacts: append(one, &workerv1.ContactCard{DisplayName: "Bia", Vcard: "v"})})
	if err != nil || len(m.GetContactsArrayMessage().GetContacts()) != 2 {
		t.Fatalf("contacts = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "poll", Poll: &workerv1.Poll{Name: "Q?", Options: []string{"a", "b"}, Selectable: 1}})
	if err != nil || m.GetPollCreationMessage().GetName() != "Q?" || len(m.GetMessageContextInfo().GetMessageSecret()) != 32 {
		t.Fatalf("poll = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "edit", TargetId: "T1", Text: "fixed"})
	pm := m.GetEditedMessage().GetMessage().GetProtocolMessage()
	if err != nil || pm.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || pm.GetKey().GetID() != "T1" ||
		pm.GetEditedMessage().GetConversation() != "fixed" {
		t.Fatalf("edit = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "revoke", TargetId: "T1", TargetFromMe: true})
	if err != nil || m.GetProtocolMessage().GetType() != waE2E.ProtocolMessage_REVOKE || !m.GetProtocolMessage().GetKey().GetFromMe() {
		t.Fatalf("revoke = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "pin", TargetId: "T1", TargetFromMe: true, PinSeconds: 86400})
	if err != nil || m.GetPinInChatMessage().GetType() != waE2E.PinInChatMessage_PIN_FOR_ALL ||
		m.GetMessageContextInfo().GetMessageAddOnDurationInSecs() != 86400 || m.GetPinInChatMessage().GetKey().GetID() != "T1" {
		t.Fatalf("pin = %v, %v", m, err)
	}
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "pin", TargetId: "T1", Unpin: true})
	if err != nil || m.GetPinInChatMessage().GetType() != waE2E.PinInChatMessage_UNPIN_FOR_ALL || m.GetMessageContextInfo() != nil ||
		m.GetPinInChatMessage().GetKey().GetFromMe() {
		t.Fatalf("unpin of a contact's message = %v, %v", m, err)
	}
}

func TestBuildContextInfo(t *testing.T) {
	w, _ := composeAdapter(t)
	ctx := context.Background()
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "@5511777776666 sim",
		Quote:    &workerv1.Quote{MessageId: "Q1", Text: "pergunta"},
		Mentions: []string{"5511777776666@s.whatsapp.net"}})
	ci := m.GetExtendedTextMessage().GetContextInfo()
	if err != nil || ci.GetStanzaID() != "Q1" || ci.GetParticipant() != dm.String() ||
		ci.GetQuotedMessage().GetConversation() != "pergunta" || len(ci.GetMentionedJID()) != 1 {
		t.Fatalf("reply = %v, %v", m, err)
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "x", Quote: &workerv1.Quote{MessageId: "Q2", FromMe: true}})
	if p := m.GetExtendedTextMessage().GetContextInfo().GetParticipant(); p != "5511999999999@s.whatsapp.net" {
		t.Fatalf("own quote participant = %q", p)
	}
	group := types.NewJID("120363", types.GroupServer)
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{AddressingMode: types.AddressingModePN, Participants: []types.GroupParticipant{
			{JID: types.NewJID("5511999999999", types.DefaultUserServer)}, {JID: dm}, {JID: types.NewJID("5511777776666", types.DefaultUserServer)},
		}}, nil
	}
	m, err = w.build(ctx, group, &workerv1.SendRequest{Type: "text", Text: "todos", MentionAll: true})
	if err != nil || len(m.GetExtendedTextMessage().GetContextInfo().GetMentionedJID()) != 2 {
		t.Fatalf("mention all = %v, %v", m, err)
	}
	if _, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "x", MentionAll: true}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("mention all in a direct chat = %v", err)
	}
	if _, err := w.build(ctx, group, &workerv1.SendRequest{Type: "text", Text: "x", Quote: &workerv1.Quote{MessageId: "Q"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("group quote without author = %v", err)
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "location", Location: &workerv1.Location{}, Forwarded: true})
	if !m.GetLocationMessage().GetContextInfo().GetIsForwarded() {
		t.Fatalf("forwarded location = %v", m)
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "veja https://ex.com",
		LinkPreview: &workerv1.LinkPreview{Url: "https://ex.com", Title: "Ex", Thumbnail: []byte{0xff, 0xd8}}})
	if e := m.GetExtendedTextMessage(); e.GetMatchedText() != "https://ex.com" || e.GetTitle() != "Ex" || len(e.GetJPEGThumbnail()) != 2 {
		t.Fatalf("link preview = %v", m)
	}
	if _, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "revoke", TargetId: "T", TargetFromMe: true, Forwarded: true}); err != nil {
		t.Fatalf("revoke ignores context: %v", err) // revoke returns before context info
	}
}

func TestBuildRevokeOfOthersNeedsGroupAdmin(t *testing.T) {
	w, _ := composeAdapter(t)
	ctx := context.Background()
	if _, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "revoke", TargetId: "T"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("contact's message in a direct chat = %v", err)
	}
	group := types.NewJID("120363", types.GroupServer)
	admin := false
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{AddressingMode: types.AddressingModePN, Participants: []types.GroupParticipant{
			{JID: types.NewJID("5511999999999", types.DefaultUserServer), IsAdmin: admin}, {JID: dm},
		}}, nil
	}
	req := &workerv1.SendRequest{Type: "revoke", TargetId: "T", TargetSender: dm.String()}
	if _, err := w.build(ctx, group, req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("not admin = %v", err)
	}
	admin = true
	m, err := w.build(ctx, group, req)
	if err != nil || m.GetProtocolMessage().GetKey().GetParticipant() != dm.String() || m.GetProtocolMessage().GetKey().GetFromMe() {
		t.Fatalf("admin revoke = %v, %v", m, err)
	}
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) { return nil, errors.New("boom") }
	if _, err := w.build(ctx, group, req); status.Code(err) != codes.Unknown {
		t.Fatalf("lookup failure is retryable: %v", err)
	}
}

func TestBuildForwardReusesTheMedia(t *testing.T) {
	w, _ := composeAdapter(t)
	img := &waE2E.ImageMessage{Caption: proto.String("foto"), DirectPath: proto.String("/p"), MediaKey: []byte{1},
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("old")}}
	m, err := w.build(context.Background(), dm, &workerv1.SendRequest{Type: "forward", ForwardKind: "image", ForwardRef: mediaRef(img), Forwarded: true})
	if err != nil || m.GetImageMessage().GetDirectPath() != "/p" || m.GetImageMessage().GetContextInfo().GetStanzaID() != "" ||
		!m.GetImageMessage().GetContextInfo().GetIsForwarded() {
		t.Fatalf("forward = %v, %v", m, err)
	}
	if _, err := w.build(context.Background(), dm, &workerv1.SendRequest{Type: "forward", ForwardKind: "image"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("forward without ref = %v", err)
	}
}

func TestWebpSize(t *testing.T) {
	vp8x := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x10\x00\x00\x00\xff\x01\x00\x7f\x00\x00")
	if got := webpSize(bytes.NewReader(vp8x)); got != [2]uint32{512, 128} {
		t.Fatalf("vp8x = %v", got)
	}
	// VP8L: signature 0x2f, then 14-bit width-1 and height-1 (99 and 49).
	bits := uint32(99) | uint32(49)<<14
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2f"), byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24), 0, 0, 0, 0, 0)
	if got := webpSize(bytes.NewReader(vp8l)); got != [2]uint32{100, 50} {
		t.Fatalf("vp8l = %v", got)
	}
	if got := webpSize(bytes.NewReader([]byte("not a webp at all, definitely"))); got != [2]uint32{} {
		t.Fatalf("garbage = %v", got)
	}
}

func TestReceiveNewTypesAndUpdates(t *testing.T) {
	w := newMsgAdapter(t)
	r := w.sink.(*dataRec)
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: dm, Sender: dm}, ID: "IN1", Timestamp: time.Unix(1_800_000_000, 0)}
	pollKey := &waCommon.MessageKey{ID: proto.String("POLL1"), FromMe: proto.Bool(true)}
	h := sha256.Sum256([]byte("sim"))
	w.decryptPoll = func(context.Context, *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: [][]byte{h[:]}}, nil
	}
	w.decryptSecret = func(context.Context, *events.Message) (*waE2E.Message, error) {
		return &waE2E.Message{Conversation: proto.String("secret edit")}, nil
	}
	msgs := []*waE2E.Message{
		{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("resp"), ContextInfo: &waE2E.ContextInfo{
			StanzaID: proto.String("Q1"), MentionedJID: []string{"5511777776666@s.whatsapp.net"}}}},
		{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(1.5), DegreesLongitude: proto.Float64(2.5), Name: proto.String("Casa")}},
		{ContactsArrayMessage: &waE2E.ContactsArrayMessage{DisplayName: proto.String("2 contatos"), Contacts: []*waE2E.ContactMessage{
			{DisplayName: proto.String("Ana"), Vcard: proto.String("v1")}, {DisplayName: proto.String("Bia"), Vcard: proto.String("v2")}}}},
		{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Vem?"), Options: []*waE2E.PollCreationMessage_Option{
			{OptionName: proto.String("sim")}, {OptionName: proto.String("não")}}, SelectableOptionsCount: proto.Uint32(1)}},
		{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}},
		{PtvMessage: &waE2E.VideoMessage{}},
		{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("OLD1")}}},
		{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("OLD2")},
			EditedMessage: &waE2E.Message{Conversation: proto.String("novo")}}},
		{SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{SecretEncType: waE2E.SecretEncryptedMessage_MESSAGE_EDIT.Enum(),
			TargetMessageKey: &waCommon.MessageKey{ID: proto.String("OLD3")}}},
		{PollUpdateMessage: &waE2E.PollUpdateMessage{PollCreationMessageKey: pollKey}},
		{PinInChatMessage: &waE2E.PinInChatMessage{}}, // not logged
	}
	for _, m := range msgs {
		w.handle(&events.Message{Info: info, Message: m})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != len(msgs)-1 {
		t.Fatalf("events = %d", len(r.evs))
	}
	get := func(i int) *workerv1.Message { return r.evs[i].GetMessage() }
	if m := get(0); m.QuotedId != "Q1" || len(m.Mentions) != 1 {
		t.Fatalf("reply = %+v", m)
	}
	if m := get(1); m.Type != "location" || m.Location.GetName() != "Casa" || m.Location.GetLatitude() != 1.5 {
		t.Fatalf("location = %+v", m)
	}
	if m := get(2); m.Type != "contact" || len(m.Contacts) != 2 || m.Contacts[1].Vcard != "v2" {
		t.Fatalf("contacts = %+v", m)
	}
	if m := get(3); m.Type != "poll" || m.Poll.GetName() != "Vem?" || len(m.Poll.GetOptions()) != 2 || m.Poll.GetSelectable() != 1 {
		t.Fatalf("poll = %+v", m)
	}
	if get(4).Type != "gif" || get(5).Type != "ptv" || get(5).Media.GetKind() != "video" {
		t.Fatalf("gif/ptv = %+v %+v", get(4), get(5))
	}
	if m := get(6); m.Type != "revoke" || m.TargetId != "OLD1" || m.Chat != dm.String() {
		t.Fatalf("revoke = %+v", m)
	}
	if m := get(7); m.Type != "edit" || m.TargetId != "OLD2" || m.Text != "novo" {
		t.Fatalf("edit = %+v", m)
	}
	if m := get(8); m.Type != "edit" || m.TargetId != "OLD3" || m.Text != "secret edit" {
		t.Fatalf("secret edit = %+v", m)
	}
	if m := get(9); m.Type != "poll_vote" || m.TargetId != "POLL1" || len(m.PollHashes) != 1 || !bytes.Equal(m.PollHashes[0], h[:]) {
		t.Fatalf("poll vote = %+v", m)
	}
}
