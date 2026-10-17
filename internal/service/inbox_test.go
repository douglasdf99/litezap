package service

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

func TestChatsMessagesAndMarkRead(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 3; i++ {
		f.EmitMessage(&workerv1.Message{Id: "IN" + string(rune('0'+i)), Chat: "5511988887777@s.whatsapp.net",
			Sender: "5511988887777@s.whatsapp.net", PushName: "João", TimestampMs: base + int64(i)*1000, Type: "text", Text: "oi"})
	}
	f.EmitMessage(&workerv1.Message{Id: "G1", Chat: "1203630@g.us", Sender: "5511977776666@s.whatsapp.net",
		IsGroup: true, TimestampMs: base - 1000, Type: "text", Text: "grupo"})
	e.eventually("ingested", func() bool {
		p, _ := e.svc.Chats(ctx, "i1", 10, "")
		return len(p.Chats) == 2
	})
	page, err := e.svc.Chats(ctx, "i1", 1, "")
	if err != nil || len(page.Chats) != 1 || page.Chats[0].Name != "João" || page.Chats[0].UnreadCount != 3 || page.NextCursor == "" {
		t.Fatalf("chats page 1 = %+v, %v", page, err)
	}
	page2, err := e.svc.Chats(ctx, "i1", 1, page.NextCursor)
	if err != nil || len(page2.Chats) != 1 || page2.Chats[0].JID != "1203630@g.us" {
		t.Fatalf("chats page 2 = %+v, %v", page2, err)
	}
	msgs, err := e.svc.ChatMessages(ctx, "i1", "5511988887777", 2, "")
	if err != nil || len(msgs.Messages) != 2 || msgs.Messages[0].WAMessageID != "IN2" || msgs.NextCursor == "" {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	rest, err := e.svc.ChatMessages(ctx, "i1", "5511988887777", 2, msgs.NextCursor)
	if err != nil || len(rest.Messages) != 1 || rest.Messages[0].WAMessageID != "IN0" {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
	if _, err := e.svc.ChatMessages(ctx, "i1", "5511988887777", 2, "%%%"); apiCode(err) != "validation_failed" {
		t.Fatalf("bad cursor = %v", err)
	}
	if err := e.svc.MarkRead(ctx, "i1", "5511988887777@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	reads := f.WA.Reads()
	if len(reads) != 1 || len(reads[0].MessageIds) != 3 || reads[0].Sender != "" {
		t.Fatalf("reads = %+v", reads)
	}
	if c, _ := e.st.GetChat(ctx, "i1", "5511988887777@s.whatsapp.net"); c.UnreadCount != 0 {
		t.Fatalf("unread = %d", c.UnreadCount)
	}
	if err := e.svc.MarkRead(ctx, "i1", "5511900000000"); apiCode(err) != "not_found" {
		t.Fatalf("unknown chat = %v", err)
	}
}

func TestContactsAndCheckNumbers(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	f.WA.SetContacts(&workerv1.Contact{Jid: "5511988887777@s.whatsapp.net", Name: "João"})
	f.WA.SetUnregistered("5511900000000")
	cs, err := e.svc.Contacts(ctx, "i1")
	if err != nil || len(cs) != 1 || cs[0].Name != "João" {
		t.Fatalf("contacts = %+v, %v", cs, err)
	}
	if c, err := e.svc.Contact(ctx, "i1", "5511988887777"); err != nil || c.Name != "João" {
		t.Fatalf("contact = %+v, %v", c, err)
	}
	if _, err := e.svc.Contact(ctx, "i1", "5511900000000"); apiCode(err) != "not_found" {
		t.Fatalf("unknown contact = %v", err)
	}
	res, err := e.svc.CheckNumbers(ctx, "i1", []string{"+55 11 98888-7777", "5511900000000"})
	if err != nil || len(res) != 2 || !res[0].Exists || res[1].Exists {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if _, err := e.svc.CheckNumbers(ctx, "i1", []string{"12"}); apiCode(err) != "validation_failed" {
		t.Fatalf("short number = %v", err)
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("55119000000%02d", i)
	}
	if _, err := e.svc.CheckNumbers(ctx, "i1", eleven); apiCode(err) != "validation_failed" {
		t.Fatalf("11 numbers = %v", err)
	}
}

func TestMediaDownloadAndSignedURL(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	f.WA.SetMedia("ref-1", []byte("jpeg-bytes"))
	f.EmitMessage(&workerv1.Message{Id: "IMG1", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "image",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 10, Ref: []byte("ref-1")}})
	var mediaID string
	e.eventually("image logged", func() bool {
		p, _ := e.svc.ChatMessages(ctx, "i1", "5511988887777", 10, "")
		if len(p.Messages) == 1 && p.Messages[0].Media != nil {
			mediaID = p.Messages[0].Media.ID
		}
		return mediaID != ""
	})
	mf, err := e.svc.Media(ctx, "i1", mediaID)
	if err != nil || mf.Mime != "image/jpeg" {
		t.Fatalf("media = %+v, %v", mf, err)
	}
	if b, _ := os.ReadFile(mf.Path); string(b) != "jpeg-bytes" {
		t.Fatalf("file = %q", b)
	}
	f.WA.SetMedia("ref-1", nil) // second read comes from disk, not the worker
	if mf2, err := e.svc.Media(ctx, "i1", mediaID); err != nil || mf2.Path != mf.Path {
		t.Fatalf("cached media = %+v, %v", mf2, err)
	}
	raw, _, err := e.svc.SignedMediaURL(ctx, "i1", mediaID)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SignedURLs = false
	if _, _, err := e.svc.SignedMediaURL(ctx, "i1", mediaID); apiCode(err) != "conflict" {
		t.Fatalf("signed URLs disabled = %v", err)
	}
	u, _ := url.Parse(raw)
	if !e.svc.VerifyMediaSignature("i1", mediaID, u.Query().Get("exp"), u.Query().Get("sig")) {
		t.Fatal("signature rejected")
	}
	if _, err := e.svc.Media(ctx, "i1", "med_nope"); apiCode(err) != "not_found" {
		t.Fatalf("unknown media = %v", err)
	}
}

// Two first reads of the same media at once share one download and both get the file.
func TestConcurrentFirstMediaReads(t *testing.T) {
	e := newMsgEnv(t)
	ctx := context.Background()
	f := e.sp.Last("i1")
	f.WA.SetMedia("ref-c", []byte("same-bytes"))
	f.EmitMessage(&workerv1.Message{Id: "IMGC", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "image",
		Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Ref: []byte("ref-c")}})
	var mediaID string
	e.eventually("image logged", func() bool {
		p, _ := e.svc.ChatMessages(ctx, "i1", "5511988887777", 10, "")
		if len(p.Messages) == 1 && p.Messages[0].Media != nil {
			mediaID = p.Messages[0].Media.ID
		}
		return mediaID != ""
	})
	type res struct {
		f   MediaFile
		err error
	}
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() { mf, err := e.svc.Media(ctx, "i1", mediaID); out <- res{mf, err} }()
	}
	a, b := <-out, <-out
	if a.err != nil || b.err != nil || a.f.Path != b.f.Path {
		t.Fatalf("results = %+v / %+v", a, b)
	}
	if data, _ := os.ReadFile(a.f.Path); string(data) != "same-bytes" {
		t.Fatalf("file = %q", data)
	}
	if n := f.WA.Downloads(); n != 1 {
		t.Fatalf("downloads = %d, want 1", n)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "media", "i1", "*.part")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}
