package api

import (
	"testing"
)

func TestNewMessageTypesAndQueueRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("M6")
	base := "/v1/instances/" + id

	code, body := e.do("POST", base+"/messages?wait=true&timeout=2s", tok,
		`{"to":"5511988887777","type":"location","location":{"latitude":-23.5,"longitude":-46.6,"name":"SP"}}`)
	if code != 200 || body["location"].(map[string]any)["name"] != "SP" {
		t.Fatalf("location = %d %v", code, body)
	}
	code, body = e.do("POST", base+"/messages?wait=true&timeout=2s", tok, `{"to":"5511988887777","type":"text","text":"oi"}`)
	if code != 200 {
		t.Fatalf("text = %d %v", code, body)
	}
	msgID := body["id"].(string)
	code, body = e.do("POST", base+"/messages?wait=true&timeout=2s", tok, `{"type":"edit","message_id":"`+msgID+`","text":"oi!"}`)
	if code != 200 || body["target"] == nil {
		t.Fatalf("edit = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/messages/"+msgID, tok, ""); code != 200 || body["text"] != "oi!" || body["edited_at"] == nil {
		t.Fatalf("edited message = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/messages", tok, `{"to":"5511988887777","type":"text","text":"x","link_preview":"maybe"}`); code != 400 {
		t.Fatalf("bad link_preview = %d %v", code, body)
	}

	// Queue: hold it by disconnecting, then list, cancel one, clear the rest.
	e.sp.Last(id).EmitConn("reconnecting", "test")
	e.waitState(id, "reconnecting")
	_, a := e.do("POST", base+"/messages", tok, `{"to":"5511988887777","type":"text","text":"a"}`)
	e.do("POST", base+"/messages", tok, `{"to":"5511988887777","type":"text","text":"b"}`)
	code, body = e.do("GET", base+"/queue", tok, "")
	if items, _ := body["items"].([]any); code != 200 || len(items) != 2 {
		t.Fatalf("queue = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/queue?status=nope", tok, ""); code != 400 {
		t.Fatalf("bad status = %d %v", code, body)
	}
	if code, body := e.do("DELETE", base+"/queue/"+a["id"].(string), tok, ""); code != 200 || body["status"] != "failed" {
		t.Fatalf("cancel = %d %v", code, body)
	}
	if code, body := e.do("DELETE", base+"/queue/"+a["id"].(string), tok, ""); code != 409 || errCode(body) != "conflict" {
		t.Fatalf("cancel twice = %d %v", code, body)
	}
	if code, body := e.do("DELETE", base+"/queue", tok, ""); code != 200 || body["canceled"] != float64(1) {
		t.Fatalf("clear = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/messages/"+a["id"].(string), tok, ""); code != 200 || body["status"] != "failed" {
		t.Fatalf("canceled message = %d %v", code, body)
	}
	// Another instance's token cannot touch this queue.
	_, tok2 := e.connected("Other")
	if code, _ := e.do("DELETE", base+"/queue", tok2, ""); code != 403 {
		t.Fatalf("foreign token = %d", code)
	}
}
