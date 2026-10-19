package desktop

import (
	"context"
	"time"
)

// Claude Desktop keeps its config in memory and writes it back when it quits: an entry
// added while it is open can be erased on Cmd+Q. After such a connect the Connector
// watches the file (guard): while Claude stays open nothing is touched; once it has quit,
// a missing LiteZap entry is written again (only our key, with the same splice as
// Connect), a few times at most, and the user is told.

// Defaults of the guard (Connector fields override them; tests use short ones).
const (
	defaultGuardPoll      = 2 * time.Second
	defaultGuardAfterQuit = 2 * time.Minute
	defaultGuardMax       = 30 * time.Minute
	defaultGuardAttempts  = 3
)

// ReappliedMessage is what the user is told after the guard wrote the entry again.
const ReappliedMessage = "O Claude apagou a configuração do LiteZap ao fechar; ela foi refeita. Abra o Claude de novo para ver as ferramentas do LiteZap."

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

// startGuard (re)starts the guard of id; a running one is replaced.
func (c *Connector) startGuard(id string) {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.guards == nil {
		c.guards = map[string]context.CancelFunc{}
	}
	if old := c.guards[id]; old != nil {
		old()
	}
	c.guards[id] = cancel
	done := make(chan struct{})
	c.guardDone = done
	c.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		c.guard(ctx, id)
	}()
}

// stopGuard stops the guard of id (Disconnect).
func (c *Connector) stopGuard(id string) {
	c.mu.Lock()
	if cancel := c.guards[id]; cancel != nil {
		cancel()
		delete(c.guards, id)
	}
	c.mu.Unlock()
}

// guard watches id after a connect made while it was open; see the comment at the top.
func (c *Connector) guard(ctx context.Context, id string) {
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
	closedPolls, reapplied := 0, 0
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
		if st := c.Status(id); st.Error == "" && !st.Connected && reapplied < attempts {
			reapplied++
			res, err := c.apply(id)
			if c.Log != nil {
				c.Log("re-applied AI client entry after the client quit", "client", id, "changed", res.Changed, "err", err)
			}
			if err == nil && res.Changed {
				if c.Notify != nil {
					c.Notify("LiteZap", ReappliedMessage)
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
