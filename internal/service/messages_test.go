package service

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type msgEnv struct {
	t   *testing.T
	st  *store.Store
	sp  *workertest.Spawner
	svc *Messaging
	dir string
}

func fast() settings.Settings {
	s := settings.Defaults()
	s.MinIntervalMS, s.JitterMS, s.TypingSimulation, s.Ramp.Enabled = 0, 0, false, false
	s.SendWindow, s.UnsafePacing = settings.Window{}, true // tests run at any hour, back to back
	return s
}

// newMsgEnv runs instance "i1" connected on a fake worker, with the real queue and ingestor.
func newMsgEnv(t *testing.T) *msgEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.DiscardHandler)
	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	q := queue.New(st, mgr, queue.Options{Defaults: fast(), Log: log, Resolver: resolver})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log}, q})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
	})
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "i1", Name: "i1", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	e := &msgEnv{t: t, st: st, sp: sp, dir: dir, svc: &Messaging{
		Store: st, Manager: mgr, Queues: q, Fetcher: &media.Fetcher{AllowPrivate: true},
		Signer:  media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://127.0.0.1:8787"),
		DataDir: dir, Defaults: fast(), Resolver: resolver, SignedURLs: true,
	}}
	e.eventually("connected", func() bool {
		in, _ := st.GetInstance(context.Background(), "i1")
		return in.State == instance.Connected
	})
	return e
}

func (e *msgEnv) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func apiCode(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestChatJID(t *testing.T) {
	ok := map[string]string{
		"+55 (11) 98888-7777":             "5511988887777@s.whatsapp.net",
		"5511988887777@s.whatsapp.net":    "5511988887777@s.whatsapp.net",
		"5511988887777:12@s.whatsapp.net": "5511988887777@s.whatsapp.net",
		"120363000000000000@g.us":         "120363000000000000@g.us",
	}
	for in, want := range ok {
		if got, err := ChatJID(in); err != nil || got != want {
			t.Errorf("ChatJID(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"123", "x@example.com", "@g.us", ""} {
		if _, err := ChatJID(in); err == nil {
			t.Errorf("ChatJID(%q) accepted", in)
		}
	}
}

func TestSendTextWaitAndIdempotency(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	in := SendInput{To: "5511988887777", Type: "text", Text: "oi", ClientMessageID: "pedido-1"}
	v, err := e.svc.Send(ctx, "i1", in, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "sent" || !strings.HasPrefix(v.WAMessageID, "3EB0") || v.Chat != "5511988887777@s.whatsapp.net" {
		t.Fatalf("view = %+v", v)
	}
	again, err := e.svc.Send(ctx, "i1", in, 0)
	if err != nil || again.ID != v.ID || len(e.sp.Last("i1").WA.Sent()) != 1 {
		t.Fatalf("repeat = %+v, %v (sent %d)", again, err, len(e.sp.Last("i1").WA.Sent()))
	}
	other := in
	other.Text = "outra coisa"
	if _, err := e.svc.Send(ctx, "i1", other, 0); apiCode(err) != "conflict" {
		t.Fatalf("client id reused with another body = %v", err)
	}
	queued, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "text", Text: "depois"}, 0)
	if err != nil || queued.Status != "queued" {
		t.Fatalf("enqueue only = %+v, %v", queued, err)
	}
	if done, err := e.svc.Wait(ctx, "i1", queued.ID, 2*time.Second); err != nil || done.Status != "sent" {
		t.Fatalf("wait = %+v, %v", done, err)
	}
	got, err := e.svc.GetMessage(ctx, "i1", v.ID)
	if err != nil || got.Status != "sent" || got.ClientMessageID != "pedido-1" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// Receipts raise the status shown for the sent item.
	e.sp.Last("i1").EmitReceipt(&workerv1.Receipt{MessageIds: []string{v.WAMessageID}, Type: "read"})
	e.eventually("read status", func() bool {
		got, _ := e.svc.GetMessage(ctx, "i1", v.ID)
		return got.Status == "read"
	})
	if _, err := e.svc.GetMessage(ctx, "i1", "msg_nope"); apiCode(err) != "not_found" {
		t.Fatalf("unknown message = %v", err)
	}
}

func TestSendValidationAndFailures(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	bad := []SendInput{
		{To: "123", Type: "text", Text: "x"},
		{To: "5511988887777", Type: "text"},
		{To: "5511988887777", Type: "sticker"},
		{To: "5511988887777", Type: "image"},
		{To: "5511988887777", Type: "image", Media: &MediaInput{URL: "http://x", Base64: "eA=="}},
		{To: "5511988887777", Type: "reaction", Reaction: &ReactionInput{MessageID: "nope", Emoji: "👍"}},
		{To: "5511988887777", Type: "text", Text: "x", ExpiresAfter: "10s"},
		{To: "5511988887777", Type: "image", Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString([]byte("not an image"))}},
	}
	for i, in := range bad {
		if _, err := e.svc.Send(ctx, "i1", in, 0); apiCode(err) != "validation_failed" {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, media.MaxSize("image")+1))
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "image", Media: &MediaInput{Base64: big}}, 0); apiCode(err) != "media_too_large" {
		t.Fatalf("big image = %v", err)
	}
	e.sp.Last("i1").WA.SetUnregistered("5511900000000")
	_, err := e.svc.Send(ctx, "i1", SendInput{To: "5511900000000", Type: "text", Text: "oi"}, 2*time.Second)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "invalid_number" || ae.Status != http.StatusBadRequest {
		t.Fatalf("failed send = %v", err)
	}
	if n := len(e.sp.Last("i1").WA.Sent()); n != 0 {
		t.Fatalf("a number without WhatsApp was sent to (%d sends)", n)
	}
	e.sp.Last("i1").WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "forbidden: no permission to send in this group")
	})
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "120363000000000001@g.us", Type: "text", Text: "oi"}, 2*time.Second); apiCode(err) != "forbidden" {
		t.Fatalf("group without permission = %v", err)
	}
	if err := e.st.UpdateInstanceState(ctx, "i1", instance.LoggedOut, "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Send(ctx, "i1", SendInput{To: "5511900000000", Type: "text", Text: "oi"}, 0); apiCode(err) != "instance_not_connected" {
		t.Fatalf("logged out = %v", err)
	}
}

func TestSendMediaAndReaction(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	png := []byte("\x89PNG\r\n\x1a\nimage")
	v, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "image", Caption: "foto",
		Media: &MediaInput{Base64: base64.StdEncoding.EncodeToString(png)}}, 2*time.Second)
	if err != nil || v.Status != "sent" || v.Media == nil || v.Media.Mime != "image/png" {
		t.Fatalf("image = %+v, %v", v, err)
	}
	req := e.sp.Last("i1").WA.Sent()[0]
	if string(e.sp.Last("i1").WA.SentMedia(req.MessageId)) != string(png) || req.Text != "foto" || req.Type != "image" {
		t.Fatalf("request = %+v", req)
	}
	if left, _ := os.ReadDir(filepath.Join(e.dir, "outbox", "i1")); len(left) != 0 {
		t.Fatalf("outbox not cleaned: %v", left)
	}
	r, err := e.svc.Send(ctx, "i1", SendInput{To: "5511988887777", Type: "reaction",
		Reaction: &ReactionInput{MessageID: v.ID, Emoji: "❤️"}}, 2*time.Second)
	if err != nil || r.Status != "sent" {
		t.Fatalf("reaction = %+v, %v", r, err)
	}
	rr := e.sp.Last("i1").WA.Sent()[1]
	if rr.ReactionMessageId != v.WAMessageID || !rr.ReactionFromMe || rr.Text != "❤️" {
		t.Fatalf("reaction request = %+v", rr)
	}
}

func TestSettingsProxyRestartsWorker(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	got, err := e.svc.Settings(ctx, "i1")
	if err != nil || got.MinIntervalMS != 0 {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	jitter := 500
	got, err = e.svc.UpdateSettings(ctx, "i1", settings.Patch{JitterMS: &jitter})
	if err != nil || got.JitterMS != 500 || e.sp.Starts("i1") != 1 {
		t.Fatalf("update = %+v, %v, starts %d", got, err, e.sp.Starts("i1"))
	}
	proxy := "socks5://bob:secret@10.0.0.1:1080"
	got, err = e.svc.UpdateSettings(ctx, "i1", settings.Patch{ProxyURL: &proxy})
	if err != nil || strings.Contains(got.ProxyURL, "secret") || got.JitterMS != 500 {
		t.Fatalf("proxy update = %+v, %v", got, err)
	}
	e.eventually("worker restarted", func() bool { return e.sp.Starts("i1") == 2 })
	bad := "ftp://x"
	if _, err := e.svc.UpdateSettings(ctx, "i1", settings.Patch{ProxyURL: &bad}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad proxy = %v", err)
	}
}
