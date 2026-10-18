package worker

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSanitizeDetail(t *testing.T) {
	cases := map[string]string{
		"proxyconnect tcp: dial tcp 10.1.2.3:1080: connect: connection refused":                         "proxyconnect tcp: dial tcp 10.1.2.3:1080: connect: connection refused",
		`socks connect tcp socks5://bob:s3cr3t@proxy.example:1080->web.whatsapp.com:443: unknown error`: `socks connect tcp socks5://[redacted]@proxy.example:1080->web.whatsapp.com:443: unknown error`,
		"auth failed for alice:hunter2@10.0.0.1:3128":                                                   "auth failed for [redacted]@10.0.0.1:3128",
		"jid 5511999990000 token lzi_abcDEF123 rejected":                                                "jid [redacted] token [redacted] rejected",
		"http://user:@host:8080 refused":                                                                "http://[redacted]@host:8080 refused",
	}
	for in, want := range cases {
		if got := SanitizeDetail(in); got != want {
			t.Errorf("SanitizeDetail(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	if got := SanitizeDetail(strings.Repeat("x", 1000)); len([]rune(got)) != maxDetail+1 {
		t.Errorf("long detail = %d runes", len([]rune(got)))
	}
}

type detailRec struct {
	stateRec
	mu      sync.Mutex
	details []string
}

func (d *detailRec) ConnStateDetail(state, reason, jid, detail string) {
	d.ConnState(state, reason, jid)
	d.mu.Lock()
	d.details = append(d.details, detail)
	d.mu.Unlock()
}

func TestProxyFailureCarriesDetail(t *testing.T) {
	var calls atomic.Int32
	w, _ := newTestAdapter(t, true, func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("socks5://u:pw@10.9.9.9:1080 connection refused")}
		}
		return nil
	})
	rec := &detailRec{}
	w.sink = rec
	w.proxied = true
	w.startConnectLoop(w.loopCtx, false, 0)
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.details) == 0 || !strings.Contains(rec.details[0], "connection refused") || strings.Contains(rec.details[0], "pw") {
		t.Fatalf("details = %q", rec.details)
	}
	if states, _ := rec.snapshot(); len(states) == 0 || states[0] != (connEvt{"reconnecting", "proxy_unreachable"}) {
		t.Fatalf("states = %v", states)
	}
}

func TestReportConnDetailFallsBackToPlainState(t *testing.T) {
	r := &stateRec{}
	reportConnDetail(r, "reconnecting", "proxy_unreachable", "", errors.New("boom"))
	if states, _ := r.snapshot(); len(states) != 1 || states[0].reason != "proxy_unreachable" {
		t.Fatalf("states = %v", states)
	}
}
