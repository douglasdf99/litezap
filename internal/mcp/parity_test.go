package mcp

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/service"
)

// MSG-24: send_event, update_event and respond_event.
func TestEventTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Eventos")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			contact := "5511999990000@s.whatsapp.net"
			f.EmitMessage(&workerv1.Message{Id: "W1", Chat: contact, Sender: contact, Type: "text", Text: "oi", TimestampMs: time.Now().UnixMilli()})
			start := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
			eventually(t, "chat logged", func() bool {
				p, err := e.msg.Chats(context.Background(), id, 10, "")
				return err == nil && len(p.Chats) == 1
			})
			var out sendOutput
			ok(t, cs, "send_event", map[string]any{"to": contact, "name": "Visita", "start_time": start,
				"location": map[string]any{"address": "Rua Exemplo, 1"}}, &out)
			if !out.Sent || out.Message.Type != "event" || out.Message.Event == nil || out.Message.Event.Location.Address != "Rua Exemplo, 1" {
				t.Fatalf("send_event = %+v", out)
			}
			ok(t, cs, "update_event", map[string]any{"message_id": out.Message.ID, "canceled": true}, &out)
			sent := f.WA.Sent()
			if !out.Sent || sent[len(sent)-1].Type != "edit" || !sent[len(sent)-1].Event.GetCanceled() {
				t.Fatalf("update_event = %+v / %+v", out, sent[len(sent)-1])
			}
			f.EmitMessage(&workerv1.Message{Id: "EV2", Chat: contact, Sender: contact, Type: "event", Text: "Festa",
				TimestampMs: time.Now().UnixMilli(), Event: &workerv1.EventInfo{Name: "Festa", StartTime: time.Now().Add(time.Hour).Unix()}})
			var evID string
			eventually(t, "event logged", func() bool {
				msg, err := e.msg.Store.MessageByWAID(context.Background(), id, contact, "EV2")
				evID = msg.ID
				return err == nil
			})
			ok(t, cs, "respond_event", map[string]any{"message_id": evID, "response": "not_going"}, &out)
			if sent = f.WA.Sent(); !out.Sent || sent[len(sent)-1].EventResponse != "not_going" {
				t.Fatalf("respond_event = %+v", out)
			}
			fails(t, cs, "respond_event", map[string]any{"message_id": evID, "response": "going", "extra_guests": 2}, "extra_guests")
		})
	}
}

// STA-4: reply_status answers a contact's status privately, quoting it.
func TestReplyStatusTool(t *testing.T) {
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 \x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(webp)
	}))
	defer srv.Close()
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Status")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			author := "5511999990000@s.whatsapp.net"
			f.EmitStatus(&workerv1.Message{Id: "ST1", Chat: "status@broadcast", Sender: author, Type: "text", Text: "bom dia",
				TimestampMs: time.Now().UnixMilli()})
			var stID string
			eventually(t, "status logged", func() bool {
				msg, err := e.msg.Store.MessageByWAID(context.Background(), id, "status@broadcast", "ST1")
				stID = msg.ID
				return err == nil
			})
			var out sendOutput
			ok(t, cs, "reply_status", map[string]any{"status_id": stID, "text": "Bom dia!"}, &out)
			sent := f.WA.Sent()
			if last := sent[len(sent)-1]; !out.Sent || last.To != author || last.Quote.GetRemoteJid() != "status@broadcast" || last.Text != "Bom dia!" {
				t.Fatalf("text reply = %+v / %+v", out, last)
			}
			ok(t, cs, "reply_status", map[string]any{"status_id": stID, "type": "sticker", "media_url": srv.URL + "/s.webp"}, &out)
			if sent = f.WA.Sent(); !out.Sent || sent[len(sent)-1].Type != "sticker" || sent[len(sent)-1].Quote.GetMessageId() != "ST1" {
				t.Fatalf("sticker reply = %+v", out)
			}
			fails(t, cs, "reply_status", map[string]any{"status_id": stID, "type": "gif"}, "media")
		})
	}
}

// CON-2: save_contact and remove_contact.
func TestAddressBookTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Agenda")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			var saved service.SavedContact
			ok(t, cs, "save_contact", map[string]any{"phone": "5511999990000", "first_name": "Ana", "last_name": "Teste"}, &saved)
			if saved.JID != "5511999990000@s.whatsapp.net" || saved.FullName != "Ana Teste" || !saved.SaveOnPhone {
				t.Fatalf("saved = %+v", saved)
			}
			var out okContactOutput
			ok(t, cs, "remove_contact", map[string]any{"contact": "5511999990000"}, &out)
			if acts := f.WA.ContactActions(); !out.OK || len(acts) != 2 || acts[1].Action != "remove" {
				t.Fatalf("remove = %+v %+v", out, acts)
			}
			fails(t, cs, "save_contact", map[string]any{"phone": "5511999990000", "first_name": " "}, "first_name is required")
		})
	}
}

// CHT-11: search_messages reads the instance's log (local and REST backends).
func TestSearchMessagesTool(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Busca")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			contact := "5511999990000@s.whatsapp.net"
			f.EmitMessage(&workerv1.Message{Id: "W1", Chat: contact, Sender: contact, Type: "text", Text: "Segue o orçamento",
				TimestampMs: time.Now().UnixMilli()})
			eventually(t, "message logged", func() bool {
				p, err := e.msg.SearchMessages(context.Background(), id, service.SearchParams{Q: "orcamento"})
				return err == nil && len(p.Messages) == 1
			})
			var page service.SearchPage
			ok(t, cs, "search_messages", map[string]any{"query": "orçamento", "chat": "5511999990000"}, &page)
			if len(page.Messages) != 1 || page.Messages[0].WAMessageID != "W1" || page.Messages[0].Snippet == "" || !page.IndexComplete {
				t.Fatalf("page = %+v", page)
			}
			ok(t, cs, "search_messages", map[string]any{"query": "orçamento", "from": "me"}, &page)
			if len(page.Messages) != 0 {
				t.Fatalf("from me = %+v", page)
			}
			fails(t, cs, "search_messages", map[string]any{"query": "  "}, "q is required")
		})
	}
}

// MSG-23: vote_poll votes; get_messages shows the poll's votes and results.
func TestPollVoteTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Enquetes")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			contact := "5500000000003@s.whatsapp.net"
			f.EmitMessage(&workerv1.Message{Id: "PL1", Chat: contact, Sender: contact, Type: "poll", Text: "Hora?", TimestampMs: time.Now().UnixMilli(),
				Poll: &workerv1.Poll{Name: "Hora?", Options: []string{"9h", "10h"}, Selectable: 1}})
			var pollID string
			eventually(t, "poll logged", func() bool {
				msg, err := e.msg.Store.MessageByWAID(context.Background(), id, contact, "PL1")
				pollID = msg.ID
				return err == nil
			})
			h := sha256.Sum256([]byte("9h"))
			f.EmitMessage(&workerv1.Message{Id: "V1", Chat: contact, Sender: contact, Type: "poll_vote", TargetId: "PL1",
				PollHashes: [][]byte{h[:]}, TimestampMs: time.Now().UnixMilli()})
			var out sendOutput
			ok(t, cs, "vote_poll", map[string]any{"message_id": pollID, "options": []string{"10h"}}, &out)
			if sent := f.WA.Sent(); !out.Sent || sent[len(sent)-1].Type != "poll_vote" || sent[len(sent)-1].Poll.GetOptions()[0] != "10h" {
				t.Fatalf("vote_poll = %+v", out)
			}
			fails(t, cs, "vote_poll", map[string]any{"message_id": pollID, "options": []string{"9h", "10h"}}, "at most 1")
			var page service.MessagePage
			eventually(t, "votes on the poll", func() bool {
				ok(t, cs, "get_messages", map[string]any{"chat": contact}, &page)
				for _, msg := range page.Messages {
					if msg.Poll != nil && msg.Poll.Results["9h"] == 1 && msg.Poll.Results["10h"] == 1 && len(msg.Poll.Votes) == 2 {
						return true
					}
				}
				return false
			})
		})
	}
}
