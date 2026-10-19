package dashboard

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/desktop"
)

// AIConnector is what the "Conectar a IA" page drives: it writes the LiteZap MCP entry
// into the config of Claude Desktop, Claude Code and Cursor on this computer. Only the
// tray app sets it (desktop.Connector, which points the entry at the app executable).
type AIConnector interface {
	Statuses() []desktop.AIStatus
	Connect(ctx context.Context, id string) (desktop.AIResult, error)
	Disconnect(ctx context.Context, id string) (desktop.AIResult, error)
	// InstallExtension hands the LiteZap extension (.mcpb) to Claude Desktop.
	InstallExtension() (desktop.ExtensionResult, error)
}

// aiHandlers serves GET /app/api/ai-clients and POST /app/api/ai-clients/{id}/{connect,
// disconnect} (and claude-desktop/extension). They change files of the local user, so they need the admin token, a
// loopback client and Host (no DNS rebinding) and, for POST, a same-origin JSON request.
func aiHandlers(mux *http.ServeMux, opt Options) {
	guard := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Cache-Control", "no-store")
		if !loopbackRemote(r.RemoteAddr) || !loopbackHost(r.Host) {
			apierr.Write(w, apierr.Forbidden())
			return false
		}
		if !adminBearer(r, opt.AdminToken) {
			apierr.Write(w, apierr.New(http.StatusUnauthorized, "unauthorized", "missing or invalid admin token", false))
			return false
		}
		if r.Method == http.MethodPost && (!sameOrigin(r) || !jsonBody(r)) {
			apierr.Write(w, apierr.Forbidden())
			return false
		}
		return true
	}
	mux.HandleFunc("GET "+Prefix+"api/ai-clients", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"clients": opt.AIClients.Statuses()})
	})
	mux.HandleFunc("POST "+Prefix+"api/ai-clients/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		id, action := r.PathValue("id"), r.PathValue("action")
		if id == desktop.ClaudeDesktop && action == "extension" {
			res, err := opt.AIClients.InstallExtension()
			if err != nil {
				writeAIError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"result": res})
			return
		}
		if desktop.ClientName(id) == "" || (action != "connect" && action != "disconnect") {
			apierr.Write(w, apierr.New(http.StatusNotFound, "not_found", "unknown AI client or action", false))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if action == "disconnect" {
			res, err := opt.AIClients.Disconnect(ctx, id)
			if err != nil {
				writeAIError(w, err)
				return
			}
			msg := desktop.ClientName(id) + " desconectado do LiteZap."
			if res.Hint != "" {
				msg += " " + res.Hint
			}
			writeJSON(w, http.StatusOK, map[string]any{"result": res, "message": msg})
			return
		}
		res, err := opt.AIClients.Connect(ctx, id)
		if err != nil {
			writeAIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": res, "message": res.Hint})
	})
}

// writeAIError maps the errors of the connector to API errors (messages are pt-BR: they
// come from the desktop package and are shown as they are).
func writeAIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, desktop.ErrMalformedConfig):
		apierr.Write(w, apierr.New(http.StatusConflict, "config_malformed", err.Error(), false))
	case errors.Is(err, desktop.ErrExeNotInstalled):
		apierr.Write(w, apierr.New(http.StatusConflict, "app_not_installed", err.Error(), false))
	case errors.Is(err, desktop.ErrNoExtension):
		apierr.Write(w, apierr.New(http.StatusNotFound, "extension_unavailable", err.Error(), false))
	default:
		apierr.Write(w, apierr.New(http.StatusInternalServerError, "ai_client_error", err.Error(), true))
	}
}

func adminBearer(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
}
