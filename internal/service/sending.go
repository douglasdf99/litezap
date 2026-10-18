package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/store"
)

// Sending is the operator's send suspension (cloud E17), global or per instance: new
// sends are refused with 403 sending_suspended and queued ones are held (the queue asks
// Held before each send) until released. Receiving, webhooks and reads are unaffected.
// A nil *Sending suspends nothing.
type Sending struct {
	Store *store.Store
	// Wake, when set, is called for an instance whose sending was released, so its held
	// queue resumes at once.
	Wake func(instanceID string)

	mu     sync.RWMutex
	global *globalSuspension
	inst   map[string]bool
}

type globalSuspension struct {
	Reason string    `json:"reason,omitempty"`
	Since  time.Time `json:"since"`
}

// SendingStatus is what GET /v1/operator/sending returns.
type SendingStatus struct {
	Suspended bool               `json:"suspended"` // global
	Reason    string             `json:"reason,omitempty"`
	Since     *time.Time         `json:"since,omitempty"`
	Instances []store.Suspension `json:"instances"`
}

const kvSendingSuspended = "sending_suspended"

// Load reads the persisted suspensions; call it before workers start.
func (s *Sending) Load(ctx context.Context) error {
	g, err := s.loadGlobal(ctx)
	if err != nil {
		return err
	}
	list, err := s.Store.ListSuspensions(ctx)
	if err != nil {
		return err
	}
	inst := map[string]bool{}
	for _, x := range list {
		inst[x.InstanceID] = true
	}
	s.mu.Lock()
	s.global, s.inst = g, inst
	s.mu.Unlock()
	return nil
}

func (s *Sending) loadGlobal(ctx context.Context) (*globalSuspension, error) {
	v, err := s.Store.GetKV(ctx, kvSendingSuspended)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g globalSuspension
	if err := json.Unmarshal([]byte(v), &g); err != nil {
		g = globalSuspension{} // unreadable: still suspended
	}
	return &g, nil
}

// Held reports whether the instance may not send now.
func (s *Sending) Held(instanceID string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.global != nil || s.inst[instanceID]
}

// Check returns the API error for a send attempted while held.
func (s *Sending) Check(instanceID string) error {
	if !s.Held(instanceID) {
		return nil
	}
	return apierr.New(http.StatusForbidden, "sending_suspended",
		"sending is suspended for this instance by the operator (receiving still works)", false)
}

func (s *Sending) Status(ctx context.Context) (SendingStatus, error) {
	list, err := s.Store.ListSuspensions(ctx)
	if err != nil {
		return SendingStatus{}, err
	}
	out := SendingStatus{Instances: list}
	s.mu.RLock()
	if g := s.global; g != nil {
		since := g.Since
		out.Suspended, out.Reason, out.Since = true, g.Reason, &since
	}
	s.mu.RUnlock()
	return out, nil
}

func (s *Sending) SetGlobal(ctx context.Context, suspended bool, reason string) error {
	if !suspended {
		if err := s.Store.DeleteKV(ctx, kvSendingSuspended); err != nil {
			return err
		}
		s.mu.Lock()
		s.global = nil
		s.mu.Unlock()
		s.wakeAll(ctx)
		return nil
	}
	g := globalSuspension{Reason: reason, Since: time.Now().UTC()}
	b, _ := json.Marshal(g)
	if err := s.Store.SetKV(ctx, kvSendingSuspended, string(b)); err != nil {
		return err
	}
	s.mu.Lock()
	s.global = &g
	s.mu.Unlock()
	return nil
}

func (s *Sending) SetInstance(ctx context.Context, id string, suspended bool, reason string) error {
	if err := s.Store.SetSendingSuspended(ctx, id, suspended, reason); err != nil {
		return mapErr(err)
	}
	s.mu.Lock()
	if s.inst == nil {
		s.inst = map[string]bool{}
	}
	if suspended {
		s.inst[id] = true
	} else {
		delete(s.inst, id)
	}
	s.mu.Unlock()
	if !suspended && s.Wake != nil {
		s.Wake(id)
	}
	return nil
}

func (s *Sending) wakeAll(ctx context.Context) {
	if s.Wake == nil {
		return
	}
	list, err := s.Store.ListInstances(ctx)
	if err != nil {
		return
	}
	for _, in := range list {
		s.Wake(in.ID)
	}
}

// GuardedMessaging is Messaging with the send suspension applied to new sends (the REST
// API and the MCP tools both go through it).
type GuardedMessaging struct {
	*Messaging
	Sending *Sending
}

func (g GuardedMessaging) Send(ctx context.Context, instanceID string, in SendInput, wait time.Duration) (MessageView, error) {
	if err := g.Sending.Check(instanceID); err != nil {
		return MessageView{}, err
	}
	return g.Messaging.Send(ctx, instanceID, in, wait)
}
