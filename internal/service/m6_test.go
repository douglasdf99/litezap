package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/store"
)

const peer = "5511988887777@s.whatsapp.net"

func f64(v float64) *float64 { return &v }

// received logs a message from the peer through the worker and returns it.
func (e *msgEnv) received(m *workerv1.Message) store.Message {
	e.t.Helper()
	if m.Chat == "" {
		m.Chat, m.Sender = peer, peer
	}
	m.TimestampMs = time.Now().UnixMilli()
	e.sp.Last("i1").EmitMessage(m)
	var got store.Message
	e.eventually("logged "+m.Id, func() bool {
		var err error
		got, err = e.st.MessageByWAID(context.Background(), "i1", m.Chat, m.Id)
		return err == nil
	})
	return got
}

func (e *msgEnv) lastSent() *workerv1.SendRequest {
	sent := e.sp.Last("i1").WA.Sent()
	if len(sent) == 0 {
		e.t.Fatal("nothing sent")
	}
	return sent[len(sent)-1]
}

func TestSendLocationContactsAndPoll(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	v, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "location",
		Location: &LocationInput{Latitude: f64(-23.56), Longitude: f64(-46.65), Name: "Paulista"}}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Location == nil || v.Location.Name != "Paulista" {
		t.Fatalf("location = %+v, %v", v, err)
	}
	if r := e.lastSent(); r.Location.GetLatitude() != -23.56 || r.Type != "location" {
		t.Fatalf("location request = %+v", r)
	}
	v, err = e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "contact",
		Contacts: []ContactInput{{Name: "Ana; Souza", Phone: "+55 11 97777-6666"}, {Name: "Bia", VCard: "BEGIN:VCARD\nFN:Bia\nEND:VCARD"}}}, 2*time.Second)
	if err != nil || len(v.Contacts) != 2 {
		t.Fatalf("contacts = %+v, %v", v, err)
	}
	r := e.lastSent()
	if len(r.Contacts) != 2 || !strings.Contains(r.Contacts[0].Vcard, "waid=5511977776666:+5511977776666") ||
		!strings.Contains(r.Contacts[0].Vcard, `FN:Ana\; Souza`) {
		t.Fatalf("contacts request = %+v", r.Contacts)
	}
	v, err = e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "poll", Poll: &PollInput{Name: "Vem?", Options: []string{"sim", "não"}}}, 2*time.Second)
	if err != nil || v.Poll == nil || v.Poll.Selectable != 1 {
		t.Fatalf("poll = %+v, %v", v, err)
	}
	if r := e.lastSent(); r.Poll.GetSelectable() != 1 || len(r.Poll.GetOptions()) != 2 {
		t.Fatalf("poll request = %+v", r)
	}
	logged, _ := e.svc.GetMessage(ctx, "i1", v.ID)
	if logged.Poll == nil || logged.Poll.Name != "Vem?" {
		t.Fatalf("logged poll = %+v", logged)
	}

	bad := []SendInput{
		{To: peer, Type: "location", Location: &LocationInput{Latitude: f64(91), Longitude: f64(0)}},
		{To: peer, Type: "location", Location: &LocationInput{Latitude: f64(1)}},
		{To: peer, Type: "contact"},
		{To: peer, Type: "contact", Contacts: []ContactInput{{Name: "x"}}},
		{To: peer, Type: "contact", Contacts: []ContactInput{{Name: "x", VCard: "not a vcard"}}},
		{To: peer, Type: "poll", Poll: &PollInput{Name: "q", Options: []string{"a"}}},
		{To: peer, Type: "poll", Poll: &PollInput{Name: "q", Options: []string{"a", "a"}}},
		{To: peer, Type: "poll", Poll: &PollInput{Name: "q", Options: []string{"a", "b"}, MaxSelections: new(3)}},
		{To: peer, Type: "text", Text: "x", DelayMS: 15001},
		{To: peer, Type: "text", Text: "x", TypingMS: -1},
		{To: peer, Type: "text", Text: "x", Mentions: []string{"all"}},
		{To: peer, Type: "text", Text: "x", Mentions: []string{"120363@g.us"}},
		{To: peer, Type: "text", Text: "x", ReplyTo: "msg_unknown"},
		{To: peer, Type: "image", Media: &MediaInput{Base64: "AA=="}, LinkPreview: &LinkPreviewInput{Mode: "auto"}},
		{To: peer, Type: "hologram"},
	}
	for i, in := range bad {
		if _, err := e.svc.Send(ctx, "i1", in, 0); apiCode(err) != "validation_failed" {
			t.Errorf("bad[%d] = %v", i, err)
		}
	}
}

func TestReplyMentionsAndTyping(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	q := e.received(&workerv1.Message{Id: "IN1", Type: "text", Text: "pergunta"})
	v, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "@5511977776666 resposta", ReplyTo: q.ID,
		Mentions: []string{"5511977776666"}, DelayMS: 1, TypingMS: 1}, 2*time.Second)
	if err != nil || v.ReplyTo != "IN1" || len(v.Mentions) != 1 {
		t.Fatalf("reply = %+v, %v", v, err)
	}
	r := e.lastSent()
	if r.Quote.GetMessageId() != "IN1" || r.Quote.GetText() != "pergunta" || r.Quote.GetFromMe() || r.Quote.GetSender() != peer ||
		len(r.Mentions) != 1 || r.Mentions[0] != "5511977776666@s.whatsapp.net" {
		t.Fatalf("request = %+v", r)
	}
	// The WhatsApp id works too.
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "de novo", ReplyTo: "IN1"}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	group := "120363@g.us"
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: group, Type: "text", Text: "todos", Mentions: []string{"all"}}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if r := e.lastSent(); !r.MentionAll {
		t.Fatalf("mention all = %+v", r)
	}
}

func TestEditRevokePinAndWindows(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	mine, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "oi"}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	theirs := e.received(&workerv1.Message{Id: "IN2", Type: "text", Text: "olá"})

	// to may be omitted with a LiteZap id.
	v, err := e.svc.Send(ctx, "i1", SendInput{Type: "edit", MessageID: mine.ID, Text: "oi!"}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Target != mine.WAMessageID {
		t.Fatalf("edit = %+v, %v", v, err)
	}
	if r := e.lastSent(); r.Type != "edit" || r.TargetId != mine.WAMessageID || r.Text != "oi!" {
		t.Fatalf("edit request = %+v", r)
	}
	got, _ := e.svc.GetMessage(ctx, "i1", mine.ID)
	if got.Text != "oi!" || got.EditedAt == nil {
		t.Fatalf("after edit = %+v", got)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "edit", MessageID: theirs.ID, Text: "x"}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("edit of a contact's message = %v", err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "revoke", MessageID: theirs.ID}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("revoke of a contact's message in a direct chat = %v", err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "pin", MessageID: theirs.ID, Pin: &PinInput{Duration: "1y"}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("bad pin duration = %v", err)
	}
	p, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "pin", MessageID: "IN2", Pin: &PinInput{Duration: "24h"}}, 2*time.Second)
	if err != nil || p.Status != "sent" {
		t.Fatalf("pin = %+v, %v", p, err)
	}
	if r := e.lastSent(); r.Type != "pin" || r.PinSeconds != 86400 || r.TargetFromMe || r.TargetId != "IN2" {
		t.Fatalf("pin request = %+v", r)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "revoke", MessageID: mine.ID}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.GetMessage(ctx, "i1", mine.ID)
	if got.RevokedAt == nil || got.Text != "" {
		t.Fatalf("after revoke = %+v", got)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "edit", MessageID: mine.ID, Text: "x"}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("edit of a deleted message = %v", err)
	}
	// Old messages: stable window errors.
	old := time.Now().Add(-3 * 24 * time.Hour)
	if _, err := e.st.InsertMessage(ctx, "i1", store.Message{ID: "msg_old", WAMessageID: "WOLD", ChatJID: peer, FromMe: true,
		Source: "api", Type: "text", Text: "velha", Status: "sent", Timestamp: old}, store.ChatUpdate{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "edit", MessageID: "msg_old", Text: "x"}, 0); apiCode(err) != "edit_window_expired" {
		t.Fatalf("old edit = %v", err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "revoke", MessageID: "msg_old"}, 0); apiCode(err) != "revoke_window_expired" {
		t.Fatalf("old revoke = %v", err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{Type: "revoke", MessageID: "WOLD"}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("WhatsApp id without to = %v", err)
	}
}

func TestForward(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	other := "5511966665555@s.whatsapp.net"
	img := e.received(&workerv1.Message{Id: "IMG1", Type: "image", Text: "foto",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 10, Ref: []byte("ref-bytes")}})
	v, err := e.svc.Send(ctx, "i1", SendInput{To: other, Type: "forward", Forward: &ForwardInput{MessageID: img.ID}}, 2*time.Second)
	if err != nil || v.Type != "image" || !v.Forwarded || v.Media == nil || v.Media.Mime != "image/jpeg" {
		t.Fatalf("forward = %+v, %v", v, err)
	}
	r := e.lastSent()
	if r.Type != "forward" || r.ForwardKind != "image" || string(r.ForwardRef) != "ref-bytes" || !r.Forwarded || r.To != other {
		t.Fatalf("forward request = %+v", r)
	}
	txt := e.received(&workerv1.Message{Id: "TXT1", Type: "text", Text: "repassa"})
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: other, Type: "forward", Forward: &ForwardInput{MessageID: "TXT1", Chat: peer}}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if r := e.lastSent(); r.Type != "text" || r.Text != "repassa" || !r.Forwarded {
		t.Fatalf("text forward = %+v", r)
	}
	_ = txt
	once := e.received(&workerv1.Message{Id: "VO1", Type: "image",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 10, Ref: []byte("r"), ViewOnce: true}})
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: other, Type: "forward", Forward: &ForwardInput{MessageID: once.ID}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("view-once forward = %v", err)
	}
	png := []byte("\x89PNG\r\n\x1a\nimage")
	mine, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "image", Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString(png)}}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: other, Type: "forward", Forward: &ForwardInput{MessageID: mine.ID}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("forward of our own media = %v", err)
	}
}

func TestStickerAndGifChecks(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	anim := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x12\x00\x00\x00\xff\x01\x00\xff\x01\x00ANIM\x06\x00\x00\x00")
	v, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "sticker", Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString(anim)}}, 2*time.Second)
	if err != nil || v.Media == nil || v.Media.Mime != "image/webp" || v.Media.Kind != "sticker" {
		t.Fatalf("sticker = %+v, %v", v, err)
	}
	if r := e.lastSent(); !r.Animated || r.Type != "sticker" {
		t.Fatalf("sticker request = %+v", r)
	}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "sticker", Media: &MediaInput{Base64: png}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("png sticker = %v", err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "sticker", Caption: "x", Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString(anim)}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("sticker caption = %v", err)
	}
	webm := base64.StdEncoding.EncodeToString([]byte("\x1a\x45\xdf\xa3webm"))
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "gif", Media: &MediaInput{Base64: webm, Mime: "video/webm"}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("webm gif = %v", err)
	}
	mp4 := base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"))
	v, err = e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "gif", Caption: "loop", Media: &MediaInput{Base64: mp4}}, 2*time.Second)
	if err != nil || v.Type != "gif" || v.Media.Kind != "video" {
		t.Fatalf("gif = %+v, %v", v, err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "ptv", Media: &MediaInput{Base64: mp4}}, 2*time.Second); err != nil {
		t.Fatalf("ptv = %v", err)
	}
}

func pngImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestLinkPreview(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	e.svc.PreviewFetcher = &media.Fetcher{AllowPrivate: true}
	big := pngImage(t, 800, 400)
	slow := make(chan struct{})
	t.Cleanup(func() { close(slow) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>Fallback</title><meta property="og:title" content="Título &amp; mais">
				<meta name="description" content="Descrição"><meta property="og:image" content="/img.png"></head><body>x</body></html>`))
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(big)
		case "/file.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF"))
		case "/slow":
			select {
			case <-slow:
			case <-r.Context().Done():
			}
		}
	}))
	defer srv.Close()

	v, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "veja " + srv.URL + "/page.", LinkPreview: &LinkPreviewInput{Mode: "auto"}}, 2*time.Second)
	if err != nil || v.LinkPreview == nil || v.LinkPreview.Title != "Título & mais" {
		t.Fatalf("auto = %+v, %v", v, err)
	}
	lp := e.lastSent().LinkPreview
	if lp.GetUrl() != srv.URL+"/page" || lp.GetDescription() != "Descrição" || len(lp.GetThumbnail()) == 0 {
		t.Fatalf("preview = %+v", lp)
	}
	thumb, _, err := image.DecodeConfig(bytes.NewReader(lp.GetThumbnail()))
	if err != nil || thumb.Width != 300 || thumb.Height != 150 {
		t.Fatalf("thumbnail = %+v, %v", thumb, err)
	}
	// Custom card, no page fetch.
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "promo https://example.com/p", LinkPreview: &LinkPreviewInput{Mode: "custom",
		URL: "https://example.com/p", Title: "Promo"}}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if lp := e.lastSent().LinkPreview; lp.GetTitle() != "Promo" || lp.GetUrl() != "https://example.com/p" {
		t.Fatalf("custom = %+v", lp)
	}
	// Not HTML, or too slow: sent without a preview.
	previewBudget = 300 * time.Millisecond
	t.Cleanup(func() { previewBudget = 5 * time.Second })
	for _, path := range []string{"/file.pdf", "/slow"} {
		start := time.Now()
		v, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: srv.URL + path, LinkPreview: &LinkPreviewInput{Mode: "auto"}}, 2*time.Second)
		if err != nil || v.LinkPreview != nil || e.lastSent().LinkPreview != nil || time.Since(start) > 2*time.Second {
			t.Fatalf("%s = %+v, %v", path, v, err)
		}
	}
	// Private addresses are refused by the same filter as media (and by default, whatever
	// media_allow_private_urls says: the env's media fetcher allows them).
	e.svc.PreviewFetcher = nil
	e.svc.previewOnce = sync.Once{}
	v, err = e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: srv.URL + "/page", LinkPreview: &LinkPreviewInput{Mode: "auto"}}, 2*time.Second)
	if err != nil || v.LinkPreview != nil || e.lastSent().LinkPreview != nil {
		t.Fatalf("private = %+v, %v", v, err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "x", LinkPreview: &LinkPreviewInput{Mode: "custom", Title: "t"}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("custom without url = %v", err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "x", LinkPreview: &LinkPreviewInput{Mode: "custom", Title: "t", URL: "https://elsewhere.com"}}, 0); apiCode(err) != "validation_failed" {
		t.Fatalf("custom url not in the text = %v", err)
	}
}

func TestLinkPreviewJSON(t *testing.T) {
	var in SendInput
	if err := json.Unmarshal([]byte(`{"link_preview":"auto"}`), &in); err != nil || in.LinkPreview.Mode != "auto" {
		t.Fatalf("auto = %+v, %v", in.LinkPreview, err)
	}
	if err := json.Unmarshal([]byte(`{"link_preview":{"title":"T","url":"https://x"}}`), &in); err != nil || in.LinkPreview.Mode != "custom" || in.LinkPreview.Title != "T" {
		t.Fatalf("custom = %+v, %v", in.LinkPreview, err)
	}
	if err := json.Unmarshal([]byte(`{"link_preview":"yes"}`), &in); err == nil {
		t.Fatal("bad mode accepted")
	}
	b, _ := json.Marshal(SendInput{LinkPreview: &LinkPreviewInput{Mode: "auto"}})
	if !strings.Contains(string(b), `"link_preview":"auto"`) {
		t.Fatalf("marshal = %s", b)
	}
	if got := firstURL("olha (https://a.com/x?y=1), ok"); got != "https://a.com/x?y=1" {
		t.Fatalf("firstURL = %q", got)
	}
	if m := parsePage([]byte(`<title>Só título</title>`)); m.title != "Só título" {
		t.Fatalf("title = %+v", m)
	}
	if thumbnail([]byte("garbage")) != nil {
		t.Fatal("garbage thumbnail")
	}
}

func TestQueueManagement(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	e.sp.Last("i1").EmitConn("reconnecting", "test") // hold the queue
	e.eventually("paused", func() bool {
		in, _ := e.st.GetInstance(ctx, "i1")
		return in.State == "reconnecting"
	})
	a, _ := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "a"}, 0)
	b, _ := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "b"}, 0)
	c, _ := e.svc.Send(ctx, "i1", SendInput{To: "5511966665555", Type: "text", Text: "c"}, 0)
	page, err := e.svc.Queue(ctx, "i1", "", 2, "")
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != a.ID || page.NextCursor == "" {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	page, err = e.svc.Queue(ctx, "i1", "", 2, page.NextCursor)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != c.ID {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	if _, err := e.svc.Queue(ctx, "i1", "bogus", 0, ""); apiCode(err) != "validation_failed" {
		t.Fatalf("bad status = %v", err)
	}
	v, err := e.svc.CancelQueued(ctx, "i1", a.ID)
	if err != nil || v.Status != "failed" {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	if _, err := e.svc.CancelQueued(ctx, "i1", a.ID); apiCode(err) != "conflict" {
		t.Fatalf("cancel twice = %v", err)
	}
	if _, err := e.svc.CancelQueued(ctx, "i1", "msg_nope"); apiCode(err) != "not_found" {
		t.Fatalf("cancel unknown = %v", err)
	}
	if _, err := e.svc.GetMessage(ctx, "i1", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: peer, Type: "text", Text: "a"}, 0); err != nil {
		t.Fatal(err) // a new message (no client id) is fine
	}
	n, err := e.svc.ClearQueue(ctx, "i1")
	if err != nil || n != 3 {
		t.Fatalf("clear = %d, %v", n, err)
	}
	failed, _ := e.svc.Queue(ctx, "i1", "failed", 0, "")
	if len(failed.Items) != 4 || !strings.HasPrefix(failed.Items[0].Error, "canceled:") {
		t.Fatalf("failed = %+v", failed)
	}
	_ = b
	e.sp.Last("i1").Emit("connected")
	time.Sleep(50 * time.Millisecond)
	if n := len(e.sp.Last("i1").WA.Sent()); n != 0 {
		t.Fatalf("canceled messages were sent: %d", n)
	}
}
