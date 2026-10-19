package desktop

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClaude is a process detector the test flips, plus a record of notifications.
type fakeClaude struct {
	open  atomic.Bool
	mu    sync.Mutex
	notes []string
}

func (f *fakeClaude) running(id string) bool { return id == ClaudeDesktop && f.open.Load() }

func (f *fakeClaude) notify(_, msg string) {
	f.mu.Lock()
	f.notes = append(f.notes, msg)
	f.mu.Unlock()
}

func (f *fakeClaude) notifications() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.notes)
}

func guardConnector(t *testing.T, f *fakeClaude) *Connector {
	c := testConnector(t)
	c.Running = f.running
	c.Notify = f.notify
	c.GuardPoll = 5 * time.Millisecond
	c.GuardAfterQuit = 150 * time.Millisecond
	c.GuardMax = 5 * time.Second
	return c
}

// waitGuard waits for the last started guard to return.
func waitGuard(t *testing.T, c *Connector) {
	t.Helper()
	c.mu.Lock()
	done := c.guardDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("no guard was started")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("guard did not stop")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// eraseEntry is Claude Desktop writing its in-memory config back without LiteZap.
func eraseEntry(t *testing.T, c *Connector) {
	t.Helper()
	p, _ := c.ConfigPath(ClaudeDesktop)
	writeFile(t, p, `{"mcpServers":{"other":{"command":"x"}},"theme":"dark"}`)
}

func TestGuardReappliesAfterClaudeQuits(t *testing.T) {
	f := &fakeClaude{}
	f.open.Store(true)
	c := guardConnector(t, f)
	res, err := c.Connect(context.Background(), ClaudeDesktop)
	if err != nil || !res.Changed || !res.Running || !strings.Contains(res.Hint, "Cmd+Q") {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	if st := c.Status(ClaudeDesktop); !st.Connected || !st.RestartNeeded || !st.Running {
		t.Fatalf("status after connect: %+v", st)
	}

	// While Claude is open the guard never touches the file, even when the entry goes.
	eraseEntry(t, c)
	time.Sleep(40 * time.Millisecond)
	if c.Status(ClaudeDesktop).Present {
		t.Fatal("entry re-applied while Claude was still open")
	}

	// Claude quits: the entry comes back once, other servers and keys are kept.
	f.open.Store(false)
	eventually(t, "re-apply", func() bool { return c.Status(ClaudeDesktop).Connected })
	p, _ := c.ConfigPath(ClaudeDesktop)
	m := readJSON(t, p)
	if m["theme"] != "dark" || m["mcpServers"].(map[string]any)["other"] == nil {
		t.Errorf("other content lost: %v", m)
	}
	eventually(t, "notification", func() bool { return f.notifications() == 1 })
	if st := c.Status(ClaudeDesktop); st.RestartNeeded {
		t.Errorf("still waiting for a restart after Claude quit: %+v", st)
	}

	// Claude opens again: the guard is done.
	f.open.Store(true)
	waitGuard(t, c)
	if n := f.notifications(); n != 1 {
		t.Errorf("notifications = %d, want 1", n)
	}
}

func TestGuardStopsAfterQuitWindow(t *testing.T) {
	f := &fakeClaude{}
	f.open.Store(true)
	c := guardConnector(t, f)
	if _, err := c.Connect(context.Background(), ClaudeDesktop); err != nil {
		t.Fatal(err)
	}
	// Claude quits and keeps the entry: nothing to do, the guard ends after GuardAfterQuit.
	f.open.Store(false)
	waitGuard(t, c)
	if f.notifications() != 0 {
		t.Errorf("notified with nothing re-applied: %v", f.notes)
	}
	if c.RestartNeeded(ClaudeDesktop) {
		t.Error("restart still needed after Claude quit")
	}
}

func TestGuardAttemptsAreLimited(t *testing.T) {
	f := &fakeClaude{}
	f.open.Store(true)
	c := guardConnector(t, f)
	c.GuardAttempts = 2
	c.GuardAfterQuit = time.Second
	if _, err := c.Connect(context.Background(), ClaudeDesktop); err != nil {
		t.Fatal(err)
	}
	f.open.Store(false)
	// Something keeps removing the entry while Claude is closed: the first removal and
	// the two re-applied entries.
	for i := 0; i < 3; i++ {
		eventually(t, "re-apply", func() bool { return c.Status(ClaudeDesktop).Connected })
		eraseEntry(t, c)
	}
	waitGuard(t, c)
	if n := f.notifications(); n != 2 {
		t.Errorf("re-applied %d times, want 2", n)
	}
	if c.Status(ClaudeDesktop).Present {
		t.Error("re-applied past the attempt limit")
	}
}

func TestGuardStoppedByDisconnect(t *testing.T) {
	f := &fakeClaude{}
	f.open.Store(true)
	c := guardConnector(t, f)
	if _, err := c.Connect(context.Background(), ClaudeDesktop); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Disconnect(context.Background(), ClaudeDesktop); err != nil {
		t.Fatal(err)
	}
	waitGuard(t, c)
	f.open.Store(false)
	time.Sleep(40 * time.Millisecond)
	if c.Status(ClaudeDesktop).Present || c.RestartNeeded(ClaudeDesktop) {
		t.Error("entry came back after Disconnect")
	}
}

func TestNoGuardWhenClaudeIsClosed(t *testing.T) {
	f := &fakeClaude{}
	c := guardConnector(t, f)
	res, err := c.Connect(context.Background(), ClaudeDesktop)
	if err != nil || res.Running || !strings.Contains(res.Hint, "Abra o Claude Desktop") {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	started := func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.guardDone != nil }
	if started() || c.RestartNeeded(ClaudeDesktop) {
		t.Error("guard started (or restart flagged) with Claude closed")
	}
	// Other clients never get a guard either.
	f.open.Store(true)
	if _, err := c.Connect(context.Background(), Cursor); err != nil || started() {
		t.Errorf("cursor: guard started, err %v", err)
	}
}

func TestRestartHints(t *testing.T) {
	for _, c := range []struct {
		goos, id string
		running  bool
		want     string
	}{
		{"darwin", ClaudeDesktop, true, "O Claude está aberto: feche com Cmd+Q e abra de novo para ver as ferramentas do LiteZap"},
		{"windows", ClaudeDesktop, true, "Arquivo > Sair"},
		{"darwin", ClaudeDesktop, false, "Abra o Claude Desktop"},
		{"darwin", ClaudeCode, false, "sessão do Claude Code"},
		{"darwin", Cursor, true, "Reload Window"},
	} {
		if got := RestartHint(c.goos, c.id, c.running); !strings.Contains(got, c.want) {
			t.Errorf("%s %s %v: %q", c.goos, c.id, c.running, got)
		}
	}
}

func TestHasProcessSuffix(t *testing.T) {
	ps := "/usr/libexec/logd\n/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper\n"
	if hasProcessSuffix(ps, "/Claude.app/Contents/MacOS/Claude") {
		t.Error("helper process taken for Claude")
	}
	if !hasProcessSuffix(ps+"/Users/ana/Applications/Claude.app/Contents/MacOS/Claude\n", "/Claude.app/Contents/MacOS/Claude") {
		t.Error("Claude in ~/Applications not found")
	}
}
