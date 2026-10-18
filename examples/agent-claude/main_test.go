package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const secret = "test-secret-0123456789"

func sign(t time.Time, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.%s", t.Unix(), body)
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(m.Sum(nil)))
}

type fakeModel struct {
	turns []Turn
	ans   Answer
	err   error
}

func (f *fakeModel) Answer(_ context.Context, _ string, turns []Turn) (Answer, error) {
	f.turns = turns
	return f.ans, f.err
}

func newServer(m Model, now time.Time) *server {
	return &server{secret: secret, model: m, log: slog.New(slog.DiscardHandler), seen: map[string]time.Time{},
		now: func() time.Time { return now }}
}

const reqBody = `{"instance_id":"i1","chat":{"jid":"5511999999999@s.whatsapp.net","name":"Ana","is_group":false},
"message":{"id":"m3","type":"text","text":"quero falar com um humano","source":"contact"},
"context":[
 {"id":"m0","from_me":true,"source":"device","type":"text","text":"Olá! (enviado antes)"},
 {"id":"m1","from_me":false,"source":"contact","type":"text","text":"oi"},
 {"id":"m2","from_me":true,"source":"api","type":"text","text":"Olá Ana, como posso ajudar?"},
 {"id":"m2b","from_me":false,"source":"contact","type":"image","text":""},
 {"id":"m3","from_me":false,"source":"contact","type":"text","text":"quero falar com um humano"}]}`

func post(s *server, body, sig, eventID string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("X-LiteZap-Signature", sig)
	r.Header.Set("X-LiteZap-Event-Id", eventID)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestHandoffAnswerAndConversation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := &fakeModel{ans: Answer{Reply: "Já chamo alguém!", Handoff: true, Note: "pediu humano"}}
	s := newServer(m, now)
	w := post(s, reqBody, sign(now, reqBody), "evt_1")
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Reply != "Já chamo alguém!" || len(resp.Actions) != 1 || resp.Actions[0].Type != "handoff" || resp.Actions[0].Note != "pediu humano" {
		t.Fatalf("resp = %+v", resp)
	}
	// Leading assistant turn dropped; the image and the last text merge into one user turn.
	want := []Turn{{false, "oi"}, {true, "Olá Ana, como posso ajudar?"}, {false, "[imagem]\nquero falar com um humano"}}
	if fmt.Sprint(m.turns) != fmt.Sprint(want) {
		t.Fatalf("turns = %+v", m.turns)
	}
	// The same event again is a replay: no second model call.
	m.turns = nil
	if w := post(s, reqBody, sign(now, reqBody), "evt_1"); w.Code != 204 || m.turns != nil {
		t.Fatalf("replay = %d", w.Code)
	}
}

func TestRejectsBadSignatures(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newServer(&fakeModel{}, now)
	for name, sig := range map[string]string{
		"missing":  "",
		"wrong":    sign(now, reqBody+" "),
		"stale":    sign(now.Add(-10*time.Minute), reqBody),
		"future":   sign(now.Add(10*time.Minute), reqBody),
		"garbage":  "t=abc,v1=00",
		"no v1":    fmt.Sprintf("t=%d", now.Unix()),
		"other ts": strings.Replace(sign(now, reqBody), fmt.Sprint(now.Unix()), fmt.Sprint(now.Unix()+1), 1),
	} {
		if w := post(s, reqBody, sig, "e-"+name); w.Code != 401 {
			t.Errorf("%s: status %d", name, w.Code)
		}
	}
}

func TestModelErrorIsAFailure(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newServer(&fakeModel{err: fmt.Errorf("overloaded")}, now)
	if w := post(s, reqBody, sign(now, reqBody), "e1"); w.Code != 502 {
		t.Fatalf("status %d", w.Code)
	}
	s = newServer(&fakeModel{ans: Answer{}}, now)
	if w := post(s, reqBody, sign(now, reqBody), "e2"); w.Code != 204 {
		t.Fatalf("empty answer status %d", w.Code)
	}
}

func TestGroupTurnsNameTheSender(t *testing.T) {
	req := Request{}
	req.Chat.IsGroup = true
	req.Message = ContextMessage{ID: "b", Sender: "x@s.whatsapp.net", PushName: "Bia", Type: "text", Text: "e aí?"}
	req.Context = []ContextMessage{{ID: "a", Sender: "y@s.whatsapp.net", Type: "text", Text: "oi"}}
	turns := conversation(req, secret)
	if len(turns) != 1 || turns[0].Text != "participante "+pseudonym(secret, "y@s.whatsapp.net")[:6]+": oi\nBia: e aí?" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestSystemPromptKeepsBusinessSeparate(t *testing.T) {
	p := systemPrompt("Somos a Padaria X.")
	if !strings.Contains(p, "untrusted content") || !strings.Contains(p, "<business>\nSomos a Padaria X.\n</business>") {
		t.Fatal(p)
	}
}
