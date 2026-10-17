package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
)

// connected creates an instance whose fake worker is connected.
func (e *env) connected(name string) (id, tok string) {
	e.t.Helper()
	id, tok = e.create(name)
	e.waitState(id, "qr_pending")
	e.sp.Last(id).Emit("connected")
	e.waitState(id, "connected")
	return id, tok
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSendAndReadMessages(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Msg")
	base := "/v1/instances/" + id

	code, body := e.do("POST", base+"/messages?wait=true&timeout=2s", tok, `{"to":"5511988887777","type":"text","text":"oi"}`)
	if code != 200 || body["status"] != "sent" || body["wa_message_id"] == nil {
		t.Fatalf("send wait = %d %v", code, body)
	}
	msgID := body["id"].(string)
	if code, body := e.do("GET", base+"/messages/"+msgID, tok, ""); code != 200 || body["status"] != "sent" {
		t.Fatalf("get message = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/messages?wait=true&timeout=nope", tok, `{"to":"5511988887777","type":"text","text":"x"}`); code != 400 {
		t.Fatalf("bad timeout = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/messages", tok, `{"to":"1","type":"text","text":"x"}`); code != 400 || errCode(body) != "validation_failed" {
		t.Fatalf("bad number = %d %v", code, body)
	}

	e.sp.Last(id).EmitMessage(&workerv1.Message{Id: "IN1", Chat: "5511988887777@s.whatsapp.net",
		Sender: "5511988887777@s.whatsapp.net", PushName: "João", TimestampMs: 1_800_000_000_000, Type: "text", Text: "olá"})
	e.eventually("chat with one unread message", func() bool {
		_, body := e.do("GET", base+"/chats", tok, "")
		chats, _ := body["chats"].([]any)
		return len(chats) == 1 && chats[0].(map[string]any)["unread_count"] == float64(1)
	})
	code, body = e.do("GET", base+"/chats/5511988887777@s.whatsapp.net/messages?limit=1", tok, "")
	msgs, _ := body["messages"].([]any)
	if code != 200 || len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "olá" || body["next_cursor"] == nil {
		t.Fatalf("chat messages = %d %v", code, body)
	}
	if code, _ := e.do("POST", base+"/chats/5511988887777/read", tok, ""); code != 204 {
		t.Fatalf("mark read = %d", code)
	}
	if reads := e.sp.Last(id).WA.Reads(); len(reads) != 1 || reads[0].MessageIds[0] != "IN1" {
		t.Fatalf("reads = %v", reads)
	}
}

func TestMessagingIsScopedToTheInstance(t *testing.T) {
	e := newEnv(t)
	id1, _ := e.connected("A")
	_, tok2 := e.create("B")
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/messages", `{"to":"5511988887777","type":"text","text":"oi"}`},
		{"GET", "/chats", ""},
		{"GET", "/settings", ""},
		{"POST", "/check-numbers", `{"phones":["5511988887777"]}`},
	} {
		if code, _ := e.do(c.method, "/v1/instances/"+id1+c.path, tok2, c.body); code != 403 {
			t.Fatalf("%s %s with another instance's token = %d", c.method, c.path, code)
		}
	}
}

func TestSettingsContactsAndNumbers(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Cfg")
	base := "/v1/instances/" + id
	if code, body := e.do("PUT", base+"/settings", tok, `{"proxy_url":"socks5://p:1080"}`); code != 403 || errCode(body) != "forbidden" {
		t.Fatalf("proxy_url with instance token = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/settings", tok, `{"unsafe_pacing":true}`); code != 403 {
		t.Fatalf("unsafe_pacing with instance token = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/settings", admin, `{"proxy_url":""}`); code != 200 {
		t.Fatalf("proxy_url with admin token = %d %v", code, body)
	}
	code, body := e.do("PUT", base+"/settings", tok, `{"max_per_minute":10,"ramp":{"enabled":true}}`)
	if code != 200 || body["max_per_minute"] != float64(10) || body["ramp"].(map[string]any)["enabled"] != true {
		t.Fatalf("put settings = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/settings", tok, `{"max_per_minute":0}`); code != 400 {
		t.Fatalf("invalid settings = %d %v", code, body)
	}
	if _, body := e.do("GET", base+"/settings", tok, ""); body["max_per_minute"] != float64(10) {
		t.Fatalf("get settings = %v", body)
	}

	f := e.sp.Last(id)
	f.WA.SetContacts(&workerv1.Contact{Jid: "5511988887777@s.whatsapp.net", Name: "João"})
	f.WA.SetUnregistered("5511900000000")
	if code, body := e.do("GET", base+"/contacts", tok, ""); code != 200 || len(body["contacts"].([]any)) != 1 {
		t.Fatalf("contacts = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/contacts/5511988887777", tok, ""); code != 200 || body["name"] != "João" {
		t.Fatalf("contact = %d %v", code, body)
	}
	code, body = e.do("POST", base+"/check-numbers", tok, `{"phones":["5511988887777","5511900000000"]}`)
	res, _ := body["results"].([]any)
	if code != 200 || len(res) != 2 || res[0].(map[string]any)["exists"] != true {
		t.Fatalf("check numbers = %d %v", code, body)
	}
}

func TestMediaDownloadAndSignedURL(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Media")
	f := e.sp.Last(id)
	f.WA.SetMedia("ref", []byte("ogg-bytes"))
	f.EmitMessage(&workerv1.Message{Id: "AUD1", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "audio",
		Media: &workerv1.Media{Kind: "audio", Mime: "audio/ogg", Ptt: true, Ref: []byte("ref")}})
	var mediaURL string
	e.eventually("media url in the log", func() bool {
		_, body := e.do("GET", "/v1/instances/"+id+"/chats/5511988887777/messages", tok, "")
		if msgs, _ := body["messages"].([]any); len(msgs) == 1 {
			mediaURL, _ = msgs[0].(map[string]any)["media"].(map[string]any)["url"].(string)
		}
		return mediaURL != ""
	})
	var lastHeader http.Header
	get := func(path, token string) (int, string, string) {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		lastHeader = resp.Header
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	if code, ct, body := get(mediaURL, tok); code != 200 || ct != "audio/ogg" || body != "ogg-bytes" {
		t.Fatalf("media = %d %s %q", code, ct, body)
	}
	if code, _, _ := get(mediaURL, ""); code != 401 {
		t.Fatalf("media without token = %d", code)
	}
	code, body := e.do("GET", mediaURL+"?signed=1", tok, "")
	signed, _ := body["url"].(string)
	if code != 200 || !strings.HasPrefix(signed, "http://example.invalid/media/"+id+"/") {
		t.Fatalf("signed = %d %v", code, body)
	}
	u, _ := url.Parse(signed)
	if code, _, body := get(u.RequestURI(), ""); code != 200 || body != "ogg-bytes" {
		t.Fatalf("signed fetch = %d %q", code, body)
	}
	if lastHeader.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(lastHeader.Get("Content-Security-Policy"), "sandbox") ||
		lastHeader.Get("Content-Disposition") != "inline" {
		t.Fatalf("headers = %v", lastHeader)
	}
	q := u.Query()
	q.Set("sig", "00"+q.Get("sig")[2:])
	if code, _, _ := get(u.Path+"?"+q.Encode(), ""); code != 403 {
		t.Fatalf("tampered signature = %d", code)
	}
}

func TestReceivedSVGIsNeverInline(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("SVG")
	f := e.sp.Last(id)
	f.WA.SetMedia("svg", []byte("<svg onload=alert(1)/>"))
	f.EmitMessage(&workerv1.Message{Id: "DOC1", Chat: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "document",
		Media: &workerv1.Media{Kind: "document", Mime: "image/svg+xml", FileName: "x.svg", Ref: []byte("svg")}})
	var mediaURL string
	e.eventually("svg logged", func() bool {
		_, body := e.do("GET", "/v1/instances/"+id+"/chats/5511988887777/messages", tok, "")
		if msgs, _ := body["messages"].([]any); len(msgs) == 1 {
			mediaURL, _ = msgs[0].(map[string]any)["media"].(map[string]any)["url"].(string)
		}
		return mediaURL != ""
	})
	req, _ := http.NewRequest("GET", e.srv.URL+mediaURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "x.svg") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Fatalf("headers = %v", resp.Header)
	}
}

// A body over the limit is 413, not a JSON error.
func TestOversizedBodyIs413(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Big")
	big := `{"to":"5511988887777","type":"text","text":"` + strings.Repeat("x", 1<<20) + `"}`
	if code, body := e.do("PUT", "/v1/instances/"+id+"/settings", tok, big); code != 413 || errCode(body) != "media_too_large" {
		t.Fatalf("oversized settings body = %d %v", code, body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"`+strings.Repeat("x", 100)+`"}`))
	var v map[string]any
	var ae *apierr.Error
	if err := decodeMax(rec, req, &v, 10); !errors.As(err, &ae) || ae.Status != 413 || ae.Code != "media_too_large" {
		t.Fatalf("decodeMax over the limit = %v", err)
	}
}
