package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
)

const caller = "5511988887777@s.whatsapp.net"

func callData(t *testing.T, ev sseEvent) events.Call {
	t.Helper()
	var env struct{ Data events.Call }
	if err := json.Unmarshal([]byte(ev.data), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func TestCallsSettingsAndAutoReject(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Calls")
	f := e.sp.Last(id)

	if code, body := e.do("PUT", "/v1/instances/"+id+"/settings", tok,
		`{"calls":{"reject_message":"`+strings.Repeat("x", 1001)+`"}}`); code != 400 {
		t.Fatalf("long message = %d %v", code, body)
	}
	code, set := e.do("PUT", "/v1/instances/"+id+"/settings", tok,
		`{"calls":{"auto_reject":true,"reject_message":"Não atendo ligações, mande mensagem."}}`)
	calls, _ := set["calls"].(map[string]any)
	if code != 200 || calls["auto_reject"] != true || set["presence"].(map[string]any)["online"] != false {
		t.Fatalf("settings = %d %v", code, set)
	}

	sse, _ := e.sse("/v1/instances/"+id+"/events?types=call.*", tok)
	start := time.Now()
	f.EmitCall(&workerv1.Call{CallId: "C1", Kind: "offer", Caller: caller, Media: "audio", TimestampMs: time.Now().UnixMilli()})
	got := callData(t, sse.next(events.TypeCallReceived))
	if !got.Rejected || got.Caller != caller || got.Chat != caller || got.Media != "audio" || got.CallID != "C1" {
		t.Fatalf("call.received = %+v", got)
	}
	if time.Since(start) > 3*time.Second || !slices.Equal(f.WA.Rejected(), []string{"C1"}) {
		t.Fatalf("rejected = %v after %v", f.WA.Rejected(), time.Since(start))
	}
	ended := callData(t, sse.next(events.TypeCallEnded))
	if ended.Reason != "rejected" || ended.Missed {
		t.Fatalf("call.ended = %+v", ended)
	}
	// WhatsApp's own terminate for the rejected call does not end it twice.
	f.EmitCall(&workerv1.Call{CallId: "C1", Kind: "terminate", Caller: caller})

	// The reject message goes through the queue to the caller.
	e.eventually("reject message sent", func() bool {
		for _, s := range f.WA.Sent() {
			if s.To == caller && s.Text == "Não atendo ligações, mande mensagem." {
				return true
			}
		}
		return false
	})
	// A second call from the same caller is rejected but not answered again (cooldown).
	f.EmitCall(&workerv1.Call{CallId: "C2", Kind: "offer", Caller: caller})
	if got := callData(t, sse.next(events.TypeCallReceived)); !got.Rejected || got.CallID != "C2" {
		t.Fatalf("second call = %+v", got)
	}
	if next := callData(t, sse.next(events.TypeCallEnded)); next.CallID != "C2" {
		t.Fatalf("C1 ended twice: %+v", next)
	}
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, s := range f.WA.Sent() {
		if s.To == caller {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reject messages = %d", n)
	}
	// Group calls are only reported (rejecting needs a creator group notices lack).
	f.EmitCall(&workerv1.Call{CallId: "G1", Kind: "offer_notice", Caller: "5511977776666@s.whatsapp.net", Group: "1203630@g.us"})
	if g := callData(t, sse.next(events.TypeCallReceived)); !g.IsGroup || g.Chat != "1203630@g.us" || g.Rejected {
		t.Fatalf("group call = %+v", g)
	}
	// An offer that rang minutes ago (delivered after a reconnect) is not rejected nor answered.
	other := "5511966665555@s.whatsapp.net"
	f.EmitCall(&workerv1.Call{CallId: "OLD", Kind: "offer", Caller: other, TimestampMs: time.Now().Add(-5 * time.Minute).UnixMilli()})
	if g := callData(t, sse.next(events.TypeCallReceived)); g.Rejected || g.CallID != "OLD" {
		t.Fatalf("stale call = %+v", g)
	}
	time.Sleep(100 * time.Millisecond)
	for _, s := range f.WA.Sent() {
		if s.To == other {
			t.Fatal("reject message for a stale call")
		}
	}
	if slices.Contains(f.WA.Rejected(), "OLD") || slices.Contains(f.WA.Rejected(), "G1") {
		t.Fatalf("rejected = %v", f.WA.Rejected())
	}
}

func TestMissedAndAnsweredCalls(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Missed")
	f := e.sp.Last(id)
	sse, _ := e.sse("/v1/instances/"+id+"/events?types=call.*", tok)

	f.EmitCall(&workerv1.Call{CallId: "M1", Kind: "offer", Caller: caller, Media: "video"})
	if got := callData(t, sse.next(events.TypeCallReceived)); got.Rejected || got.Media != "video" {
		t.Fatalf("received = %+v", got)
	}
	// The phone's reject right after the offer (seen on real devices) does not end the call.
	f.EmitCall(&workerv1.Call{CallId: "M1", Kind: "reject", Caller: caller, FromSelf: true})
	f.EmitCall(&workerv1.Call{CallId: "M1", Kind: "terminate", Caller: caller, Reason: "timeout"})
	if got := callData(t, sse.next(events.TypeCallEnded)); !got.Missed || got.Reason != "timeout" || got.Media != "video" {
		t.Fatalf("missed = %+v", got)
	}
	if len(f.WA.Rejected()) != 0 {
		t.Fatal("auto_reject is off by default")
	}

	f.EmitCall(&workerv1.Call{CallId: "A1", Kind: "offer", Caller: caller})
	sse.next(events.TypeCallReceived)
	f.EmitCall(&workerv1.Call{CallId: "A1", Kind: "accept", Caller: caller})
	if got := callData(t, sse.next(events.TypeCallAccepted)); got.CallID != "A1" {
		t.Fatalf("accepted = %+v", got)
	}
	f.EmitCall(&workerv1.Call{CallId: "A1", Kind: "terminate", Caller: caller})
	if got := callData(t, sse.next(events.TypeCallEnded)); got.Missed {
		t.Fatalf("answered call reported missed: %+v", got)
	}
}

func TestPresenceOptInAndSubscribe(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Presence")
	f := e.sp.Last(id)

	// Off by default: never marked available, subscriptions refused.
	if code, body := e.do("POST", "/v1/instances/"+id+"/presence/subscribe", tok, `{"to":"5511988887777"}`); code != 409 || errCode(body) != "presence_offline" {
		t.Fatalf("offline subscribe = %d %v", code, body)
	}
	if len(f.WA.PresenceStates()) != 0 {
		t.Fatalf("presence sent while off: %v", f.WA.PresenceStates())
	}
	e.do("PUT", "/v1/instances/"+id+"/settings", tok, `{"presence":{"online":true}}`)
	e.eventually("marked available", func() bool { return slices.Equal(f.WA.PresenceStates(), []bool{true}) })
	// Every reconnection marks it available again.
	f.Emit("reconnecting")
	e.waitState(id, "reconnecting")
	f.Emit("connected")
	e.eventually("available after reconnect", func() bool { return slices.Equal(f.WA.PresenceStates(), []bool{true, true}) })

	if code, body := e.do("POST", "/v1/instances/"+id+"/presence/subscribe", tok, `{"to":"1203630@g.us"}`); code != 400 {
		t.Fatalf("group subscribe = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/instances/"+id+"/presence/subscribe", tok, `{}`); code != 400 {
		t.Fatalf("missing to = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/instances/"+id+"/presence/subscribe", tok, `{"to":"+55 11 98888-7777"}`); code != 204 {
		t.Fatalf("subscribe = %d %v", code, body)
	}
	if subs := f.WA.Subscriptions(); len(subs) != 1 || subs[0] != caller {
		t.Fatalf("subscriptions = %v", subs)
	}
	limited := false
	for i := 0; i < 25 && !limited; i++ {
		code, body := e.do("POST", "/v1/instances/"+id+"/presence/subscribe", tok, fmt.Sprintf(`{"to":"55119888%05d"}`, i))
		limited = code == 429 && errCode(body) == "rate_limited"
	}
	if !limited {
		t.Fatal("presence subscriptions are not rate limited")
	}

	e.do("PUT", "/v1/instances/"+id+"/settings", tok, `{"presence":{"online":false}}`)
	e.eventually("marked unavailable", func() bool { return slices.Equal(f.WA.PresenceStates(), []bool{true, true, false}) })

	// Quick toggles always end in the latest setting.
	for i := 0; i < 5; i++ {
		e.do("PUT", "/v1/instances/"+id+"/settings", tok, `{"presence":{"online":true}}`)
		e.do("PUT", "/v1/instances/"+id+"/settings", tok, `{"presence":{"online":false}}`)
	}
	e.rt.Wait()
	if st := f.WA.PresenceStates(); st[len(st)-1] {
		t.Fatalf("final presence = %v", st)
	}
}

func TestChatPresenceIsOptInAndSurvivesBursts(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Typing")
	f := e.sp.Last(id)
	e.do("PUT", "/v1/instances/"+id+"/settings", tok, `{"presence":{"online":true}}`)
	e.eventually("online", func() bool { return len(f.WA.PresenceStates()) == 1 })
	rcv := newHookReceiver(t)
	if code, body := e.do("POST", "/v1/webhooks", tok, `{"url":"`+rcv.srv.URL+`","events":["*"]}`); code != 201 {
		t.Fatalf("webhook = %d %v", code, body)
	}
	all, _ := e.sse("/v1/instances/"+id+"/events", tok) // no types: chat.presence excluded
	typing, _ := e.sse("/v1/instances/"+id+"/events?types=chat.presence", tok)

	f.EmitChatPresence(&workerv1.ChatPresence{Chat: caller, Sender: caller, State: "composing"})
	f.EmitChatPresence(&workerv1.ChatPresence{Chat: caller, Sender: caller, State: "composing"}) // repeated: dropped
	f.EmitPresence(&workerv1.Presence{Jid: caller, Online: false, LastSeenMs: 1_800_000_000_000})
	var p struct{ Data events.ChatPresence }
	_ = json.Unmarshal([]byte(typing.next(events.TypeChatPresence).data), &p)
	if p.Data.State != "composing" || p.Data.Chat != caller {
		t.Fatalf("typing = %+v", p.Data)
	}
	_ = json.Unmarshal([]byte(typing.next(events.TypeChatPresence).data), &p)
	if p.Data.State != "offline" || p.Data.LastSeen == nil {
		t.Fatalf("presence = %+v", p.Data)
	}
	f.EmitMessage(&workerv1.Message{Id: "IN1", Chat: caller, Sender: caller, TimestampMs: 1, Type: "text", Text: "oi"})
	all.next(events.TypeMessageReceived)
	e.eventually("webhook got the message", func() bool { _, _, ok := rcv.find(events.TypeMessageReceived); return ok })
	if _, _, ok := rcv.find(events.TypeChatPresence); ok {
		t.Fatal(`a "*" webhook received chat.presence`)
	}
	select {
	case ev := <-all.evs:
		if ev.typ == events.TypeChatPresence {
			t.Fatal("chat.presence reached a stream that did not ask for it")
		}
	default:
	}

	// 1,000 typing events in about a second do not drop the stream.
	go func() {
		for i := 0; i < 1000; i++ {
			f.EmitChatPresence(&workerv1.ChatPresence{Chat: fmt.Sprintf("55119%08d@s.whatsapp.net", i), State: "composing"})
			time.Sleep(time.Millisecond)
		}
	}()
	for i := 0; i < 1000; i++ {
		typing.next(events.TypeChatPresence)
	}
}

func TestWebhookFilters(t *testing.T) {
	e := newEnv(t)
	rcv := newHookReceiver(t)
	id, tok := e.connected("Filters")

	for _, bad := range []string{`{"chat":"private"}`, `{"chats":["abc"]}`, `{"message_types":["call"]}`} {
		if code, body := e.do("POST", "/v1/webhooks", tok, `{"url":"`+rcv.srv.URL+`","events":["*"],"filters":`+bad+`}`); code != 400 {
			t.Fatalf("filters %s = %d %v", bad, code, body)
		}
	}
	code, wh := e.do("POST", "/v1/webhooks", tok, `{"url":"`+rcv.srv.URL+`","events":["message.*"],
		"filters":{"chat":"direct","message_types":["text"],"from_me":false,"exclude_chats":["+55 11 97777-0000"]}}`)
	fl, _ := wh["filters"].(map[string]any)
	if code != 201 || fl["chat"] != "direct" || fl["exclude_chats"].([]any)[0] != "5511977770000@s.whatsapp.net" {
		t.Fatalf("create = %d %v", code, wh)
	}
	f := e.sp.Last(id)
	emit := func(id, chat, typ string, fromMe bool) {
		f.EmitMessage(&workerv1.Message{Id: id, Chat: chat, Sender: caller, IsGroup: strings.HasSuffix(chat, "@g.us"),
			FromMe: fromMe, TimestampMs: 1, Type: typ, Text: id})
	}
	emit("GROUP", "1203630@g.us", "text", false)
	emit("IMAGE", caller, "image", false)
	emit("MINE", caller, "text", true)
	emit("BLOCKED", "5511977770000@s.whatsapp.net", "text", false)
	emit("OK", caller, "text", false)
	e.eventually("direct text delivered", func() bool { _, _, ok := rcv.find(events.TypeMessageReceived); return ok })
	time.Sleep(200 * time.Millisecond)
	rcv.mu.Lock()
	n := len(rcv.bodies)
	body := string(rcv.bodies[0])
	rcv.mu.Unlock()
	if n != 1 || !strings.Contains(body, `"text":"OK"`) {
		t.Fatalf("deliveries = %d, first = %s", n, body)
	}

	// {} clears the filters.
	code, wh = e.do("PATCH", "/v1/webhooks/"+wh["id"].(string), tok, `{"filters":{}}`)
	if code != 200 || wh["filters"] != nil {
		t.Fatalf("clear = %d %v", code, wh)
	}
}

func TestPlayedStatusAndButtonReplyEvents(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Played")
	f := e.sp.Last(id)
	sse, _ := e.sse("/v1/instances/"+id+"/events?types=message.*", tok)
	code, v := e.do("POST", "/v1/instances/"+id+"/messages?wait=true", tok, `{"to":"5511988887777","type":"text","text":"oi"}`)
	if code != 200 {
		t.Fatalf("send = %d %v", code, v)
	}
	wa := v["wa_message_id"].(string)
	f.EmitReceipt(&workerv1.Receipt{MessageIds: []string{wa}, Chat: caller, Type: "played"})
	for {
		var s struct{ Data events.MessageStatus }
		_ = json.Unmarshal([]byte(sse.next(events.TypeMessageStatus).data), &s)
		if s.Data.Status == "played" {
			break
		}
	}
	if _, m := e.do("GET", "/v1/instances/"+id+"/messages/"+v["id"].(string), tok, ""); m["status"] != "played" {
		t.Fatalf("message = %v", m)
	}
	f.EmitMessage(&workerv1.Message{Id: "BR1", Chat: caller, Sender: caller, TimestampMs: 1, Type: "button_reply", Text: "Sim",
		ButtonReply: &workerv1.ButtonReply{Kind: "list", Id: "row1", Title: "Sim"}})
	var m struct{ Data events.Message }
	_ = json.Unmarshal([]byte(sse.next(events.TypeMessageButton).data), &m)
	if m.Data.ButtonReply == nil || m.Data.ButtonReply.ID != "row1" || m.Data.Type != "button_reply" {
		t.Fatalf("button reply = %+v", m.Data)
	}
}
