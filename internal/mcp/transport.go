package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/service"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPDeps are the services behind the MCP HTTP endpoint.
type HTTPDeps struct {
	Instances InstanceService
	Messaging MessagingService
	Events    *events.Bus
	Groups    GroupService // marco 8; nil: the group tools answer not_implemented
	Version   string
	Logger    *slog.Logger
	// PublicHost is the host[:port] of public_url, if any. The SDK rejects requests that
	// reach a loopback listener with a non-loopback Host (DNS rebinding protection); a
	// reverse proxy on the same machine forwards the public Host, so that one is allowed.
	PublicHost string
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
	// One server per scope, reused across requests (the SDK asks for it twice per POST):
	// a server holds only the scope, never the token.
	var servers sync.Map // service.Scope -> *sdk.Server
	h := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		sc, ok := service.ScopeFrom(r.Context())
		if !ok || (!sc.Admin && sc.InstanceID == "") {
			return nil // unauthenticated: never reached behind the API middleware
		}
		if s, ok := servers.Load(sc); ok {
			return s.(*sdk.Server)
		}
		b := &Local{Instances: d.Instances, Messaging: d.Messaging, Events: d.Events, Groups: d.Groups, Caller: sc}
		s, _ := servers.LoadOrStore(sc, NewServer(b, Options{Version: d.Version, Logger: d.Logger, SchemaCache: cache}))
		return s.(*sdk.Server)
	}, &sdk.StreamableHTTPOptions{Stateless: true, Logger: d.Logger, DisableLocalhostProtection: d.PublicHost != ""})
	if d.PublicHost == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK check, with the public host also allowed.
		if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && la != nil &&
			isLoopbackHost(la.String()) && !isLoopbackHost(r.Host) && !strings.EqualFold(hostOnly(r.Host), hostOnly(d.PublicHost)) {
			http.Error(w, "Forbidden: invalid Host header", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// hostOnly drops the port of a host[:port].
func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// isLoopbackHost reports whether a host[:port] names the loopback interface.
func isLoopbackHost(hostport string) bool {
	host := hostOnly(hostport)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RunStdio serves MCP over stdin/stdout, backed by the REST API of a running
// `litezap serve` (the `litezap mcp` subcommand). It never starts a supervisor.
func RunStdio(ctx context.Context, baseURL, token, version string, log *slog.Logger) error {
	b, err := NewREST(baseURL, token, nil)
	if err != nil {
		return err
	}
	if u, err := url.Parse(baseURL); err == nil && u.Scheme == "http" && !isLoopbackHost(u.Host) {
		log.Warn("LITEZAP_URL uses plain http to a non-loopback host: the token travels unencrypted; prefer https")
	}
	err = NewServer(b, Options{Version: version, Logger: log}).Run(ctx, &sdk.StdioTransport{})
	if ctx.Err() != nil || errors.Is(err, io.EOF) {
		return nil // interrupted, or the client closed stdin: a normal end
	}
	return err
}
