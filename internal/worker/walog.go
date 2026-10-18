package worker

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// slogWA adapts whatsmeow's logger to slog (used with LITEZAP_LOG_FORMAT=json, so the
// worker's output stays one JSON object per line). Messages are redacted: whatsmeow's
// warnings often carry JIDs, i.e. phone numbers.
type slogWA struct {
	log *slog.Logger
	min slog.Level
}

var longDigits = regexp.MustCompile(`\d{6,}`)

func newSlogWA(log *slog.Logger, min slog.Level) waLog.Logger {
	return &slogWA{log: log.With("module", "whatsmeow"), min: min}
}

func (l *slogWA) emit(level slog.Level, msg string, args []any) {
	if level < l.min {
		return
	}
	l.log.Log(context.Background(), level, longDigits.ReplaceAllString(fmt.Sprintf(msg, args...), "[redacted]"))
}

func (l *slogWA) Warnf(msg string, args ...any)  { l.emit(slog.LevelWarn, msg, args) }
func (l *slogWA) Errorf(msg string, args ...any) { l.emit(slog.LevelError, msg, args) }
func (l *slogWA) Infof(msg string, args ...any)  { l.emit(slog.LevelInfo, msg, args) }
func (l *slogWA) Debugf(msg string, args ...any) { l.emit(slog.LevelDebug, msg, args) }
func (l *slogWA) Sub(module string) waLog.Logger {
	return &slogWA{log: l.log.With("submodule", module), min: l.min}
}
