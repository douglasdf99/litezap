package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/service"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPDeps are the services behind the MCP HTTP endpoint.
type HTTPDeps struct {
	Instances InstanceService
	Messaging MessagingService
	Events    *events.Bus
	Version   string
	Logger    *slog.Logger
}

// NewHTTPHandler serves MCP streamable HTTP. It must sit behind the API authentication,
// which puts the caller's service.Scope in the request context. It is stateless: every
// POST gets a server bound to the scope of that request's token, so no session can be
// reused by another token.
func NewHTTPHandler(d HTTPDeps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	cache := sdk.NewSchemaCache()
	return sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		sc, ok := service.ScopeFrom(r.Context())
		if !ok || (!sc.Admin && sc.InstanceID == "") {
			return nil // unauthenticated: never reached behind the API middleware
		}
		b := &Local{Instances: d.Instances, Messaging: d.Messaging, Events: d.Events, Caller: sc}
		return NewServer(b, Options{Version: d.Version, Logger: d.Logger, SchemaCache: cache})
	}, &sdk.StreamableHTTPOptions{Stateless: true, Logger: d.Logger})
}

// RunStdio serves MCP over stdin/stdout, backed by the REST API of a running
// `litezap serve` (the `litezap mcp` subcommand). It never starts a supervisor.
func RunStdio(ctx context.Context, baseURL, token, version string, log *slog.Logger) error {
	b, err := NewREST(baseURL, token, nil)
	if err != nil {
		return err
	}
	err = NewServer(b, Options{Version: version, Logger: log}).Run(ctx, &sdk.StdioTransport{})
	if ctx.Err() != nil || errors.Is(err, io.EOF) {
		return nil // interrupted, or the client closed stdin: a normal end
	}
	return err
}
