package mcp

import (
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/workertest"
)

func TestAccountTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Conta")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			contact := "5511988887777@s.whatsapp.net"

			fails(t, cs, "get_profile_picture", map[string]any{"contact": contact}, "not_found")
			f.WA.SetPicture(contact, workertest.FakePicture{ID: "P1", URL: "https://pps.whatsapp.net/p.jpg"})
			f.WA.SetUserInfo(contact, &workerv1.GetUserInfoResponse{Jid: contact, About: "Ocupado"})
			var about service.ContactProfileView
			ok(t, cs, "get_contact_about", map[string]any{"contact": "5511988887777"}, &about)
			if about.About != "Ocupado" {
				t.Fatalf("about = %+v", about)
			}

			var bl blockedOutput
			ok(t, cs, "block_contact", map[string]any{"contact": contact}, &bl)
			if len(bl.Blocked) != 1 || !f.WA.Blocked(contact) {
				t.Fatalf("blocked = %+v", bl)
			}
			ok(t, cs, "list_blocked", nil, &bl)
			ok(t, cs, "unblock_contact", map[string]any{"contact": contact}, &bl)
			if len(bl.Blocked) != 0 {
				t.Fatalf("unblocked = %+v", bl)
			}

			f.EmitMessage(&workerv1.Message{Id: "W1", Chat: contact, Sender: contact, Type: "text", Text: "oi", TimestampMs: time.Now().UnixMilli()})
			eventually(t, "chat logged", func() bool {
				p, err := e.msg.Chats(context.Background(), id, 10, "")
				return err == nil && len(p.Chats) == 1
			})
			var chat service.ChatView
			ok(t, cs, "update_chat", map[string]any{"chat": contact, "pinned": true, "muted": true, "mute_hours": 8}, &chat)
			if !chat.Pinned || !chat.Muted || chat.MutedUntil == nil {
				t.Fatalf("chat = %+v", chat)
			}
			fails(t, cs, "update_chat", map[string]any{"chat": contact}, "nothing to change")

			ok(t, cs, "update_chat", map[string]any{"chat": contact, "disappearing": "24h"}, &chat)
			if chat.Disappearing != "24h" {
				t.Fatalf("disappearing = %+v", chat)
			}
			fails(t, cs, "update_chat", map[string]any{"chat": contact, "disappearing": "1h"}, "")

			var cleared service.ClearResult
			ok(t, cs, "clear_chat", map[string]any{"chat": contact, "keep_starred": true}, &cleared)
			acts := f.WA.ChatActions()
			if last := acts[len(acts)-1]; cleared.DeletedMessages != 1 || cleared.Chat.JID != contact ||
				last.Action != "clear" || !last.KeepStarred || last.Message.GetId() != "W1" {
				t.Fatalf("clear = %+v, action = %+v", cleared, last)
			}
			fails(t, cs, "clear_chat", map[string]any{"chat": "5511900001111"}, "not_found")
		})
	}
}
