package realtime

import (
	"context"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
)

type sendRec struct {
	mu   sync.Mutex
	sent []service.SendInput
}

func (s *sendRec) Send(_ context.Context, _ string, in service.SendInput, _ time.Duration) (service.MessageView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, in)
	return service.MessageView{}, nil
}

// The built-in attendant's follow-up replaces the static reject message, within the
// per-caller cooldown; when it declines, the static message goes out.
func TestRejectFollowUp(t *testing.T) {
	h, _ := newHandler(settings.Defaults(), clock.NewFake(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)))
	sr := &sendRec{}
	h.SetSender(sr)
	var asked []string
	take := true
	h.FollowUp = func(id, caller, callID, fallback string) bool {
		asked = append(asked, callID+"|"+fallback)
		return take
	}
	c := &workerv1.Call{CallId: "C1", Caller: "5511@s.whatsapp.net"}
	h.reply("i1", c, "static")
	h.reply("i1", &workerv1.Call{CallId: "C2", Caller: c.Caller}, "static") // cooldown: not asked
	if len(asked) != 1 || asked[0] != "C1|static" || len(sr.sent) != 0 {
		t.Fatalf("asked %v sent %v", asked, sr.sent)
	}
	take = false
	h.reply("i1", &workerv1.Call{CallId: "C3", Caller: "5522@s.whatsapp.net"}, "static")
	if len(sr.sent) != 1 || sr.sent[0].Text != "static" {
		t.Fatalf("sent %v", sr.sent)
	}
	// No static message and a declined follow-up: nothing is sent.
	h.reply("i1", &workerv1.Call{CallId: "C4", Caller: "5533@s.whatsapp.net"}, "")
	if len(sr.sent) != 1 {
		t.Fatalf("sent %v", sr.sent)
	}
}
