package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/worker"
)

// newLogger returns the process logger: text by default, JSON lines with
// LITEZAP_LOG_FORMAT=json (log shippers; workers inherit the variable). The level is
// info, or LITEZAP_LOG_LEVEL; a worker also lets whatsmeow's LITEZAP_WA_LOG_LEVEL through.
func newLogger(w io.Writer, format string, isWorker bool) *slog.Logger {
	opt := &slog.HandlerOptions{Level: worker.LogLevel()}
	if isWorker {
		opt.Level = worker.ProcessLogLevel()
	}
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return slog.New(slog.NewJSONHandler(w, opt))
	}
	return slog.New(slog.NewTextHandler(w, opt))
}

// envDuration parses a duration variable set by the supervisor (0 when unset or invalid).
func envDuration(name string) time.Duration {
	d, err := time.ParseDuration(os.Getenv(name))
	if err != nil || d < 0 {
		return 0
	}
	return d
}
