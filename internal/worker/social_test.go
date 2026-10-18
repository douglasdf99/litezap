package worker

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestStatusAndChannelEvents(t *testing.T) {
	w := newMsgAdapter(t)
	r := w.sink.(*dataRec)
	author := types.NewJID("5511888887777", types.DefaultUserServer)
	ts := time.Unix(1_800_000_000, 0)
	status := types.MessageInfo{MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: author, IsGroup: true},
		ID: "ST1", Timestamp: ts, PushName: "Ana"}
	w.handle(&events.Message{Info: status, Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("praia"),
		Mimetype: proto.String("image/jpeg"), DirectPath: proto.String("/d")}}})
	mine := status
	mine.ID, mine.IsFromMe = "ST2", true
	w.handle(&events.Message{Info: mine, Message: &waE2E.Message{Conversation: proto.String("meu")}})
	w.handle(&events.Message{Info: status, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum()}}})

	ch := types.NewJID("120363000000000001", types.NewsletterServer)
	post := types.MessageInfo{MessageSource: types.MessageSource{Chat: ch, Sender: ch}, ID: "P1", ServerID: 77, Timestamp: ts}
	w.handle(&events.Message{Info: post, Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("novidade")}}})

	w.handle(&events.LabelEdit{LabelID: "5", Timestamp: ts, Action: &waSyncAction.LabelEditAction{Name: proto.String("Novo cliente"), Color: proto.Int32(3)}})
	w.handle(&events.LabelAssociationChat{LabelID: "5", JID: author, Timestamp: ts, Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(true)}})
	w.handle(&events.LabelAssociationMessage{LabelID: "5", JID: author, MessageID: "M9", Timestamp: ts,
		Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(false)}, FromFullSync: true})

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != 5 {
		t.Fatalf("events = %d: %v", len(r.evs), r.evs)
	}
	s := r.evs[0].GetStatus()
	if s == nil || s.Chat != "status@broadcast" || s.Sender != author.String() || s.Type != "image" || s.Text != "praia" ||
		s.Media == nil || len(s.Media.Ref) == 0 || s.PushName != "Ana" || s.FromMe {
		t.Fatalf("status = %+v", s)
	}
	p := r.evs[1].GetChannelPost()
	if p == nil || p.Channel != ch.String() || p.ServerId != 77 || p.Message.Text != "novidade" || p.Message.Type != "text" {
		t.Fatalf("channel post = %+v", p)
	}
	if l := r.evs[2].GetLabel(); l.Kind != "edit" || l.LabelId != "5" || l.Name != "Novo cliente" || l.Color != 3 {
		t.Fatalf("label edit = %+v", l)
	}
	if l := r.evs[3].GetLabel(); l.Kind != "chat" || l.Chat != author.String() || !l.Labeled {
		t.Fatalf("label chat = %+v", l)
	}
	if l := r.evs[4].GetLabel(); l.Kind != "message" || l.MessageId != "M9" || l.Labeled || !l.FullSync {
		t.Fatalf("label message = %+v", l)
	}
}

func TestBuildStatusAndChannelPosts(t *testing.T) {
	w, dir := composeAdapter(t)
	ctx := context.Background()

	m, _, ok, err := w.buildSpecial(ctx, types.StatusBroadcastJID, &workerv1.SendRequest{Type: "text", Text: "bom dia",
		StatusStyle: &workerv1.StatusStyle{BackgroundArgb: 0xFF112233, Font: 2}})
	ext := m.GetExtendedTextMessage()
	if !ok || err != nil || ext.GetText() != "bom dia" || ext.GetBackgroundArgb() != 0xFF112233 || ext.GetTextArgb() != 0xFFFFFFFF ||
		ext.GetFont() != waE2E.ExtendedTextMessage_FontType(2) {
		t.Fatalf("text status = %v, %v, %v", m, ok, err)
	}
	m, _, _, err = w.buildSpecial(ctx, types.StatusBroadcastJID, &workerv1.SendRequest{Type: "image", Mime: "image/jpeg", Text: "foto",
		MediaPath: stage(t, dir, "s1", []byte("jpg"))})
	if err != nil || m.GetImageMessage().GetCaption() != "foto" {
		t.Fatalf("image status = %v, %v", m, err)
	}
	if _, _, _, err := w.buildSpecial(ctx, types.StatusBroadcastJID, &workerv1.SendRequest{Type: "poll"}); err == nil {
		t.Fatal("a poll status must be refused")
	}
	if _, _, _, err := w.buildSpecial(ctx, types.StatusBroadcastJID, &workerv1.SendRequest{Type: "text", Text: "x",
		Quote: &workerv1.Quote{MessageId: "Q"}}); err == nil {
		t.Fatal("a reply as status must be refused")
	}
	m, _, _, err = w.buildSpecial(ctx, types.StatusBroadcastJID, &workerv1.SendRequest{Type: "revoke", TargetId: "OWN1", TargetFromMe: true})
	if k := m.GetProtocolMessage().GetKey(); err != nil || k.GetID() != "OWN1" || !k.GetFromMe() || k.GetRemoteJID() != "status@broadcast" {
		t.Fatalf("revoke status = %v, %v", m, err)
	}
	if _, _, _, err := w.buildSpecial(ctx, types.StatusBroadcastJID, &workerv1.SendRequest{Type: "revoke", TargetId: "X"}); err == nil {
		t.Fatal("revoking someone else's status must be refused")
	}

	ch := types.NewJID("120363000000000001", types.NewsletterServer)
	var plain []byte
	w.social.uploadChannel = func(_ context.Context, r io.ReadSeeker, mt whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
		plain, _ = io.ReadAll(r)
		return whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", Handle: "H1", MediaKey: []byte("k"), FileEncSHA256: []byte("e")}, nil
	}
	m, handle, ok, err := w.buildSpecial(ctx, ch, &workerv1.SendRequest{Type: "image", Mime: "image/jpeg", Text: "c",
		MediaPath: stage(t, dir, "c1", []byte("plainjpg"))})
	if !ok || err != nil || handle != "H1" || string(plain) != "plainjpg" || m.GetImageMessage().GetMediaKey() != nil ||
		m.GetImageMessage().GetFileEncSHA256() != nil || m.GetImageMessage().GetCaption() != "c" {
		t.Fatalf("channel image = %v, %q, %v", m, handle, err)
	}
	m, handle, _, err = w.buildSpecial(ctx, ch, &workerv1.SendRequest{Type: "text", Text: "post"})
	if err != nil || handle != "" || m.GetExtendedTextMessage().GetText() != "post" {
		t.Fatalf("channel text = %v, %v", m, err)
	}
	if _, _, _, err := w.buildSpecial(ctx, ch, &workerv1.SendRequest{Type: "poll"}); err == nil {
		t.Fatal("a poll in a channel must be refused here")
	}
	if _, _, ok, _ := w.buildSpecial(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "x"}); ok {
		t.Fatal("direct chats are not special")
	}
}

// A reply to a contact's status quotes it from status@broadcast, with its author.
func TestStatusReplyContext(t *testing.T) {
	w, _ := composeAdapter(t)
	m, err := w.build(context.Background(), dm, &workerv1.SendRequest{Type: "text", Text: "que lindo",
		Quote: &workerv1.Quote{MessageId: "ST1", Sender: dm.String(), Text: "praia", RemoteJid: "status@broadcast"}})
	ci := m.GetExtendedTextMessage().GetContextInfo()
	if err != nil || ci.GetRemoteJID() != "status@broadcast" || ci.GetStanzaID() != "ST1" || ci.GetParticipant() != dm.String() {
		t.Fatalf("status reply = %v, %v", m, err)
	}
}

func TestChannelCalls(t *testing.T) {
	w := newMsgAdapter(t)
	ctx := context.Background()
	ch := types.NewJID("120363000000000001", types.NewsletterServer)
	meta := &types.NewsletterMetadata{ID: ch, ViewerMeta: &types.NewsletterViewerMetadata{Role: types.NewsletterRoleOwner, Mute: types.NewsletterMuteOn}}
	meta.ThreadMeta.Name.Text, meta.ThreadMeta.SubscriberCount, meta.ThreadMeta.InviteCode = "Loja", 42, "ABC"
	meta.ThreadMeta.VerificationState = types.NewsletterVerificationStateVerified
	var invited string
	var calls []string
	w.social = socialHooks{
		channelInfo: func(context.Context, types.JID) (*types.NewsletterMetadata, error) { return meta, nil },
		channelInvite: func(_ context.Context, code string) (*types.NewsletterMetadata, error) {
			invited = code
			return meta, nil
		},
		follow: func(_ context.Context, j types.JID) error { calls = append(calls, "follow "+j.User); return nil },
		mute: func(_ context.Context, j types.JID, m bool) error {
			calls = append(calls, fmt.Sprint("mute ", m))
			return nil
		},
		channelPosts: func(context.Context, types.JID, *whatsmeow.GetNewsletterMessagesParams) ([]*types.NewsletterMessage, error) {
			return []*types.NewsletterMessage{{MessageServerID: 9, MessageID: "M", ViewsCount: 3, ReactionCounts: map[string]int{"👍": 2},
				Message: &waE2E.Message{Conversation: proto.String("oi")}}}, nil
		},
		react: func(_ context.Context, _ types.JID, sid types.MessageServerID, e string, _ types.MessageID) error {
			calls = append(calls, fmt.Sprint("react ", sid, e))
			return nil
		},
	}
	c, err := w.Channel(ctx, ch.String(), "")
	if err != nil || c.Name != "Loja" || c.Role != "owner" || !c.Muted || c.Subscribers != 42 || !c.Verified {
		t.Fatalf("channel = %+v, %v", c, err)
	}
	if _, err := w.Channel(ctx, "", "https://whatsapp.com/channel/ABC"); err != nil || invited != "ABC" {
		t.Fatalf("invite = %q, %v", invited, err)
	}
	if _, err := w.Channel(ctx, "123@g.us", ""); err == nil {
		t.Fatal("a group JID is not a channel")
	}
	if err := w.ChannelAction(ctx, ch.String(), "follow"); err != nil {
		t.Fatal(err)
	}
	if err := w.ChannelAction(ctx, ch.String(), "unmute"); err != nil {
		t.Fatal(err)
	}
	if c, _ := codeOf(w.ChannelAction(ctx, ch.String(), "delete")); c != codes.InvalidArgument {
		t.Fatal("unknown action must be refused")
	}
	posts, err := w.ChannelMessages(ctx, ch.String(), 0, 0)
	if err != nil || len(posts) != 1 || posts[0].ServerId != 9 || posts[0].Views != 3 || posts[0].Reactions["👍"] != 2 || posts[0].Message.Text != "oi" {
		t.Fatalf("posts = %v, %v", posts, err)
	}
	if err := w.ChannelReact(ctx, ch.String(), 9, "🔥"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"follow 120363000000000001", "mute false", "react 9🔥"}; fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("calls = %v", calls)
	}
	w.social.follow = func(context.Context, types.JID) error {
		return fmt.Errorf("graphql error: %w", types.GraphQLErrors{{Extensions: types.GraphQLErrorExtensions{ErrorCode: 404}}})
	}
	if c, code := codeOf(w.ChannelAction(ctx, ch.String(), "follow")); c != codes.NotFound || code != "not_found" {
		t.Fatalf("graphql 404 = %v %v", c, code)
	}
}

func TestLabelActionNeedsBusiness(t *testing.T) {
	w := newMsgAdapter(t)
	ctx := context.Background()
	var sent []appstate.PatchInfo
	w.acct.sendAppState = func(_ context.Context, p appstate.PatchInfo) error { sent = append(sent, p); return nil }
	w.acct.chatSettings = func(context.Context, types.JID) (types.LocalChatSettings, error) {
		return types.LocalChatSettings{}, nil
	}
	req := &workerv1.LabelActionRequest{Kind: "chat", LabelId: "5", Chat: dm.String(), Labeled: true}
	if c, code := codeOf(w.LabelAction(ctx, req)); c != codes.FailedPrecondition || code != "business_account_required" {
		t.Fatalf("personal account = %v %v", c, code)
	}
	w.social.business = func() bool { return true }
	if err := w.LabelAction(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := w.LabelAction(ctx, &workerv1.LabelActionRequest{Kind: "edit", LabelId: "6", Name: "VIP", Color: 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.LabelAction(ctx, &workerv1.LabelActionRequest{Kind: "message", LabelId: "6", Chat: dm.String(), MessageId: "M1", Labeled: true}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 3 {
		t.Fatalf("patches = %d", len(sent))
	}
	if idx := sent[0].Mutations[0].Index; idx[0] != appstate.IndexLabelAssociationChat || idx[1] != "5" || idx[2] != dm.String() {
		t.Fatalf("chat index = %v", idx)
	}
	if a := sent[1].Mutations[0].Value.GetLabelEditAction(); a.GetName() != "VIP" || a.GetColor() != 2 {
		t.Fatalf("edit = %v", a)
	}
	if idx := sent[2].Mutations[0].Index; idx[0] != appstate.IndexLabelAssociationMessage || idx[3] != "M1" {
		t.Fatalf("message index = %v", idx)
	}
	if c, _ := codeOf(w.LabelAction(ctx, &workerv1.LabelActionRequest{Kind: "edit", LabelId: "7"})); c != codes.InvalidArgument {
		t.Fatal("a label without a name must be refused")
	}
}

func TestBusinessProfile(t *testing.T) {
	w := newMsgAdapter(t)
	ctx := context.Background()
	w.social.bizProfile = func(_ context.Context, j types.JID) (*types.BusinessProfile, error) {
		return &types.BusinessProfile{JID: j, Email: "a@b.c", Categories: []types.Category{{ID: "1", Name: "Loja"}},
			BusinessHours: []types.BusinessHoursConfig{{DayOfWeek: "mon", Mode: "open_24h"}}}, nil
	}
	if c, code := codeOf(errOf(w.BusinessProfile(ctx, ""))); c != codes.FailedPrecondition || code != "business_account_required" {
		t.Fatalf("own profile of a personal account = %v %v", c, code)
	}
	bp, err := w.BusinessProfile(ctx, dm.String())
	if err != nil || bp.Email != "a@b.c" || bp.Categories[0] != "Loja" || bp.Hours[0].Mode != "open_24h" {
		t.Fatalf("contact profile = %+v, %v", bp, err)
	}
	w.social.business = func() bool { return true }
	if bp, err := w.BusinessProfile(ctx, ""); err != nil || bp.Jid != "5511999999999@s.whatsapp.net" {
		t.Fatalf("own profile = %+v, %v", bp, err)
	}
}

func errOf[T any](_ T, err error) error { return err }
