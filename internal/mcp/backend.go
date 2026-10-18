// Package mcp exposes LiteZap to AI clients over the Model Context Protocol (spec
// section 7). The tools are defined once over Backend, which has two implementations:
// Local (the supervisor's own services, served at /mcp) and REST (a client of a running
// `litezap serve`, used by the `litezap mcp` stdio bridge).
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/service"
)

// Backend is what the tools need. Every implementation enforces the caller's scope: an
// instance token only reaches its own instance.
type Backend interface {
	// Scope says who the caller is (admin, or one instance's token).
	Scope(ctx context.Context) (service.Scope, error)
	// ListInstances lists every instance (admin only).
	ListInstances(ctx context.Context) ([]service.InstanceView, error)
	GetInstance(ctx context.Context, id string) (service.InstanceView, error)
	QR(ctx context.Context, id string) (string, error)
	// Send queues a message (always through the paced send queue) and waits up to wait
	// for its outcome.
	Send(ctx context.Context, id string, in service.SendInput, wait time.Duration) (service.MessageView, error)
	GetMessage(ctx context.Context, id, msgID string) (service.MessageView, error)
	Chats(ctx context.Context, id string, limit int, cursor string) (service.ChatPage, error)
	ChatMessages(ctx context.Context, id, chat string, limit int, cursor string) (service.MessagePage, error)
	MarkRead(ctx context.Context, id, chat string) error
	Contacts(ctx context.Context, id string) ([]service.ContactView, error)
	Contact(ctx context.Context, id, jid string) (service.ContactView, error)
	CheckNumbers(ctx context.Context, id string, phones []string) ([]service.NumberView, error)
	// WaitEvents returns the instance's events published from now on, waiting up to
	// timeout for the first one (then briefly for a burst), at most max events.
	WaitEvents(ctx context.Context, id string, types []string, timeout time.Duration, limit int) ([]json.RawMessage, error)
}

// InstanceService is the subset of service.Instances used by Local.
type InstanceService interface {
	List(ctx context.Context) ([]service.InstanceView, error)
	Get(ctx context.Context, id string) (service.InstanceView, error)
	QR(ctx context.Context, id string) (string, error)
}

// MessagingService is the subset of service.Messaging used by Local.
type MessagingService interface {
	Send(ctx context.Context, instanceID string, in service.SendInput, wait time.Duration) (service.MessageView, error)
	GetMessage(ctx context.Context, instanceID, id string) (service.MessageView, error)
	Chats(ctx context.Context, instanceID string, limit int, cursor string) (service.ChatPage, error)
	ChatMessages(ctx context.Context, instanceID, chat string, limit int, cursor string) (service.MessagePage, error)
	MarkRead(ctx context.Context, instanceID, chat string) error
	Contacts(ctx context.Context, instanceID string) ([]service.ContactView, error)
	Contact(ctx context.Context, instanceID, jid string) (service.ContactView, error)
	CheckNumbers(ctx context.Context, instanceID string, phones []string) ([]service.NumberView, error)
}

// Local calls the supervisor's services directly, on behalf of one caller.
type Local struct {
	Instances InstanceService
	Messaging MessagingService
	Events    *events.Bus  // nil: wait_for_events is unavailable
	Groups    GroupService // nil: the group tools answer not_implemented (marco 8)
	Caller    service.Scope
	Account   AccountService // marco 9 tools (nil: they answer not_implemented)
}

var _ Backend = (*Local)(nil)

// burstWindow is how long WaitEvents keeps collecting after the first event.
const burstWindow = time.Second

func (l *Local) allow(id string) error {
	if !l.Caller.Allows(id) {
		return apierr.Forbidden()
	}
	return nil
}

func (l *Local) Scope(context.Context) (service.Scope, error) { return l.Caller, nil }

func (l *Local) ListInstances(ctx context.Context) ([]service.InstanceView, error) {
	if !l.Caller.Admin {
		return nil, apierr.Forbidden()
	}
	return l.Instances.List(ctx)
}

func (l *Local) GetInstance(ctx context.Context, id string) (service.InstanceView, error) {
	if err := l.allow(id); err != nil {
		return service.InstanceView{}, err
	}
	return l.Instances.Get(ctx, id)
}

func (l *Local) QR(ctx context.Context, id string) (string, error) {
	if err := l.allow(id); err != nil {
		return "", err
	}
	return l.Instances.QR(ctx, id)
}

func (l *Local) Send(ctx context.Context, id string, in service.SendInput, wait time.Duration) (service.MessageView, error) {
	if err := l.allow(id); err != nil {
		return service.MessageView{}, err
	}
	return l.Messaging.Send(ctx, id, in, wait)
}

func (l *Local) GetMessage(ctx context.Context, id, msgID string) (service.MessageView, error) {
	if err := l.allow(id); err != nil {
		return service.MessageView{}, err
	}
	return l.Messaging.GetMessage(ctx, id, msgID)
}

func (l *Local) Chats(ctx context.Context, id string, limit int, cursor string) (service.ChatPage, error) {
	if err := l.allow(id); err != nil {
		return service.ChatPage{}, err
	}
	return l.Messaging.Chats(ctx, id, limit, cursor)
}

func (l *Local) ChatMessages(ctx context.Context, id, chat string, limit int, cursor string) (service.MessagePage, error) {
	if err := l.allow(id); err != nil {
		return service.MessagePage{}, err
	}
	return l.Messaging.ChatMessages(ctx, id, chat, limit, cursor)
}

func (l *Local) MarkRead(ctx context.Context, id, chat string) error {
	if err := l.allow(id); err != nil {
		return err
	}
	return l.Messaging.MarkRead(ctx, id, chat)
}

func (l *Local) Contacts(ctx context.Context, id string) ([]service.ContactView, error) {
	if err := l.allow(id); err != nil {
		return nil, err
	}
	return l.Messaging.Contacts(ctx, id)
}

func (l *Local) Contact(ctx context.Context, id, jid string) (service.ContactView, error) {
	if err := l.allow(id); err != nil {
		return service.ContactView{}, err
	}
	return l.Messaging.Contact(ctx, id, jid)
}

func (l *Local) CheckNumbers(ctx context.Context, id string, phones []string) ([]service.NumberView, error) {
	if err := l.allow(id); err != nil {
		return nil, err
	}
	return l.Messaging.CheckNumbers(ctx, id, phones)
}

func (l *Local) WaitEvents(ctx context.Context, id string, types []string, timeout time.Duration, limit int) ([]json.RawMessage, error) {
	if err := l.allow(id); err != nil {
		return nil, err
	}
	if l.Events == nil {
		return nil, apierr.New(http.StatusNotImplemented, "not_implemented", "events are not available on this server", false)
	}
	if _, err := l.Instances.Get(ctx, id); err != nil {
		return nil, err
	}
	sub := l.Events.Subscribe(events.Filter{InstanceID: id, Types: types}, max(256, limit))
	defer sub.Close()
	return collect(ctx, timeout, limit, func(ctx context.Context) (json.RawMessage, bool) {
		select {
		case e, ok := <-sub.C():
			if !ok {
				return nil, false
			}
			return json.RawMessage(e.JSON), true
		case <-ctx.Done():
			return nil, false
		}
	}), nil
}

// collect reads events with next until max are in, timeout passes without any, or
// burstWindow passes after the first one. next returns false when ctx ends or the
// stream is over.
func collect(ctx context.Context, timeout time.Duration, limit int, next func(context.Context) (json.RawMessage, bool)) []json.RawMessage {
	out := []json.RawMessage{}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	end := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, cancel)
	defer func() { timer.Stop() }()
	for len(out) < limit {
		ev, ok := next(wctx)
		if !ok {
			break
		}
		if len(out) == 0 { // a reply often comes in a burst: give it a moment
			if timer.Stop() {
				timer = time.AfterFunc(min(burstWindow, time.Until(end)), cancel)
			}
		}
		out = append(out, ev)
	}
	return out
}
