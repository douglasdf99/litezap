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
		})
	}
}
