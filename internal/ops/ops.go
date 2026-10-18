// Package ops serves the operational endpoints (cloud spec E2, E3, E12): /healthz,
// /readyz and /metrics (Prometheus text format), on a separate listener without a token
// (ops_listen_addr: platform health checks and the metrics scraper on a private network)
// and, protected like before, on the API listener.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/usage"
)

// Health is the readiness of the process: not ready once draining (shutdown started,
// so a platform stops routing to it and an upgrade wave can wait) or when the store fails.
type Health struct {
	Ping     func(context.Context) error
	draining atomic.Bool
}

var ErrDraining = errors.New("shutting down")

func (h *Health) Ready(ctx context.Context) error {
	if h.draining.Load() {
		return ErrDraining
	}
	if h.Ping != nil {
		return h.Ping(ctx)
	}
	return nil
}

// Drain marks the process as shutting down (readiness fails from now on).
func (h *Health) Drain()                     { h.draining.Store(true) }
func (h *Health) Draining() bool             { return h.draining.Load() }
func (h *Health) ready(r *http.Request) bool { return h.Ready(r.Context()) == nil }

// Handler serves /healthz, /readyz and /metrics without authentication. Mount it only on
// a listener that is not exposed to the internet.
func Handler(h *Health, metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		switch err := h.Ready(r.Context()); {
		case errors.Is(err, ErrDraining):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		case err != nil:
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		}
	})
	if metrics != nil {
		mux.Handle("GET /metrics", metrics)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Source is what /metrics reads. Nil fields are skipped.
type Source struct {
	Store   *store.Store
	Usage   *usage.Recorder
	Proxy   *usage.ProxyWatch
	Health  *Health
	Sending *service.Sending
	Version string
}

var allStates = []instance.State{instance.Created, instance.Starting, instance.QRPending, instance.Connecting,
	instance.Connected, instance.Reconnecting, instance.LoggedOut, instance.Banned, instance.Failed, instance.Stopped}

// Metrics renders the Prometheus text exposition (format 0.0.4). Gauges come from the
// store at scrape time; counters are process totals since start.
func Metrics(src Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var b strings.Builder
		m := &writer{w: &b}
		m.gauge("litezap_up", "The process is running.", nil, 1)
		if src.Version != "" {
			m.gauge("litezap_build_info", "Build information.", map[string]string{"version": src.Version}, 1)
		}
		if h := src.Health; h != nil {
			m.gauge("litezap_ready", "1 when /readyz succeeds.", nil, b2f(h.ready(r)))
			m.gauge("litezap_draining", "1 once the graceful shutdown started.", nil, b2f(h.Draining()))
		}
		if st := src.Store; st != nil {
			if by, err := st.CountInstancesByState(ctx); err == nil {
				m.help("litezap_instances", "gauge", "Instances by state.")
				for _, s := range allStates {
					m.sample("litezap_instances", map[string]string{"state": string(s)}, float64(by[string(s)]))
				}
			}
			if by, err := st.CountActiveOutbound(ctx); err == nil {
				m.help("litezap_queue_messages", "gauge", "Sends waiting in the queue, by status.")
				for _, s := range []string{"queued", "in_flight"} {
					m.sample("litezap_queue_messages", map[string]string{"status": s}, float64(by[s]))
				}
			}
			if by, err := st.CountDeliveriesByStatus(ctx); err == nil {
				m.help("litezap_webhook_deliveries", "gauge", "Webhook deliveries kept in the outbox, by status.")
				for _, s := range []string{store.DeliveryPending, store.DeliveryDelivered, store.DeliveryFailed} {
					m.sample("litezap_webhook_deliveries", map[string]string{"status": s}, float64(by[s]))
				}
			}
		}
		if u := src.Usage; u != nil {
			c := u.Counters()
			m.counter("litezap_messages_sent_total", "API sends that reached WhatsApp.", nil, float64(c.MessagesSent))
			m.counter("litezap_messages_received_total", "Messages received.", nil, float64(c.MessagesReceived))
			m.counter("litezap_media_bytes_total", "Attachment bytes sent and received.", nil, float64(c.MediaBytes))
			states := make([]string, 0, len(c.StateChanges))
			for s := range c.StateChanges {
				states = append(states, s)
			}
			sort.Strings(states)
			m.help("litezap_instance_state_changes_total", "counter", "Instance state changes, by new state (starting counts worker starts).")
			for _, s := range states {
				m.sample("litezap_instance_state_changes_total", map[string]string{"state": s}, float64(c.StateChanges[s]))
			}
		}
		if p := src.Proxy; p != nil {
			m.counter("litezap_proxy_failures_total", "Proxy outages (instance.proxy_failed first notices).", nil, float64(p.Failures()))
			m.gauge("litezap_proxy_unreachable_instances", "Instances currently unable to reach WhatsApp through their proxy.", nil, float64(p.Unreachable()))
		}
		if s := src.Sending; s != nil {
			if st, err := s.Status(ctx); err == nil {
				m.gauge("litezap_sending_suspended", "1 while sending is suspended for every instance.", nil, b2f(st.Suspended))
				m.gauge("litezap_sending_suspended_instances", "Instances whose sending is suspended.", nil, float64(len(st.Instances)))
			}
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		m.gauge("go_goroutines", "Number of goroutines.", nil, float64(runtime.NumGoroutine()))
		m.gauge("go_memstats_heap_alloc_bytes", "Heap bytes allocated and in use (supervisor only).", nil, float64(ms.HeapAlloc))
		m.gauge("go_memstats_sys_bytes", "Bytes obtained from the OS (supervisor only).", nil, float64(ms.Sys))
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, b.String())
	})
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

type writer struct{ w io.Writer }

func (m *writer) help(name, typ, help string) {
	fmt.Fprintf(m.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (m *writer) sample(name string, labels map[string]string, v float64) {
	if len(labels) == 0 {
		fmt.Fprintf(m.w, "%s %g\n", name, v)
		return
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	fmt.Fprintf(m.w, "%s{%s} %g\n", name, strings.Join(parts, ","), v)
}

func (m *writer) gauge(name, help string, labels map[string]string, v float64) {
	m.help(name, "gauge", help)
	m.sample(name, labels, v)
}

func (m *writer) counter(name, help string, labels map[string]string, v float64) {
	m.help(name, "counter", help)
	m.sample(name, labels, v)
}
