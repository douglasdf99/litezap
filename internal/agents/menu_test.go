package agents

import (
	"strings"
	"testing"
)

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
