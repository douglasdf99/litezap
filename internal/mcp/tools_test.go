package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolList(t *testing.T) {
	e := newEnv(t, nil)
	cs := e.client(modeLocal, admin)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]*sdk.Tool{}
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		byName[tl.Name] = tl
		if tl.Description == "" || tl.Annotations == nil {
			t.Errorf("%s lacks a description or annotations", tl.Name)
		}
	}
	want := []string{"block_contact", "check_numbers", "clear_chat", "create_group", "delete_message", "edit_message", "follow_channel",
		"get_attendant", "get_attendant_agent", "get_business_profile", "get_catalog", "get_channel", "get_channel_messages", "get_contact",
		"get_contact_about", "get_group", "get_group_invite_link", "get_instance_status", "get_message_status", "get_messages", "get_order",
		"get_product", "get_profile_picture", "get_qr", "label_chat", "list_blocked", "list_channels", "list_chats", "list_contacts", "list_groups",
		"list_instances", "list_labels", "list_status_updates",
		"manage_group_participants", "mark_read", "send_contact", "send_location", "send_media", "send_message", "send_product", "send_reaction",
		"unblock_contact", "update_business_profile", "update_chat", "update_group", "wait_for_events"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}
	b, _ := json.Marshal(byName["send_message"].InputSchema)
	for _, frag := range []string{`"required":["to","text"]`, `"maximum":55`, `"default":true`} {
		if !strings.Contains(string(b), frag) {
			t.Errorf("send_message schema %s lacks %s", b, frag)
		}
	}
	b, _ = json.Marshal(byName["send_media"].InputSchema)
	if !strings.Contains(string(b), `"enum":["image","video","audio","document","sticker","gif","ptv"]`) {
		t.Errorf("send_media schema %s lacks the type enum", b)
	}
	b, _ = json.Marshal(byName["check_numbers"].InputSchema)
	if !strings.Contains(string(b), `"maxItems":10`) {
		t.Errorf("check_numbers schema %s lacks maxItems", b)
	}
	if byName["send_message"].Annotations.ReadOnlyHint || !byName["list_chats"].Annotations.ReadOnlyHint {
		t.Error("read-only hints are wrong")
	}
	for _, name := range []string{"edit_message", "delete_message", "block_contact"} {
		if a := byName[name].Annotations; a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Errorf("%s must be marked destructive", name)
		}
	}
}

func TestSendMessage(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Send")
			cs := e.client(m, tok)
			var out sendOutput
			ok(t, cs, "send_message", map[string]any{"to": "5511988887777", "text": "oi", "client_message_id": "c1"}, &out)
			if !out.Sent || out.Message.Status != "sent" || !strings.Contains(out.Note, "sent") {
				t.Fatalf("send = %+v", out)
			}
			// Same idempotency key: the same message, not a second send.
			var again sendOutput
			ok(t, cs, "send_message", map[string]any{"to": "5511988887777", "text": "oi", "client_message_id": "c1"}, &again)
			if again.Message.ID != out.Message.ID {
				t.Fatalf("idempotent send returned %s, want %s", again.Message.ID, out.Message.ID)
			}
			if sent := e.sp.Last(id).WA.Sent(); len(sent) != 1 || sent[0].Text != "oi" {
				t.Fatalf("worker sends = %v", sent)
			}
			var st service.MessageView
			ok(t, cs, "get_message_status", map[string]any{"message_id": out.Message.ID}, &st)
			if st.Status != "sent" {
				t.Fatalf("status = %+v", st)
			}
			fails(t, cs, "send_message", map[string]any{"to": "123", "text": "x"}, "validation_failed")
			fails(t, cs, "send_message", map[string]any{"to": "5511988887777"}, "text")
			fails(t, cs, "send_message", map[string]any{"to": "5511988887777", "text": "x", "timeout_seconds": 500}, "")
		})
	}
}

// A message the queue cannot send yet is reported as queued, with a note, not an error.
func TestSendMessageQueuedIsNotAnError(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.create("Pending") // qr_pending: the queue holds the message
			cs := e.client(m, tok)
			var out sendOutput
			ok(t, cs, "send_message", map[string]any{"to": "5511988887777", "text": "depois", "timeout_seconds": 1}, &out)
			if out.Sent || out.Message.Status != "queued" {
				t.Fatalf("send = %+v", out)
			}
			for _, frag := range []string{"queued", "send window", "qr_pending", "Do not send it again", "get_message_status"} {
				if !strings.Contains(out.Note, frag) {
					t.Errorf("note %q lacks %q", out.Note, frag)
				}
			}
			ok(t, cs, "send_message", map[string]any{"to": "5511988887777", "text": "sem esperar", "wait": false}, &out)
			if out.Sent {
				t.Fatalf("wait=false = %+v", out)
			}
			if n := len(e.sp.Last(id).WA.Sent()); n != 0 {
				t.Fatalf("worker sent %d messages before connecting", n)
			}
		})
	}
}

// The tools never bypass the pacing: with a send window closed all day, a send waits.
func TestSendRespectsTheSendWindow(t *testing.T) {
	e := newEnv(t, func(s *settings.Settings) {
		s.UnsafePacing = false
		now := time.Now().UTC()
		// A one-minute window two hours away is closed now.
		start := now.Add(2 * time.Hour)
		s.Timezone = "UTC"
		s.SendWindow = settings.Window{Start: start.Format("15:04"), End: start.Add(time.Minute).Format("15:04")}
	})
	id, tok := e.connected("Window")
	cs := e.client(modeLocal, tok)
	var out sendOutput
	ok(t, cs, "send_message", map[string]any{"to": "5511988887777", "text": "fora da janela", "timeout_seconds": 1}, &out)
	if out.Sent || out.Message.Status != "queued" {
		t.Fatalf("send outside the window = %+v", out)
	}
	if n := len(e.sp.Last(id).WA.Sent()); n != 0 {
		t.Fatalf("sent %d messages outside the send window", n)
	}
}

func TestSendMedia(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))
	files := http.NewServeMux()
	files.HandleFunc("/cat.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	fsrv := newHTTPServer(t, files)
	for _, m := range []mode{modeLocal, modeREST} {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Media")
			cs := e.client(m, tok)
			var out sendOutput
			ok(t, cs, "send_media", map[string]any{"to": "5511988887777", "type": "image", "url": fsrv + "/cat.png", "caption": "gato"}, &out)
			if !out.Sent {
				t.Fatalf("send_media = %+v", out)
			}
			sent := e.sp.Last(id).WA.Sent()
			if len(sent) != 1 || !bytes.Equal(e.sp.Last(id).WA.SentMedia(sent[0].MessageId), png) {
				t.Fatalf("worker sends = %v", sent)
			}
			fails(t, cs, "send_media", map[string]any{"to": "5511988887777", "type": "sticker", "url": fsrv + "/cat.png"}, "")
			fails(t, cs, "send_media", map[string]any{"to": "5511988887777", "type": "image", "url": fsrv + "/missing.png"}, "media_fetch_failed")
		})
	}
}

func TestInstanceScope(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			idA, tokA := e.create("A")
			cs := e.client(m, admin)
			// Admin with a single instance: instance_id defaults to it.
			var st instanceStatusOutput
			ok(t, cs, "get_instance_status", nil, &st)
			if st.Instance.ID != idA || !strings.Contains(st.Hint, "get_qr") {
				t.Fatalf("status = %+v", st)
			}
			idB, _ := e.create("B")
			fails(t, cs, "get_instance_status", nil, "instance_id is required")
			var list listInstancesOutput
			ok(t, cs, "list_instances", nil, &list)
			if len(list.Instances) != 2 {
				t.Fatalf("admin list = %+v", list)
			}
			fails(t, cs, "get_instance_status", map[string]any{"instance_id": "inst_nope"}, "instance_not_found")

			// An instance token sees and operates only its own instance.
			ci := e.client(m, tokA)
			ok(t, ci, "list_instances", nil, &list)
			if len(list.Instances) != 1 || list.Instances[0].ID != idA {
				t.Fatalf("instance list = %+v", list)
			}
			ok(t, ci, "get_instance_status", nil, &st)
			if st.Instance.ID != idA {
				t.Fatalf("instance status = %+v", st)
			}
			for _, c := range []struct {
				tool string
				args map[string]any
			}{
				{"get_instance_status", nil},
				{"get_qr", nil},
				{"send_message", map[string]any{"to": "5511988887777", "text": "x"}},
				{"list_chats", nil},
				{"get_messages", map[string]any{"chat": "5511988887777"}},
				{"mark_read", map[string]any{"chat": "5511988887777"}},
				{"list_contacts", nil},
				{"check_numbers", map[string]any{"phones": []string{"5511988887777"}}},
				{"wait_for_events", map[string]any{"timeout_seconds": 1}},
			} {
				args := map[string]any{"instance_id": idB}
				for k, v := range c.args {
					args[k] = v
				}
				fails(t, ci, c.tool, args, "forbidden")
			}
		})
	}
}

// Local enforces the scope even if a tool forgot to (defense in depth).
func TestLocalEnforcesScope(t *testing.T) {
	e := newEnv(t, nil)
	idA, _ := e.create("A")
	idB, _ := e.create("B")
	l := &Local{Instances: e.svc, Messaging: e.msg, Events: e.bus, Caller: service.Scope{InstanceID: idA}}
	ctx := context.Background()
	if _, err := l.ListInstances(ctx); err == nil {
		t.Error("ListInstances with an instance scope succeeded")
	}
	if _, err := l.GetInstance(ctx, idB); err == nil {
		t.Error("GetInstance of another instance succeeded")
	}
	if _, err := l.Send(ctx, idB, service.SendInput{To: "5511988887777", Type: "text", Text: "x"}, 0); err == nil {
		t.Error("Send on another instance succeeded")
	}
	if _, err := l.GetInstance(ctx, idA); err != nil {
		t.Errorf("GetInstance of its own instance: %v", err)
	}
}

func TestReadTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Inbox")
			f := e.sp.Last(id)
			f.EmitMessage(&workerv1.Message{Id: "IN1", Chat: "5511988887777@s.whatsapp.net",
				Sender: "5511988887777@s.whatsapp.net", PushName: "João", TimestampMs: 1_800_000_000_000, Type: "text", Text: "olá"})
			f.WA.SetContacts(&workerv1.Contact{Jid: "5511988887777@s.whatsapp.net", Name: "João Silva"},
				&workerv1.Contact{Jid: "5511977776666@s.whatsapp.net", Name: "Maria"})
			f.WA.SetUnregistered("5511900000000")
			cs := e.client(m, tok)

			var chats service.ChatPage
			eventually(t, "the chat", func() bool {
				ok(t, cs, "list_chats", nil, &chats)
				return len(chats.Chats) == 1
			})
			if chats.Chats[0].UnreadCount != 1 {
				t.Fatalf("chats = %+v", chats)
			}
			var msgs service.MessagePage
			ok(t, cs, "get_messages", map[string]any{"chat": chats.Chats[0].JID, "limit": 1}, &msgs)
			if len(msgs.Messages) != 1 || msgs.Messages[0].Text != "olá" || msgs.NextCursor == "" {
				t.Fatalf("messages = %+v", msgs)
			}
			ok(t, cs, "get_messages", map[string]any{"chat": "5511988887777", "cursor": msgs.NextCursor}, &msgs)
			if len(msgs.Messages) != 0 {
				t.Fatalf("second page = %+v", msgs)
			}
			ok(t, cs, "mark_read", map[string]any{"chat": "+55 11 98888-7777"}, nil)
			if reads := f.WA.Reads(); len(reads) != 1 || reads[0].MessageIds[0] != "IN1" {
				t.Fatalf("reads = %v", reads)
			}
			fails(t, cs, "mark_read", map[string]any{"chat": "5511911112222"}, "not_found")

			var contacts listContactsOutput
			ok(t, cs, "list_contacts", map[string]any{"query": "joão"}, &contacts)
			if contacts.Matched != 1 || contacts.Contacts[0].Name != "João Silva" || contacts.Truncated {
				t.Fatalf("contacts = %+v", contacts)
			}
			ok(t, cs, "list_contacts", map[string]any{"limit": 1}, &contacts)
			if contacts.Matched != 2 || len(contacts.Contacts) != 1 || !contacts.Truncated {
				t.Fatalf("limited contacts = %+v", contacts)
			}
			var c service.ContactView
			ok(t, cs, "get_contact", map[string]any{"contact": "5511977776666"}, &c)
			if c.Name != "Maria" {
				t.Fatalf("contact = %+v", c)
			}
			fails(t, cs, "get_contact", map[string]any{"contact": "5511955554444"}, "not_found")

			var nums checkNumbersOutput
			ok(t, cs, "check_numbers", map[string]any{"phones": []string{"5511900000000"}}, &nums)
			if len(nums.Results) != 1 || nums.Results[0].Exists {
				t.Fatalf("check = %+v", nums)
			}
			fails(t, cs, "check_numbers", map[string]any{"phones": []string{}}, "")
			eleven := make([]string, 11)
			for i := range eleven {
				eleven[i] = fmt.Sprintf("55119000000%02d", i)
			}
			fails(t, cs, "check_numbers", map[string]any{"phones": eleven}, "")
		})
	}
}

func TestDisconnectedErrorsCarryHints(t *testing.T) {
	e := newEnv(t, nil)
	id, tok := e.create("Off")
	if err := e.svc.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	cs := e.client(modeREST, tok)
	fails(t, cs, "list_contacts", nil, "instance_not_connected")
	fails(t, cs, "list_contacts", nil, "get_qr")
	fails(t, cs, "list_contacts", nil, "(retryable)")
}

func TestGetQR(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.create("QR")
			cs := e.client(m, tok)
			res := call(t, cs, "get_qr", nil)
			if res.IsError || len(res.Content) != 2 {
				t.Fatalf("get_qr = %v %s", res.IsError, text(res))
			}
			img, isImg := res.Content[0].(*sdk.ImageContent)
			if !isImg || img.MIMEType != "image/png" || !bytes.HasPrefix(img.Data, []byte("\x89PNG")) {
				t.Fatalf("get_qr content = %#v", res.Content[0])
			}
			if !strings.Contains(text(res), "Linked devices") {
				t.Fatalf("get_qr text = %s", text(res))
			}
			e.sp.Last(id).Emit("connected")
			e.waitState(id, "connected")
			fails(t, cs, "get_qr", nil, "instance_not_pairing")
		})
	}
}

func TestWaitForEvents(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Events")
			cs := e.client(m, tok)
			var out waitEventsOutput
			ok(t, cs, "wait_for_events", map[string]any{"timeout_seconds": 1, "types": []string{"message.received"}}, &out)
			if !out.TimedOut || len(out.Events) != 0 {
				t.Fatalf("idle wait = %+v", out)
			}
			fails(t, cs, "wait_for_events", map[string]any{"types": []string{"nope"}}, "unknown event type")

			// Emit messages until the waiting call returns (it may subscribe after the first).
			done := make(chan waitEventsOutput, 1)
			go func() {
				var o waitEventsOutput
				res := call(t, cs, "wait_for_events", map[string]any{"timeout_seconds": 10, "types": []string{"message.*"}, "max_events": 1})
				b, _ := json.Marshal(res.StructuredContent)
				_ = json.Unmarshal(b, &o)
				done <- o
			}()
			var n atomic.Int64
			giveUp := time.After(15 * time.Second)
			for {
				select {
				case o := <-done:
					if o.TimedOut || len(o.Events) != 1 || o.Events[0]["type"] != "message.received" || o.Events[0]["instance_id"] != id {
						t.Fatalf("wait = %+v", o)
					}
					return
				case <-time.After(100 * time.Millisecond):
					i := n.Add(1)
					e.sp.Last(id).EmitMessage(&workerv1.Message{Id: fmt.Sprintf("EV%d", i), Chat: "5511988887777@s.whatsapp.net",
						Sender: "5511988887777@s.whatsapp.net", TimestampMs: 1_800_000_000_000 + i, Type: "text", Text: "resposta"})
				case <-giveUp:
					t.Fatal("wait_for_events never returned")
				}
			}
		})
	}
}

func TestHTTPEndpointAuth(t *testing.T) {
	e := newEnv(t, nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	for _, tok := range []string{"", "lzi_wrong", "nope"} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: %d %s", tok, resp.StatusCode, b)
		}
	}
	// A valid token reaches the MCP server; a client works end to end.
	_, tok := e.create("H")
	var list listInstancesOutput
	ok(t, e.client(modeHTTP, tok), "list_instances", nil, &list)
	if len(list.Instances) != 1 {
		t.Fatalf("list = %+v", list)
	}
}

func TestRESTUnreachable(t *testing.T) {
	r, err := NewREST("http://127.0.0.1:1", "lzi_x", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := sdk.NewInMemoryTransports()
	ss, err := NewServer(r, Options{}).Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "v"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	fails(t, cs, "list_instances", nil, "server_unreachable")
	if strings.Contains(text(call(t, cs, "list_instances", nil)), "lzi_x") {
		t.Fatal("the token leaked into an error")
	}
}

func TestNewRESTValidation(t *testing.T) {
	for _, c := range []struct{ url, tok string }{
		{"", "t"}, {"ftp://x", "t"}, {"http://", "t"}, {"http://u:p@h", "t"}, {"http://h", ""}, {"http://h", "  "},
	} {
		if _, err := NewREST(c.url, c.tok, nil); err == nil {
			t.Errorf("NewREST(%q, %q) succeeded", c.url, c.tok)
		}
	}
	if _, err := NewREST("http://127.0.0.1:8787/", "lza_x", nil); err != nil {
		t.Error(err)
	}
}

func newHTTPServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestMessageActionTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Actions")
			cs := e.client(m, tok)
			peer := "5511988887777@s.whatsapp.net"
			e.sp.Last(id).EmitMessage(&workerv1.Message{Id: "IN1", Chat: peer, Sender: peer, TimestampMs: time.Now().UnixMilli(), Type: "text", Text: "oi?"})
			var page service.MessagePage
			deadline := time.Now().Add(3 * time.Second)
			for len(page.Messages) == 0 && time.Now().Before(deadline) {
				ok(t, cs, "get_messages", map[string]any{"chat": peer}, &page)
				time.Sleep(10 * time.Millisecond)
			}
			if len(page.Messages) == 0 {
				t.Fatal("received message not logged")
			}
			var out sendOutput
			ok(t, cs, "send_message", map[string]any{"to": peer, "text": "@5511977776666 sim", "reply_to": page.Messages[0].ID,
				"mentions": []string{"5511977776666"}}, &out)
			if !out.Sent || out.Message.ReplyTo != "IN1" {
				t.Fatalf("reply = %+v", out)
			}
			sent := out.Message.ID
			ok(t, cs, "send_reaction", map[string]any{"to": peer, "message_id": "IN1", "emoji": "👍"}, &out)
			ok(t, cs, "send_location", map[string]any{"to": peer, "latitude": -23.5, "longitude": -46.6, "name": "SP"}, &out)
			ok(t, cs, "send_contact", map[string]any{"to": peer, "contacts": []map[string]any{{"name": "Ana", "phone": "5511977776666"}}}, &out)
			ok(t, cs, "edit_message", map[string]any{"message_id": sent, "text": "sim!"}, &out)
			ok(t, cs, "delete_message", map[string]any{"message_id": sent}, &out)
			var types []string
			for _, r := range e.sp.Last(id).WA.Sent() {
				types = append(types, r.Type)
			}
			if fmt.Sprint(types) != "[text reaction location contact edit revoke]" {
				t.Fatalf("worker requests = %v", types)
			}
			fails(t, cs, "edit_message", map[string]any{"message_id": "IN1", "text": "x"}, "validation_failed")
			fails(t, cs, "delete_message", map[string]any{"message_id": ""}, "validation_failed")
			fails(t, cs, "send_location", map[string]any{"to": peer, "latitude": 100, "longitude": 0}, "validation_failed")
		})
	}
}
