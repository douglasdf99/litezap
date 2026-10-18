package agenthook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// View is the config as the API shows it: the secret is never returned, except right
// after it was generated (Secret then holds it, once).
type View struct {
	Configured          bool     `json:"configured"`
	URL                 string   `json:"url"`
	Enabled             bool     `json:"enabled"`
	Timeout             Duration `json:"timeout"`
	ContextMessages     int      `json:"context_messages"`
	GroupsEnabled       bool     `json:"groups_enabled"`
	HumanPause          Duration `json:"human_pause"`
	PauseOnAPISend      bool     `json:"pause_on_api_send"`
	Ignore              []string `json:"ignore"`
	MaxRepliesPerWindow int      `json:"max_replies_per_window"`
	ReplyWindow         Duration `json:"reply_window"`
	SecretSet           bool     `json:"secret_set"`
	Secret              string   `json:"secret,omitempty"`
}

func view(c Config, configured bool) View {
	return View{Configured: configured, URL: c.URL, Enabled: c.Enabled, Timeout: c.Timeout,
		ContextMessages: c.ContextMessages, GroupsEnabled: c.GroupsEnabled, HumanPause: c.HumanPause,
		PauseOnAPISend: c.PauseOnAPISend, Ignore: c.Ignore, MaxRepliesPerWindow: c.MaxRepliesPerWindow, ReplyWindow: c.ReplyWindow,
		SecretSet: c.Secret != ""}
}

// ChatView is a chat's agent state.
type ChatView struct {
	JID         string     `json:"jid"`
	Name        string     `json:"name,omitempty"`
	IsGroup     bool       `json:"is_group"`
	Enabled     *bool      `json:"enabled"` // null: inherits the instance default
	PausedUntil *time.Time `json:"paused_until,omitempty"`
}

// CallView is one entry of the call log.
type CallView struct {
	ID         string    `json:"id"`
	Chat       string    `json:"chat"`
	MessageID  string    `json:"message_id,omitempty"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Replied    bool      `json:"replied"`
	ReplyID    string    `json:"reply_id,omitempty"`
	Actions    []string  `json:"actions"`
	CreatedAt  time.Time `json:"created_at"`
}

// Admin holds the agent hook use cases of the REST API.
type Admin struct {
	Store *store.Store
	Clock func() time.Time // default time.Now

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (a *Admin) now() time.Time {
	if a.Clock != nil {
		return a.Clock()
	}
	return time.Now()
}

// lock serializes read-merge-write of an instance's config.
func (a *Admin) lock(instanceID string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locks == nil {
		a.locks = map[string]*sync.Mutex{}
	}
	l := a.locks[instanceID]
	if l == nil {
		l = &sync.Mutex{}
		a.locks[instanceID] = l
	}
	return l
}

func (a *Admin) instance(ctx context.Context, id string) error {
	if _, err := a.Store.GetInstance(ctx, id); errors.Is(err, store.ErrNotFound) {
		return apierr.InstanceNotFound()
	} else if err != nil {
		return err
	}
	return nil
}

func (a *Admin) load(ctx context.Context, instanceID string) (Config, bool, error) {
	js, err := a.Store.GetAgentHook(ctx, instanceID)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultConfig(), false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	c, err := parseConfig(js)
	return c, true, err
}

// Get returns the config (defaults with configured=false when there is none).
func (a *Admin) Get(ctx context.Context, instanceID string) (View, error) {
	if err := a.instance(ctx, instanceID); err != nil {
		return View{}, err
	}
	c, ok, err := a.load(ctx, instanceID)
	if err != nil {
		return View{}, err
	}
	return view(c, ok), nil
}

// Put applies a patch to the config (creating it); a missing secret is generated and
// returned once in the view.
func (a *Admin) Put(ctx context.Context, instanceID string, p Patch) (View, error) {
	if err := a.instance(ctx, instanceID); err != nil {
		return View{}, err
	}
	l := a.lock(instanceID)
	l.Lock()
	defer l.Unlock()
	cur, _, err := a.load(ctx, instanceID)
	if err != nil {
		return View{}, err
	}
	c := p.Apply(cur)
	generated := ""
	if c.Secret == "" {
		generated = NewSecret()
		c.Secret = generated
	}
	if err := c.Validate(service.ChatJID); err != nil {
		return View{}, apierr.Validation(err.Error())
	}
	b, _ := json.Marshal(c)
	if err := a.Store.PutAgentHook(ctx, instanceID, string(b)); err != nil {
		return View{}, err
	}
	v := view(c, true)
	v.Secret = generated
	return v, nil
}

// Delete removes the config (the hook stops); per-chat overrides are kept.
func (a *Admin) Delete(ctx context.Context, instanceID string) error {
	if err := a.instance(ctx, instanceID); err != nil {
		return err
	}
	if err := a.Store.DeleteAgentHook(ctx, instanceID); errors.Is(err, store.ErrNotFound) {
		return apierr.New(http.StatusNotFound, "not_found", "agent hook not configured", false)
	} else if err != nil {
		return err
	}
	return nil
}

func chatView(c store.ChatAgent, now time.Time) ChatView {
	v := ChatView{JID: c.JID, Name: c.Name, IsGroup: c.IsGroup, Enabled: c.Enabled}
	if c.PausedUntil.After(now) {
		t := c.PausedUntil
		v.PausedUntil = &t
	}
	return v
}

// SetChat sets a chat's override: true or false, or nil to inherit the instance default.
// true and nil also end a pause (re-activation after a handoff or a human reply).
func (a *Admin) SetChat(ctx context.Context, instanceID, chatRaw string, enabled *bool) (ChatView, error) {
	if err := a.instance(ctx, instanceID); err != nil {
		return ChatView{}, err
	}
	chat, err := service.ChatJID(chatRaw)
	if err != nil {
		return ChatView{}, err
	}
	if err := a.Store.SetChatAgent(ctx, instanceID, chat, enabled, enabled == nil || *enabled); err != nil {
		return ChatView{}, err
	}
	c, err := a.Store.GetChatAgent(ctx, instanceID, chat)
	if err != nil {
		return ChatView{}, err
	}
	return chatView(c, a.now()), nil
}

// Chats lists the chats with an override or a running pause.
func (a *Admin) Chats(ctx context.Context, instanceID string) ([]ChatView, error) {
	if err := a.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	now := a.now()
	list, err := a.Store.ListChatAgents(ctx, instanceID, now, 500)
	if err != nil {
		return nil, err
	}
	out := make([]ChatView, 0, len(list))
	for _, c := range list {
		out = append(out, chatView(c, now))
	}
	return out, nil
}

// Calls returns the most recent hook calls (at most limit, default 50, max 200).
func (a *Admin) Calls(ctx context.Context, instanceID string, limit int) ([]CallView, error) {
	if err := a.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	list, err := a.Store.ListAgentCalls(ctx, instanceID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]CallView, 0, len(list))
	for _, c := range list {
		v := CallView{ID: c.ID, Chat: c.ChatJID, MessageID: c.MessageID, Status: c.Status, Reason: c.Reason,
			Detail: c.Detail, HTTPStatus: c.HTTPStatus, DurationMS: c.DurationMS, Replied: c.Replied,
			ReplyID: c.ReplyID, Actions: []string{}, CreatedAt: c.CreatedAt}
		if c.Actions != "" {
			v.Actions = strings.Split(c.Actions, ",")
		}
		out = append(out, v)
	}
	return out, nil
}
