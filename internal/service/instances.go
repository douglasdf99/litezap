// Package service holds LiteZap use cases shared by the REST and MCP adapters.
package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Instances struct {
	Store   *store.Store
	Manager *manager.Manager
	DataDir string
	// Queues, when set, has a deleted instance's send dispatcher stopped.
	Queues *queue.Queues
	// Events, when set, receives instance.pair_code.
	Events events.Publisher
	// Policy, when set, applies the managed-hosting limits (instance cap, required proxy).
	Policy *Policy
}

type InstanceView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	State       instance.State `json:"state"`
	StateReason string         `json:"state_reason,omitempty"`
	Phone       string         `json:"phone,omitempty"`
	Tags        []string       `json:"tags"`
	IPAllowlist []string       `json:"ip_allowlist"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func view(in store.Instance) InstanceView {
	user, _, _ := strings.Cut(in.JID, "@")
	phone, _, _ := strings.Cut(user, ":") // drop device suffix
	return InstanceView{
		ID: in.ID, Name: in.Name, State: in.State, StateReason: in.StateReason,
		Phone: phone, Tags: nonNil(in.Tags), IPAllowlist: nonNil(in.IPAllowlist),
		CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
	}
}

// mapErr turns known domain errors into API errors; unknown errors pass through (rendered as internal).
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return apierr.InstanceNotFound()
	case errors.Is(err, manager.ErrNotRunning):
		return apierr.NotConnected()
	case errors.Is(err, manager.ErrBanned):
		return apierr.New(http.StatusConflict, "instance_banned", "instance is banned; delete it", false)
	case errors.Is(err, manager.ErrShuttingDown):
		return apierr.New(http.StatusServiceUnavailable, "shutting_down", "server is shutting down", true)
	}
	// Errors from worker RPCs carry a gRPC status (possibly wrapped). Messaging RPCs prefix
	// the message with a stable code ("invalid_number: ...", see worker.proto).
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		code, detail, _ := strings.Cut(st.Message(), ": ")
		switch st.Code() {
		case codes.Unavailable, codes.Canceled:
			if code != "media_download_failed" { // a CDN failure, not a disconnected instance
				return apierr.NotConnected()
			}
		case codes.DeadlineExceeded:
			return apierr.New(http.StatusGatewayTimeout, "worker_timeout", "instance worker did not answer in time", true)
		case codes.InvalidArgument:
			if code == "invalid_number" {
				return apierr.New(http.StatusBadRequest, "invalid_number", redact(detail), false)
			}
			return apierr.Validation(redact(st.Message()))
		case codes.NotFound:
			if code == "media_unavailable" {
				return apierr.New(http.StatusGone, "media_unavailable", "media is no longer available on WhatsApp", false)
			}
		case codes.FailedPrecondition:
			return apierr.New(http.StatusConflict, "instance_not_connected", "instance is not logged in", false)
		}
		msg := "worker request failed"
		if m := redact(st.Message()); m != "" {
			msg += ": " + m
		}
		return apierr.New(http.StatusBadGateway, "worker_error", msg, true)
	}
	return err
}

var (
	digitRun  = regexp.MustCompile(`\d{6,}`)
	tokenLike = regexp.MustCompile(`\blz[a-z]_[A-Za-z0-9_-]+`)
)

// redact strips anything that may be a phone number or a token from a worker message.
func redact(msg string) string {
	msg = digitRun.ReplaceAllString(msg, "[redacted]")
	return tokenLike.ReplaceAllString(msg, "[redacted]")
}

func (s *Instances) Create(ctx context.Context, name string) (InstanceView, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return InstanceView{}, "", apierr.Validation("name is required")
	}
	id := secure.NewID("inst_")
	tok := secure.NewInstanceToken()
	release, err := s.Policy.admit(ctx)
	if err != nil {
		return InstanceView{}, "", err
	}
	err = s.Store.CreateInstance(ctx, store.Instance{
		ID: id, Name: name, State: instance.Created, TokenHash: secure.HashToken(tok),
	})
	release()
	if err != nil {
		return InstanceView{}, "", err
	}
	if held, err := s.Policy.holdWithoutProxy(ctx, id); err != nil {
		return InstanceView{}, "", err
	} else if held { // stays stopped until the operator sets a proxy and starts it
		v, err := s.Get(ctx, id)
		return v, tok, err
	}
	if err := s.Manager.Start(ctx, id); err != nil {
		return InstanceView{}, "", mapErr(err)
	}
	v, err := s.Get(ctx, id)
	return v, tok, err
}

func (s *Instances) List(ctx context.Context) ([]InstanceView, error) {
	list, err := s.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InstanceView, 0, len(list))
	for _, in := range list {
		out = append(out, view(in))
	}
	return out, nil
}

func (s *Instances) Get(ctx context.Context, id string) (InstanceView, error) {
	in, err := s.Store.GetInstance(ctx, id)
	if err != nil {
		return InstanceView{}, mapErr(err)
	}
	return view(in), nil
}

// Delete unlinks the number when possible, stops the worker and removes all instance data.
func (s *Instances) Delete(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
	}
	// A client disconnect must not leave the teardown half-done.
	ctx = context.WithoutCancel(ctx)
	if s.Manager.Running(id) {
		// Best effort: frees the linked-device slot on the phone. Bounded so a hung worker cannot block Delete.
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = s.Manager.Logout(lctx, id)
		cancel()
	}
	// Retire (not Stop): a Start racing this Delete must not respawn the worker.
	if err := s.Manager.Retire(ctx, id); err != nil {
		return mapErr(err)
	}
	// The worker is stopped; if the rest fails, let the instance be started again.
	for _, dir := range []string{"instances", "media", "outbox", "menu"} {
		if err := os.RemoveAll(filepath.Join(s.DataDir, dir, id)); err != nil {
			s.Manager.Unretire(id)
			return err
		}
	}
	if err := s.Store.DeleteInstance(ctx, id); err != nil {
		s.Manager.Unretire(id)
		return mapErr(err)
	}
	if s.Queues != nil {
		s.Queues.Forget(id) // after the row is gone, so the dispatcher sees it and exits
	}
	return nil
}

func (s *Instances) Start(ctx context.Context, id string) error {
	if err := s.Policy.checkProxy(ctx, id); err != nil {
		return err
	}
	return mapErr(s.Manager.Start(ctx, id))
}

func (s *Instances) Stop(ctx context.Context, id string) error {
	return mapErr(s.Manager.Stop(ctx, id))
}

func (s *Instances) Restart(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
	}
	if err := s.Policy.checkProxy(ctx, id); err != nil {
		return err
	}
	return mapErr(s.Manager.Restart(ctx, id))
}

func (s *Instances) Logout(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
	}
	return mapErr(s.Manager.Logout(ctx, id))
}

func (s *Instances) RotateToken(ctx context.Context, id string) (string, error) {
	tok := secure.NewInstanceToken()
	if err := s.Store.SetInstanceTokenHash(ctx, id, secure.HashToken(tok)); err != nil {
		return "", mapErr(err)
	}
	// API keys minted with the old token go with it (a leaked token must not leave keys behind).
	if _, err := s.Store.DeleteInstanceCreatedAPIKeys(ctx, id); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Instances) QR(ctx context.Context, id string) (string, error) {
	in, err := s.Store.GetInstance(ctx, id)
	if err != nil {
		return "", mapErr(err)
	}
	code, ok := s.Manager.QR(id)
	if in.State != instance.QRPending || !ok {
		return "", apierr.New(http.StatusConflict, "instance_not_pairing",
			"instance is not waiting for a QR scan (state: "+string(in.State)+")", in.State.Active())
	}
	return code, nil
}

func (s *Instances) PairPhone(ctx context.Context, id, phone string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(digits) < 10 || len(digits) > 15 {
		return "", apierr.Validation("phone must have 10 to 15 digits including country code")
	}
	in, err := s.Store.GetInstance(ctx, id)
	if err != nil {
		return "", mapErr(err)
	}
	if in.State != instance.QRPending {
		return "", apierr.New(http.StatusConflict, "instance_not_pairing",
			"instance is not waiting to be paired (state: "+string(in.State)+")", in.State.Active())
	}
	code, err := s.Manager.PairPhone(ctx, id, digits)
	if err != nil {
		return "", mapErr(err)
	}
	if s.Events != nil {
		s.Events.Publish(id, events.TypeInstancePairCode, events.PairCode{Code: code})
	}
	return code, nil
}

// Authenticate resolves an instance token to its instance ID.
func (s *Instances) Authenticate(ctx context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, "lzi_") {
		return "", apierr.Unauthorized()
	}
	in, err := s.Store.InstanceByTokenHash(ctx, secure.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return "", apierr.Unauthorized()
	}
	if err != nil {
		return "", err
	}
	return in.ID, nil
}
