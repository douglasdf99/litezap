// Package dashboard serves the embedded web dashboard (`/app/`) and its one-click login
// exchange. The dashboard is static: it talks to the REST API with the admin token as a
// Bearer header (no cookies, so no CSRF surface).
package dashboard

import (
	"embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/douglasdf99/litezap/internal/apierr"
)

//go:embed static
var staticFS embed.FS

// Prefix is where the dashboard is mounted.
const Prefix = "/app/"

type Options struct {
	// AdminToken is what a redeemed launch code hands to the browser.
	AdminToken string
	// LaunchCodes enables one-click login (POST /app/api/session). Set it only when the
	// server listens on loopback; nil disables the endpoint.
	LaunchCodes *LaunchCodes
}

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
	"connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler serves everything under Prefix. Mount it at Prefix (the path is not stripped).
func Handler(opt Options) http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix(strings.TrimSuffix(Prefix, "/"), http.FileServerFS(sub))
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"api/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]bool{"one_click_login": opt.LaunchCodes != nil})
	})
	mux.HandleFunc("POST "+Prefix+"api/session", func(w http.ResponseWriter, r *http.Request) {
		redeem(w, r, opt)
	})
	mux.Handle("GET "+Prefix, files)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		// Small, unversioned assets: always revalidate so upgrades show up immediately.
		if h.Get("Cache-Control") == "" {
			h.Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// redeem exchanges a launch code for the admin token. Every check fails closed with the
// same 403 so a probe learns nothing about which one tripped.
func redeem(w http.ResponseWriter, r *http.Request, opt Options) {
	w.Header().Set("Cache-Control", "no-store")
	if opt.LaunchCodes == nil {
		apierr.Write(w, apierr.New(http.StatusNotFound, "not_found", "one-click login is not enabled", false))
		return
	}
	if !loopbackRemote(r.RemoteAddr) || !loopbackHost(r.Host) || !sameOrigin(r) || !jsonBody(r) {
		apierr.Write(w, apierr.Forbidden())
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		apierr.Write(w, apierr.Validation("invalid JSON body"))
		return
	}
	if !opt.LaunchCodes.Redeem(req.Code) {
		apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "login code invalid, used or expired; open the dashboard again from the tray", false))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": opt.AdminToken})
}

func loopbackRemote(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackHost checks the Host header: a DNS-rebinding page reaches 127.0.0.1 under its
// own hostname, which fails here.
func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin requires Origin (sent by browsers on every POST) to match the Host; a
// non-browser client without Origin still has to pass the loopback checks.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(o)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func jsonBody(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
