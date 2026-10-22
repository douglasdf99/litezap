package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/google/jsonschema-go/jsonschema"
)

// fakeTools are tool defs that record their calls (unit tests of the executor).
type fakeTools struct{ calls []string }

func (f *fakeTools) defs() map[string]ToolDef {
	out := map[string]ToolDef{}
	for _, name := range []string{"send_message", "send_reaction", "get_messages", "get_message_status", "get_contact",
		"get_group", "list_chats", "update_group", "get_group_invite_link", "label_chat"} {
		out[name] = ToolDef{Name: name,
			Schema: func() (*jsonschema.Schema, error) { return &jsonschema.Schema{Type: "object"}, nil },
			Call: func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
				f.calls = append(f.calls, name+" "+string(args))
				if name == "get_message_status" {
					return json.RawMessage(`{"id":"msg_1","chat":"` + other + `","text":"segredo de outra conversa"}`), nil
				}
				return json.RawMessage(`{"message":{"id":"msg_x"},"ok":true}`), nil
			}}
	}
	return out
}

func storeDraft() store.AgentDraft {
	return store.AgentDraft{ID: "adr_1", AgentID: "agn_t", RunID: "arun_t", Tool: "send_message", ChatJID: contact,
		ArgsJSON: `{"to":"` + contact + `","text":"texto privado"}`}
}

func testAgent(mode string, groups ...string) Agent {
	a := defaultAgent()
	a.ID, a.Name, a.Mode, a.Tools = "agn_t", "Teste", mode, groups
	return a
}

func newExec(a Agent, f *fakeTools) *executor {
	return &executor{agent: a, mode: a.Mode, runID: "arun_t", trigger: contact, defs: f.defs(), recipients: map[string]bool{}}
}

// do runs one call and returns its result and logged step.
func do(e *executor, tool string, args map[string]any) (string, Step) {
	in, _ := json.Marshal(args)
	content, _ := e.exec(context.Background(), 1, attendant.ToolCall{ID: "c", Name: tool, Input: in})
	return content, e.steps[len(e.steps)-1]
}

func TestMentionsAllRefusedWithAndWithoutTo(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"to omitted", map[string]any{"text": "oi", "mentions": []any{"all"}}},
		{"to omitted, spaced and upper case", map[string]any{"text": "oi", "mentions": []any{" ALL "}}},
		{"to set", map[string]any{"to": contact, "text": "oi", "mentions": []any{"5511900000000", "All"}}},
		{"non-string item", map[string]any{"text": "oi", "mentions": []any{1}}},
		{"not a list", map[string]any{"text": "oi", "mentions": "all"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeTools{}
			e := newExec(testAgent(ModeAutonomous, GroupRead, GroupReply), f)
			_, st := do(e, "send_message", tc.args)
			if st.Outcome != OutcomeRefused || len(f.calls) != 0 {
				t.Fatalf("step %+v calls %v", st, f.calls)
			}
		})
	}
	f := &fakeTools{}
	e := newExec(testAgent(ModeAutonomous, GroupRead, GroupReply), f)
	if _, st := do(e, "send_message", map[string]any{"text": "oi", "mentions": []any{"5511900000000"}}); st.Outcome != OutcomeExecuted {
		t.Fatalf("a normal mention was refused: %+v", st)
	}
}

func TestSendAndWriteCaps(t *testing.T) {
	// Sends to the trigger chat count too.
	f := &fakeTools{}
	a := testAgent(ModeAutonomous, GroupRead, GroupReply)
	a.Limits.MaxSends = 2
	e := newExec(a, f)
	var outcomes []string
	for i := 0; i < 3; i++ {
		_, st := do(e, "send_message", map[string]any{"text": fmt.Sprint("msg ", i)})
		outcomes = append(outcomes, st.Outcome)
	}
	if strings.Join(outcomes, ",") != "executed,executed,refused" || len(f.calls) != 2 {
		t.Fatalf("outcomes %v calls %d", outcomes, len(f.calls))
	}
	// The write cap applies before the mode: suggestions count as well.
	f = &fakeTools{}
	a = testAgent(ModeSuggest, GroupRead, GroupReply, GroupLabels)
	a.Limits.MaxWrites = 2
	e = newExec(a, f)
	outcomes = nil
	for _, tool := range []string{"label_chat", "send_message", "label_chat"} {
		_, st := do(e, tool, map[string]any{"text": "x", "label": "1", "labeled": true})
		outcomes = append(outcomes, st.Outcome)
	}
	if strings.Join(outcomes, ",") != "note,note,refused" {
		t.Fatalf("outcomes %v", outcomes)
	}
	// Group changes have their own cap.
	f = &fakeTools{}
	a = testAgent(ModeSuggest, GroupRead, GroupGroups)
	a.Limits.MaxGroupChanges = 1
	e = newExec(a, f)
	do(e, "update_group", map[string]any{"group": "120363000000000001@g.us", "name": "a"})
	if _, st := do(e, "update_group", map[string]any{"group": "120363000000000001@g.us", "name": "b"}); st.Outcome != OutcomeRefused ||
		!strings.Contains(st.Result, "group changes") {
		t.Fatalf("second group change %+v", st)
	}
}

// TestMaxWritesCapsDraftsInApproval runs the real loop in aprovacao: the drafts stop at
// max_writes.
func TestMaxWritesCapsDraftsInApproval(t *testing.T) {
	e := newEnv(t)
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		if n == 1 {
			var blocks []map[string]any
			for i := 0; i < 3; i++ {
				blocks = append(blocks, toolUse(fmt.Sprintf("tu%d", i), "send_message", map[string]any{"text": fmt.Sprint("parte ", i)}))
			}
			return blocks, "tool_use"
		}
		return []map[string]any{text("ok")}, "end_turn"
	}
	a := e.agent(Input{Name: ptr("A"), Enabled: ptr(true), Limits: &Limits{MaxWrites: 2}})
	e.receive(contact, "oi")
	run := e.lastRun(a.ID)
	if run.Drafts != 2 || run.Steps[2].Outcome != OutcomeRefused {
		t.Fatalf("run %+v", run)
	}
	drafts, _ := e.runner.Drafts(e.ctx, e.inst, DraftPending, 0)
	if len(drafts) != 2 {
		t.Fatalf("drafts %d", len(drafts))
	}
}

func TestReadScope(t *testing.T) {
	f := &fakeTools{}
	e := newExec(testAgent(ModeAutonomous, GroupRead, GroupReply), f)
	if _, st := do(e, "get_messages", map[string]any{"chat": other}); st.Outcome != OutcomeRefused ||
		!strings.Contains(st.Result, "only read the chat") {
		t.Fatalf("other chat read: %+v", st)
	}
	if _, st := do(e, "get_messages", map[string]any{}); st.Outcome != OutcomeRead || !strings.Contains(f.calls[0], contact) {
		t.Fatalf("trigger chat read: %+v %v", st, f.calls)
	}
	if _, st := do(e, "get_contact", map[string]any{"contact": "5511977776666"}); st.Outcome != OutcomeRefused {
		t.Fatalf("other contact: %+v", st)
	}
	if _, st := do(e, "get_group", map[string]any{"group": "120363000000000001@g.us"}); st.Outcome != OutcomeRefused {
		t.Fatalf("other group: %+v", st)
	}
	// A message status of another chat carries its text: dropped.
	content, st := do(e, "get_message_status", map[string]any{"message_id": "msg_1"})
	if st.Outcome != OutcomeRefused || strings.Contains(content, "segredo") {
		t.Fatalf("status of another chat: %+v %s", st, content)
	}
	if _, st := do(e, "list_chats", map[string]any{}); st.Outcome != OutcomeRead {
		t.Fatalf("list_chats (metadata): %+v", st)
	}
	if testAgent(ModeAutonomous, GroupRead).allowedTools()["list_contacts"] {
		t.Fatal("ler alone grants the address book")
	}
	// With ler_outros_chats, other chats are readable.
	f = &fakeTools{}
	e = newExec(testAgent(ModeAutonomous, GroupReadOther), f)
	if _, st := do(e, "get_messages", map[string]any{"chat": other}); st.Outcome != OutcomeRead {
		t.Fatalf("ler_outros_chats: %+v", st)
	}
	if _, st := do(e, "get_message_status", map[string]any{"message_id": "msg_1"}); st.Outcome != OutcomeRead {
		t.Fatalf("ler_outros_chats status: %+v", st)
	}
	if !testAgent(ModeAutonomous, GroupReadOther).allowedTools()["list_contacts"] {
		t.Fatal("ler_outros_chats lacks list_contacts")
	}
	for _, g := range defaultAgent().Tools {
		if g == GroupReadOther {
			t.Fatal("the default agent reads other chats")
		}
	}
}

func TestInviteLinkIsAWrite(t *testing.T) {
	args := map[string]any{"group": "120363000000000001@g.us"}
	f := &fakeTools{}
	e := newExec(testAgent(ModeSuggest, GroupRead, GroupGroups), f)
	if _, st := do(e, "get_group_invite_link", args); st.Outcome != OutcomeNote || len(f.calls) != 0 {
		t.Fatalf("sugerir: %+v %v", st, f.calls)
	}
	e = newExec(testAgent(ModeAutonomous, GroupRead, GroupGroups), f)
	e.dryRun = true
	if _, st := do(e, "get_group_invite_link", args); st.Outcome != OutcomeDryRun || len(f.calls) != 0 {
		t.Fatalf("simulator: %+v %v", st, f.calls)
	}
}

func TestRedaction(t *testing.T) {
	steps := redactSteps([]Step{
		{Tool: "get_messages", Outcome: OutcomeRead, Result: `{"text":"conversa privada"}`},
		{Tool: "send_message", Outcome: OutcomeRefused, Result: "refused: to 5511977776666@s.whatsapp.net"},
		{Tool: "send_message", Outcome: OutcomeDraft, Result: `{"draft_id":"x"}`},
	})
	if steps[0].Result != "" || steps[2].Result != "" {
		t.Fatalf("results kept: %+v", steps)
	}
	if strings.Contains(steps[1].Result, "5511977776666") || !strings.Contains(steps[1].Result, "6666@s.whatsapp.net") {
		t.Fatalf("phone not masked: %q", steps[1].Result)
	}
	ev := draftEvent(storeDraft(), DraftPending)
	keys := []string{}
	for k := range ev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "agent_id,chat,draft_id,status,tool" {
		t.Fatalf("draft event keys %v", keys)
	}
}

// TestApprovalRevalidates: approving re-checks the agent (exists, on, permission) and the
// run's caps; a draft left "approving" by a crash is finished at startup.
func TestApprovalRevalidates(t *testing.T) {
	e := newEnv(t)
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		if n == 1 {
			return []map[string]any{toolUse("a", "send_message", map[string]any{"text": "um"}),
				toolUse("b", "send_message", map[string]any{"text": "dois"}),
				toolUse("c", "send_message", map[string]any{"text": "três"})}, "tool_use"
		}
		return []map[string]any{text("ok")}, "end_turn"
	}
	a := e.agent(Input{Name: ptr("A"), Enabled: ptr(true)})
	e.receive(contact, "oi")
	e.lastRun(a.ID)
	drafts, _ := e.runner.Drafts(e.ctx, e.inst, DraftPending, 0)
	if len(drafts) != 3 {
		t.Fatalf("drafts %d", len(drafts))
	}
	// Drafts are listed newest first.
	d3, d2, d1 := drafts[0], drafts[1], drafts[2]
	approve := func(id string) error { _, err := e.runner.Decide(e.ctx, e.inst, id, true, "admin"); return err }

	if _, err := e.runner.Update(e.ctx, e.inst, a.ID, Input{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if err := approve(d1.ID); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("paused agent: %v", err)
	}
	if _, err := e.runner.Update(e.ctx, e.inst, a.ID, Input{Enabled: ptr(true), Tools: []string{GroupRead}}); err != nil {
		t.Fatal(err)
	}
	if err := approve(d1.ID); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("permission removed: %v", err)
	}
	if _, err := e.runner.Update(e.ctx, e.inst, a.ID, Input{Tools: []string{GroupRead, GroupReply},
		Limits: &Limits{MaxSends: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := approve(d1.ID); err != nil {
		t.Fatal(err)
	}
	if err := approve(d2.ID); err == nil || !strings.Contains(err.Error(), "sends per run") {
		t.Fatalf("cap at approval: %v", err)
	}
	e.wait("first approved send", func() bool { return len(e.fake.WA.Sent()) == 1 })

	// Crash recovery: a draft stuck in approving is executed at startup.
	if _, err := e.runner.Update(e.ctx, e.inst, a.ID, Input{Limits: &Limits{MaxSends: 5}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.st.ClaimAgentDraft(e.ctx, e.inst, d3.ID, DraftApproving, "admin", time.Now()); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	e.runner.RecoverDrafts(e.ctx)
	got, _ := e.st.GetAgentDraft(e.ctx, e.inst, d3.ID)
	if got.Status != DraftApproved {
		t.Fatalf("recovered draft %+v", got)
	}
	e.wait("recovered send", func() bool { return len(e.fake.WA.Sent()) == 2 })
}

// TestStoredRunHasNoReadResults: the run log keeps no third-party content.
func TestStoredRunHasNoReadResults(t *testing.T) {
	e := newEnv(t)
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		if n == 1 {
			return []map[string]any{toolUse("r", "get_messages", map[string]any{})}, "tool_use"
		}
		return []map[string]any{text("ok")}, "end_turn"
	}
	a := e.agent(Input{Name: ptr("A"), Enabled: ptr(true)})
	e.receive(contact, "mensagem privada do cliente")
	run := e.lastRun(a.ID)
	if len(run.Steps) != 1 || run.Steps[0].Outcome != OutcomeRead || run.Steps[0].Result != "" {
		t.Fatalf("stored steps %+v", run.Steps)
	}
	// The model still got the read (the transcript of its second call has it).
	if !strings.Contains(fmt.Sprint(e.claude.body(1)["messages"]), "mensagem privada do cliente") {
		t.Fatal("the read did not reach the model")
	}
}
