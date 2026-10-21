package mcp

import (
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/service"
)

func TestSocialTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("S")
			const ch = "120363000000000001@newsletter"
			wa := e.sp.Last(id).WA
			wa.SetChannel(&workerv1.Channel{Jid: ch, Name: "Notícias", InviteCode: "NEWS"}, false)
			wa.SetChannelPosts(ch, []*workerv1.ChannelPost{{Channel: ch, ServerId: 3, Message: &workerv1.Message{Id: "A", Type: "text", Text: "manchete"}}})
			cs := e.client(m, tok)

			var v service.ChannelView
			ok(t, cs, "get_channel", map[string]any{"invite": "https://whatsapp.com/channel/NEWS"}, &v)
			if v.JID != ch {
				t.Fatalf("channel = %+v", v)
			}
			var f followOutput
			ok(t, cs, "follow_channel", map[string]any{"channel": ch, "follow": true}, &f)
			var list channelsOutput
			ok(t, cs, "list_channels", nil, &list)
			if len(list.Channels) != 1 || list.Channels[0].Role != "subscriber" {
				t.Fatalf("channels = %+v", list)
			}
			var posts channelPostsOutput
			ok(t, cs, "get_channel_messages", map[string]any{"channel": ch}, &posts)
			if len(posts.Messages) != 1 || posts.Messages[0].Text != "manchete" {
				t.Fatalf("posts = %+v", posts)
			}
			var st service.MessagePage
			ok(t, cs, "list_status_updates", nil, &st)
			fails(t, cs, "list_labels", nil, "business")
			fails(t, cs, "get_business_profile", nil, "business")
			fails(t, cs, "update_business_profile", map[string]any{"description": "x"}, "business")

			wa.SetBusiness(true, &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net", Email: "oi@loja.x"})
			var bp service.BusinessAccountView
			ok(t, cs, "update_business_profile", map[string]any{"description": "Pizza", "websites": []string{},
				"hours": map[string]any{"timezone": "America/Sao_Paulo", "days": map[string]any{"mon": map[string]any{"mode": "24h"}}}}, &bp)
			if bp.Description != "Pizza" || bp.Email != "oi@loja.x" || bp.BusinessHours == nil || bp.BusinessHours.Days["mon"].Mode != "24h" {
				t.Fatalf("updated = %+v", bp)
			}
			if sets := wa.BusinessSets(); len(sets) != 1 || !sets[0].SetWebsites || sets[0].Email != nil {
				t.Fatalf("worker requests = %v", sets)
			}
			fails(t, cs, "update_business_profile", map[string]any{"latitude": 1.5}, "latitude")
		})
	}
}
