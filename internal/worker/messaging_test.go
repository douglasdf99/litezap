package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestContentTypes(t *testing.T) {
	img := &waE2E.ImageMessage{Caption: proto.String("foto"), Mimetype: proto.String("image/jpeg"),
		FileLength: proto.Uint64(42), DirectPath: proto.String("/p"), MediaKey: []byte{1}}
	cases := []struct {
		msg        *waE2E.Message
		typ, text  string
		media      string
		reactionTo string
		ok         bool
	}{
		{&waE2E.Message{Conversation: proto.String("oi")}, "text", "oi", "", "", true},
		{&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("link")}}, "text", "link", "", "", true},
		{&waE2E.Message{ImageMessage: img}, "image", "foto", "image", "", true},
		{&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "audio", "", "audio", "", true},
		{&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf")}}, "document", "", "document", "", true},
		{&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "", "sticker", "", true},
		{&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍"), Key: &waCommon.MessageKey{ID: proto.String("T1")}}}, "reaction", "👍", "", "T1", true},
		{&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana")}}, "contact", "Ana", "", "", true},
		{&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}, "", "", "", "", false},
		{nil, "", "", "", "", false},
	}
	for i, c := range cases {
		typ, text, media, reactionTo, ok := content(c.msg)
		if typ != c.typ || text != c.text || reactionTo != c.reactionTo || ok != c.ok || media.GetKind() != c.media {
			t.Errorf("case %d: got %q %q %v %q %v", i, typ, text, media, reactionTo, ok)
		}
	}
	_, _, media, _, _ := content(&waE2E.Message{ImageMessage: img})
	if media.Mime != "image/jpeg" || media.Size != 42 {
		t.Fatalf("media = %+v", media)
	}
	back, err := downloadable("image", media.Ref)
	if err != nil || back.GetDirectPath() != "/p" || string(back.GetMediaKey()) != "\x01" {
		t.Fatalf("ref round trip = %v, %v", back, err)
	}
	if _, err := downloadable("hologram", media.Ref); err == nil {
		t.Fatal("unknown kind must fail")
	}
}

func TestSendErrCodes(t *testing.T) {
	cases := []struct {
		err  error
		code codes.Code
	}{
		{whatsmeow.ErrNotConnected, codes.Unavailable},
		{whatsmeow.ErrNotLoggedIn, codes.FailedPrecondition},
		{whatsmeow.ErrMessageTimedOut, codes.DeadlineExceeded},
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{errors.New("no LID found for 5511@s.whatsapp.net from server"), codes.InvalidArgument},
		{whatsmeow.ErrNoPermissionInGroup, codes.PermissionDenied},
		{errors.New("boom"), codes.Unknown},
	}
	for _, c := range cases {
		if got := status.Code(sendErr(c.err)); got != c.code {
			t.Errorf("%v -> %v, want %v", c.err, got, c.code)
		}
	}
	if msg := status.Convert(sendErr(errors.New("no LID found for x"))).Message(); msg != "invalid_number: recipient is not on WhatsApp" {
		t.Fatalf("message = %q", msg)
	}
}

func TestBuildMedia(t *testing.T) {
	up := whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", MediaKey: []byte{9}, FileLength: 3}
	m := buildMedia(&workerv1.SendRequest{Type: "audio", Mime: "audio/ogg", Ptt: true}, up)
	if !m.GetAudioMessage().GetPTT() || m.GetAudioMessage().GetDirectPath() != "/d" {
		t.Fatalf("audio = %v", m)
	}
	d := buildMedia(&workerv1.SendRequest{Type: "document", Mime: "application/pdf", Text: "veja"}, up)
	if d.GetDocumentMessage().GetFileName() != "file" || d.GetDocumentMessage().GetCaption() != "veja" {
		t.Fatalf("document = %v", d)
	}
	i := buildMedia(&workerv1.SendRequest{Type: "image", Mime: "image/png"}, up)
	if i.GetImageMessage().Caption != nil || i.GetImageMessage().GetFileLength() != 3 {
		t.Fatalf("image = %v", i)
	}
}

type dataRec struct {
	stateRec
	mu  sync.Mutex
	evs []*workerv1.Event
}

func (r *dataRec) Data(ev *workerv1.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func TestHandleForwardsMessagesAndReceipts(t *testing.T) {
	own := types.NewJID("5511999999999", types.DefaultUserServer)
	w := NewWhatsmeow(whatsmeow.NewClient(&store.Device{ID: &own}, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := &dataRec{}
	w.sink = r
	lid := types.NewJID("123456", types.HiddenUserServer)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	ts := time.Unix(1_800_000_000, 0)
	w.handle(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn},
			ID:            "WA1", PushName: "João", Timestamp: ts,
		},
		Message: &waE2E.Message{Conversation: proto.String("oi")},
	})
	w.handle(&events.Message{ // status broadcast: ignored
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: pn}, ID: "S1"},
		Message: &waE2E.Message{Conversation: proto.String("story")},
	})
	w.handle(&events.Receipt{
		MessageSource: types.MessageSource{Chat: pn, Sender: pn},
		MessageIDs:    []types.MessageID{"OUT1"}, Type: types.ReceiptTypeRead, Timestamp: ts,
	})
	w.handle(&events.Receipt{ // retry receipts are not delivery reports
		MessageSource: types.MessageSource{Chat: pn, Sender: pn},
		MessageIDs:    []types.MessageID{"OUT1"}, Type: types.ReceiptTypeRetry,
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != 2 {
		t.Fatalf("events = %v", r.evs)
	}
	m := r.evs[0].GetMessage()
	if m.GetChat() != pn.String() || m.GetSender() != pn.String() || m.GetText() != "oi" ||
		m.GetPushName() != "João" || m.GetTimestampMs() != ts.UnixMilli() {
		t.Fatalf("message = %+v", m)
	}
	rc := r.evs[1].GetReceipt()
	if rc.GetType() != "read" || len(rc.GetMessageIds()) != 1 || rc.GetMessageIds()[0] != "OUT1" {
		t.Fatalf("receipt = %+v", rc)
	}
}

func TestDataPathOnlyInstanceAreas(t *testing.T) {
	w := &Whatsmeow{dataDir: "/srv/litezap/data", instanceID: "i1"}
	if p, err := w.dataPath("/srv/litezap/data/outbox/i1/msg_1", "outbox"); err != nil || p != "/srv/litezap/data/outbox/i1/msg_1" {
		t.Fatalf("outbox = %q, %v", p, err)
	}
	if _, err := w.dataPath("/srv/litezap/data/media/i1/med_1-123.part", "media"); err != nil {
		t.Fatalf("media = %v", err)
	}
	bad := []struct{ path, area string }{
		{"", "outbox"}, {"relative/file", "outbox"}, {"/etc/hosts", "outbox"},
		{"/srv/litezap/data/litezap.db", "outbox"}, {"/srv/litezap/data/media_key", "media"},
		{"/srv/litezap/data/outbox/i2/msg_1", "outbox"},        // another instance
		{"/srv/litezap/data/media/i1/med_1", "outbox"},         // reading from media
		{"/srv/litezap/data/outbox/i1/msg_1", "media"},         // writing to outbox
		{"/srv/litezap/data/instances/i1/session.db", "media"}, // own session
		{"/srv/litezap/data/outbox/i1/../../litezap.db", "outbox"},
		{"/srv/litezap/data/outbox/i1/sub/x", "outbox"},
	}
	for _, b := range bad {
		if _, err := w.dataPath(b.path, b.area); status.Code(err) != codes.InvalidArgument {
			t.Errorf("dataPath(%q, %s) = %v, want InvalidArgument", b.path, b.area, err)
		}
	}
}
