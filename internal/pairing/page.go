package pairing

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed static
var staticFS embed.FS

// Prefix is where the page is mounted.
const Prefix = "/pair/"

// TokenHeader carries the link token on the page's API calls (never the URL).
const TokenHeader = "X-Pairing-Token"

// Instances is what the page needs from service.Instances.
type Instances interface {
	Get(ctx context.Context, id string) (service.InstanceView, error)
	QR(ctx context.Context, id string) (string, error)
	PairPhone(ctx context.Context, id, phone string) (string, error)
	Start(ctx context.Context, id string) error
}

type PageOptions struct {
	Links     *Links
	Instances Instances
	// FrameAncestors are the origins allowed to embed the page (validated by config).
	FrameAncestors []string
	Log            *slog.Logger
}

// per-link budgets of the actions that reach the worker.
const (
	maxPhoneCodes = 5
	maxStarts     = 5
)

type page struct {
	opt PageOptions
	csp string

	mu      sync.Mutex
	budgets map[string]*budget // link ID
}

type budget struct {
	phone, start int
	until        time.Time
}

// Handler serves the page, its assets and its API under Prefix (mount it there).
func Handler(opt PageOptions) http.Handler {
	p := &page{opt: opt, budgets: map[string]*budget{}}
	ancestors := "'none'"
	if len(opt.FrameAncestors) > 0 {
		ancestors = strings.Join(opt.FrameAncestors, " ")
	}
	p.csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors " + ancestors
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"{$}", p.asset("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET "+Prefix+"pair.js", p.asset("pair.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+Prefix+"pair.css", p.asset("pair.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET "+Prefix+"embed.js", p.asset("embed.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET "+Prefix+"api/state", p.withLink(p.state))
	mux.HandleFunc("POST "+Prefix+"api/phone", p.withLink(p.phone))
	mux.HandleFunc("POST "+Prefix+"api/start", p.withLink(p.start))
	return mux
}

func (p *page) asset(name, typ string) http.HandlerFunc {
	body, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", typ)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		switch name {
		case "index.html":
			h.Set("Content-Security-Policy", p.csp)
			if len(p.opt.FrameAncestors) == 0 {
				h.Set("X-Frame-Options", "DENY") // older browsers without frame-ancestors
			}
			h.Set("Cache-Control", "no-store")
		case "embed.js":
			// Loaded by the partner's page with <script src>: cross-origin by design.
			h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		default:
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
		}
		_, _ = w.Write(body)
	}
}

type linkHandler func(w http.ResponseWriter, r *http.Request, linkID, instanceID string, used bool)

// withLink resolves the token header; API answers are never cacheable nor frameable.
func (p *page) withLink(next linkHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodPost {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				apierr.Write(w, apierr.New(http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json", false))
				return
			}
		}
		link, err := p.opt.Links.Resolve(r.Context(), r.Header.Get(TokenHeader))
		if err != nil {
			p.writeErr(w, err)
			return
		}
		next(w, r, link.ID, link.InstanceID, !link.UsedAt.IsZero())
	}
}

func (p *page) writeErr(w http.ResponseWriter, err error) {
	var e *apierr.Error
	if !errors.As(err, &e) && p.opt.Log != nil {
		p.opt.Log.Error("pairing page", "err", err)
	}
	apierr.Write(w, err)
}

type stateOut struct {
	State instance.State `json:"state"`
	// Done is true once the instance is paired (the page shows success and stops).
	Done bool `json:"done"`
	// QR is the current code as a PNG data URL while the instance waits for a scan.
	QR string `json:"qr,omitempty"`
	// CanStart tells the page to offer starting a stopped instance.
	CanStart bool `json:"can_start,omitempty"`
}

func (p *page) state(w http.ResponseWriter, r *http.Request, _, id string, used bool) {
	v, err := p.opt.Instances.Get(r.Context(), id)
	if err != nil {
		p.writeErr(w, err)
		return
	}
	out := stateOut{State: v.State, Done: used || v.State == instance.Connected}
	if used {
		out.State = instance.Connected
	}
	if !out.Done {
		switch v.State {
		case instance.QRPending:
			if code, err := p.opt.Instances.QR(r.Context(), id); err == nil {
				if png, err := qrcode.Encode(code, qrcode.Medium, 320); err == nil {
					out.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
				}
			}
		case instance.Created, instance.Stopped, instance.LoggedOut, instance.Failed:
			out.CanStart = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// spend takes one unit of a link's budget; false when it is exhausted.
func (p *page) spend(linkID string, phone bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for id, b := range p.budgets {
		if now.After(b.until) {
			delete(p.budgets, id)
		}
	}
	b := p.budgets[linkID]
	if b == nil {
		b = &budget{until: now.Add(MaxTTL + UsedGrace)}
		p.budgets[linkID] = b
	}
	if phone {
		b.phone++
		return b.phone <= maxPhoneCodes
	}
	b.start++
	return b.start <= maxStarts
}

var errBudget = apierr.New(http.StatusTooManyRequests, "rate_limited", "too many attempts with this pairing link; ask for a new one", false)

func (p *page) phone(w http.ResponseWriter, r *http.Request, linkID, id string, used bool) {
	if used {
		p.writeErr(w, ErrInvalid)
		return
	}
	var req struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		p.writeErr(w, apierr.Validation("invalid JSON body"))
		return
	}
	if !p.spend(linkID, true) {
		p.writeErr(w, errBudget)
		return
	}
	code, err := p.opt.Instances.PairPhone(r.Context(), id, req.Phone)
	if err != nil {
		p.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

func (p *page) start(w http.ResponseWriter, r *http.Request, linkID, id string, used bool) {
	if used {
		p.writeErr(w, ErrInvalid)
		return
	}
	v, err := p.opt.Instances.Get(r.Context(), id)
	if err != nil {
		p.writeErr(w, err)
		return
	}
	switch v.State {
	case instance.Created, instance.Stopped, instance.LoggedOut, instance.Failed:
	default: // already running (or banned): nothing to do
		writeJSON(w, http.StatusOK, map[string]any{"state": v.State})
		return
	}
	if !p.spend(linkID, false) {
		p.writeErr(w, errBudget)
		return
	}
	if err := p.opt.Instances.Start(r.Context(), id); err != nil {
		p.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": instance.Starting})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
