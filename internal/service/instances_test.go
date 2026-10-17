package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/apierr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMapErrWorkerStatus(t *testing.T) {
	for _, c := range []codes.Code{codes.Unavailable, codes.Canceled} {
		var e *apierr.Error
		if !errors.As(mapErr(status.Error(c, "boom")), &e) || e.Code != "instance_not_connected" {
			t.Fatalf("%v -> %v, want instance_not_connected", c, e)
		}
	}
	var to *apierr.Error
	if !errors.As(mapErr(status.Error(codes.DeadlineExceeded, "slow")), &to) ||
		to.Code != "worker_timeout" || to.Status != http.StatusGatewayTimeout || !to.Retryable {
		t.Fatalf("DeadlineExceeded -> %+v, want 504 worker_timeout retryable", to)
	}

	err := fmt.Errorf("pair: %w", status.Error(codes.Unknown, "waiting for pairing round: not pairing (5511987654321, lzi_secret123)"))
	var e *apierr.Error
	if !errors.As(mapErr(err), &e) {
		t.Fatalf("not an api error: %v", mapErr(err))
	}
	if e.Status != http.StatusBadGateway || e.Code != "worker_error" || !e.Retryable {
		t.Fatalf("got %+v", e)
	}
	if !strings.Contains(e.Message, "not pairing") {
		t.Fatalf("message lost the worker's reason: %q", e.Message)
	}
	if strings.Contains(e.Message, "5511987654321") || strings.Contains(e.Message, "lzi_secret123") {
		t.Fatalf("message leaks phone or token: %q", e.Message)
	}
}

func TestMapErrPlainErrorPassesThrough(t *testing.T) {
	plain := errors.New("disk full")
	if got := mapErr(plain); got != plain {
		t.Fatalf("got %v", got)
	}
}

func TestMapErrMessagingCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{status.Error(codes.InvalidArgument, "invalid_number: 5511987654321 is not on WhatsApp"), http.StatusBadRequest, "invalid_number"},
		{status.Error(codes.InvalidArgument, "validation_failed: invalid JID"), http.StatusBadRequest, "validation_failed"},
		{status.Error(codes.NotFound, "media_unavailable: 404"), http.StatusGone, "media_unavailable"},
		{status.Error(codes.FailedPrecondition, "logged_out: session is not logged in"), http.StatusConflict, "instance_not_connected"},
	}
	for _, c := range cases {
		var e *apierr.Error
		if !errors.As(mapErr(c.err), &e) || e.Status != c.status || e.Code != c.code {
			t.Errorf("%v -> %+v, want %d %s", c.err, e, c.status, c.code)
		}
		if e != nil && strings.Contains(e.Message, "5511987654321") {
			t.Errorf("message leaks a phone number: %q", e.Message)
		}
	}
}
