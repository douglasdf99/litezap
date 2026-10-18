package api

import (
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

func TestSocialRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Social")
	_, otherTok := e.create("Outra")
	f := e.sp.Last(id)
	base := "/v1/instances/" + id

	// Status: queued like a message, answered 200 once sent with ?wait=true.
	code, body := e.do("POST", base+"/status?wait=true", tok, `{"type":"text","text":"Bom dia","background_color":"#204060"}`)
	if code != 200 || body["chat"] != "status@broadcast" || body["status"] != "sent" {
		t.Fatalf("post status = %d %v", code, body)
	}
	if code, _ := e.do("POST", base+"/status", otherTok, `{"type":"text","text":"x"}`); code != 403 {
		t.Fatalf("other token = %d", code)
	}
	if code, body := e.do("POST", base+"/status", tok, `{"type":"sticker"}`); code != 400 {
		t.Fatalf("bad type = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/status/privacy", tok, ""); code != 200 || body["type"] != "contacts" {
		t.Fatalf("privacy = %d %v", code, body)
	}

	// A contact's status: opt-in event, listed, answerable.
	sse, _ := e.sse(base+"/events?types=status.received", tok)
	f.EmitStatus(&workerv1.Message{Id: "ST9", Chat: "status@broadcast", Sender: "5511988887777@s.whatsapp.net", Type: "text",
		Text: "story", TimestampMs: time.Now().UnixMilli()})
	ev := sse.next("status.received")
	if !strings.Contains(ev.data, `"story"`) {
		t.Fatalf("status.received = %v", ev)
	}
	code, body = e.do("GET", base+"/status", tok, "")
	msgs, _ := body["messages"].([]any)
	if code != 200 || len(msgs) != 2 {
		t.Fatalf("list status = %d %v", code, body)
	}
	theirs := msgs[0].(map[string]any)["id"].(string)
	if code, body := e.do("POST", base+"/status/"+theirs+"/reply?wait=true", tok, `{"text":"Legal!"}`); code != 200 || body["chat"] != "5511988887777@s.whatsapp.net" {
		t.Fatalf("reply = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/chats", tok, ""); code != 200 || strings.Contains(strings.Join(chatJIDs(body), ","), "status@broadcast") {
		t.Fatalf("chats = %d %v", code, body)
	}

	// Channels.
	f.WA.SetChannel(&workerv1.Channel{Jid: "120363000000000001@newsletter", Name: "Loja", Role: "owner", InviteCode: "LOJA"}, false)
	if code, body := e.do("POST", base+"/channels/preview", tok, `{"invite":"https://whatsapp.com/channel/LOJA"}`); code != 200 || body["name"] != "Loja" {
		t.Fatalf("preview = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/channels/120363000000000001/follow", tok, ""); code != 204 {
		t.Fatalf("follow = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/channels", tok, ""); code != 200 || len(body["channels"].([]any)) != 1 {
		t.Fatalf("channels = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/channels/120363000000000001@newsletter/messages?wait=true", tok, `{"type":"text","text":"Oferta"}`); code != 200 {
		t.Fatalf("publish = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/channels/120363000000000001@newsletter/messages?count=5", tok, ""); code != 200 {
		t.Fatalf("channel messages = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/channels/120363000000000001@newsletter/reactions", tok, `{"server_id":3,"emoji":"👍"}`); code != 204 {
		t.Fatalf("react = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/channels", tok, `{"name":"Novo canal"}`); code != 201 || body["role"] != "owner" {
		t.Fatalf("create channel = %d %v", code, body)
	}

	// Labels need a business account.
	if code, body := e.do("GET", base+"/labels", tok, ""); code != 409 || body["error"].(map[string]any)["code"] != "business_account_required" {
		t.Fatalf("labels on a personal account = %d %v", code, body)
	}
	if code, _ := e.do("GET", base+"/business/profile", tok, ""); code != 409 {
		t.Fatalf("business profile on a personal account = %d", code)
	}
}

func chatJIDs(body map[string]any) []string {
	var out []string
	list, _ := body["chats"].([]any)
	for _, c := range list {
		out = append(out, c.(map[string]any)["jid"].(string))
	}
	return out
}

// Scoped API keys (marco 11) on the marco 10 routes: read keys read statuses, channels,
// labels and the business profile; every write is refused for any key; the key's IP
// allowlist applies to these routes too.
func TestSocialRoutesWithAPIKeys(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("SocialKeys")
	base := "/v1/instances/" + id
	e.sp.Last(id).WA.SetChannel(&workerv1.Channel{Jid: "120363000000000001@newsletter", Name: "Loja", Role: "owner"}, false)
	_, out := e.do("POST", base+"/api-keys", tok, `{"name":"bi","scopes":["read","send"]}`)
	key := out["key"].(string)
	for _, p := range []string{"/status", "/status/privacy", "/channels", "/channels/120363000000000001@newsletter",
		"/channels/120363000000000001@newsletter/messages", "/labels", "/business/profile"} {
		if code, body := e.do("GET", base+p, key, ""); code == 401 || code == 403 {
			t.Errorf("read %s = %d %v", p, code, body)
		}
	}
	for _, d := range []struct{ method, path, body string }{
		{"POST", "/status", `{"type":"text","text":"x"}`},
		{"DELETE", "/status/msg_x", ""},
		{"POST", "/status/msg_x/reply", `{"text":"x"}`},
		{"POST", "/channels", `{"name":"x"}`},
		{"POST", "/channels/preview", `{"invite":"ABC"}`},
		{"POST", "/channels/120363000000000001/follow", ""},
		{"POST", "/channels/120363000000000001/messages", `{"type":"text","text":"x"}`},
		{"POST", "/channels/120363000000000001/reactions", `{"server_id":1,"emoji":"x"}`},
		{"POST", "/labels", `{"name":"x"}`},
		{"PATCH", "/labels/1", `{"name":"x"}`},
		{"DELETE", "/labels/1", ""},
		{"PUT", "/labels/1/chats/5511988887777", ""},
		{"PUT", "/labels/1/messages/msg_x", ""},
	} {
		if code, body := e.do(d.method, base+d.path, key, d.body); code != 403 {
			t.Errorf("key %s %s = %d %v", d.method, d.path, code, body)
		}
	}
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"far","scopes":["read"],"ip_allowlist":["10.0.0.0/8"]}`)
	if code, _ := e.do("GET", base+"/channels", out["key"].(string), ""); code != 403 {
		t.Fatalf("key outside its allowlist = %d", code)
	}
}
