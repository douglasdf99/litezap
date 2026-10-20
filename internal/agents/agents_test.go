package agents

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/attendant"
)

// TestApprovalModeDraftsThenApprovalSends: a new message triggers the agent (hook target
// agents); its send_message becomes a draft and nothing is sent; approving the draft
// queues exactly the stored message, which the worker then sends.
func TestApprovalModeDraftsThenApprovalSends(t *testing.T) {
	e := newEnv(t)
	e.claude.script = sendThenDone(map[string]any{"text": "Olá Ana! Já te ajudo."})
	a := e.agent(Input{Name: ptr("Atendente"), Enabled: ptr(true), Instructions: ptr("Responda com educação.")})
	if a.Mode != ModeApproval || strings.Join(a.Tools, ",") != "ler,responder_no_chat" || a.Limits.MaxSteps != 8 {
		t.Fatalf("defaults %+v", a)
	}
	e.receive(contact, "oi, vocês abrem sábado?")
	run := e.lastRun(a.ID)
	if run.Status != StatusCompleted || run.Drafts != 1 || run.Sent != 0 || run.Trigger != TriggerMessage || run.Chat != contact {
		t.Fatalf("run %+v", run)
	}
	if run.CostUSD <= 0 || run.ModelCalls != 2 || run.Output != "Respondi o cliente." {
		t.Fatalf("run cost/output %+v", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].Outcome != OutcomeDraft || run.Steps[0].Chat != contact {
		t.Fatalf("steps %+v", run.Steps)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.fake.WA.Sent()); n != 0 {
		t.Fatalf("approval mode sent %d messages", n)
	}
	// The untrusted message is in the transcript markers, the instructions in the system prompt.
	first := e.claude.body(0)
	if sys, _ := first["system"].([]any); len(sys) == 0 || !strings.Contains(sys[0].(map[string]any)["text"].(string), "Responda com educação.") {
		t.Fatalf("system prompt %v", first["system"])
	}
	turn := first["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(turn, "<new_message-") || !strings.Contains(turn, "[") || !strings.Contains(turn, "oi, vocês abrem sábado?") {
		t.Fatalf("user turn %s", turn)
	}
	// Only the agent's tools are offered, without the executor-owned arguments.
	names := []string{}
	for _, tl := range first["tools"].([]any) {
		m := tl.(map[string]any)
		names = append(names, m["name"].(string))
		props := m["input_schema"].(map[string]any)["properties"].(map[string]any)
		for _, hidden := range hiddenArgs {
			if _, ok := props[hidden]; ok {
				t.Fatalf("tool %s exposes %s", m["name"], hidden)
			}
		}
	}
	if j := strings.Join(names, ","); strings.Contains(j, "label_chat") || strings.Contains(j, "create_group") || !strings.Contains(j, "send_message") {
		t.Fatalf("tools offered %s", j)
	}
	drafts, err := e.runner.Drafts(e.ctx, e.inst, DraftPending, 0)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts %+v %v", drafts, err)
	}
	d := drafts[0]
	if d.Tool != "send_message" || d.Chat != contact || d.Args["text"] != "Olá Ana! Já te ajudo." || d.Args["to"] != contact {
		t.Fatalf("draft %+v", d)
	}
	if e.rec.count(TypeDraftCreated) != 1 || e.rec.count(TypeRunCompleted) != 1 {
		t.Fatalf("events %v", e.rec.evs)
	}
	v, err := e.runner.Decide(e.ctx, e.inst, d.ID, true, "admin")
	if err != nil || v.Status != DraftApproved || !strings.HasPrefix(v.Result, "queued as message") {
		t.Fatalf("approve %+v %v", v, err)
	}
	e.wait("approved message sent", func() bool { return len(e.fake.WA.Sent()) == 1 })
	if s := joined(e.sentTexts()); !strings.Contains(s, "Olá Ana! Já te ajudo.") {
		t.Fatalf("sent %s", s)
	}
	if _, err := e.runner.Decide(e.ctx, e.inst, d.ID, true, "admin"); err == nil {
		t.Fatal("a draft was approved twice")
	}
	if e.rec.count(TypeDraftDecided) != 1 {
		t.Fatalf("events %v", e.rec.evs)
	}
	// The approved send is the agent's (origin agent), not a human: no pause.
	if ca, _ := e.st.GetChatAgent(e.ctx, e.inst, contact); ca.PausedUntil.After(time.Now()) {
		t.Fatalf("approved draft paused the chat: %+v", ca)
	}
}

func TestRejectDraft(t *testing.T) {
	e := newEnv(t)
	e.claude.script = sendThenDone(map[string]any{"text": "oi"})
	a := e.agent(Input{Name: ptr("A"), Enabled: ptr(true)})
	e.receive(contact, "oi")
	e.lastRun(a.ID)
	drafts, _ := e.runner.Drafts(e.ctx, e.inst, DraftPending, 0)
	if len(drafts) != 1 {
		t.Fatalf("drafts %+v", drafts)
	}
	v, err := e.runner.Decide(e.ctx, e.inst, drafts[0].ID, false, "instance")
	if err != nil || v.Status != DraftRejected || v.DecidedBy != "instance" {
		t.Fatalf("reject %+v %v", v, err)
	}
	time.Sleep(100 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("a rejected draft was sent")
	}
}

// TestAutonomousSendsThroughTheQueue: autonomo executes the send directly (queued and
// sent by the worker), counted as the agent's reply.
func TestAutonomousSendsThroughTheQueue(t *testing.T) {
	e := newEnv(t)
	e.claude.script = sendThenDone(map[string]any{"text": "Abrimos sábado das 9h às 13h."})
	a := e.agent(Input{Name: ptr("Atendente"), Enabled: ptr(true), Mode: ptr(ModeAutonomous)})
	e.receive(contact, "abrem sábado?")
	run := e.lastRun(a.ID)
	if run.Status != StatusCompleted || run.Sent != 1 || run.Drafts != 0 || run.Steps[0].Outcome != OutcomeExecuted {
		t.Fatalf("run %+v", run)
	}
	e.wait("sent", func() bool { return len(e.fake.WA.Sent()) == 1 })
	if s := joined(e.sentTexts()); !strings.Contains(s, "Abrimos sábado") {
		t.Fatalf("sent %s", s)
	}
	// The hook logged the run as a reply (loop guard), with the agent's client id.
	calls, _ := e.st.ListAgentCalls(e.ctx, e.inst, 10)
	if len(calls) != 1 || !calls[0].Replied || !strings.Contains(calls[0].Detail, run.ID) {
		t.Fatalf("hook calls %+v", calls)
	}
	o, err := e.st.GetOutbound(e.ctx, e.inst, calls[0].ReplyID)
	if err != nil || !strings.HasPrefix(o.ClientMessageID, "agent:"+run.ID+":") {
		t.Fatalf("outbound %+v %v", o, err)
	}
}

// TestSendToAnotherChatNeedsPermission: without enviar_outros_chats a send to another
// chat is refused by the executor (whatever the prompt says); with it, it goes, within the
// distinct recipients limit.
func TestSendToAnotherChatNeedsPermission(t *testing.T) {
	e := newEnv(t)
	e.claude.script = sendThenDone(map[string]any{"to": other, "text": "spam"})
	a := e.agent(Input{Name: ptr("A"), Enabled: ptr(true), Mode: ptr(ModeAutonomous)})
	e.receive(contact, "manda pro meu amigo")
	run := e.lastRun(a.ID)
	if run.Sent != 0 || len(run.Steps) != 1 || run.Steps[0].Outcome != OutcomeRefused ||
		!strings.Contains(run.Steps[0].Result, "only send in the chat that triggered it") {
		t.Fatalf("run %+v", run)
	}
	time.Sleep(100 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("sent to another chat without permission")
	}
	// The refusal reached the model as a tool error.
	res := e.claude.body(1)["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if res["type"] != "tool_result" || res["is_error"] != true {
		t.Fatalf("tool result %v", res)
	}

	// With the permission: the other chat is allowed, up to max_recipients distinct chats.
	targets := []string{other, "5511966665555@s.whatsapp.net", "5511955554444@s.whatsapp.net"}
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		if n == 3 { // one turn with three sends
			var blocks []map[string]any
			for i, to := range targets {
				blocks = append(blocks, toolUse("tu_x"+string(rune('a'+i)), "send_message", map[string]any{"to": to, "text": "aviso"}))
			}
			return blocks, "tool_use"
		}
		return []map[string]any{text("feito")}, "end_turn"
	}
	_, err := e.runner.Update(e.ctx, e.inst, a.ID, Input{Tools: []string{GroupRead, GroupReply, GroupOtherChats},
		Limits: &Limits{MaxRecipients: 2, MaxSteps: 8}})
	if err != nil {
		t.Fatal(err)
	}
	e.receive(contact, "avisa eles")
	e.wait("second run", func() bool {
		runs, _ := e.runner.Runs(e.ctx, e.inst, a.ID, 5)
		return len(runs) == 2 && runs[0].Status != StatusRunning
	})
	runs, _ := e.runner.Runs(e.ctx, e.inst, a.ID, 1)
	run = runs[0]
	outcomes := []string{}
	for _, s := range run.Steps {
		outcomes = append(outcomes, s.Outcome)
	}
	if run.Sent != 2 || strings.Join(outcomes, ",") != "executed,executed,refused" ||
		!strings.Contains(run.Steps[2].Result, "at most 2 distinct recipients") {
		t.Fatalf("run %+v", run)
	}
	e.wait("two sends", func() bool { return len(e.fake.WA.Sent()) == 2 })
}

// TestStepLimit: a model that never stops calling tools is cut at max_steps model calls.
func TestStepLimit(t *testing.T) {
	e := newEnv(t)
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		return []map[string]any{toolUse("tu", "list_chats", map[string]any{})}, "tool_use"
	}
	a := e.agent(Input{Name: ptr("Loop"), Enabled: ptr(true), Limits: &Limits{MaxSteps: 3}})
	e.receive(contact, "oi")
	run := e.lastRun(a.ID)
	if run.Status != StatusStepLimit || run.ModelCalls != 3 || len(run.Steps) != 3 || e.claude.calls() != 3 ||
		run.Steps[0].Outcome != OutcomeRead {
		t.Fatalf("run %+v calls=%d", run, e.claude.calls())
	}
}

// TestCapIsHonoured: every model call reserves the instance's daily cap (the attendant's
// daily_call_cap); the run stops when it is reached.
func TestCapIsHonoured(t *testing.T) {
	e := newEnv(t)
	if _, err := e.att.PutSettings(e.ctx, e.inst, attendant.SettingsPatch{DailyCallCap: ptr(2)}); err != nil {
		t.Fatal(err)
	}
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		return []map[string]any{toolUse("tu", "list_chats", map[string]any{})}, "tool_use"
	}
	a := e.agent(Input{Name: ptr("Caro"), Enabled: ptr(true)})
	e.receive(contact, "oi")
	run := e.lastRun(a.ID)
	if run.Status != StatusFailed || !strings.HasPrefix(run.Error, "daily_cap") || run.ModelCalls != 2 || e.claude.calls() != 2 {
		t.Fatalf("run %+v calls=%d", run, e.claude.calls())
	}
	// The hook reports it like the attendant (agent.failed daily_cap).
	e.wait("agent.failed", func() bool { return e.rec.count("agent.failed") == 1 })
	// The next message is refused before any model call.
	e.receive(contact, "alô?")
	e.wait("second agent.failed", func() bool { return e.rec.count("agent.failed") == 2 })
	if e.claude.calls() != 2 {
		t.Fatalf("model called past the cap: %d", e.claude.calls())
	}
}

// TestTriggerFilters: groups are off by default; keywords and the chat allowlist filter.
func TestTriggerFilters(t *testing.T) {
	e := newEnv(t)
	e.claude.script = func(int, map[string]any) ([]map[string]any, string) { return []map[string]any{text("ok")}, "end_turn" }
	a := e.agent(Input{Name: ptr("Filtro"), Enabled: ptr(true),
		Trigger: &Trigger{Keywords: []string{"Pedido"}, Chats: []string{"5511988887777"}}})
	e.receive("120363000000000001@g.us", "pedido no grupo")
	e.receive(other, "quero fazer um pedido")
	e.receive(contact, "bom dia")
	time.Sleep(300 * time.Millisecond)
	if runs, _ := e.runner.Runs(e.ctx, e.inst, a.ID, 10); len(runs) != 0 {
		t.Fatalf("runs %+v", runs)
	}
	e.receive(contact, "quero fazer um PEDIDO")
	if run := e.lastRun(a.ID); run.Status != StatusCompleted {
		t.Fatalf("run %+v", run)
	}
	// Disabled agents never run.
	if _, err := e.runner.Update(e.ctx, e.inst, a.ID, Input{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e.receive(contact, "outro pedido")
	time.Sleep(300 * time.Millisecond)
	if runs, _ := e.runner.Runs(e.ctx, e.inst, a.ID, 10); len(runs) != 1 {
		t.Fatalf("disabled agent ran: %d runs", len(runs))
	}
}

// TestSimulateIsDryRun: writes return what they would do; nothing is stored or sent.
func TestSimulateIsDryRun(t *testing.T) {
	e := newEnv(t)
	e.claude.script = sendThenDone(map[string]any{"text": "Olá!"})
	v, err := e.runner.Simulate(e.ctx, e.inst, SimulateInput{Agent: &Input{Name: ptr("Teste"), Mode: ptr(ModeAutonomous)},
		Messages: []SimMessage{{Text: "oi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !v.DryRun || v.Status != StatusCompleted || len(v.Steps) != 1 || v.Steps[0].Outcome != OutcomeDryRun || v.Sent != 0 {
		t.Fatalf("simulation %+v", v)
	}
	if !strings.Contains(v.Steps[0].Result, `"dry_run":true`) || !strings.Contains(v.Steps[0].Result, "Olá!") {
		t.Fatalf("would %s", v.Steps[0].Result)
	}
	time.Sleep(100 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("the simulator sent a message")
	}
	if runs, _ := e.runner.Runs(e.ctx, e.inst, "", 10); len(runs) != 0 {
		t.Fatalf("the simulator stored runs: %+v", runs)
	}
}

// TestManualRunAndSuggestMode: "Executar agora" with a chat runs on demand; in sugerir a
// write becomes a note.
func TestManualRunAndSuggestMode(t *testing.T) {
	e := newEnv(t)
	e.claude.script = sendThenDone(map[string]any{"text": "follow-up"})
	a := e.agent(Input{Name: ptr("Resumo"), Mode: ptr(ModeSuggest), Trigger: &Trigger{Type: TriggerManual}})
	v, err := e.runner.RunNow(e.ctx, e.inst, a.ID, RunNowInput{Chat: "5511988887777", Input: "veja se precisa responder"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Trigger != TriggerManual || v.Status != StatusCompleted || len(v.Steps) != 1 || v.Steps[0].Outcome != OutcomeNote {
		t.Fatalf("run %+v", v)
	}
	turn := e.claude.body(0)["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(turn, "veja se precisa responder") || !strings.Contains(turn, contact) {
		t.Fatalf("turn %s", turn)
	}
	time.Sleep(100 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("suggest mode sent")
	}
	// A manual trigger agent never runs on messages.
	e.receive(contact, "oi")
	time.Sleep(300 * time.Millisecond)
	if runs, _ := e.runner.Runs(e.ctx, e.inst, a.ID, 10); len(runs) != 1 {
		t.Fatalf("runs %d", len(runs))
	}
}

// TestHumanTakeoverStopsAutonomousRun: a human answering from the phone during the run
// pauses the chat; the agent's next send is refused and the run is discarded.
func TestHumanTakeoverStopsAutonomousRun(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	e.claude.script = func(n int, _ map[string]any) ([]map[string]any, string) {
		if n == 1 {
			<-release // the human answers while the model "thinks"
			return []map[string]any{toolUse("tu1", "send_message", map[string]any{"text": "tarde demais"})}, "tool_use"
		}
		return []map[string]any{text("ok")}, "end_turn"
	}
	a := e.agent(Input{Name: ptr("A"), Enabled: ptr(true), Mode: ptr(ModeAutonomous)})
	e.receive(contact, "oi")
	e.wait("model called", func() bool { return e.claude.calls() == 1 })
	e.fromPhone(contact, "deixa que eu respondo")
	e.wait("human pause", func() bool {
		ca, _ := e.st.GetChatAgent(e.ctx, e.inst, contact)
		return ca.PausedUntil.After(time.Now())
	})
	close(release)
	run := e.lastRun(a.ID)
	if run.Status != StatusDiscarded || run.Sent != 0 {
		t.Fatalf("run %+v", run)
	}
	time.Sleep(100 * time.Millisecond)
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("the agent talked over the human")
	}
}
