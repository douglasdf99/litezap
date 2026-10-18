package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// newLogger returns the process logger: text by default, JSON lines with
// LITEZAP_LOG_FORMAT=json (log shippers; workers inherit the variable).
func newLogger(w io.Writer, format string) *slog.Logger {
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return slog.New(slog.NewJSONHandler(w, nil))
	}
	return slog.New(slog.NewTextHandler(w, nil))
}

// envDuration parses a duration variable set by the supervisor (0 when unset or invalid).
func envDuration(name string) time.Duration {
	d, err := time.ParseDuration(os.Getenv(name))
	if err != nil || d < 0 {
		return 0
	}
	return d
}
