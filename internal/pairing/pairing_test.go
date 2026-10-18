package pairing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

type clockT struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockT) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clockT) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newLinks(t *testing.T) (*Links, *store.Store, *clockT) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(context.Background(), store.Instance{ID: "inst_1", Name: "A", State: instance.QRPending, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	c := &clockT{t: time.Now()}
	return &Links{Store: st, Key: []byte("0123456789abcdef0123456789abcdef"), BaseURL: "https://zap.example.com/", Now: c.now}, st, c
}

func TestMintAndResolve(t *testing.T) {
	ctx := context.Background()
	l, st, c := newLinks(t)
	link, err := l.Mint(ctx, "inst_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link.URL, "https://zap.example.com/pair/#t=pl1.") || !strings.HasSuffix(link.URL, link.Token) {
		t.Fatalf("url = %s", link.URL)
	}
	if d := link.ExpiresAt.Sub(c.now()); d > MaxTTL || d < MaxTTL-2*time.Second {
		t.Fatalf("ttl = %v", d)
	}
	if got, err := l.Resolve(ctx, link.Token); err != nil || got.InstanceID != "inst_1" {
		t.Fatalf("resolve = %+v %v", got, err)
	}
	// Tampering with any part fails.
	parts := strings.Split(link.Token, ".")
	for _, bad := range []string{
		"", "garbage", parts[0] + "." + parts[1] + "." + parts[2],
		parts[0] + "." + parts[1] + "." + "9999999999" + "." + parts[3],   // later expiry, old signature
		parts[0] + ".AAAAAAAAAAAAAAAAAAAAAA." + parts[2] + "." + parts[3], // another link id
		link.Token[:len(link.Token)-2] + "xx",
	} {
		if _, err := l.Resolve(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Resolve(%q) = %v", bad, err)
		}
	}
	// Another key cannot verify it.
	other := *l
	other.Key = []byte("another-key-another-key-another-k")
	if _, err := other.Resolve(ctx, link.Token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other key = %v", err)
	}
	// Spent: still resolvable during the grace period (the page shows success), then not.
	if n, _ := st.MarkPairingLinksUsed(ctx, "inst_1", c.now()); n != 1 {
		t.Fatalf("spent = %d", n)
	}
	if got, err := l.Resolve(ctx, link.Token); err != nil || got.UsedAt.IsZero() {
		t.Fatalf("used within grace = %+v %v", got, err)
	}
	c.add(UsedGrace + time.Second)
	if _, err := l.Resolve(ctx, link.Token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("used after grace = %v", err)
	}
}

func TestExpiryRevokeAndLimits(t *testing.T) {
	ctx := context.Background()
	l, _, c := newLinks(t)
	if _, err := l.Mint(ctx, "inst_1", 11*time.Minute); err == nil {
		t.Fatal("ttl above 10 min accepted")
	}
	if _, err := l.Mint(ctx, "inst_1", 10*time.Second); err == nil {
		t.Fatal("ttl below 1 min accepted")
	}
	var e *apierr.Error
	if _, err := l.Mint(ctx, "nope", 0); !errors.As(err, &e) || e.Code != "instance_not_found" {
		t.Fatalf("unknown instance = %v", err)
	}
	short, _ := l.Mint(ctx, "inst_1", time.Minute)
	c.add(time.Minute)
	if _, err := l.Resolve(ctx, short.Token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired = %v", err)
	}
	live, _ := l.Mint(ctx, "inst_1", 0)
	if n, err := l.Revoke(ctx, "inst_1"); err != nil || n != 2 { // the expired one is revoked too (not spent)
		t.Fatalf("revoke = %d %v", n, err)
	}
	if _, err := l.Resolve(ctx, live.Token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked = %v", err)
	}
	for i := 0; i < maxLive; i++ {
		if _, err := l.Mint(ctx, "inst_1", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Mint(ctx, "inst_1", 0); !errors.As(err, &e) || e.Code != "limit_reached" {
		t.Fatalf("over the limit = %v", err)
	}
}

type recPub struct {
	mu  sync.Mutex
	evs []events.InstancePaired
}

func (r *recPub) Publish(_ string, typ string, data any) {
	if typ == events.TypeInstancePaired {
		r.mu.Lock()
		r.evs = append(r.evs, data.(events.InstancePaired))
		r.mu.Unlock()
	}
}

func TestObserverPublishesPairedOncePerPairing(t *testing.T) {
	ctx := context.Background()
	l, st, c := newLinks(t)
	link, _ := l.Mint(ctx, "inst_1", 0)
	_ = st.SetInstanceJID(ctx, "inst_1", "5511999990000:3@s.whatsapp.net")
	pub := &recPub{}
	o := &Observer{Store: st, Events: pub, Now: c.now}
	o.StateChanged("inst_1", instance.Connected, "") // a reconnection: no event
	o.StateChanged("inst_1", instance.QRPending, "")
	o.StateChanged("inst_1", instance.Connecting, "")
	o.StateChanged("inst_1", instance.Connected, "")
	o.StateChanged("inst_1", instance.Reconnecting, "")
	o.StateChanged("inst_1", instance.Connected, "")
	o.Close()
	if len(pub.evs) != 1 || pub.evs[0].Phone != "5511999990000" || !pub.evs[0].ViaLink {
		t.Fatalf("events = %+v", pub.evs)
	}
	if got, _ := st.GetPairingLink(ctx, link.ID); got.UsedAt.IsZero() {
		t.Fatal("link not spent")
	}
	// A later pairing without a live link.
	o.StateChanged("inst_1", instance.QRPending, "")
	o.StateChanged("inst_1", instance.Connected, "")
	o.Close()
	if len(pub.evs) != 2 || pub.evs[1].ViaLink {
		t.Fatalf("events = %+v", pub.evs)
	}
}

type fakeInstances struct {
	mu      sync.Mutex
	state   instance.State
	starts  int
	phones  []string
	pairErr error
}

func (f *fakeInstances) Get(_ context.Context, id string) (service.InstanceView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return service.InstanceView{ID: id, State: f.state, Name: "secret-name"}, nil
}
func (f *fakeInstances) QR(context.Context, string) (string, error) { return "2@QRCODE", nil }
func (f *fakeInstances) PairPhone(_ context.Context, _ string, phone string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phones = append(f.phones, phone)
	return "ABCD1234", f.pairErr
}
func (f *fakeInstances) Start(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.state = instance.Starting
	return nil
}

func call(t *testing.T, srv *httptest.Server, method, path, tok, body string) (*http.Response, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if tok != "" {
		req.Header.Set(TokenHeader, tok)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp, out
}

func TestPage(t *testing.T) {
	ctx := context.Background()
	l, st, _ := newLinks(t)
	inst := &fakeInstances{state: instance.QRPending}
	srv := httptest.NewServer(Handler(PageOptions{Links: l, Instances: inst}))
	defer srv.Close()
	link, _ := l.Mint(ctx, "inst_1", 0)

	resp, _ := call(t, srv, "GET", "/pair/", "", "")
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'self'") ||
		strings.Contains(csp, "unsafe") || resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("page headers = %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := call(t, srv, "GET", "/pair/embed.js", "", ""); resp.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatalf("embed.js headers = %v", resp.Header)
	}

	// No token / bad token: 410 without detail.
	for _, tok := range []string{"", "pl1.x.1.y"} {
		if resp, out := call(t, srv, "GET", "/pair/api/state", tok, ""); resp.StatusCode != 410 || out["error"] == nil {
			t.Fatalf("state(%q) = %d %v", tok, resp.StatusCode, out)
		}
	}
	// The token in the query string is not accepted.
	if resp, _ := call(t, srv, "GET", "/pair/api/state?t="+link.Token, "", ""); resp.StatusCode != 410 {
		t.Fatalf("query token = %d", resp.StatusCode)
	}
	resp, out := call(t, srv, "GET", "/pair/api/state", link.Token, "")
	if resp.StatusCode != 200 || out["state"] != "qr_pending" || !strings.HasPrefix(out["qr"].(string), "data:image/png;base64,") || out["done"] != false {
		t.Fatalf("state = %d %v", resp.StatusCode, out)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("api cache = %v", resp.Header)
	}
	// Only state, QR and flags: never the name, id or token of the instance.
	for k := range out {
		if k != "state" && k != "done" && k != "qr" && k != "can_start" {
			t.Fatalf("unexpected field %q in %v", k, out)
		}
	}
	// POST needs JSON (blocks form posts from other sites).
	req, _ := http.NewRequest("POST", srv.URL+"/pair/api/phone", strings.NewReader("phone=1"))
	req.Header.Set(TokenHeader, link.Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post = %d", r.StatusCode)
	}
	for i := 0; i < maxPhoneCodes; i++ {
		if resp, out := call(t, srv, "POST", "/pair/api/phone", link.Token, `{"phone":"5511999990000"}`); resp.StatusCode != 200 || out["code"] != "ABCD1234" {
			t.Fatalf("phone = %d %v", resp.StatusCode, out)
		}
	}
	if resp, out := call(t, srv, "POST", "/pair/api/phone", link.Token, `{"phone":"5511999990000"}`); resp.StatusCode != 429 {
		t.Fatalf("phone over budget = %d %v", resp.StatusCode, out)
	}

	// Stopped instance: the page may start it.
	inst.state = instance.Stopped
	if _, out := call(t, srv, "GET", "/pair/api/state", link.Token, ""); out["can_start"] != true || out["qr"] != nil {
		t.Fatalf("stopped = %v", out)
	}
	if resp, _ := call(t, srv, "POST", "/pair/api/start", link.Token, `{}`); resp.StatusCode != 200 || inst.starts != 1 {
		t.Fatalf("start = %d %d", resp.StatusCode, inst.starts)
	}
	// Running: start is a no-op.
	if resp, _ := call(t, srv, "POST", "/pair/api/start", link.Token, `{}`); resp.StatusCode != 200 || inst.starts != 1 {
		t.Fatalf("start again = %d %d", resp.StatusCode, inst.starts)
	}

	// Paired: done, then actions refuse the spent link.
	_, _ = st.MarkPairingLinksUsed(ctx, "inst_1", time.Now())
	if _, out := call(t, srv, "GET", "/pair/api/state", link.Token, ""); out["done"] != true || out["state"] != "connected" {
		t.Fatalf("after pairing = %v", out)
	}
	if resp, _ := call(t, srv, "POST", "/pair/api/phone", link.Token, `{"phone":"5511999990000"}`); resp.StatusCode != 410 {
		t.Fatalf("phone after pairing = %d", resp.StatusCode)
	}
}

func TestPageFrameAncestors(t *testing.T) {
	l, _, _ := newLinks(t)
	srv := httptest.NewServer(Handler(PageOptions{Links: l, Instances: &fakeInstances{},
		FrameAncestors: []string{"https://app.example.com", "https://*.partner.io"}}))
	defer srv.Close()
	resp, _ := call(t, srv, "GET", "/pair/", "", "")
	if !strings.HasSuffix(resp.Header.Get("Content-Security-Policy"), "frame-ancestors https://app.example.com https://*.partner.io") ||
		resp.Header.Get("X-Frame-Options") != "" {
		t.Fatalf("headers = %v", resp.Header)
	}
}
