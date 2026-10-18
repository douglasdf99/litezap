package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRemoteCodesWorkFromAnyAddressSameOriginOnly(t *testing.T) {
	tray, remote := NewLaunchCodes(), NewRemoteLaunchCodes()
	h := Handler(Options{AdminToken: "lza_secret", LaunchCodes: tray, RemoteCodes: remote})
	good := func(code string) sessReq {
		return sessReq{remote: "203.0.113.7:5555", host: "abc.litezap.cloud", origin: "https://abc.litezap.cloud",
			ctype: "application/json", fetchSite: "same-origin", body: `{"code":"` + code + `"}`}
	}
	a, exp := remote.MintWithExpiry()
	if time.Until(exp) > RemoteLaunchCodeTTL || time.Until(exp) < RemoteLaunchCodeTTL-time.Minute {
		t.Fatalf("expiry = %v", exp)
	}
	b := remote.Mint()
	rec := session(h, good(a))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "lza_secret") {
		t.Fatalf("remote exchange = %d %s", rec.Code, rec.Body)
	}
	if rec := session(h, good(a)); rec.Code != 403 {
		t.Fatalf("replay = %d", rec.Code)
	}
	// Another member's pending code survives.
	if rec := session(h, good(b)); rec.Code != 200 {
		t.Fatalf("second member = %d", rec.Code)
	}
	// Still same-origin JSON only, and a refused request does not burn the code.
	for name, mut := range map[string]func(*sessReq){
		"cross-origin": func(r *sessReq) { r.origin = "https://evil.example" },
		"form post":    func(r *sessReq) { r.ctype = "text/plain" },
	} {
		c := remote.Mint()
		r := good(c)
		mut(&r)
		if rec := session(h, r); rec.Code != 403 {
			t.Fatalf("%s = %d", name, rec.Code)
		}
		if !remote.Redeem(c) {
			t.Fatalf("%s consumed the code", name)
		}
	}
	// A tray code never works from a non-loopback client, even with remote codes on.
	tc := tray.Mint()
	if rec := session(h, good(tc)); rec.Code != 403 {
		t.Fatalf("tray code from remote = %d", rec.Code)
	}
	if !tray.Redeem(tc) {
		t.Fatal("remote attempt consumed the tray code")
	}
	var info map[string]bool
	_ = json.NewDecoder(get(t, Handler(Options{RemoteCodes: remote}), "/app/api/info").Body).Decode(&info)
	if !info["one_click_login"] {
		t.Fatal("info does not report remote one-click login")
	}
}

func TestRemoteCodesExpire(t *testing.T) {
	l := NewRemoteLaunchCodes()
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	c := l.Mint()
	now = now.Add(RemoteLaunchCodeTTL + time.Second)
	if l.Redeem(c) {
		t.Fatal("expired remote code accepted")
	}
}
