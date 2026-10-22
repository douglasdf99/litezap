package agents

import (
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/service"
)

// The menu editing tools are for the user's own assistant: they are flagged in the MCP
// table, left out of the agents' table, and no permission group can grant them.
func TestAgentsNeverGetMenuWriteTools(t *testing.T) {
	write := []string{"create_menu_category", "update_menu_category", "delete_menu_category", "create_menu_item", "update_menu_item",
		"delete_menu_item", "set_menu_item_available", "add_menu_option_group", "update_menu_option_group", "delete_menu_option_group",
		"set_menu_item_image_url", "import_menu_rows", "reorder_menu"}
	b := &mcp.Local{Caller: service.Scope{InstanceID: "inst_a"}}
	flagged := map[string]bool{}
	for _, d := range mcp.Catalog(b, nil) {
		if d.AssistantOnly {
			flagged[d.Name] = true
		}
	}
	for _, name := range write {
		if !flagged[name] {
			t.Fatalf("%s is not flagged assistant-only (flagged: %v)", name, flagged)
		}
	}
	for _, d := range mcp.AgentCatalog(b, nil) {
		if flagged[d.Name] {
			t.Fatalf("the agents' table has %s", d.Name)
		}
	}
	for group, tools := range groupTools {
		for _, name := range tools {
			if flagged[name] {
				t.Fatalf("group %s grants %s", group, name)
			}
		}
	}
	all := testAgent(ModeAutonomous, Groups...).allowedTools()
	for name := range flagged {
		if all[name] {
			t.Fatalf("an agent with every group is allowed %s", name)
		}
	}
}

// The menu tools: reads in ler, send_menu in responder_no_chat, send_menu_item in midia;
// each message of send_menu counts in max_sends.
func TestMenuToolsPermissionsAndCaps(t *testing.T) {
	a := testAgent(ModeAutonomous, GroupRead)
	tools := a.allowedTools()
	if !tools["get_menu"] || !tools["get_menu_item"] || tools["send_menu"] || tools["send_menu_item"] {
		t.Fatalf("ler grants %v", tools)
	}
	if tools := testAgent(ModeAutonomous, GroupRead, GroupReply).allowedTools(); !tools["send_menu"] || tools["send_menu_item"] {
		t.Fatalf("responder_no_chat grants %v", tools)
	}
	if tools := testAgent(ModeAutonomous, GroupRead, GroupMedia).allowedTools(); !tools["send_menu_item"] {
		t.Fatalf("midia grants %v", tools)
	}

	f := &fakeTools{}
	a = testAgent(ModeAutonomous, GroupRead, GroupReply, GroupMedia)
	a.Limits.MaxSends = 4
	e := newExec(a, f)
	// A menu asking for 9 messages may only use the sends left (4), and counts them all.
	if _, st := do(e, "send_menu", map[string]any{"max_messages": 9}); st.Outcome != OutcomeExecuted {
		t.Fatalf("send_menu %+v", st)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], `"max_messages":4`) || !strings.Contains(f.calls[0], `"client_message_id":"agent:arun_t:1"`) {
		t.Fatalf("call %v", f.calls)
	}
	if e.sends != 4 || !e.replied {
		t.Fatalf("sends = %d replied = %v", e.sends, e.replied)
	}
	if _, st := do(e, "send_menu_item", map[string]any{"item_id": "mitm_0123456789abcdef"}); st.Outcome != OutcomeRefused ||
		!strings.Contains(st.Result, "sends per run") {
		t.Fatalf("over the cap %+v", st)
	}
	// Default: 3 messages; and only in the trigger chat without enviar_outros_chats.
	f = &fakeTools{}
	e = newExec(testAgent(ModeAutonomous, GroupRead, GroupReply), f)
	do(e, "send_menu", map[string]any{})
	if e.sends != 3 || !strings.Contains(f.calls[0], `"max_messages":3`) {
		t.Fatalf("default weight %d %v", e.sends, f.calls)
	}
	if _, st := do(e, "send_menu", map[string]any{"to": "5511900000001"}); st.Outcome != OutcomeRefused {
		t.Fatalf("other chat %+v", st)
	}
	// The menu's messages leave no room for another send.
	f = &fakeTools{}
	a = testAgent(ModeAutonomous, GroupRead, GroupReply)
	a.Limits.MaxSends = 2
	e = newExec(a, f)
	do(e, "send_menu", map[string]any{"max_messages": 2})
	if _, st := do(e, "send_message", map[string]any{"text": "oi"}); st.Outcome != OutcomeRefused {
		t.Fatalf("send after a 2-message menu %+v", st)
	}
}
