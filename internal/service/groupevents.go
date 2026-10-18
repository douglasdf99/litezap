package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// nameRetry is how long a group whose name was looked up is not looked up again.
const nameRetry = 30 * time.Minute

// GroupEvents is a manager observer (marco 8): it publishes group.updated for group
// notifications and keeps group chats named in the log — from subject changes, and by
// looking a group up (in the background, once per nameRetry) when a message arrives in a
// chat that has no name yet.
type GroupEvents struct {
	Store   *store.Store
	Workers GroupWorkers
	Events  events.Publisher
	Log     *slog.Logger

	mu     sync.Mutex
	tried  map[string]time.Time // instance|group -> last lookup
	slots  chan struct{}        // concurrent lookups
	wg     sync.WaitGroup
	closed bool
}

func (o *GroupEvents) log() *slog.Logger {
	if o.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Log
}

func (o *GroupEvents) StateChanged(string, instance.State, string) {}

func (o *GroupEvents) WorkerEvent(instanceID string, ev *workerv1.Event) {
	if u := ev.GetGroup(); u != nil {
		o.onUpdate(instanceID, u)
		return
	}
	if m := ev.GetMessage(); m != nil && m.IsGroup && m.Chat != "" {
		o.ensureName(instanceID, m.Chat)
	}
}

// Close waits for the lookups in flight and stops new ones.
func (o *GroupEvents) Close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.wg.Wait()
}

func (o *GroupEvents) publish(instanceID string, u events.GroupUpdate) {
	if o.Events != nil {
		o.Events.Publish(instanceID, events.TypeGroupUpdated, u)
	}
}

// onUpdate publishes one group.updated per kind of change in u.
func (o *GroupEvents) onUpdate(instanceID string, u *workerv1.GroupUpdate) {
	ts := time.UnixMilli(u.TimestampMs).UTC()
	if u.TimestampMs == 0 {
		ts = time.Now().UTC()
	}
	base := events.GroupUpdate{Chat: u.Group, Actor: u.Actor, Timestamp: ts}
	emit := func(kind string, f func(*events.GroupUpdate)) {
		e := base
		e.Kind = kind
		if f != nil {
			f(&e)
		}
		o.publish(instanceID, e)
	}
	if u.JoinedSelf {
		name := u.GetInfo().GetName()
		emit(events.GroupJoined, func(e *events.GroupUpdate) {
			e.Reason = u.JoinReason
			if name != "" {
				e.Name = &name
			}
		})
		o.setName(instanceID, u.Group, name)
	}
	for _, p := range []struct {
		kind string
		list []string
	}{
		{events.GroupParticipantsAdded, u.Joined}, {events.GroupParticipantsRemoved, u.Left},
		{events.GroupParticipantsPromoted, u.Promoted}, {events.GroupParticipantsDemoted, u.Demoted},
		{events.GroupJoinRequest, u.JoinRequests},
	} {
		if len(p.list) == 0 {
			continue
		}
		emit(p.kind, func(e *events.GroupUpdate) {
			e.Participants = p.list
			if p.kind == events.GroupParticipantsAdded {
				e.Reason = u.JoinReason
			}
		})
	}
	if u.Name != nil {
		name := u.GetName()
		emit(events.GroupSubject, func(e *events.GroupUpdate) { e.Name = &name })
		o.setName(instanceID, u.Group, name)
	}
	if u.Description != nil {
		d := u.GetDescription()
		emit(events.GroupDescription, func(e *events.GroupUpdate) { e.Description = &d })
	}
	if u.PhotoChanged || u.PhotoRemoved {
		emit(events.GroupPhoto, func(e *events.GroupUpdate) { e.Removed = u.PhotoRemoved })
	}
	if u.Announce != nil || u.Locked != nil || u.ApprovalRequired != nil || u.EphemeralSeconds != nil {
		emit(events.GroupSettings, func(e *events.GroupUpdate) {
			e.Settings = &events.GroupSettingsChange{Announce: u.Announce, Locked: u.Locked,
				ApprovalRequired: u.ApprovalRequired, EphemeralSeconds: u.EphemeralSeconds}
		})
	}
	if u.InviteLinkReset {
		emit(events.GroupInviteLinkReset, nil)
	}
	if u.Deleted {
		emit(events.GroupDeleted, nil)
	}
	if u.LinkedGroup != "" {
		emit(events.GroupCommunityLinked, func(e *events.GroupUpdate) { e.Group = u.LinkedGroup })
	}
	if u.UnlinkedGroup != "" {
		emit(events.GroupCommunityUnlinked, func(e *events.GroupUpdate) { e.Group = u.UnlinkedGroup })
	}
	if len(u.Unknown) > 0 {
		o.log().Info("group change not understood", "instance_id", instanceID, "tags", u.Unknown)
	}
}

func (o *GroupEvents) setName(instanceID, group, name string) {
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := o.Store.SetChatName(ctx, instanceID, group, name); err != nil {
		o.log().Warn("set group chat name", "instance_id", instanceID, "err", err)
	}
}

// ensureName looks the group up in the background when its chat has no name yet.
func (o *GroupEvents) ensureName(instanceID, group string) {
	if o.Workers == nil {
		return
	}
	key := instanceID + "|" + group
	now := time.Now()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	if o.tried == nil {
		o.tried = map[string]time.Time{}
		o.slots = make(chan struct{}, 2)
	}
	if t, ok := o.tried[key]; ok && now.Sub(t) < nameRetry {
		o.mu.Unlock()
		return
	}
	if len(o.tried) > 10000 { // bounded memory: forget old attempts
		for k, t := range o.tried {
			if now.Sub(t) >= nameRetry {
				delete(o.tried, k)
			}
		}
	}
	select {
	case o.slots <- struct{}{}:
	default:
		o.mu.Unlock()
		return // busy: the next message retries
	}
	o.tried[key] = now
	o.wg.Add(1)
	o.mu.Unlock()
	go func() {
		defer o.wg.Done()
		defer func() { <-o.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c, err := o.Store.GetChat(ctx, instanceID, group)
		if err != nil || c.Name != "" {
			return
		}
		cli, err := o.Workers.Groups(instanceID)
		if err != nil {
			return
		}
		gi, err := cli.GetGroup(ctx, &workerv1.GetGroupRequest{Jid: group})
		if err != nil {
			o.log().Debug("group name lookup failed", "instance_id", instanceID, "err", err)
			return
		}
		o.setName(instanceID, group, gi.GetName())
	}()
}
