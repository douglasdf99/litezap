package agents

import (
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/service"
)

// The catalog-parity editing tools are for the user's own assistant: flagged, left out
// of the agents' table and of every permission group. The reads and the two sends are
// in the agents' table and granted by their groups.
func TestAgentsMenuCatalogTools(t *testing.T) {
	assistant := []string{"create_order_draft", "list_order_drafts", "get_order_draft", "update_order_draft_status", "get_menu_text",
		"export_menu_csv", "create_menu_collection", "update_menu_collection", "delete_menu_collection", "set_menu_collection_items",
		"reorder_menu_collections", "add_menu_item_photo", "remove_menu_item_photo", "reorder_menu_item_photos", "set_menu_item_main_photo",
		"duplicate_menu_item", "bulk_update_menu_items"}
	agentTools := []string{"search_menu", "get_collection", "send_collection", "send_order_summary"}
	b := &mcp.Local{Caller: service.Scope{InstanceID: "inst_a"}}
	flagged := map[string]bool{}
	for _, d := range mcp.Catalog(b, nil) {
		if d.AssistantOnly {
			flagged[d.Name] = true
		}
	}
	inAgents := map[string]bool{}
	for _, d := range mcp.AgentCatalog(b, nil) {
		inAgents[d.Name] = true
	}
	for _, name := range assistant {
		if !flagged[name] || inAgents[name] {
			t.Fatalf("%s: flagged=%v in agents' table=%v", name, flagged[name], inAgents[name])
		}
	}
	for _, name := range agentTools {
		if flagged[name] || !inAgents[name] {
			t.Fatalf("%s: flagged=%v in agents' table=%v", name, flagged[name], inAgents[name])
		}
	}
	all := testAgent(ModeAutonomous, Groups...).allowedTools()
	for _, name := range assistant {
		if all[name] {
			t.Fatalf("an agent with every group is allowed %s", name)
		}
	}
	read := testAgent(ModeAutonomous, GroupRead).allowedTools()
	if !read["search_menu"] || !read["get_collection"] || read["send_collection"] || read["send_order_summary"] {
		t.Fatalf("ler grants %v", read)
	}
	reply := testAgent(ModeAutonomous, GroupRead, GroupReply).allowedTools()
	if !reply["send_collection"] || !reply["send_order_summary"] {
		t.Fatalf("responder_no_chat grants %v", reply)
	}
}

// Their sends count in max_sends: a collection's messages and photos (photos only with
// midia), an album's photos and the order summary.
func TestAgentsMenuCatalogCaps(t *testing.T) {
	f := &fakeTools{}
	a := testAgent(ModeAutonomous, GroupRead, GroupReply)
	a.Limits.MaxSends = 6
	e := newExec(a, f)
	// Without midia a collection goes without photos.
	if _, st := do(e, "send_collection", map[string]any{"name": "Promoções", "photos": 5, "max_messages": 2}); st.Outcome != OutcomeExecuted {
		t.Fatalf("send_collection %+v", st)
	}
	if !strings.Contains(f.calls[0], `"photos":0`) || !strings.Contains(f.calls[0], `"max_messages":2`) || e.sends != 2 {
		t.Fatalf("call %v sends %d", f.calls, e.sends)
	}
	// With midia the photos count, within the sends left.
	f = &fakeTools{}
	a = testAgent(ModeAutonomous, GroupRead, GroupReply, GroupMedia)
	a.Limits.MaxSends = 6
	e = newExec(a, f)
	do(e, "send_collection", map[string]any{"name": "Promoções", "photos": 9, "max_messages": 2})
	if !strings.Contains(f.calls[0], `"photos":4`) || e.sends != 6 {
		t.Fatalf("call %v sends %d", f.calls, e.sends)
	}
	if _, st := do(e, "send_order_summary", map[string]any{"order_id": "odr_0123456789abcdef"}); st.Outcome != OutcomeRefused ||
		!strings.Contains(st.Result, "sends per run") {
		t.Fatalf("over the cap %+v", st)
	}
	// An album is one send per photo; a summary is one send, only in the trigger chat.
	f = &fakeTools{}
	a = testAgent(ModeAutonomous, GroupRead, GroupReply, GroupMedia)
	a.Limits.MaxSends = 4
	e = newExec(a, f)
	do(e, "send_menu_item", map[string]any{"item_id": "mitm_0123456789abcdef", "album": true})
	if !strings.Contains(f.calls[0], `"max_photos":4`) || e.sends != 4 {
		t.Fatalf("album call %v sends %d", f.calls, e.sends)
	}
	f = &fakeTools{}
	e = newExec(testAgent(ModeAutonomous, GroupRead, GroupReply), f)
	if _, st := do(e, "send_order_summary", map[string]any{"items": []any{map[string]any{"item_id": "mitm_0123456789abcdef", "quantity": 1}}}); st.Outcome != OutcomeExecuted || e.sends != 1 {
		t.Fatalf("summary %+v sends %d", st, e.sends)
	}
	if !strings.Contains(f.calls[0], `"to":"`+contact+`"`) {
		t.Fatalf("summary not sent to the trigger chat: %v", f.calls)
	}
	if _, st := do(e, "send_order_summary", map[string]any{"to": "5511900000001", "order_id": "odr_0123456789abcdef"}); st.Outcome != OutcomeRefused {
		t.Fatalf("summary to another chat %+v", st)
	}
}
