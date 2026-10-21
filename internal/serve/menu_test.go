package serve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/apierr"
)

func TestMenuAIErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&agenthook.CallError{Reason: "not_configured", Detail: "no provider"}, "ai_not_configured"},
		{&agenthook.CallError{Reason: "daily_cap", Detail: "cap"}, "rate_limited"},
		{&agenthook.CallError{Reason: "llm_error", Detail: "sk-secret upstream body"}, "ai_error"},
		{errors.New("sqlite: database is locked at /data/litezap.db"), "ai_error"},
		{context.DeadlineExceeded, "ai_error"},
	} {
		fn := menuAIFrom(func(context.Context, string, string, string, json.RawMessage) (string, error) { return "", tc.err })
		_, err := fn(context.Background(), "i1", "s", "u", nil)
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Code != tc.code {
			t.Fatalf("%v -> %v", tc.err, err)
		}
		if tc.code == "ai_error" && (strings.Contains(ae.Message, "secret") || strings.Contains(ae.Message, "sqlite")) {
			t.Fatalf("details leaked: %q", ae.Message)
		}
	}
	fn := menuAIFrom(func(context.Context, string, string, string, json.RawMessage) (string, error) { return "ok", nil })
	if out, err := fn(context.Background(), "i1", "s", "u", nil); out != "ok" || err != nil {
		t.Fatal(out, err)
	}
}
