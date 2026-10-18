package agenthook

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

func adminEnv(t *testing.T) (*Admin, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: inst, Name: inst, State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	return &Admin{Store: st}, st
}

func code(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func TestAdminConfigLifecycle(t *testing.T) {
	ctx := context.Background()
	a, st := adminEnv(t)
	v, err := a.Get(ctx, inst)
	if err != nil || v.Configured || v.Timeout.D() != 30*time.Second || v.ContextMessages != 20 ||
		v.HumanPause.D() != 30*time.Minute || v.MaxRepliesPerWindow != 10 || v.SecretSet {
		t.Fatalf("defaults = %+v %v", v, err)
	}
	if _, err := a.Get(ctx, "nope"); code(err) != "instance_not_found" {
		t.Fatalf("missing instance = %v", err)
	}
	// Enabling without a URL is refused.
	if _, err := a.Put(ctx, inst, Patch{Enabled: ptr(true)}); code(err) != "validation_failed" {
		t.Fatalf("enable without url = %v", err)
	}
	v, err = a.Put(ctx, inst, Patch{URL: ptr("https://agent.example.com/hook"), Enabled: ptr(true),
		Ignore: &[]string{"+55 11 98888-7777", "5511988887777@s.whatsapp.net"}})
	if err != nil || !v.Configured || !v.SecretSet || !strings.HasPrefix(v.Secret, "agsec_") {
		t.Fatalf("put = %+v %v", v, err)
	}
	if len(v.Ignore) != 1 || v.Ignore[0] != "5511988887777@s.whatsapp.net" {
		t.Fatalf("ignore = %v", v.Ignore)
	}
	generated := v.Secret
	// The secret is never shown again, and a patch keeps it.
	v, err = a.Put(ctx, inst, Patch{Timeout: ptr(Duration(10 * time.Second))})
	if err != nil || v.Secret != "" || v.Timeout.D() != 10*time.Second || v.URL != "https://agent.example.com/hook" {
		t.Fatalf("patch = %+v %v", v, err)
	}
	js, _ := st.GetAgentHook(ctx, inst)
	c, _ := parseConfig(js)
	if c.Secret != generated {
		t.Fatal("secret changed by a patch")
	}
	if g, _ := a.Get(ctx, inst); g.Secret != "" || !g.SecretSet {
		t.Fatalf("get leaks the secret: %+v", g)
	}
	bad := []Patch{
		{URL: ptr("ftp://x")}, {URL: ptr("/relative")}, {Secret: ptr("short")},
		{Timeout: ptr(Duration(0))}, {Timeout: ptr(Duration(5 * time.Minute))},
		{ContextMessages: ptr(101)}, {HumanPause: ptr(Duration(-time.Second))},
		{MaxRepliesPerWindow: ptr(0)}, {ReplyWindow: ptr(Duration(time.Second))},
		{Ignore: &[]string{"not a number"}},
	}
	for _, p := range bad {
		if _, err := a.Put(ctx, inst, p); code(err) != "validation_failed" {
			b, _ := json.Marshal(p)
			t.Errorf("patch %s accepted: %v", b, err)
		}
	}
	if err := a.Delete(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, inst); code(err) != "not_found" {
		t.Fatalf("second delete = %v", err)
	}
}

func TestDurationJSON(t *testing.T) {
	var p Patch
	if err := json.Unmarshal([]byte(`{"timeout":"45s","human_pause":90,"reply_window":"1h"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Timeout.D() != 45*time.Second || p.HumanPause.D() != 90*time.Second || p.ReplyWindow.D() != time.Hour {
		t.Fatalf("patch = %+v", p)
	}
	if err := json.Unmarshal([]byte(`{"timeout":"soon"}`), &p); err == nil {
		t.Fatal("bad duration accepted")
	}
	b, _ := json.Marshal(View{Timeout: Duration(30 * time.Second)})
	if !strings.Contains(string(b), `"timeout":"30s"`) {
		t.Fatalf("encoded %s", b)
	}
}

func TestAdminChatOverrides(t *testing.T) {
	ctx := context.Background()
	a, st := adminEnv(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	a.Clock = func() time.Time { return now }
	v, err := a.SetChat(ctx, inst, "+55 11 99999-9999", ptr(false))
	if err != nil || v.JID != chat || v.Enabled == nil || *v.Enabled {
		t.Fatalf("set off = %+v %v", v, err)
	}
	if _, err := a.SetChat(ctx, inst, "bad", nil); code(err) != "validation_failed" {
		t.Fatalf("bad jid = %v", err)
	}
	_ = st.PauseChatAgent(ctx, inst, chat, now.Add(time.Hour))
	list, err := a.Chats(ctx, inst)
	if err != nil || len(list) != 1 || list[0].PausedUntil == nil {
		t.Fatalf("chats = %+v %v", list, err)
	}
	// Re-activating (null) inherits the default and ends the pause.
	v, err = a.SetChat(ctx, inst, chat, nil)
	if err != nil || v.Enabled != nil || v.PausedUntil != nil {
		t.Fatalf("reset = %+v %v", v, err)
	}
	if list, _ := a.Chats(ctx, inst); len(list) != 0 {
		t.Fatalf("chats after reset = %+v", list)
	}
	// false keeps a running pause.
	_ = st.PauseChatAgent(ctx, inst, chat, now.Add(time.Hour))
	if v, _ := a.SetChat(ctx, inst, chat, ptr(false)); v.PausedUntil == nil {
		t.Fatalf("false cleared the pause: %+v", v)
	}
	_ = st.InsertAgentCall(ctx, store.AgentCall{ID: "c1", InstanceID: inst, ChatJID: chat, Status: "ok",
		Actions: "mark_read,react", CreatedAt: now})
	calls, err := a.Calls(ctx, inst, 0)
	if err != nil || len(calls) != 1 || len(calls[0].Actions) != 2 || calls[0].Actions[1] != "react" {
		t.Fatalf("calls = %+v %v", calls, err)
	}
}
