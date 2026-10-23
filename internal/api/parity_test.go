package api

import (
	"context"
	"errors"
	"testing"

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
