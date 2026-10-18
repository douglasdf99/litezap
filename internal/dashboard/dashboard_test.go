package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLaunchCodesSingleUseAndExpiry(t *testing.T) {
	l := NewLaunchCodes()
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }

	c := l.Mint()
	if len(c) < 40 {
		t.Fatalf("code too short: %q", c)
	}
	if !l.Redeem(c) {
		t.Fatal("fresh code rejected")
	}
	if l.Redeem(c) {
		t.Fatal("code redeemed twice")
	}
	if l.Redeem("") || l.Redeem("nope") {
		t.Fatal("bogus code accepted")
	}

	c = l.Mint()
	now = now.Add(LaunchCodeTTL + time.Second)
	if l.Redeem(c) {
		t.Fatal("expired code accepted")
	}

	// A successful redeem drops the other pending codes.
	a, b := l.Mint(), l.Mint()
	if !l.Redeem(b) || l.Redeem(a) {
		t.Fatal("pending codes should be dropped after a successful redeem")
	}

	// Minting past the cap drops the oldest pending code.
	first := l.Mint()
	for i := 0; i < maxPendingCodes; i++ {
		l.Mint()
	}
	if l.Redeem(first) {
		t.Fatal("oldest code should have been dropped")
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestStaticAssetsAndHeaders(t *testing.T) {
	h := Handler(Options{AdminToken: "adm"})
	for path, want := range map[string]string{
		"/app/":               "<title>LiteZap</title>",
		"/app/app.js":         "consumeLaunchCode",
		"/app/api.js":         "subscribe",
		"/app/style.css":      "--accent",
		"/app/logo.svg":       "<svg",
		"/app/webhooks.js":    "webhooksPanel",
		"/app/agent.js":       "agentPanel",
		"/app/profile.js":     "profilePanel",
		"/app/chatactions.js": "chatActions",
		"/app/groups.js":      "groupsPanel",
		"/app/social.js":      "labelsPanel",
	} {
		rec := get(t, h, path)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s: body lacks %q", path, want)
		}
		hd := rec.Header()
		if !strings.Contains(hd.Get("Content-Security-Policy"), "script-src 'self'") ||
			hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s: missing security headers: %v", path, hd)
		}
	}
	for p, want := range map[string]string{"/app/app.js": "text/javascript; charset=utf-8", "/app/style.css": "text/css; charset=utf-8",
		"/app/logo.svg": "image/svg+xml", "/app/": "text/html; charset=utf-8"} {
		if ct := get(t, h, p).Header().Get("Content-Type"); ct != want {
			t.Fatalf("%s content type %q", p, ct)
		}
	}
	if rec := get(t, h, "/app/missing.js"); rec.Code != 404 {
		t.Fatalf("missing asset: %d", rec.Code)
	}
	// The embedded assets carry no secrets and never reference the token.
	if strings.Contains(get(t, h, "/app/").Body.String(), "adm") {
		t.Fatal("index leaks the admin token")
	}
}

func TestNoInnerHTMLInAssets(t *testing.T) {
	// XSS guard: the dashboard builds the DOM with text nodes only.
	entries, err := staticFS.ReadDir("static")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, _ := staticFS.ReadFile("static/" + e.Name())
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
			if strings.Contains(string(b), bad) {
				t.Fatalf("%s uses %s", e.Name(), bad)
			}
		}
	}
}

func TestInfo(t *testing.T) {
	var out map[string]bool
	_ = json.NewDecoder(get(t, Handler(Options{}), "/app/api/info").Body).Decode(&out)
	if out["one_click_login"] {
		t.Fatal("one-click login reported without codes")
	}
	_ = json.NewDecoder(get(t, Handler(Options{LaunchCodes: NewLaunchCodes()}), "/app/api/info").Body).Decode(&out)
	if !out["one_click_login"] {
		t.Fatal("one-click login not reported")
	}
}

type sessReq struct {
	remote, host, origin, ctype, fetchSite, body string
}

func session(h http.Handler, r sessReq) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/app/api/session", strings.NewReader(r.body))
	req.RemoteAddr = r.remote
	req.Host = r.host
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.ctype != "" {
		req.Header.Set("Content-Type", r.ctype)
	}
	if r.fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", r.fetchSite)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSessionExchange(t *testing.T) {
	codes := NewLaunchCodes()
	h := Handler(Options{AdminToken: "lza_secret", LaunchCodes: codes})
	good := func(code string) sessReq {
		return sessReq{remote: "127.0.0.1:5555", host: "127.0.0.1:8787", origin: "http://127.0.0.1:8787",
			ctype: "application/json", fetchSite: "same-origin", body: `{"code":"` + code + `"}`}
	}

	code := codes.Mint()
	rec := session(h, good(code))
	if rec.Code != 200 {
		t.Fatalf("valid exchange: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "lza_secret") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange response: %s %v", rec.Body, rec.Header())
	}
	if rec := session(h, good(code)); rec.Code != 403 {
		t.Fatalf("replayed code: %d", rec.Code)
	}

	cases := map[string]func(*sessReq){
		"non-loopback client":        func(r *sessReq) { r.remote = "192.168.0.10:5555" },
		"dns rebinding host":         func(r *sessReq) { r.host = "evil.example:8787"; r.origin = "http://evil.example:8787" },
		"cross-origin":               func(r *sessReq) { r.origin = "http://evil.example" },
		"null origin":                func(r *sessReq) { r.origin = "null" },
		"form post":                  func(r *sessReq) { r.ctype = "text/plain" },
		"cross-site no origin":       func(r *sessReq) { r.origin = ""; r.fetchSite = "cross-site" },
		"other loopback port origin": func(r *sessReq) { r.origin = "http://127.0.0.1:9999" },
	}
	for name, mut := range cases {
		c := codes.Mint()
		r := good(c)
		mut(&r)
		rec := session(h, r)
		if rec.Code != 403 {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), "lza_secret") {
			t.Fatalf("%s: token leaked", name)
		}
		// A refused request must not burn the code: the real browser can still use it.
		if !codes.Redeem(c) {
			t.Fatalf("%s: refused request consumed the code", name)
		}
	}

	// localhost and [::1] are loopback hosts too.
	for _, host := range []string{"localhost:8787", "[::1]:8787"} {
		r := good(codes.Mint())
		r.host, r.origin, r.remote = host, "http://"+host, "[::1]:5555"
		if rec := session(h, r); rec.Code != 200 {
			t.Fatalf("host %s: %d", host, rec.Code)
		}
	}
	// curl-like client on loopback without Origin.
	r := good(codes.Mint())
	r.origin, r.fetchSite = "", ""
	if rec := session(h, r); rec.Code != 200 {
		t.Fatalf("no-origin loopback client: %d", rec.Code)
	}
}

func TestSessionDisabledWithoutCodes(t *testing.T) {
	h := Handler(Options{AdminToken: "lza_secret"})
	rec := session(h, sessReq{remote: "127.0.0.1:1", host: "127.0.0.1:8787", ctype: "application/json", body: `{"code":"x"}`})
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "lza_secret") {
		t.Fatalf("disabled endpoint: %d %s", rec.Code, rec.Body)
	}
}
