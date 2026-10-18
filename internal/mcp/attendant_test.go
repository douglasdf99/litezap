package mcp

import (
	"context"
	"testing"

	"github.com/douglasdf99/litezap/internal/attendant"
)

func TestAttendantTools(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.create("Atendente")
			key := "sk-ant-never-shown-123456"
			if _, err := e.att.PutSettings(context.Background(), id, attendant.SettingsPatch{APIKey: &key}); err != nil {
				t.Fatal(err)
			}
			a, err := e.att.CreateAgent(context.Background(), id, attendant.Profile{Name: "Bia", Mode: "pedido",
				Knowledge: []attendant.KnowledgeEntry{{Kind: "faq", Title: "Entregam?", Content: "Sim"}}})
			if err != nil {
				t.Fatal(err)
			}
			cs := e.client(m, tok)
			var out attendantOutput
			ok(t, cs, "get_attendant", nil, &out)
			if out.Provider != "anthropic" || !out.Ready || len(out.Agents) != 1 || out.Agents[0].ID != a.ID ||
				!out.Agents[0].Active || out.Agents[0].Knowledge != 1 {
				t.Fatalf("get_attendant = %+v", out)
			}
			var ag attendant.AgentView
			ok(t, cs, "get_attendant_agent", map[string]any{"agent_id": a.ID}, &ag)
			if ag.Name != "Bia" || ag.Mode != "pedido" || len(ag.Knowledge) != 1 {
				t.Fatalf("agent = %+v", ag)
			}
			fails(t, cs, "get_attendant_agent", map[string]any{"agent_id": "agt_nope"}, "not_found")
			// Another instance's token never sees this instance's attendant.
			_, tok2 := e.create("Outra")
			fails(t, e.client(m, tok2), "get_attendant", map[string]any{"instance_id": id}, "forbidden")
		})
	}
}
