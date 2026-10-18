package mcp

import (
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/service"
)

func TestGroupTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("G")
			idB, _ := e.connected("Outra")
			const grp = "120363000000000001@g.us"
			e.sp.Last(id).WA.SetGroup(&workerv1.GroupInfo{Jid: grp, Name: "Equipe", ParticipantCount: 2,
				Participants: []*workerv1.GroupParticipant{{Jid: "5511999999999@s.whatsapp.net", Admin: true}, {Jid: "5511988887777@s.whatsapp.net"}}})
			e.sp.Last(id).WA.SetUnregistered("5511900000000")
			cs := e.client(m, tok)

			var list listGroupsOutput
			ok(t, cs, "list_groups", nil, &list)
			if len(list.Groups) != 1 || list.Groups[0].Name != "Equipe" {
				t.Fatalf("list = %+v", list)
			}
			var g service.GroupView
			ok(t, cs, "get_group", map[string]any{"group": grp}, &g)
			if len(g.Participants) != 2 || !g.Participants[0].Admin {
				t.Fatalf("group = %+v", g)
			}
			var res participantsOutput
			ok(t, cs, "manage_group_participants", map[string]any{"group": grp, "action": "add",
				"participants": []string{"5511900000000"}}, &res)
			if len(res.Results) != 1 || res.Results[0].Status != "not_on_whatsapp" {
				t.Fatalf("add = %+v", res)
			}
			fails(t, cs, "manage_group_participants", map[string]any{"group": grp, "action": "add",
				"participants": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}}, "too many participants")
			var inv inviteOutput
			ok(t, cs, "get_group_invite_link", map[string]any{"group": grp}, &inv)
			if inv.Link == "" {
				t.Fatalf("invite = %+v", inv)
			}
			// An instance token never reaches another instance's groups.
			fails(t, cs, "list_groups", map[string]any{"instance_id": idB}, "forbidden")
		})
	}
}
