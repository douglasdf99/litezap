package desktop

import (
	"context"
	"time"
)

// Claude Desktop keeps its config in memory and writes it back when it quits: an entry
// added while it is open can be erased on Cmd+Q, and an entry removed while it is open
// can come back. After such a connect (or disconnect, or an extension install that removed
// the manual entry) the Connector watches the file (guard) and enforces the wanted state:
// while Claude stays open nothing is touched; once it has quit, a missing LiteZap entry is
// written again (connect) or a returned one is removed again (disconnect) — only our key,
// with the same splice as Connect/Disconnect —, a few times at most, and the user is told.
// A new connect or disconnect replaces the running guard of that client.

// Defaults of the guard (Connector fields override them; tests use short ones).
const (
	defaultGuardPoll      = 2 * time.Second
	defaultGuardAfterQuit = 2 * time.Minute
	defaultGuardMax       = 30 * time.Minute
	defaultGuardAttempts  = 3
)

// ReappliedMessage is what the user is told after the guard wrote the entry again.
const ReappliedMessage = "O Claude apagou a configuração do LiteZap ao fechar; ela foi refeita. Abra o Claude de novo para ver as ferramentas do LiteZap."

// ReremovedMessage is what the user is told after the guard removed the entry again.
const ReremovedMessage = "O Claude recolocou a configuração do LiteZap ao fechar; ela foi removida de novo."

func orDuration(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// clientRunning asks the process detector (false when there is none).
func (c *Connector) clientRunning(id string) bool {
	return c.Running != nil && c.Running(id)
}

// RestartNeeded reports whether id was connected while it was open and has not been
// restarted since (it has not loaded the LiteZap entry yet).
func (c *Connector) RestartNeeded(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending[id]
}

func (c *Connector) setPending(id string, on bool) {
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]bool{}
	}
	changed := c.pending[id] != on
	c.pending[id] = on
	c.mu.Unlock()
	if changed && c.OnChange != nil {
		c.OnChange()
	}
}

// startGuard (re)starts the guard of id; a running one is replaced. present: the entry
// must exist (connect); otherwise it must not (disconnect).
func (c *Connector) startGuard(id string, present bool) {
	ctx, cancel := context.WithCancel(context.Background())
	g := &guardHandle{cancel: cancel}
	c.mu.Lock()
	if c.guards == nil {
		c.guards = map[string]*guardHandle{}
	}
	if old := c.guards[id]; old != nil {
		old.cancel()
	}
	c.guards[id] = g
	done := make(chan struct{})
	c.guardDone = done
	c.mu.Unlock()
	go func() {
		defer close(done)
		defer func() {
			cancel()
			c.mu.Lock()
			if c.guards[id] == g { // not replaced by a newer guard
				delete(c.guards, id)
			}
			c.mu.Unlock()
		}()
		c.guard(ctx, id, present)
	}()
}

// guardHandle is one running guard (compared by pointer when it ends).
type guardHandle struct{ cancel context.CancelFunc }

// stopGuard stops the guard of id (a new connect or disconnect).
func (c *Connector) stopGuard(id string) {
	c.mu.Lock()
	if g := c.guards[id]; g != nil {
		g.cancel()
		delete(c.guards, id)
	}
	c.mu.Unlock()
}

// guard watches id after a change made while it was open; see the comment at the top.
func (c *Connector) guard(ctx context.Context, id string, present bool) {
	poll := orDuration(c.GuardPoll, defaultGuardPoll)
	afterQuit := orDuration(c.GuardAfterQuit, defaultGuardAfterQuit)
	attempts := c.GuardAttempts
	if attempts <= 0 {
		attempts = defaultGuardAttempts
	}
	deadline := time.Now().Add(orDuration(c.GuardMax, defaultGuardMax))
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var quitAt time.Time
	closedPolls, fixed := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		if now.After(deadline) {
			return
		}
		if c.clientRunning(id) {
			closedPolls = 0
			if !quitAt.IsZero() {
				return // reopened: it read the file as it is now
			}
			continue // open: it would overwrite any change on quit anyway
		}
		// Two polls in a row without the process: it really quit (and wrote its file).
		if closedPolls++; closedPolls < 2 {
			continue
		}
		if quitAt.IsZero() {
			quitAt = now
			c.setPending(id, false) // the next start reads the file
		}
		st := c.Status(id)
		wrong := st.Error == "" && (present && !st.Connected || !present && st.Present)
		if wrong && fixed < attempts {
			if ctx.Err() != nil {
				return // a newer connect/disconnect while we were reading the file
			}
			fixed++
			var res AIResult
			var err error
			msg := ReappliedMessage
			if present {
				res, err = c.apply(id)
			} else {
				res, err = c.unapply(id)
				msg = ReremovedMessage
			}
			if c.Log != nil {
				c.Log("re-enforced AI client entry after the client quit", "client", id, "present", present, "changed", res.Changed, "err", err)
			}
			if err == nil && res.Changed {
				if c.Notify != nil {
					go c.Notify("LiteZap", msg) // a dialog must never hold the guard
				}
				if c.OnChange != nil {
					c.OnChange()
				}
			}
		}
		if now.Sub(quitAt) >= afterQuit {
			return
		}
	}
}
