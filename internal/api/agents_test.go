package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeMessagesAPI answers like the Claude Messages API: odd calls send a message (to,
// when set), even calls end the turn.
func fakeMessagesAPI(t *testing.T, to string) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		content := []map[string]any{{"type": "text", "text": "Pronto: respondi."}}
		stop := "end_turn"
		if k%2 == 1 {
			input := map[string]any{"text": "Olá! Como posso ajudar?"}
			if to != "" {
				input["to"] = to
			}
			content = []map[string]any{{"type": "tool_use", "id": fmt.Sprintf("tu_%d", k), "name": "send_message", "input": input}}
			stop = "tool_use"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
			"content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 500, "output_tokens": 50}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentsAPI(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Agentes")
	_, otherTok := e.connected("Outra")
	base := "/v1/instances/" + id
	// Without a provider the run is refused with a clear error.
	code, body := e.do("POST", base+"/agents", tok, `{"name":"Atendente","instructions":"Seja breve."}`)
	if code != http.StatusCreated || body["mode"] != "aprovacao" || body["enabled"] != false {
		t.Fatalf("create = %d %v", code, body)
	}
	agentID := body["id"].(string)
	if code, body := e.do("POST", base+"/agents/"+agentID+"/run", tok, `{"chat":"5511988887777"}`); code != http.StatusConflict ||
		errCode(body) != "not_configured" {
		t.Fatalf("run without provider = %d %v", code, body)
	}
	// Provider: only the admin sets it (the attendant's), here a fake Messages API.
	claude := fakeMessagesAPI(t, "")
	if code, _ := e.do("PUT", base+"/attendant", tok, `{"api_key":"sk-x"}`); code != http.StatusForbidden {
		t.Fatalf("instance token set the provider: %d", code)
	}
	if code, body := e.do("PUT", base+"/attendant", admin, `{"provider":"anthropic","base_url":"`+claude.URL+`","api_key":"sk-ant-test-0123456789"}`); code != 200 {
		t.Fatalf("provider = %d %v", code, body)
	}
	// Validation and isolation.
	if code, body := e.do("POST", base+"/agents", tok, `{"name":"X","tools":["tudo"]}`); code != 400 || errCode(body) != "validation_failed" {
		t.Fatalf("bad group = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/agents", tok, `{"name":"X","oops":1}`); code != 400 {
		t.Fatalf("unknown field = %d %v", code, body)
	}
	if code, _ := e.do("GET", base+"/agents", otherTok, ""); code != http.StatusForbidden {
		t.Fatalf("other instance's token = %d", code)
	}
	code, body = e.do("GET", base+"/agents", tok, "")
	if list := body["agents"].([]any); code != 200 || len(list) != 1 || len(body["groups"].([]any)) != 6 {
		t.Fatalf("list = %d %v", code, body)
	}
	// Simulator: write tools are dry runs.
	code, body = e.do("POST", base+"/agents/simulate", tok, `{"agent_id":"`+agentID+`","messages":[{"text":"oi"}]}`)
	steps, _ := body["steps"].([]any)
	if code != 200 || body["dry_run"] != true || len(steps) != 1 || steps[0].(map[string]any)["outcome"] != "dry_run" {
		t.Fatalf("simulate = %d %v", code, body)
	}
	// Manual run in approval mode: a draft, nothing queued.
	code, body = e.do("POST", base+"/agents/"+agentID+"/run", tok, `{"chat":"5511988887777","input":"responda o cliente"}`)
	if code != 200 || body["status"] != "completed" || body["drafts"] != float64(1) || body["trigger"] != "manual" {
		t.Fatalf("run = %d %v", code, body)
	}
	runID := body["id"].(string)
	if code, body := e.do("GET", base+"/agents/runs/"+runID, tok, ""); code != 200 || body["id"] != runID {
		t.Fatalf("get run = %d %v", code, body)
	}
	if code, body := e.do("GET", base+"/agents/"+agentID+"/runs", tok, ""); code != 200 || len(body["runs"].([]any)) != 1 {
		t.Fatalf("runs = %d %v", code, body)
	}
	_, q := e.do("GET", base+"/queue", tok, "")
	if items, _ := q["items"].([]any); len(items) != 0 {
		t.Fatalf("approval mode queued %v", q)
	}
	code, body = e.do("GET", base+"/agents/drafts?status=pending", tok, "")
	drafts := body["drafts"].([]any)
	if code != 200 || len(drafts) != 1 {
		t.Fatalf("drafts = %d %v", code, body)
	}
	d := drafts[0].(map[string]any)
	args := d["args"].(map[string]any)
	if d["chat"] != "5511988887777@s.whatsapp.net" || args["text"] != "Olá! Como posso ajudar?" || args["client_message_id"] != nil {
		t.Fatalf("draft %v", d)
	}
	draftID := d["id"].(string)
	if code, _ := e.do("POST", base+"/agents/drafts/"+draftID+"/approve", otherTok, ""); code != http.StatusForbidden {
		t.Fatalf("other token approved: %d", code)
	}
	code, body = e.do("POST", base+"/agents/drafts/"+draftID+"/approve", tok, "")
	if code != 200 || body["status"] != "approved" || body["decided_by"] != "instance" {
		t.Fatalf("approve = %d %v", code, body)
	}
	e.eventually("approved draft sent", func() bool { return len(e.sp.Last(id).WA.Sent()) == 1 })
	if code, body := e.do("POST", base+"/agents/drafts/"+draftID+"/reject", tok, ""); code != http.StatusConflict {
		t.Fatalf("decided twice = %d %v", code, body)
	}
	// Autonomous: the manual run queues the send itself.
	if code, body := e.do("PUT", base+"/agents/"+agentID, tok, `{"mode":"autonomo"}`); code != 200 || body["mode"] != "autonomo" ||
		body["instructions"] != "Seja breve." {
		t.Fatalf("update = %d %v", code, body)
	}
	code, body = e.do("POST", base+"/agents/"+agentID+"/run", tok, `{"chat":"5511988887777"}`)
	if code != 200 || body["sent"] != float64(1) {
		t.Fatalf("autonomous run = %d %v", code, body)
	}
	e.eventually("autonomous send", func() bool { return len(e.sp.Last(id).WA.Sent()) == 2 })
	if code, body := e.do("GET", base+"/agents/"+agentID, tok, ""); code != 200 ||
		body["today"].(map[string]any)["runs"] != float64(2) {
		t.Fatalf("get = %d %v", code, body)
	}
	// Delete.
	if code, _ := e.do("DELETE", base+"/agents/"+agentID, tok, ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := e.do("GET", base+"/agents/"+agentID, tok, ""); code != http.StatusNotFound {
		t.Fatalf("deleted agent = %d", code)
	}
}

// TestAgentsAPIRefusesOtherChats: a send to a chat other than the run's is refused
// unless the agent may send to other chats.
func TestAgentsAPIRefusesOtherChats(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Agentes")
	base := "/v1/instances/" + id
	claude := fakeMessagesAPI(t, "5511977776666")
	e.do("PUT", base+"/attendant", admin, `{"provider":"anthropic","base_url":"`+claude.URL+`","api_key":"sk-ant-test-0123456789"}`)
	_, body := e.do("POST", base+"/agents", tok, `{"name":"A","mode":"autonomo"}`)
	agentID := body["id"].(string)
	code, body := e.do("POST", base+"/agents/"+agentID+"/run", tok, `{"chat":"5511988887777"}`)
	steps, _ := body["steps"].([]any)
	if code != 200 || body["sent"] != float64(0) || len(steps) != 1 || steps[0].(map[string]any)["outcome"] != "refused" {
		t.Fatalf("run = %d %v", code, body)
	}
	if !strings.Contains(steps[0].(map[string]any)["result"].(string), "chat that triggered it") {
		t.Fatalf("refusal %v", steps[0])
	}
}
