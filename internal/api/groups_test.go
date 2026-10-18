package api

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
)

func TestGroupRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Grupos")
	_, otherTok := e.create("Outra")
	f := e.sp.Last(id)
	base := "/v1/instances/" + id
	f.WA.SetUnregistered("5511900000000")

	// Create, with one result per participant.
	code, body := e.do("POST", base+"/groups", tok,
		`{"name":"Equipe","participants":["5511988887777","5511900000000"],"settings":{"announce":true}}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, body)
	}
	g := body["group"].(map[string]any)
	jid := g["jid"].(string)
	res := body["results"].([]any)
	if res[0].(map[string]any)["status"] != "ok" || res[1].(map[string]any)["status"] != "not_on_whatsapp" ||
		g["settings"].(map[string]any)["announce"] != true || len(g["participants"].([]any)) != 2 {
		t.Fatalf("create body = %v", body)
	}
	// Another instance's token cannot touch it.
	if code, _ := e.do("GET", base+"/groups/"+jid, otherTok, ""); code != 403 {
		t.Fatalf("other token = %d", code)
	}
	if code, body := e.do("GET", base+"/groups", tok, ""); code != 200 || len(body["groups"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, body)
	}
	// PATCH accepts nested settings.
	code, body = e.do("PATCH", base+"/groups/"+jid, tok, `{"name":"Novo","settings":{"locked":true,"ephemeral_seconds":86400}}`)
	if code != 200 || body["name"] != "Novo" || body["settings"].(map[string]any)["locked"] != true {
		t.Fatalf("patch = %d %v", code, body)
	}
	if code, body := e.do("PATCH", base+"/groups/"+jid, tok, `{"ephemeral_seconds":5}`); code != 400 {
		t.Fatalf("bad ephemeral = %d %v", code, body)
	}
	// Participants.
	code, body = e.do("POST", base+"/groups/"+jid+"/participants", tok, `{"action":"promote","participants":["5511988887777","5511977776666"]}`)
	rs := body["results"].([]any)
	if code != 200 || rs[0].(map[string]any)["status"] != "ok" || rs[1].(map[string]any)["status"] != "not_participant" {
		t.Fatalf("promote = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/groups/"+jid+"/participants", tok, `{"action":"ban","participants":["5511988887777"]}`); code != 400 {
		t.Fatalf("bad action = %d %v", code, body)
	}
	// Photo: JPEG only.
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n...."))
	if code, body := e.do("PUT", base+"/groups/"+jid+"/photo", tok, `{"base64":"`+png+`"}`); code != 400 {
		t.Fatalf("png photo = %d %v", code, body)
	}
	jpeg := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0fakejpeg"))
	if code, body := e.do("PUT", base+"/groups/"+jid+"/photo", tok, `{"base64":"`+jpeg+`"}`); code != 204 {
		t.Fatalf("photo = %d %v", code, body)
	}
	if code, _ := e.do("DELETE", base+"/groups/"+jid+"/photo", tok, ""); code != 204 || len(f.WA.GroupPhoto(jid)) != 0 {
		t.Fatalf("remove photo = %d", code)
	}
	// Invite link.
	code, body = e.do("GET", base+"/groups/"+jid+"/invite", tok, "")
	link, _ := body["link"].(string)
	if code != 200 || !strings.HasPrefix(link, "https://chat.whatsapp.com/") {
		t.Fatalf("invite = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/groups/"+jid+"/invite/reset", tok, ""); code != 200 || body["link"] == link {
		t.Fatalf("reset = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/groups/preview", tok, `{"code":"`+link+`"}`); code != 410 || errCode(body) != "invite_revoked" {
		t.Fatalf("revoked preview = %d %v", code, body)
	}
	// Join requests.
	f.WA.SetJoinRequests(jid, &workerv1.JoinRequest{Jid: "5511955554444@s.whatsapp.net"})
	if code, body := e.do("GET", base+"/groups/"+jid+"/requests", tok, ""); code != 200 || len(body["requests"].([]any)) != 1 {
		t.Fatalf("requests = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/groups/"+jid+"/requests", tok, `{"action":"approve","participants":["5511955554444"]}`); code != 200 ||
		body["results"].([]any)[0].(map[string]any)["status"] != "ok" {
		t.Fatalf("approve = %d %v", code, body)
	}
	// Communities.
	code, body = e.do("POST", base+"/communities", tok, `{"name":"Bairro"}`)
	if code != 201 || body["group"].(map[string]any)["community"] != true {
		t.Fatalf("community = %d %v", code, body)
	}
	cjid := body["group"].(map[string]any)["jid"].(string)
	if code, body := e.do("POST", base+"/communities/"+cjid+"/groups", tok, `{"group":"`+jid+`"}`); code != 204 {
		t.Fatalf("link = %d %v", code, body)
	}
	code, body = e.do("GET", base+"/communities/"+cjid, tok, "")
	if code != 200 || len(body["groups"].([]any)) != 1 || body["community"].(map[string]any)["name"] != "Bairro" {
		t.Fatalf("community get = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/communities/"+jid, tok, ""); code != 404 {
		t.Fatalf("group as community = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/communities", tok, ""); code != 200 || len(body["communities"].([]any)) != 1 {
		t.Fatalf("communities = %d %v", code, body)
	}
	if code, _ := e.do("DELETE", base+"/communities/"+cjid+"/groups/"+jid, tok, ""); code != 204 {
		t.Fatalf("unlink = %d", code)
	}
	// Join by link and leave.
	f.WA.SetInvite("XYZ", &workerv1.GroupInfo{Jid: "120363000000000088@g.us", Name: "Externo"})
	if code, body := e.do("POST", base+"/groups/preview", tok, `{"code":"XYZ"}`); code != 200 || body["name"] != "Externo" {
		t.Fatalf("preview = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/groups/join", tok, `{"code":"https://chat.whatsapp.com/XYZ"}`); code != 200 || body["jid"] != "120363000000000088@g.us" {
		t.Fatalf("join = %d %v", code, body)
	}
	if code, _ := e.do("POST", base+"/groups/120363000000000088@g.us/leave", tok, ""); code != 204 {
		t.Fatalf("leave = %d", code)
	}
	if code, body := e.do("GET", base+"/groups/120363000000000088@g.us", tok, ""); code != 404 || errCode(body) != "group_not_found" {
		t.Fatalf("after leave = %d %v", code, body)
	}
	// Daily creation limit (3 by default; the community counted).
	e.do("POST", base+"/groups", tok, `{"name":"Mais um"}`)
	if code, body := e.do("POST", base+"/groups", tok, `{"name":"Demais"}`); code != 429 || errCode(body) != "rate_limited" {
		t.Fatalf("over limit = %d %v", code, body)
	}
}

func TestGroupRoutesNeedRunningInstance(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Parada")
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/stop", tok, ""); code != 200 {
		t.Fatal("stop")
	}
	if code, body := e.do("GET", "/v1/instances/"+id+"/groups", tok, ""); code != 409 || errCode(body) != "instance_not_connected" {
		t.Fatalf("stopped = %d %v", code, body)
	}
}

func TestGroupUpdatedEvents(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Eventos")
	f := e.sp.Last(id)
	groupHook, directHook := newHookReceiver(t), newHookReceiver(t)
	e.do("POST", "/v1/webhooks", tok, `{"url":"`+groupHook.srv.URL+`","events":["group.updated"],"filters":{"chat":"group"}}`)
	e.do("POST", "/v1/webhooks", tok, `{"url":"`+directHook.srv.URL+`","events":["*"],"filters":{"chat":"direct"}}`)
	sse, _ := e.sse("/v1/instances/"+id+"/events?types=group.*", tok)
	const grp = "120363000000000001@g.us"
	f.EmitGroupUpdate(&workerv1.GroupUpdate{Group: grp, Actor: caller, TimestampMs: time.Now().UnixMilli(),
		Joined: []string{"5511977776666@s.whatsapp.net"}, JoinReason: "invite"})
	var env struct{ Data events.GroupUpdate }
	if err := json.Unmarshal([]byte(sse.next(events.TypeGroupUpdated).data), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Kind != "participants_added" || env.Data.Chat != grp || env.Data.Reason != "invite" || env.Data.Actor != caller {
		t.Fatalf("event = %+v", env.Data)
	}
	e.eventually("group webhook", func() bool { _, _, ok := groupHook.find(events.TypeGroupUpdated); return ok })
	time.Sleep(100 * time.Millisecond)
	if _, _, ok := directHook.find(events.TypeGroupUpdated); ok {
		t.Fatal("a direct-only webhook received group.updated")
	}
}
