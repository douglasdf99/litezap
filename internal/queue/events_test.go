package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recPub struct {
	mu  sync.Mutex
	evs []struct {
		typ  string
		data any
	}
}

func (r *recPub) Publish(_, typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, struct {
		typ  string
		data any
	}{typ, data})
}

func (r *recPub) len() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.evs) }

// A receipt that arrives before the send confirmation already moved the status past
// "sent": no message.status sent is published after it.
func TestNoSentStatusAfterEarlyReceipt(t *testing.T) {
	pub := &recPub{}
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) { o.Events = pub; o.Resolver = nil })
	e.fake.WA.SetSendFunc(func(req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
		_, _ = e.st.ApplyReceipt(context.Background(), "i1", []string{req.MessageId}, "delivered")
		return &workerv1.SendResponse{MessageId: req.MessageId}, nil
	})
	id := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("sent", func() bool { return e.status(id).Status == store.OutSent })
	time.Sleep(20 * time.Millisecond)
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.evs) != 1 || pub.evs[0].typ != events.TypeMessageSent {
		t.Fatalf("events = %+v", pub.evs)
	}
}

func TestSentAndFailedPublishEvents(t *testing.T) {
	pub := &recPub{}
	e := newEnvAt(t, t0, fastSettings(), instance.Connected, func(o *Options) { o.Events = pub; o.Resolver = nil })
	ok := e.enqueue(chatA, Payload{Type: "text", Text: "oi"})
	e.eventually("sent events", func() bool { return pub.len() == 2 })
	e.fake.WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid_number: not on WhatsApp")
	})
	bad := e.enqueue(chatB, Payload{Type: "text", Text: "x"})
	e.eventually("failed event", func() bool { return pub.len() == 3 })

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if pub.evs[0].typ != events.TypeMessageSent || pub.evs[1].typ != events.TypeMessageStatus || pub.evs[2].typ != events.TypeMessageStatus {
		t.Fatalf("events = %+v", pub.evs)
	}
	m := pub.evs[0].data.(events.Message)
	if m.ID != ok || m.WAMessageID != "WA-"+ok || m.Source != "api" || !m.FromMe || m.Text != "oi" || m.Chat != chatA {
		t.Fatalf("message.sent = %+v", m)
	}
	if s := pub.evs[1].data.(events.MessageStatus); s.ID != ok || s.Status != store.OutSent {
		t.Fatalf("status sent = %+v", s)
	}
	if f := pub.evs[2].data.(events.MessageStatus); f.ID != bad || f.Status != store.OutFailed || f.Error != "invalid_number: not on WhatsApp" {
		t.Fatalf("status failed = %+v", f)
	}
}
