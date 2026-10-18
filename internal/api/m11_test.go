package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/ipallow"
	"github.com/douglasdf99/litezap/internal/pairing"
)

// doH is e.do with extra request headers.
func (e *env) doH(method, path, token, body string, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })

func mustList(t *testing.T, v ...string) ipallow.List {
	t.Helper()
	l, err := ipallow.Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestPatchInstanceRenameTagsAllowlist(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Loja")
	base := "/v1/instances/" + id
	if code, out := e.do("PATCH", base, tok, `{"name":"  Loja Centro "}`); code != 200 || out["name"] != "Loja Centro" {
		t.Fatalf("instance rename = %d %v", code, out)
	}
	// Tags and the allowlist are admin-only.
	for _, body := range []string{`{"tags":["vip"]}`, `{"ip_allowlist":["10.0.0.0/8"]}`} {
		if code, out := e.do("PATCH", base, tok, body); code != 403 || errCode(out) != "forbidden" {
			t.Errorf("instance %s = %d %v", body, code, out)
		}
	}
	code, out := e.do("PATCH", base, admin, `{"tags":["VIP","sp","vip"],"ip_allowlist":["127.0.0.1","10.1.2.3/8"]}`)
	if code != 200 {
		t.Fatalf("admin patch = %d %v", code, out)
	}
	if tags := out["tags"].([]any); len(tags) != 2 || tags[0] != "vip" {
		t.Errorf("tags = %v", tags)
	}
	if l := out["ip_allowlist"].([]any); len(l) != 2 || l[0] != "127.0.0.1/32" || l[1] != "10.0.0.0/8" {
		t.Errorf("allowlist = %v", l)
	}
	for _, body := range []string{`{"name":""}`, `{"tags":["no spaces"]}`, `{"ip_allowlist":["nope"]}`, `{"tags":["` + strings.Repeat("a", 41) + `"]}`} {
		if code, out := e.do("PATCH", base, admin, body); code != 400 {
			t.Errorf("invalid %s = %d %v", body, code, out)
		}
	}
	if code, _ := e.do("PATCH", "/v1/instances/inst_missing", admin, `{"name":"x"}`); code != 404 {
		t.Errorf("missing = %d", code)
	}
	// Another instance's token cannot rename this one.
	_, tok2 := e.create("Outra")
	if code, _ := e.do("PATCH", base, tok2, `{"name":"x"}`); code != 403 {
		t.Errorf("other instance = %d", code)
	}
}

func TestInstanceIPAllowlist(t *testing.T) {
	e := newEnvWith(t, func(o *Options) {
		o.Access = AccessOptions{TrustedProxies: mustList(t, "127.0.0.1")}
		o.MCP = okHandler
	})
	id, tok := e.create("IP")
	base := "/v1/instances/" + id
	if code, _ := e.do("GET", base, tok, ""); code != 200 {
		t.Fatalf("no allowlist = %d", code)
	}
	if code, out := e.do("PATCH", base, admin, `{"ip_allowlist":["10.0.0.0/8"]}`); code != 200 {
		t.Fatalf("set = %d %v", code, out)
	}
	// The test client connects from 127.0.0.1 (a trusted proxy here): without a forwarded
	// address the client is the proxy itself, which is not in the list.
	if code, out := e.do("GET", base, tok, ""); code != 403 || errCode(out) != "forbidden" {
		t.Fatalf("outside = %d %v", code, out)
	}
	xff := func(v string) map[string]string { return map[string]string{"X-Forwarded-For": v} }
	if code, _ := e.doH("GET", base, tok, "", xff("10.4.5.6")); code != 200 {
		t.Fatalf("forwarded inside = %d", code)
	}
	// A forged leftmost entry does not help: the proxy-appended (rightmost) one counts.
	if code, _ := e.doH("GET", base, tok, "", xff("10.4.5.6, 203.0.113.50")); code != 403 {
		t.Fatalf("forged = %d", code)
	}
	// MCP and every route go through the same check.
	if code, _ := e.do("POST", "/mcp", tok, `{}`); code != 403 {
		t.Fatalf("mcp outside = %d", code)
	}
	// The admin token is not affected by the instance list.
	if code, _ := e.do("GET", base, admin, ""); code != 200 {
		t.Fatalf("admin = %d", code)
	}
	// Clearing it restores access.
	if code, _ := e.do("PATCH", base, admin, `{"ip_allowlist":[]}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := e.do("GET", base, tok, ""); code != 200 {
		t.Fatalf("cleared = %d", code)
	}
}

func TestForwardedForIgnoredWithoutTrustedProxies(t *testing.T) {
	e := newEnvWith(t, func(o *Options) {
		o.Access = AccessOptions{AdminAllow: mustList(t, "10.0.0.0/8")}
	})
	if code, _ := e.do("GET", "/v1/instances", admin, ""); code != 403 {
		t.Fatalf("admin outside = %d", code)
	}
	if code, _ := e.doH("GET", "/v1/instances", admin, "", map[string]string{"X-Forwarded-For": "10.0.0.1"}); code != 403 {
		t.Fatalf("spoofed XFF accepted = %d", code)
	}
}

func TestOperatorAllowlistSeparateFromAdmin(t *testing.T) {
	e2 := newEnvWith(t, func(o *Options) {
		o.Operator.Tokens = []string{operator}
		o.Access = AccessOptions{OperatorAllow: mustList(t, "10.0.0.0/8"), AdminAllow: mustList(t, "127.0.0.0/8")}
	})
	if code, _ := e2.do("GET", "/v1/me", operator, ""); code != 403 {
		t.Fatalf("operator outside = %d", code)
	}
	if code, _ := e2.do("GET", "/v1/me", admin, ""); code != 200 {
		t.Fatalf("admin inside = %d", code)
	}
}

func TestListInstancesFiltersAndPages(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for _, n := range []string{"Loja A", "Loja B", "Suporte"} {
		id, _ := e.create(n)
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	e.do("PATCH", "/v1/instances/"+ids[1], admin, `{"tags":["vip"]}`)
	names := func(out map[string]any) []string {
		var r []string
		for _, x := range out["instances"].([]any) {
			r = append(r, x.(map[string]any)["name"].(string))
		}
		return r
	}
	_, out := e.do("GET", "/v1/instances", admin, "")
	if len(names(out)) != 3 || out["next_cursor"] != nil {
		t.Fatalf("all = %v", out)
	}
	if _, out := e.do("GET", "/v1/instances?tag=VIP", admin, ""); strings.Join(names(out), ",") != "Loja B" {
		t.Errorf("tag = %v", out)
	}
	if _, out := e.do("GET", "/v1/instances?q=loja", admin, ""); strings.Join(names(out), ",") != "Loja A,Loja B" {
		t.Errorf("q = %v", out)
	}
	if _, out := e.do("GET", "/v1/instances?state=qr_pending,connected", admin, ""); len(names(out)) != 3 {
		t.Errorf("state = %v", out)
	}
	if code, _ := e.do("GET", "/v1/instances?state=bogus", admin, ""); code != 400 {
		t.Errorf("bad state = %d", code)
	}
	_, p1 := e.do("GET", "/v1/instances?limit=2", admin, "")
	cur, _ := p1["next_cursor"].(string)
	if len(names(p1)) != 2 || cur == "" {
		t.Fatalf("page 1 = %v", p1)
	}
	_, p2 := e.do("GET", "/v1/instances?limit=2&cursor="+url.QueryEscape(cur), admin, "")
	if strings.Join(names(p2), ",") != "Suporte" || p2["next_cursor"] != nil {
		t.Fatalf("page 2 = %v", p2)
	}
	for _, q := range []string{"limit=0", "limit=abc", "limit=501", "cursor=zzz&limit=2", "cursor=abc"} {
		if code, _ := e.do("GET", "/v1/instances?"+q, admin, ""); code != 400 {
			t.Errorf("%s = %d", q, code)
		}
	}
}

func TestBulkOperations(t *testing.T) {
	e := newEnv(t)
	a, tokA := e.create("A")
	b, _ := e.create("B")
	c, _ := e.create("C")
	e.do("PATCH", "/v1/instances/"+a, admin, `{"tags":["wave1"]}`)
	e.do("PATCH", "/v1/instances/"+b, admin, `{"tags":["wave1"]}`)
	if code, _ := e.do("POST", "/v1/instances/bulk", tokA, `{"action":"stop","ids":["`+a+`"]}`); code != 403 {
		t.Fatalf("instance token = %d", code)
	}
	code, out := e.do("POST", "/v1/instances/bulk", admin, `{"action":"stop","tag":"wave1"}`)
	if code != 200 || out["succeeded"] != float64(2) || out["failed"] != float64(0) {
		t.Fatalf("stop by tag = %d %v", code, out)
	}
	for _, id := range []string{a, b} {
		if _, v := e.do("GET", "/v1/instances/"+id, admin, ""); v["state"] != "stopped" {
			t.Errorf("%s state = %v", id, v["state"])
		}
	}
	if _, v := e.do("GET", "/v1/instances/"+c, admin, ""); v["state"] == "stopped" {
		t.Error("untagged instance stopped")
	}
	// Delete needs confirm; unknown ids fail alone.
	if code, out := e.do("POST", "/v1/instances/bulk", admin, `{"action":"delete","ids":["`+a+`"]}`); code != 400 {
		t.Fatalf("delete without confirm = %d %v", code, out)
	}
	code, out = e.do("POST", "/v1/instances/bulk", admin, `{"action":"delete","ids":["`+a+`","inst_nope","`+a+`"],"confirm":true}`)
	res := out["results"].([]any)
	if code != 200 || len(res) != 2 || out["succeeded"] != float64(1) {
		t.Fatalf("delete = %d %v", code, out)
	}
	if r1 := res[1].(map[string]any); r1["ok"] != false || r1["error"].(map[string]any)["code"] != "instance_not_found" {
		t.Fatalf("missing result = %v", r1)
	}
	if code, _ := e.do("GET", "/v1/instances/"+a, admin, ""); code != 404 {
		t.Fatalf("deleted instance = %d", code)
	}
	for _, body := range []string{`{"action":"explode","ids":["x"]}`, `{"action":"stop"}`, `{"action":"stop","ids":["x"],"tag":"y"}`} {
		if code, _ := e.do("POST", "/v1/instances/bulk", admin, body); code != 400 {
			t.Errorf("%s = %d", body, code)
		}
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = `"i` + string(rune('a'+i%26)) + strings.Repeat("x", i) + `"`
	}
	if code, _ := e.do("POST", "/v1/instances/bulk", admin, `{"action":"stop","ids":[`+strings.Join(many, ",")+`]}`); code != 400 {
		t.Errorf("101 ids = %d", code)
	}
}

func TestAPIKeysScopes(t *testing.T) {
	e := newEnvWith(t, func(o *Options) { o.MCP = okHandler })
	id, tok := e.create("K")
	other, _ := e.create("Other")
	base := "/v1/instances/" + id
	code, out := e.do("POST", base+"/api-keys", tok, `{"name":"bi","scopes":["read"]}`)
	if code != 201 || !strings.HasPrefix(out["key"].(string), "lzk_") {
		t.Fatalf("create = %d %v", code, out)
	}
	readKey := out["key"].(string)
	keyID := out["api_key"].(map[string]any)["id"].(string)
	_, out = e.do("POST", base+"/api-keys", admin, `{"name":"bot","scopes":["send"]}`)
	sendKey := out["key"].(string)

	if _, me := e.do("GET", "/v1/me", readKey, ""); me["role"] != "instance" || me["key_id"] != keyID || me["instance_id"] != id {
		t.Fatalf("me = %v", me)
	}
	// Read key: reads, never writes, QR, settings, keys, webhooks, MCP or another instance.
	for _, path := range []string{base, base + "/chats", base + "/contacts", base + "/queue", base + "/usage"} {
		if code, out := e.do("GET", path, readKey, ""); code == 403 || code == 401 {
			t.Errorf("read %s = %d %v", path, code, out)
		}
	}
	denied := []struct{ method, path, body string }{
		{"POST", base + "/messages", `{"to":"5511999990000","type":"text","text":"x"}`},
		{"GET", base + "/qr", ""},
		{"GET", base + "/settings", ""},
		{"PUT", base + "/settings", `{}`},
		{"POST", base + "/stop", ""},
		{"POST", base + "/logout", ""},
		{"PATCH", base, `{"name":"x"}`},
		{"GET", base + "/api-keys", ""},
		{"POST", base + "/api-keys", `{"name":"x","scopes":["read","send"]}`},
		{"GET", base + "/agent-hook", ""},
		{"GET", "/v1/webhooks", ""},
		{"GET", "/v1/instances", ""},
		{"GET", "/v1/instances/" + other, ""},
		{"HEAD", base, ""},
		{"POST", "/mcp", `{}`},
	}
	for _, d := range denied {
		if code, out := e.do(d.method, d.path, readKey, d.body); code != 403 {
			t.Errorf("read key %s %s = %d %v", d.method, d.path, code, out)
		}
	}
	// Send key: send and read the message status only.
	code, out = e.do("POST", base+"/messages", sendKey, `{"to":"5511999990000","type":"text","text":"oi"}`)
	if code == 403 || code == 401 {
		t.Fatalf("send = %d %v", code, out)
	}
	for _, path := range []string{base, base + "/chats", base + "/events"} {
		if code, _ := e.do("GET", path, sendKey, ""); code != 403 {
			t.Errorf("send key GET %s = %d", path, code)
		}
	}
	if code, _ := e.do("GET", base+"/messages/msg_nope", sendKey, ""); code == 403 {
		t.Errorf("send key message status = %d", code)
	}
	// Escaped paths are checked as routed.
	if code, _ := e.do("POST", base+"/messages%2F..%2Fstop", sendKey, ""); code != 403 && code != 404 && code != 405 {
		t.Errorf("escaped path = %d", code)
	}
	// Listing hides the secret; deleting revokes immediately.
	_, out = e.do("GET", base+"/api-keys", tok, "")
	if keys := out["api_keys"].([]any); len(keys) != 2 || keys[0].(map[string]any)["key"] != nil {
		t.Fatalf("list = %v", out)
	}
	if code, _ := e.do("DELETE", "/v1/instances/"+other+"/api-keys/"+keyID, admin, ""); code != 404 {
		t.Errorf("delete via another instance = %d", code)
	}
	if code, _ := e.do("DELETE", base+"/api-keys/"+keyID, tok, ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := e.do("GET", base, readKey, ""); code != 401 {
		t.Fatalf("revoked key = %d", code)
	}
	for _, body := range []string{`{"name":"x"}`, `{"name":"x","scopes":["admin"]}`, `{"name":"","scopes":["read"]}`,
		`{"name":"x","scopes":["read"],"ip_allowlist":["bad"]}`, `{"name":"x","scopes":["read"],"expires_in_seconds":-1}`} {
		if code, _ := e.do("POST", base+"/api-keys", tok, body); code != 400 {
			t.Errorf("invalid %s = %d", body, code)
		}
	}
}

func TestAPIKeyAllowlistAndExpiry(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("K")
	base := "/v1/instances/" + id
	_, out := e.do("POST", base+"/api-keys", tok, `{"name":"far","scopes":["read"],"ip_allowlist":["10.0.0.0/8"]}`)
	far := out["key"].(string)
	if code, _ := e.do("GET", base, far, ""); code != 403 {
		t.Fatalf("key outside allowlist = %d", code)
	}
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"short","scopes":["read"],"expires_in_seconds":1}`)
	short := out["key"].(string)
	if code, _ := e.do("GET", base, short, ""); code != 200 {
		t.Fatalf("fresh key = %d", code)
	}
	time.Sleep(1100 * time.Millisecond)
	if code, _ := e.do("GET", base, short, ""); code != 401 {
		t.Fatalf("expired key = %d", code)
	}
	// Deleting the instance removes its keys.
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"x","scopes":["read"]}`)
	k := out["key"].(string)
	e.do("DELETE", base, admin, "")
	if code, _ := e.do("GET", "/v1/me", k, ""); code != 401 {
		t.Fatalf("key of deleted instance = %d", code)
	}
}

func TestInstanceUsage(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("U")
	code, out := e.do("GET", "/v1/instances/"+id+"/usage?from=2026-01-01&to=2026-01-31", tok, "")
	if code != 200 || out["instance_id"] != id || out["usage"] == nil {
		t.Fatalf("usage = %d %v", code, out)
	}
	if code, _ := e.do("GET", "/v1/instances/"+id+"/usage?from=2026-02-01&to=2026-01-01", tok, ""); code != 400 {
		t.Fatalf("bad range = %d", code)
	}
}

func TestPairingLinksAPI(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("P")
	base := "/v1/instances/" + id + "/pairing-links"
	if code, _ := e.do("POST", base, tok, ""); code != 403 {
		t.Fatalf("instance token mint = %d", code)
	}
	code, link := e.do("POST", base, admin, "")
	if code != 201 || !strings.HasPrefix(link["url"].(string), "https://zap.example.com/pair/#t=pl1.") {
		t.Fatalf("mint = %d %v", code, link)
	}
	if strings.Contains(link["url"].(string), tok) || strings.Contains(link["token"].(string), "lzi_") {
		t.Fatal("link carries the instance token")
	}
	if code, _ := e.do("POST", base, admin, `{"ttl_seconds":3600}`); code != 400 {
		t.Fatalf("ttl over 10 min = %d", code)
	}
	// The page answers with the link token (no Bearer).
	st := func() (int, map[string]any) {
		return e.doH("GET", "/pair/api/state", "", "", map[string]string{pairing.TokenHeader: link["token"].(string)})
	}
	code, out := st()
	if code != 200 || out["state"] != "qr_pending" || out["qr"] == nil {
		t.Fatalf("page state = %d %v", code, out)
	}
	// The link token is not an API credential.
	if code, _ := e.do("GET", "/v1/instances/"+id, link["token"].(string), ""); code != 401 {
		t.Fatalf("link as bearer = %d", code)
	}
	if code, out := e.do("DELETE", base, admin, ""); code != 200 || out["revoked"] != float64(1) {
		t.Fatalf("revoke = %d %v", code, out)
	}
	if code, _ := st(); code != 410 {
		t.Fatalf("revoked page = %d", code)
	}
	if code, _ := e.do("POST", "/v1/instances/inst_nope/pairing-links", admin, ""); code != 404 {
		t.Fatalf("unknown instance = %d", code)
	}
}
