// Package service holds LiteZap use cases shared by the REST and MCP adapters.
package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

type Instances struct {
	Store   *store.Store
	Manager *manager.Manager
	DataDir string
}

type InstanceView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	State       instance.State `json:"state"`
	StateReason string         `json:"state_reason,omitempty"`
	Phone       string         `json:"phone,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func view(in store.Instance) InstanceView {
	user, _, _ := strings.Cut(in.JID, "@")
	phone, _, _ := strings.Cut(user, ":") // drop device suffix
	return InstanceView{
		ID: in.ID, Name: in.Name, State: in.State, StateReason: in.StateReason,
		Phone: phone, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
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
	return err
}

func (s *Instances) Create(ctx context.Context, name string) (InstanceView, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return InstanceView{}, "", apierr.Validation("name is required")
	}
	id := secure.NewID("inst_")
	tok := secure.NewInstanceToken()
	if err := s.Store.CreateInstance(ctx, store.Instance{
		ID: id, Name: name, State: instance.Created, TokenHash: secure.HashToken(tok),
	}); err != nil {
		return InstanceView{}, "", err
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
		_ = s.Manager.Logout(ctx, id) // best effort: frees the linked-device slot on the phone
	}
	// Retire (not Stop): a Start racing this Delete must not respawn the worker.
	if err := s.Manager.Retire(ctx, id); err != nil {
		return mapErr(err)
	}
	if err := os.RemoveAll(filepath.Join(s.DataDir, "instances", id)); err != nil {
		return err
	}
	return mapErr(s.Store.DeleteInstance(ctx, id))
}

func (s *Instances) Start(ctx context.Context, id string) error {
	return mapErr(s.Manager.Start(ctx, id))
}

func (s *Instances) Stop(ctx context.Context, id string) error {
	return mapErr(s.Manager.Stop(ctx, id))
}

func (s *Instances) Restart(ctx context.Context, id string) error {
	if _, err := s.Store.GetInstance(ctx, id); err != nil {
		return mapErr(err)
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
	return code, mapErr(err)
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
