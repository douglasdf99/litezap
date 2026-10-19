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
}

// aiHandlers serves GET /app/api/ai-clients and POST /app/api/ai-clients/{id}/{connect,
// disconnect}. They change files of the local user, so they need the admin token, a
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
		if desktop.ClientName(id) == "" || (action != "connect" && action != "disconnect") {
			apierr.Write(w, apierr.New(http.StatusNotFound, "not_found", "unknown AI client or action", false))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		op, msg := opt.AIClients.Connect, desktop.RestartHint(id)
		if action == "disconnect" {
			op, msg = opt.AIClients.Disconnect, desktop.ClientName(id)+" desconectado do LiteZap."
		}
		res, err := op(ctx, id)
		switch {
		case errors.Is(err, desktop.ErrMalformedConfig):
			apierr.Write(w, apierr.New(http.StatusConflict, "config_malformed", err.Error(), false))
			return
		case errors.Is(err, desktop.ErrExeNotInstalled):
			apierr.Write(w, apierr.New(http.StatusConflict, "app_not_installed", "Mova o LiteZap para a pasta Aplicativos, abra-o de lá e conecte de novo.", false))
			return
		case err != nil:
			apierr.Write(w, apierr.New(http.StatusInternalServerError, "ai_client_error", err.Error(), true))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": res, "message": msg})
	})
}

func adminBearer(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
}
