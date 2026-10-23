package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Account holds the marco 9 use cases: chat actions through the app state, contact
// lookups, the blocklist, the account profile and the privacy settings. It shares the
// store, the workers and the media directory with Messaging.
type Account struct {
	Messaging *Messaging
	// Store, when set, is used before Messaging exists (the observer starts receiving
	// events when the workers boot); otherwise Messaging.Store.
	Store *store.Store
	// Sending, when set, refuses lookups about third parties while the operator suspends
	// sending (like check-numbers and presence subscriptions).
	Sending *Sending
	// Events receives chat.updated (nil: not published).
	Events events.Publisher
	// Groups changes a group's disappearing messages timer (nil: only direct chats).
	Groups *Groups

	lookupQuota slidingQuota // contact pictures and profiles, per instance
	pictures    pictureCache
	actionMu    sync.Mutex
	actionLocks map[string]*sync.Mutex // per instance and chat: API actions applied in order
	// pending marks API actions in flight (instance, chat, field): their app state echo
	// may arrive before the RPC returns and is then reported with source "api".
	pending map[string]int
}

func pendingKey(instanceID, chat, field string) string {
	return instanceID + "\x00" + chat + "\x00" + field
}

func (a *Account) markPending(k string, delta int) {
	a.actionMu.Lock()
	defer a.actionMu.Unlock()
	if a.pending == nil {
		a.pending = map[string]int{}
	}
	if a.pending[k] += delta; a.pending[k] <= 0 {
		delete(a.pending, k)
	}
}

func (a *Account) isPending(k string) bool {
	a.actionMu.Lock()
	defer a.actionMu.Unlock()
	return a.pending[k] > 0
}

// Limits of network lookups about contacts, per instance (spec M9: a per-minute quota).
const (
	lookupsPerMinute = 30
	lookupsPerDay    = 1000
	maxPinnedChats   = 3
	maxMuteSeconds   = 366 * 24 * 3600
)

// slidingQuota counts calls per instance over a sliding minute and a rolling day.
type slidingQuota struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (q *slidingQuota) take(instanceID string, now time.Time, perMinute, perDay int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.hits == nil {
		q.hits = map[string][]time.Time{}
	}
	var keep []time.Time
	minute := 0
	for _, t := range q.hits[instanceID] {
		if now.Sub(t) < 24*time.Hour {
			keep = append(keep, t)
			if now.Sub(t) < time.Minute {
				minute++
			}
		}
	}
	if minute >= perMinute || len(keep) >= perDay {
		q.hits[instanceID] = keep
		return false
	}
	q.hits[instanceID] = append(keep, now)
	return true
}

func (a *Account) st() *store.Store {
	if a.Store != nil {
		return a.Store
	}
	return a.Messaging.Store
}

func (a *Account) chatLock(instanceID, chat string) *sync.Mutex {
	a.actionMu.Lock()
	defer a.actionMu.Unlock()
	if a.actionLocks == nil {
		a.actionLocks = map[string]*sync.Mutex{}
	}
	k := instanceID + "\x00" + chat
	mu := a.actionLocks[k]
	if mu == nil {
		mu = &sync.Mutex{}
		a.actionLocks[k] = mu
	}
	return mu
}

// accountErr maps worker errors of the account RPCs, then falls back to mapErr.
func accountErr(err error) error {
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		code, detail, _ := strings.Cut(st.Message(), ": ")
		switch {
		case st.Code() == codes.NotFound && code == "not_found":
			return apierr.New(http.StatusNotFound, "not_found", redact(detail), false)
		case st.Code() == codes.FailedPrecondition && code == "app_state_not_synced":
			return apierr.New(http.StatusConflict, "app_state_not_synced", detail, true)
		case st.Code() == codes.PermissionDenied:
			return apierr.New(http.StatusForbidden, "forbidden", redact(detail), false)
		case st.Code() == codes.Unimplemented:
			return apierr.New(http.StatusNotImplemented, "not_supported", "this worker does not support the operation", false)
		}
	}
	return mapErr(err)
}

// ChatView is a chat of the log with its app state (marco 9).
type ChatView struct {
	JID           string    `json:"jid"`
	Name          string    `json:"name,omitempty"`
	IsGroup       bool      `json:"is_group"`
	LastMessageAt time.Time `json:"last_message_at"`
	UnreadCount   int       `json:"unread_count"`
	Archived      bool      `json:"archived"`
	Pinned        bool      `json:"pinned"`
	Muted         bool      `json:"muted"`
	// MutedUntil is set while muted with an end; muted without it means until unmuted.
	MutedUntil *time.Time `json:"muted_until,omitempty"`
	// Unread: marked as unread by hand (unread_count counts received messages).
	Unread    bool       `json:"unread"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	// Disappearing is the disappearing messages timer when known (off | 24h | 7d | 90d),
	// with DisappearingSeconds; both absent when LiteZap has not learned it yet.
	Disappearing        string  `json:"disappearing,omitempty"`
	DisappearingSeconds *uint32 `json:"disappearing_seconds,omitempty"`
	// DisappearingQueued is set by PATCH when the change to a direct chat is still in the
	// send queue (it is a message to the contact): the timer changes once it is sent.
	DisappearingQueued *QueuedChange `json:"disappearing_queued,omitempty"`
}

// QueuedChange is a chat change that waits in the send queue.
type QueuedChange struct {
	MessageID    string `json:"message_id"` // follow it with GET /messages/{id}
	Status       string `json:"status"`
	Disappearing string `json:"disappearing"`
}

func chatView(c store.Chat) ChatView {
	v := ChatView{JID: c.JID, Name: c.Name, IsGroup: c.IsGroup, LastMessageAt: c.LastMessageAt,
		UnreadCount: c.UnreadCount, Archived: c.Archived, Pinned: c.Pinned, Unread: c.MarkedUnread}
	if c.Muted(time.Now()) {
		v.Muted = true
		if !c.MutedUntil.Equal(store.MutedForever) {
			t := c.MutedUntil
			v.MutedUntil = &t
		}
	}
	if !c.DeletedAt.IsZero() {
		t := c.DeletedAt
		v.DeletedAt = &t
	}
	if !c.DisappearingAt.IsZero() {
		s := c.DisappearingSeconds
		v.Disappearing, v.DisappearingSeconds = events.DisappearingLabel(s), &s
	}
	return v
}

func (a *Account) chatUpdated(_ string, c store.Chat, changed []string, source string) events.ChatUpdated {
	return queue.ChatUpdatedEvent(c, changed, source, time.Now())
}

func (a *Account) publish(instanceID string, ev events.ChatUpdated) {
	if a.Events != nil {
		a.Events.Publish(instanceID, events.TypeChatUpdated, ev)
	}
}

// ApplyChange records a chat state change and publishes chat.updated when it changed
// something (the app state echo of an API action changes nothing). Chats that are not in
// the log are ignored.
func (a *Account) ApplyChange(ctx context.Context, instanceID, chat string, ch store.ChatChange, source string) (store.Chat, error) {
	c, changed, err := a.st().ApplyChatChange(ctx, instanceID, chat, ch)
	if err != nil {
		return c, err
	}
	if changed {
		a.publish(instanceID, a.chatUpdated(instanceID, c, []string{ch.Field}, source))
	}
	return c, nil
}

func (a *Account) loadChat(ctx context.Context, instanceID, raw string) (store.Chat, error) {
	if _, err := a.st().GetInstance(ctx, instanceID); err != nil {
		return store.Chat{}, mapErr(err)
	}
	jid, err := ChatJID(raw)
	if err != nil {
		return store.Chat{}, err
	}
	c, err := a.st().GetChat(ctx, instanceID, jid)
	if errors.Is(err, store.ErrNotFound) {
		return c, notFound("chat")
	}
	return c, err
}

// GetChat returns a chat with its app state. While the worker runs, the state is first
// reconciled with the worker's local copy (it also holds what the initial full sync set,
// which produces no events).
func (a *Account) GetChat(ctx context.Context, instanceID, raw string) (ChatView, error) {
	c, err := a.loadChat(ctx, instanceID, raw)
	if err != nil {
		return ChatView{}, err
	}
	if cli, err := a.Messaging.client(instanceID); err == nil {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cs, err := cli.GetChatSettings(rctx, &workerv1.GetChatSettingsRequest{Chat: c.JID})
		cancel()
		if err == nil && cs.Found {
			c = a.reconcile(ctx, instanceID, c, cs)
		}
	}
	return chatView(c), nil
}

func (a *Account) reconcile(ctx context.Context, instanceID string, c store.Chat, cs *workerv1.GetChatSettingsResponse) store.Chat {
	var muted time.Time
	switch {
	case cs.MutedUntilMs < 0:
		muted = store.MutedForever
	case cs.MutedUntilMs > 0:
		muted = time.UnixMilli(cs.MutedUntilMs)
	}
	changes := []store.ChatChange{}
	if cs.Archived != c.Archived {
		changes = append(changes, store.ChatChange{Field: "archived", Value: cs.Archived})
	}
	if cs.Pinned != c.Pinned {
		changes = append(changes, store.ChatChange{Field: "pinned", Value: cs.Pinned})
	}
	if c.Muted(time.Now()) != !muted.IsZero() || (!muted.IsZero() && !muted.Equal(c.MutedUntil)) {
		changes = append(changes, store.ChatChange{Field: "muted", Value: !muted.IsZero(), MutedUntil: muted})
	}
	for _, ch := range changes {
		if next, _, err := a.st().ApplyChatChange(ctx, instanceID, c.JID, ch); err == nil {
			c = next
		}
	}
	return c
}

// ChatPatch changes a chat's app state; nil fields are left alone.
type ChatPatch struct {
	Archived *bool `json:"archived,omitempty"`
	Pinned   *bool `json:"pinned,omitempty"`
	Muted    *bool `json:"muted,omitempty"`
	// MuteSeconds bounds a mute (with muted: true); 0 or absent mutes until unmuted.
	MuteSeconds *int `json:"mute_seconds,omitempty"`
	// Unread marks the chat as unread (true) or reads it (false: also sends read receipts).
	Unread *bool `json:"unread,omitempty"`
	// Disappearing sets the disappearing messages timer: off | 24h | 7d | 90d. In a direct
	// chat it is a message to the contact (it goes through the send queue); in a group it
	// changes the group setting (admins only when the group is locked).
	Disappearing *string `json:"disappearing,omitempty"`
}

func lastRef(m store.Message) *workerv1.MessageRef {
	return &workerv1.MessageRef{Id: m.WAMessageID, FromMe: m.FromMe, Sender: m.SenderJID, TimestampMs: m.Timestamp.UnixMilli()}
}

// UpdateChat applies a ChatPatch on WhatsApp (the phone follows through the app state)
// and in the log. Fields apply in the order unread, archived, pinned, muted; a failure
// stops there (earlier fields stay applied).
func (a *Account) UpdateChat(ctx context.Context, instanceID, raw string, p ChatPatch) (ChatView, error) {
	if p.Archived == nil && p.Pinned == nil && p.Muted == nil && p.Unread == nil && p.Disappearing == nil {
		return ChatView{}, apierr.Validation("nothing to change: set archived, pinned, muted, unread or disappearing")
	}
	var disappearing uint32
	if p.Disappearing != nil {
		s, ok := events.DisappearingSeconds(*p.Disappearing)
		if !ok {
			return ChatView{}, apierr.Validation(`disappearing must be "off", "24h", "7d" or "90d"`)
		}
		disappearing = s
	}
	if p.MuteSeconds != nil {
		switch {
		case p.Muted == nil || !*p.Muted:
			return ChatView{}, apierr.Validation("mute_seconds needs muted: true")
		case *p.MuteSeconds < 0 || *p.MuteSeconds > maxMuteSeconds:
			return ChatView{}, apierr.Validation("mute_seconds must be between 0 and 31622400 (0: until unmuted)")
		}
	}
	if p.Archived != nil && p.Pinned != nil && *p.Archived && *p.Pinned {
		return ChatView{}, apierr.Validation("an archived chat cannot be pinned")
	}
	c, err := a.loadChat(ctx, instanceID, raw)
	if err != nil {
		return ChatView{}, err
	}
	mu := a.chatLock(instanceID, c.JID)
	mu.Lock()
	defer mu.Unlock()
	if p.Pinned != nil && *p.Pinned && !c.Pinned {
		n, err := a.st().PinnedChats(ctx, instanceID)
		if err != nil {
			return ChatView{}, err
		}
		if n >= maxPinnedChats {
			return ChatView{}, apierr.New(http.StatusConflict, "pin_limit", "WhatsApp allows at most 3 pinned chats; unpin one first", false)
		}
	}
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return ChatView{}, err
	}
	var last *workerv1.MessageRef
	if m, err := a.st().LastChatMessage(ctx, instanceID, c.JID); err == nil {
		last = lastRef(m)
	}
	do := func(req *workerv1.ChatActionRequest, ch store.ChatChange) error {
		req.Chat = c.JID
		k := pendingKey(instanceID, c.JID, ch.Field)
		a.markPending(k, 1)
		defer a.markPending(k, -1)
		if req.Action == "archive" { // archiving also unpins: its pin echo is ours too
			kp := pendingKey(instanceID, c.JID, "pinned")
			a.markPending(kp, 1)
			defer a.markPending(kp, -1)
		}
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		defer cancel()
		if _, err := cli.ChatAction(rctx, req); err != nil {
			return accountErr(err)
		}
		next, err := a.ApplyChange(ctx, instanceID, c.JID, ch, "api")
		if err == nil {
			c = next
		}
		return err
	}
	if p.Unread != nil {
		if !*p.Unread {
			// Reading also sends the read receipts and clears the unread count.
			if err := a.Messaging.MarkRead(ctx, instanceID, c.JID); err != nil {
				return ChatView{}, err
			}
		}
		action := "read"
		if *p.Unread {
			action = "unread"
		}
		if err := do(&workerv1.ChatActionRequest{Action: action, Message: last}, store.ChatChange{Field: "unread", Value: *p.Unread}); err != nil {
			return ChatView{}, err
		}
	}
	if p.Archived != nil {
		action := "unarchive"
		if *p.Archived {
			action = "archive"
		}
		if err := do(&workerv1.ChatActionRequest{Action: action, Message: last}, store.ChatChange{Field: "archived", Value: *p.Archived}); err != nil {
			return ChatView{}, err
		}
	}
	if p.Pinned != nil {
		action := "unpin"
		if *p.Pinned {
			action = "pin"
		}
		if err := do(&workerv1.ChatActionRequest{Action: action}, store.ChatChange{Field: "pinned", Value: *p.Pinned}); err != nil {
			return ChatView{}, err
		}
	}
	if p.Muted != nil {
		req := &workerv1.ChatActionRequest{Action: "unmute"}
		ch := store.ChatChange{Field: "muted"}
		if *p.Muted {
			req.Action, ch.Value = "mute", true
			if p.MuteSeconds != nil && *p.MuteSeconds > 0 {
				until := time.Now().Add(time.Duration(*p.MuteSeconds) * time.Second).Truncate(time.Millisecond)
				req.MuteUntilMs, ch.MutedUntil = until.UnixMilli(), until
			}
		}
		if err := do(req, ch); err != nil {
			return ChatView{}, err
		}
	}
	if p.Disappearing != nil {
		return a.setDisappearing(ctx, instanceID, c, disappearing)
	}
	return chatView(c), nil
}

// disappearingWait bounds how long PATCH waits for the queued timer change of a direct chat.
const disappearingWait = 30 * time.Second

// setDisappearing changes a chat's disappearing messages timer: through the group
// settings for a group, through the send queue (a protocol message to the contact, with
// the queue's pacing) for a direct chat.
func (a *Account) setDisappearing(ctx context.Context, instanceID string, c store.Chat, seconds uint32) (ChatView, error) {
	if c.IsGroup || strings.HasSuffix(c.JID, "@g.us") {
		if a.Groups == nil {
			return ChatView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "group settings are not available", false)
		}
		s := seconds
		if _, err := a.Groups.Update(ctx, instanceID, c.JID, UpdateGroupInput{GroupSettingsInput: GroupSettingsInput{EphemeralSeconds: &s}}); err != nil {
			return ChatView{}, err
		}
		if changed, err := a.st().SetChatDisappearing(ctx, instanceID, c.JID, seconds, time.Now(), false); err != nil {
			return ChatView{}, err
		} else if next, err := a.st().GetChat(ctx, instanceID, c.JID); err == nil {
			c = next
			if changed {
				a.publish(instanceID, a.chatUpdated(instanceID, c, []string{"disappearing"}, "api"))
			}
		}
		return chatView(c), nil
	}
	s := seconds
	v, err := a.Messaging.send(ctx, instanceID, SendInput{Type: "disappearing", disappearing: &s}, c.JID, disappearingWait)
	if err != nil {
		return ChatView{}, err
	}
	if next, err := a.st().GetChat(ctx, instanceID, c.JID); err == nil {
		c = next
	}
	out := chatView(c)
	if v.Status != store.OutSent {
		out.DisappearingQueued = &QueuedChange{MessageID: v.ID, Status: v.Status, Disappearing: events.DisappearingLabel(seconds)}
	}
	return out, nil
}

// DeleteChat deletes a chat on WhatsApp (all devices). The log keeps its messages; the
// chat leaves GET /chats until a newer message arrives.
func (a *Account) DeleteChat(ctx context.Context, instanceID, raw string, deleteMedia bool) error {
	c, err := a.loadChat(ctx, instanceID, raw)
	if err != nil {
		return err
	}
	mu := a.chatLock(instanceID, c.JID)
	mu.Lock()
	defer mu.Unlock()
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return err
	}
	k := pendingKey(instanceID, c.JID, "deleted")
	a.markPending(k, 1)
	defer a.markPending(k, -1)
	req := &workerv1.ChatActionRequest{Chat: c.JID, Action: "delete", DeleteMedia: deleteMedia}
	if m, err := a.st().LastChatMessage(ctx, instanceID, c.JID); err == nil {
		req.Message = lastRef(m)
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.ChatAction(rctx, req); err != nil {
		return accountErr(err)
	}
	// The chat's newest message time (WhatsApp's clock) hides it, whatever the local clock says.
	at := time.Now()
	if req.Message != nil && req.Message.TimestampMs > 0 {
		at = time.UnixMilli(req.Message.TimestampMs)
	}
	_, err = a.ApplyChange(ctx, instanceID, c.JID, store.ChatChange{Field: "deleted", Value: true, At: at}, "api")
	return err
}

// ClearInput is the body of POST /chats/{jid}/clear.
type ClearInput struct {
	// KeepStarred keeps the starred messages (on WhatsApp and in the log).
	KeepStarred bool `json:"keep_starred,omitempty"`
	// DeleteMedia also deletes the chat's media files from the phone's gallery.
	DeleteMedia bool `json:"delete_media,omitempty"`
}

// ClearResult is the answer of a clear: the chat (kept) and how many messages left the log.
type ClearResult struct {
	Chat            ChatView `json:"chat"`
	DeletedMessages int64    `json:"deleted_messages"`
}

// ClearChat clears a chat's messages on WhatsApp for this account (phone and linked
// devices; the contact keeps theirs) and deletes them from the log, keeping the chat. It
// is destructive and cannot be undone. Messages still in the send queue are kept.
func (a *Account) ClearChat(ctx context.Context, instanceID, raw string, in ClearInput) (ClearResult, error) {
	c, err := a.loadChat(ctx, instanceID, raw)
	if err != nil {
		return ClearResult{}, err
	}
	mu := a.chatLock(instanceID, c.JID)
	mu.Lock()
	defer mu.Unlock()
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return ClearResult{}, err
	}
	k := pendingKey(instanceID, c.JID, "cleared")
	a.markPending(k, 1)
	defer a.markPending(k, -1)
	req := &workerv1.ChatActionRequest{Chat: c.JID, Action: "clear", KeepStarred: in.KeepStarred, DeleteMedia: in.DeleteMedia}
	upTo := time.Now()
	if m, err := a.st().LastChatMessage(ctx, instanceID, c.JID); err == nil {
		req.Message = lastRef(m)
		upTo = m.Timestamp
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.ChatAction(rctx, req); err != nil {
		return ClearResult{}, accountErr(err)
	}
	n, err := a.st().ClearChatMessages(ctx, instanceID, c.JID, upTo, in.KeepStarred)
	if err != nil {
		return ClearResult{}, err
	}
	if next, err := a.st().GetChat(ctx, instanceID, c.JID); err == nil {
		c = next
	}
	ev := a.chatUpdated(instanceID, c, []string{"cleared"}, "api")
	ev.Cleared = true
	a.publish(instanceID, ev)
	return ClearResult{Chat: chatView(c), DeletedMessages: n}, nil
}

// StarMessage stars or unstars a logged message on WhatsApp (all devices).
func (a *Account) StarMessage(ctx context.Context, instanceID, id string, starred bool) (MessageView, error) {
	if _, err := a.st().GetInstance(ctx, instanceID); err != nil {
		return MessageView{}, mapErr(err)
	}
	m, err := a.st().GetMessage(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("message")
	}
	if err != nil {
		return MessageView{}, err
	}
	if m.WAMessageID == "" || m.Type == "reaction" {
		return MessageView{}, apierr.Validation("this message cannot be starred")
	}
	cli, err := a.Messaging.client(instanceID)
	if err != nil {
		return MessageView{}, err
	}
	action := "unstar"
	if starred {
		action = "star"
	}
	k := pendingKey(instanceID, m.ChatJID, "starred")
	a.markPending(k, 1)
	defer a.markPending(k, -1)
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.ChatAction(rctx, &workerv1.ChatActionRequest{Chat: m.ChatJID, Action: action, Message: lastRef(m)}); err != nil {
		return MessageView{}, accountErr(err)
	}
	if err := a.applyStar(ctx, instanceID, m.ChatJID, m.WAMessageID, starred, "api"); err != nil {
		return MessageView{}, err
	}
	m, err = a.st().GetMessage(ctx, instanceID, id)
	if err != nil {
		return MessageView{}, err
	}
	return messageView(instanceID, m), nil
}

// applyStar records a star change and publishes chat.updated when it changed something.
func (a *Account) applyStar(ctx context.Context, instanceID, chat, waID string, starred bool, source string) error {
	changed, err := a.st().SetStarred(ctx, instanceID, chat, waID, starred)
	if err != nil || !changed {
		return err
	}
	m, err := a.st().MessageByWAID(ctx, instanceID, chat, waID)
	if err != nil {
		return nil
	}
	ev := events.ChatUpdated{Chat: chat, IsGroup: strings.HasSuffix(chat, "@g.us"), Changed: []string{"starred"},
		MessageID: m.ID, WAMessageID: waID, Starred: &starred, Source: source, Timestamp: time.Now().UTC()}
	if c, err := a.st().GetChat(ctx, instanceID, chat); err == nil {
		full := a.chatUpdated(instanceID, c, ev.Changed, source)
		full.MessageID, full.WAMessageID, full.Starred = ev.MessageID, ev.WAMessageID, ev.Starred
		ev = full
	}
	a.publish(instanceID, ev)
	return nil
}

// applySilently records a full-sync chat state without publishing chat.updated.
func (a *Account) applySilently(ctx context.Context, instanceID string, u *workerv1.ChatUpdate) {
	switch u.Field {
	case "starred":
		_, _ = a.st().SetStarred(ctx, instanceID, u.Chat, u.MessageId, u.Value)
	case "archived", "pinned", "unread":
		_, _, _ = a.st().ApplyChatChange(ctx, instanceID, u.Chat, store.ChatChange{Field: u.Field, Value: u.Value})
	case "muted":
		ch := store.ChatChange{Field: "muted", Value: u.Value}
		if u.Value && u.MutedUntilMs > 0 {
			ch.MutedUntil = time.UnixMilli(u.MutedUntilMs)
		}
		_, _, _ = a.st().ApplyChatChange(ctx, instanceID, u.Chat, ch)
	}
}

// StateChanged implements manager.Observer (nothing to do).
func (a *Account) StateChanged(string, instance.State, string) {}

// WorkerEvent applies app state changes made on the phone (or echoed from our own
// actions) to the log; it implements manager.Observer. It never waits for API actions.
func (a *Account) WorkerEvent(instanceID string, ev *workerv1.Event) {
	u := ev.GetChatUpdate()
	if u == nil || u.Chat == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := "device"
	if a.isPending(pendingKey(instanceID, u.Chat, u.Field)) {
		source = "api"
	}
	if u.FullSync {
		// The whole app state again (pairing, recovery): record it without an event per chat.
		a.applySilently(ctx, instanceID, u)
		return
	}
	switch u.Field {
	case "starred":
		_ = a.applyStar(ctx, instanceID, u.Chat, u.MessageId, u.Value, source)
		return
	case "disappearing":
		// The contact (or another device of the account) changed the timer of a direct chat.
		at := time.Now()
		if u.TimestampMs > 0 {
			at = time.UnixMilli(u.TimestampMs)
		}
		changed, err := a.st().SetChatDisappearing(ctx, instanceID, u.Chat, u.DisappearingSeconds, at, true)
		if err != nil || !changed {
			return
		}
		src := "contact"
		if u.FromMe {
			src = "device"
		}
		if c, err := a.st().GetChat(ctx, instanceID, u.Chat); err == nil {
			a.publish(instanceID, a.chatUpdated(instanceID, c, []string{"disappearing"}, src))
		}
		return
	case "cleared":
		// Cleared on the phone: reported, the log keeps the messages (only POST
		// /chats/{jid}/clear deletes them). The echo of our own clear was already reported.
		if source == "api" {
			return
		}
		if c, err := a.st().GetChat(ctx, instanceID, u.Chat); err == nil {
			ev := a.chatUpdated(instanceID, c, []string{"cleared"}, "device")
			ev.Cleared = true
			a.publish(instanceID, ev)
		}
		return
	}
	ch := store.ChatChange{Field: u.Field, Value: u.Value}
	switch u.Field {
	case "archived", "pinned", "unread":
	case "muted":
		switch {
		case !u.Value:
		case u.MutedUntilMs < 0:
			ch.MutedUntil = store.MutedForever
		case u.MutedUntilMs > 0:
			ch.MutedUntil = time.UnixMilli(u.MutedUntilMs)
		}
	case "deleted":
		if u.TimestampMs > 0 {
			ch.At = time.UnixMilli(u.TimestampMs)
		}
	default:
		return
	}
	_, _ = a.ApplyChange(ctx, instanceID, u.Chat, ch, source)
}
