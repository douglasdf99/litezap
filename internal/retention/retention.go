// Package retention prunes old data on a schedule (cloud spec E15): local copies of
// media past media_retention_days (downloaded again on the next read while WhatsApp still
// has them) and messages older than message_retention_days (with their media and finished
// sends). Both are off by default: nothing is deleted unless configured.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/douglasdf99/litezap/internal/store"
)

type Sweeper struct {
	Store *store.Store
	Log   *slog.Logger
	// Media enables expiring local media copies (their expiry is set at download time to
	// now + media_retention_days); Messages is the message log retention (0: forever).
	Media    bool
	Messages time.Duration
	// Interval between sweeps (default 1h; the first runs a minute after Start).
	Interval time.Duration
	Now      func() time.Time

	stop chan struct{}
	done chan struct{}
}

// Enabled reports whether there is anything to sweep.
func (s *Sweeper) Enabled() bool { return s.Media || s.Messages > 0 }

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sweep runs one pass.
func (s *Sweeper) Sweep(ctx context.Context) {
	now := s.now()
	if s.Media {
		if n, err := s.Store.ExpireMediaFiles(ctx, now); err != nil {
			s.logErr("retention: expire media", err)
		} else if n > 0 && s.Log != nil {
			s.Log.Info("retention: expired local media copies", "count", n)
		}
	}
	if s.Messages > 0 {
		if n, err := s.Store.PruneMessages(ctx, now.Add(-s.Messages)); err != nil {
			s.logErr("retention: prune messages", err)
		} else if n > 0 && s.Log != nil {
			s.Log.Info("retention: pruned old messages", "count", n)
		}
	}
}

func (s *Sweeper) logErr(msg string, err error) {
	if s.Log != nil {
		s.Log.Error(msg, "err", err)
	}
}

// Start sweeps periodically until Close; it does nothing when not Enabled.
func (s *Sweeper) Start() {
	if !s.Enabled() {
		return
	}
	every := s.Interval
	if every <= 0 {
		every = time.Hour
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(s.done)
		next := time.NewTimer(min(time.Minute, every))
		defer next.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-next.C:
				s.Sweep(context.Background())
				next.Reset(every)
			}
		}
	}()
}

func (s *Sweeper) Close() {
	if s.stop != nil {
		close(s.stop)
		<-s.done
		s.stop = nil
	}
}
