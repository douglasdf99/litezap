package api

import (
	"strings"
	"testing"
)

func TestAgentHookRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Agent")
	base := "/v1/instances/" + id

	code, body := e.do("GET", base+"/agent-hook", tok, "")
	if code != 200 || body["configured"] != false || body["timeout"] != "30s" || body["context_messages"] != float64(20) {
		t.Fatalf("get default = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/agent-hook", tok, `{"enabled":true}`); code != 400 || errCode(body) != "validation_failed" {
		t.Fatalf("enable without url = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/agent-hook", tok, `{"url":"https://a.example/h","bogus":1}`); code != 400 {
		t.Fatalf("unknown field = %d %v", code, body)
	}
	code, body = e.do("PUT", base+"/agent-hook", tok, `{"url":"https://a.example/h","enabled":true,"timeout":"20s","ignore":["5511988887777"]}`)
	if code != 200 || body["configured"] != true || !strings.HasPrefix(body["secret"].(string), "agsec_") || body["timeout"] != "20s" {
		t.Fatalf("put = %d %v", code, body)
	}
	code, body = e.do("GET", base+"/agent-hook", tok, "")
	if code != 200 || body["secret"] != nil || body["secret_set"] != true || body["enabled"] != true {
		t.Fatalf("get = %d %v", code, body)
	}

	jid := "5511999999999@s.whatsapp.net"
	if code, body := e.do("PUT", base+"/chats/"+jid+"/agent", tok, `{}`); code != 400 {
		t.Fatalf("missing enabled = %d %v", code, body)
	}
	if code, body := e.do("PUT", base+"/chats/"+jid+"/agent", tok, `{"enabled":"yes"}`); code != 400 {
		t.Fatalf("bad enabled = %d %v", code, body)
	}
	code, body = e.do("PUT", base+"/chats/"+jid+"/agent", tok, `{"enabled":false}`)
	if code != 200 || body["enabled"] != false || body["jid"] != jid {
		t.Fatalf("chat off = %d %v", code, body)
	}
	code, body = e.do("GET", base+"/agent-hook/chats", tok, "")
	if chats, _ := body["chats"].([]any); code != 200 || len(chats) != 1 {
		t.Fatalf("chats = %d %v", code, body)
	}
	code, body = e.do("PUT", base+"/chats/"+jid+"/agent", tok, `{"enabled":null}`)
	if code != 200 || body["enabled"] != nil {
		t.Fatalf("chat inherit = %d %v", code, body)
	}
	code, body = e.do("GET", base+"/agent-hook/calls?limit=10", tok, "")
	if calls, ok := body["calls"].([]any); code != 200 || !ok || len(calls) != 0 {
		t.Fatalf("calls = %d %v", code, body)
	}
	if code, _ := e.do("GET", base+"/agent-hook/calls?limit=x", tok, ""); code != 400 {
		t.Fatalf("bad limit = %d", code)
	}
	// Another instance's token is refused.
	_, tok2 := e.create("Other")
	if code, _ := e.do("GET", base+"/agent-hook", tok2, ""); code != 403 {
		t.Fatalf("foreign token = %d", code)
	}
	if code, _ := e.do("DELETE", base+"/agent-hook", tok, ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, body := e.do("GET", "/v1/instances/inst_nope/agent-hook", admin, ""); code != 404 {
		t.Fatalf("missing instance = %d %v", code, body)
	}
}
