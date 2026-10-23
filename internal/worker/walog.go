package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// slogWA adapts whatsmeow's logger to slog, so its lines carry the instance id and follow
// LITEZAP_LOG_FORMAT. Messages are redacted (unless redact is off, at debug): whatsmeow's
// warnings often carry JIDs, i.e. phone numbers and LIDs.
type slogWA struct {
	log    *slog.Logger
	min    slog.Level
	redact bool
}

var longDigits = regexp.MustCompile(`\d{6,}`)

func newSlogWA(log *slog.Logger, min slog.Level, redact bool) waLog.Logger {
	return &slogWA{log: log.With("module", "whatsmeow"), min: min, redact: redact}
}

func (l *slogWA) emit(level slog.Level, msg string, args []any) {
	if level < l.min {
		return
	}
	s := fmt.Sprintf(msg, args...)
	if l.redact {
		s = longDigits.ReplaceAllString(s, "[redacted]")
	}
	l.log.Log(context.Background(), level, s)
}

// LogLevel is the level of LiteZap's own logs: info, or LITEZAP_LOG_LEVEL
// (debug | info | warn | error).
func LogLevel() slog.Level { return envLevel("LITEZAP_LOG_LEVEL", slog.LevelInfo) }

// WALogLevel is the level of whatsmeow's logs in the worker: warn, or LITEZAP_WA_LOG_LEVEL
// (the supervisor sets it from wa_log_level). At debug, JIDs are not redacted.
func WALogLevel() slog.Level { return envLevel("LITEZAP_WA_LOG_LEVEL", slog.LevelWarn) }

// ProcessLogLevel is the handler level of a worker process: the lower of LogLevel and
// WALogLevel, so whatsmeow's debug lines get through when asked for.
func ProcessLogLevel() slog.Level { return min(LogLevel(), WALogLevel()) }

func envLevel(name string, def slog.Level) slog.Level {
	if l, ok := ParseLogLevel(os.Getenv(name)); ok {
		return l
	}
	return def
}

// ParseLogLevel reads debug, info, warn or error (case-insensitive); "" is not a level.
func ParseLogLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

func (l *slogWA) Warnf(msg string, args ...any)  { l.emit(slog.LevelWarn, msg, args) }
func (l *slogWA) Errorf(msg string, args ...any) { l.emit(slog.LevelError, msg, args) }
func (l *slogWA) Infof(msg string, args ...any)  { l.emit(slog.LevelInfo, msg, args) }
func (l *slogWA) Debugf(msg string, args ...any) { l.emit(slog.LevelDebug, msg, args) }
func (l *slogWA) Sub(module string) waLog.Logger {
	return &slogWA{log: l.log.With("submodule", module), min: l.min, redact: l.redact}
}
