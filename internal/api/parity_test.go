package api

import (
	"context"
	"errors"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/store"
)

// CHT-7: clearing a chat sends the clearChat app state action and deletes the chat's
// messages from the log (starred ones kept on request), keeping the chat.
func TestClearChat(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Limpar")
	f := e.sp.Last(id)
	f.WA.SetChatEcho(true)
	e.inbound(id, "W1", contact)
	e.inbound(id, "W2", contact)
	ctx := context.Background()
	if _, err := e.st.SetStarred(ctx, id, contact, "W1", true); err != nil {
		t.Fatal(err)
	}
	sse, _ := e.sse("/v1/instances/"+id+"/events?types=chat.updated", tok)
	base := "/v1/instances/" + id + "/chats/" + contact

	code, v := e.do("POST", base+"/clear", tok, `{"keep_starred":true}`)
	if code != 200 || v["deleted_messages"] != float64(1) || v["chat"].(map[string]any)["jid"] != contact {
		t.Fatalf("clear = %d %v", code, v)
	}
	a := f.WA.ChatActions()
	if last := a[len(a)-1]; last.Action != "clear" || !last.KeepStarred || last.DeleteMedia || last.Message.GetId() != "W2" {
		t.Fatalf("clear action = %+v", last)
	}
	if _, err := e.st.MessageByWAID(ctx, id, contact, "W2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("W2 still logged: %v", err)
	}
	if _, err := e.st.MessageByWAID(ctx, id, contact, "W1"); err != nil {
		t.Fatalf("starred W1 was cleared: %v", err)
	}
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); !ev.Cleared || ev.Source != "api" || ev.Changed[0] != "cleared" {
		t.Fatalf("chat.updated = %+v", ev)
	}
	// The app state echo of our own clear is not reported again; a later change is.
	f.EmitChatUpdate(&workerv1.ChatUpdate{Chat: contact, Field: "pinned", Value: true})
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); ev.Changed[0] != "pinned" {
		t.Fatalf("echo reported: %+v", ev)
	}

	// Without a body everything goes, starred included; the chat stays listed.
	if code, v := e.do("POST", base+"/clear", tok, ""); code != 200 || v["deleted_messages"] != float64(1) {
		t.Fatalf("clear all = %d %v", code, v)
	}
	_, list := e.do("GET", "/v1/instances/"+id+"/chats", tok, "")
	if len(list["chats"].([]any)) != 1 {
		t.Fatalf("cleared chat left the list: %v", list)
	}
	_, msgs := e.do("GET", base+"/messages", tok, "")
	if n := len(msgs["messages"].([]any)); n != 0 {
		t.Fatalf("messages after clear = %d", n)
	}

	if code, v := e.do("POST", base+"/clear", tok, `{"keep_starred":"sim"}`); code != 400 || errCode(v) != "validation_failed" {
		t.Fatalf("bad body = %d %v", code, v)
	}
	if code, _ := e.do("POST", "/v1/instances/"+id+"/chats/5511999990000@s.whatsapp.net/clear", tok, ""); code != 404 {
		t.Fatalf("unknown chat = %d", code)
	}
}

// CON-2: POST /contacts and DELETE /contacts/{jid} change the account's address book.
func TestAddressBook(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Agenda")
	f := e.sp.Last(id)
	base := "/v1/instances/" + id + "/contacts"

	code, v := e.do("POST", base, tok, `{"phone":"+55 11 99999-0000","first_name":"Ana","last_name":"Teste"}`)
	if code != 200 || v["jid"] != "5511999990000@s.whatsapp.net" || v["full_name"] != "Ana Teste" || v["save_on_phone"] != true {
		t.Fatalf("save = %d %v", code, v)
	}
	acts := f.WA.ContactActions()
	if len(acts) != 1 || acts[0].Action != "save" || acts[0].FirstName != "Ana" || acts[0].FullName != "Ana Teste" || !acts[0].SaveOnPhone {
		t.Fatalf("actions = %+v", acts)
	}
	if f.WA.Checks() != 1 {
		t.Fatalf("a number never seen must be checked first (checks = %d)", f.WA.Checks())
	}
	if _, v := e.do("GET", base+"/5511999990000@s.whatsapp.net", tok, ""); v["name"] != "Ana Teste" {
		t.Fatalf("contact = %v", v)
	}
	// A chat of the log is not looked up again; save_on_phone false is passed on.
	e.inbound(id, "W1", contact)
	if code, _ := e.do("POST", base, tok, `{"phone":"5511988887777","first_name":"Bia","save_on_phone":false}`); code != 200 {
		t.Fatalf("save known = %d", code)
	}
	if acts = f.WA.ContactActions(); acts[1].SaveOnPhone || acts[1].FullName != "Bia" || f.WA.Checks() != 1 {
		t.Fatalf("second save = %+v (checks %d)", acts[1], f.WA.Checks())
	}
	if code, _ := e.do("DELETE", base+"/5511999990000", tok, ""); code != 204 {
		t.Fatalf("remove = %d", code)
	}
	if acts = f.WA.ContactActions(); acts[2].Action != "remove" || acts[2].Jid != "5511999990000@s.whatsapp.net" {
		t.Fatalf("remove = %+v", acts[2])
	}
	f.WA.SetUnregistered("5511999990009")
	for body, want := range map[string]string{
		`{"phone":"5511999990000"}`:                                   "validation_failed",
		`{"first_name":"Ana"}`:                                        "validation_failed",
		`{"phone":"120363000000000001@g.us","first_name":"Grupo"}`:    "validation_failed",
		`{"phone":"5511999990000","first_name":"Ana","nickname":"x"}`: "validation_failed",
		`{"phone":"5511999990009","first_name":"Ninguém"}`:            "invalid_number",
	} {
		if code, v := e.do("POST", base, tok, body); code < 400 || errCode(v) != want {
			t.Errorf("%s = %d %v", body, code, v)
		}
	}
	if code, v := e.do("DELETE", base+"/120363000000000001@g.us", tok, ""); code != 400 {
		t.Fatalf("remove group = %d %v", code, v)
	}
}

// CHT-11: GET /messages/search searches the instance's log.
func TestSearchMessagesRoute(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Busca")
	f := e.sp.Last(id)
	now := time.Now()
	for i, m := range []*workerv1.Message{
		{Id: "S1", Chat: contact, Sender: contact, Type: "text", Text: "Qual o preço do orçamento?"},
		{Id: "S2", Chat: contact, Sender: contact, Type: "document", Media: &workerv1.Media{Kind: "document", FileName: "Orcamento-2026.pdf", Ref: []byte("r")}},
		{Id: "S3", Chat: "5511999990003@s.whatsapp.net", Sender: "5511999990003@s.whatsapp.net", Type: "text", Text: "orçamento aprovado"},
	} {
		m.TimestampMs = now.Add(time.Duration(i) * time.Second).UnixMilli()
		f.EmitMessage(m)
	}
	base := "/v1/instances/" + id + "/messages/search"
	e.eventually("indexed", func() bool {
		_, v := e.do("GET", base+"?q=orcamento", tok, "")
		return v["messages"] != nil && len(v["messages"].([]any)) == 3
	})
	_, v := e.do("GET", base+"?q=orcamento&chat=5511988887777&limit=1", tok, "")
	msgs := v["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["wa_message_id"] != "S2" || v["next_cursor"] == nil || v["index_complete"] != true {
		t.Fatalf("page 1 = %v", v)
	}
	_, v = e.do("GET", base+"?q=orcamento&chat=5511988887777&limit=1&cursor="+v["next_cursor"].(string), tok, "")
	if hit := v["messages"].([]any)[0].(map[string]any); hit["wa_message_id"] != "S1" || hit["snippet"] == "" {
		t.Fatalf("page 2 = %v", v)
	}
	_, v = e.do("GET", base+"?q=aprovado&from=5511999990003", tok, "")
	if len(v["messages"].([]any)) != 1 {
		t.Fatalf("from = %v", v)
	}
	_, v = e.do("GET", base+"?q=orcamento&from=me", tok, "")
	if len(v["messages"].([]any)) != 0 {
		t.Fatalf("from me = %v", v)
	}
	for _, q := range []string{"", "?q=", "?q=%3F%3F", "?q=a&before=ontem", "?q=a&cursor=xx", "?q=a&from=120363000000000001%40g.us"} {
		if code, v := e.do("GET", base+q, tok, ""); code != 400 || errCode(v) != "validation_failed" {
			t.Errorf("%q = %d %v", q, code, v)
		}
	}
}

// CHT-9: a direct chat's timer change goes through the queue as a protocol message; later
// sends carry the timer; the contact turning it off is recorded; groups use their setting.
func TestDisappearingMessages(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Temporarias")
	f := e.sp.Last(id)
	e.inbound(id, "W1", contact)
	sse, _ := e.sse("/v1/instances/"+id+"/events?types=chat.updated", tok)
	base := "/v1/instances/" + id + "/chats/" + contact

	if _, v := e.do("GET", base, tok, ""); v["disappearing"] != nil {
		t.Fatalf("unknown timer exposed: %v", v)
	}
	code, v := e.do("PATCH", base, tok, `{"disappearing":"7d"}`)
	if code != 200 || v["disappearing"] != "7d" || v["disappearing_seconds"] != float64(604800) || v["disappearing_queued"] != nil {
		t.Fatalf("set 7d = %d %v", code, v)
	}
	sent := f.WA.Sent()
	if last := sent[len(sent)-1]; last.Type != "disappearing" || last.DisappearingSeconds != 604800 || last.To != contact {
		t.Fatalf("sent = %+v", last)
	}
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); ev.Changed[0] != "disappearing" || ev.Disappearing != "7d" || ev.Source != "api" {
		t.Fatalf("chat.updated = %+v", ev)
	}
	// The change is not a message of the log; the next message carries the timer.
	_, msgs := e.do("GET", base+"/messages", tok, "")
	if n := len(msgs["messages"].([]any)); n != 1 {
		t.Fatalf("log = %v", msgs)
	}
	if code, _ := e.do("POST", "/v1/instances/"+id+"/messages?wait=true&timeout=2s", tok, `{"to":"`+contact+`","type":"text","text":"some"}`); code != 200 {
		t.Fatalf("send = %d", code)
	}
	sent = f.WA.Sent()
	if last := sent[len(sent)-1]; last.Type != "text" || last.EphemeralExpiration != 604800 || last.EphemeralSettingTs == 0 {
		t.Fatalf("text = %+v", last)
	}
	// The contact turns it off (newer setting): recorded and reported.
	f.EmitChatUpdate(&workerv1.ChatUpdate{Chat: contact, Field: "disappearing", DisappearingSeconds: 0, TimestampMs: time.Now().Add(time.Minute).UnixMilli()})
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); ev.Disappearing != "off" || ev.Source != "contact" {
		t.Fatalf("contact change = %+v", ev)
	}
	if _, v := e.do("GET", base, tok, ""); v["disappearing"] != "off" {
		t.Fatalf("after contact change = %v", v)
	}
	if code, _ := e.do("POST", "/v1/instances/"+id+"/messages?wait=true&timeout=2s", tok, `{"to":"`+contact+`","type":"text","text":"fica"}`); code != 200 {
		t.Fatalf("send = %d", code)
	}
	if sent = f.WA.Sent(); sent[len(sent)-1].EphemeralExpiration != 0 {
		t.Fatalf("timer kept after off: %+v", sent[len(sent)-1])
	}
	for body, want := range map[string]string{
		`{"disappearing":"1h"}`:  "validation_failed",
		`{"disappearing":86400}`: "validation_failed",
		`{"ephemeral":"7d"}`:     "validation_failed",
	} {
		if code, v := e.do("PATCH", base, tok, body); code != 400 || errCode(v) != want {
			t.Errorf("%s = %d %v", body, code, v)
		}
	}
	// The type is internal: POST /messages refuses it.
	if code, v := e.do("POST", "/v1/instances/"+id+"/messages", tok, `{"to":"`+contact+`","type":"disappearing"}`); code != 400 {
		t.Fatalf("public disappearing = %d %v", code, v)
	}

	// A received message carrying a timer teaches the chat's timer.
	other := "5511999990002@s.whatsapp.net"
	f.EmitMessage(&workerv1.Message{Id: "X1", Chat: other, Sender: other, Type: "text", Text: "oi",
		TimestampMs: time.Now().UnixMilli(), EphemeralSeconds: 7776000})
	e.eventually("timer learned", func() bool {
		_, v := e.do("GET", "/v1/instances/"+id+"/chats/"+other, tok, "")
		return v["disappearing"] == "90d"
	})

	// Groups: the group setting (no message through the queue).
	group := "120363000000000001@g.us"
	f.WA.SetGroup(&workerv1.GroupInfo{Jid: group, Name: "Teste"})
	e.inbound(id, "G1", group)
	before := len(f.WA.Sent())
	code, v = e.do("PATCH", "/v1/instances/"+id+"/chats/"+group, tok, `{"disappearing":"24h"}`)
	if code != 200 || v["disappearing"] != "24h" || len(f.WA.Sent()) != before || f.WA.Group(group).GetEphemeralSeconds() != 86400 {
		t.Fatalf("group = %d %v", code, v)
	}
}
