package api

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/webhook"
	"github.com/douglasdf99/litezap/internal/workertest"
)

const operator = "lzo_operator_token_0123456789"

type cloudCfg struct {
	maxInstances int
	requireProxy bool
	locked       []string
}

// newCloudEnv is newEnv with the managed-hosting options: an operator token, the policy
// and the send suspension wired as serve does.
func newCloudEnv(t *testing.T, c cloudCfg) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp := workertest.NewSpawner()
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-API") }
	log := slog.New(slog.DiscardHandler)
	mopt := manager.DefaultOptions()
	mopt.HeartbeatInterval = time.Hour
	mgr := manager.New(st, sp, mopt, log)
	defaults := settings.Defaults()
	defaults.MinIntervalMS, defaults.JitterMS, defaults.TypingSimulation, defaults.Ramp.Enabled = 0, 0, false, false
	defaults.SendWindow, defaults.UnsafePacing = settings.Window{}, true
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	disp := webhook.New(st, webhook.Options{AllowPrivate: true, Log: log})
	bus := events.NewBus(nil, disp)
	sending := &service.Sending{Store: st}
	q := queue.New(st, mgr, queue.Options{Defaults: defaults, Log: log, Resolver: resolver, Events: bus, Hold: sending.Held})
	sending.Wake = q.Wake
	if err := sending.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log, Events: bus}, q, events.Observer{Bus: bus}})
	if err := disp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
		disp.Close(ctx)
	})
	policy := &service.Policy{Store: st, MaxInstances: c.maxInstances, RequireProxy: c.requireProxy}
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir, Queues: q, Events: bus, Policy: policy}
	msg := &service.Messaging{Store: st, Manager: mgr, Queues: q, Fetcher: &media.Fetcher{AllowPrivate: true},
		Signer:  media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://example.invalid"),
		DataDir: dir, Defaults: defaults, Resolver: resolver, SignedURLs: true}
	opt := Options{AdminToken: admin, Ready: st.Ping, Events: bus,
		Webhooks: &service.Webhooks{Store: st, Dispatcher: disp},
		Operator: OperatorOptions{Tokens: []string{"lzo_old_operator_token_000000", operator}, Locked: c.locked,
			Policy: policy, Sending: sending, PublicURL: "https://p.example.com/",
			MintLogin: func() (string, time.Time) { return "CODE123", time.Now().Add(time.Minute) }}}
	h := New(svc, service.GuardedMessaging{Messaging: msg, Sending: sending}, opt, log)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { bus.CloseSubscribers(); srv.Close() })
	return &env{t: t, srv: srv, sp: sp, bus: bus, st: st}
}

func TestRolesInMe(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{})
	for tok, want := range map[string]string{operator: "operator", "lzo_old_operator_token_000000": "operator", admin: "admin"} {
		if code, body := e.do("GET", "/v1/me", tok, ""); code != 200 || body["role"] != want || body["admin"] != true {
			t.Errorf("me(%s) = %d %v", want, code, body)
		}
	}
	_, tok := e.create("R")
	if _, body := e.do("GET", "/v1/me", tok, ""); body["role"] != "instance" {
		t.Errorf("instance role = %v", body)
	}
	if code, _ := e.do("GET", "/v1/me", "lzo_operator_token_012345678", ""); code != 401 {
		t.Errorf("near-miss operator token = %d", code)
	}
}

func TestLockedSettingsNeedTheOperator(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{locked: []string{"proxy_url", "unsafe_pacing"}})
	id, tok := e.create("L")
	base := "/v1/instances/" + id + "/settings"
	for _, body := range []string{`{"proxy_url":"socks5://u:p@h:1080"}`, `{"unsafe_pacing":true}`, `{"proxy_url":""}`} {
		if code, out := e.do("PUT", base, admin, body); code != 403 || errCode(out) != "forbidden" {
			t.Errorf("admin %s = %d %v", body, code, out)
		}
		if code, _ := e.do("PUT", base, tok, body); code != 403 {
			t.Errorf("instance %s = %d", body, code)
		}
	}
	if code, out := e.do("PUT", base, admin, `{"max_per_minute":10}`); code != 200 || out["max_per_minute"] != float64(10) {
		t.Fatalf("admin unlocked field = %d %v", code, out)
	}
	if code, out := e.do("PUT", base, operator, `{"proxy_url":"socks5://user:secret@10.1.2.3:1080","unsafe_pacing":true}`); code != 200 {
		t.Fatalf("operator = %d %v", code, out)
	}
	_, out := e.do("GET", base, admin, "")
	if out["proxy_url"] != "socks5://10.1.2.3:1080" || out["unsafe_pacing"] != true {
		t.Fatalf("admin view = %v", out)
	}
	if l, _ := out["locked"].([]any); len(l) != 2 {
		t.Fatalf("locked = %v", out["locked"])
	}
	_, out = e.do("GET", base, tok, "")
	if out["proxy_url"] != "socks5://10.1.2.3:1080" {
		t.Fatalf("instance view = %v", out)
	}
	_, out = e.do("GET", base, operator, "")
	if out["proxy_url"] != "socks5://user:xxxxx@10.1.2.3:1080" || out["locked"] != nil {
		t.Fatalf("operator view = %v", out)
	}
}

func TestUnlockedProxyStaysAdminOnly(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{})
	id, _ := e.create("U")
	if code, out := e.do("PUT", "/v1/instances/"+id+"/settings", admin, `{"proxy_url":"socks5://u:p@h:1080"}`); code != 200 || out["proxy_url"] != "socks5://u:xxxxx@h:1080" {
		t.Fatalf("admin without locks = %d %v", code, out)
	}
}

func TestInstanceCap(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{maxInstances: 1})
	e.create("one")
	if code, out := e.do("POST", "/v1/instances", admin, `{"name":"two"}`); code != 403 || errCode(out) != "plan_limit" {
		t.Fatalf("admin over the cap = %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/instances", operator, `{"name":"two"}`); code != 201 {
		t.Fatalf("operator over the cap = %d %v", code, out)
	}
	if code, _ := e.do("PUT", "/v1/operator/limits", admin, `{"max_instances":5}`); code != 403 {
		t.Fatalf("admin changing limits = %d", code)
	}
	code, out := e.do("PUT", "/v1/operator/limits", operator, `{"max_instances":3}`)
	if code != 200 || out["max_instances"] != float64(3) || out["overridden"] != true || out["instances"] != float64(2) {
		t.Fatalf("set limits = %d %v", code, out)
	}
	if code, _ := e.do("POST", "/v1/instances", admin, `{"name":"three"}`); code != 201 {
		t.Fatalf("admin under the new cap = %d", code)
	}
	if code, _ := e.do("POST", "/v1/instances", admin, `{"name":"four"}`); code != 403 {
		t.Fatalf("admin over the new cap = %d", code)
	}
	code, out = e.do("PUT", "/v1/operator/limits", operator, `{"max_instances":null}`)
	if code != 200 || out["max_instances"] != float64(1) || out["overridden"] != false {
		t.Fatalf("reset limits = %d %v", code, out)
	}
	if code, _ := e.do("PUT", "/v1/operator/limits", operator, `{"max_instances":-1}`); code != 400 {
		t.Fatalf("negative = %d", code)
	}
}

func TestRequireProxy(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{requireProxy: true, locked: []string{"proxy_url"}})
	code, out := e.do("POST", "/v1/instances", operator, `{"name":"p"}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, out)
	}
	inst := out["instance"].(map[string]any)
	id := inst["id"].(string)
	if inst["state"] != "stopped" || inst["state_reason"] != "proxy_required" {
		t.Fatalf("instance without proxy = %v", inst)
	}
	if e.sp.Last(id) != nil {
		t.Fatal("a worker started without a proxy")
	}
	if code, out := e.do("POST", "/v1/instances/"+id+"/start", admin, ""); code != 409 || errCode(out) != "proxy_required" {
		t.Fatalf("start without proxy = %d %v", code, out)
	}
	if code, _ := e.do("PUT", "/v1/instances/"+id+"/settings", operator, `{"proxy_url":"socks5://h:1080"}`); code != 200 {
		t.Fatalf("set proxy = %d", code)
	}
	if code, out := e.do("PUT", "/v1/instances/"+id+"/settings", operator, `{"proxy_url":""}`); code != 400 {
		t.Fatalf("clearing the proxy = %d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/instances/"+id+"/start", admin, ""); code != 200 {
		t.Fatalf("start with proxy = %d %v", code, out)
	}
	e.waitState(id, "qr_pending")
}

func TestOperatorWebhooksAreInvisibleToTheAdmin(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{})
	code, out := e.do("POST", "/v1/webhooks", operator, `{"url":"https://cp.example.com/hook","events":["instance.status"]}`)
	if code != 201 {
		t.Fatalf("operator webhook = %d %v", code, out)
	}
	opID := out["id"].(string)
	code, out = e.do("POST", "/v1/webhooks", admin, `{"url":"https://customer.example.com/hook","events":["*"]}`)
	if code != 201 {
		t.Fatalf("admin webhook = %d %v", code, out)
	}
	adminID := out["id"].(string)
	_, out = e.do("GET", "/v1/webhooks", admin, "")
	if list := out["webhooks"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != adminID {
		t.Fatalf("admin list = %v", out)
	}
	for _, m := range []string{"GET", "DELETE"} {
		if code, _ := e.do(m, "/v1/webhooks/"+opID, admin, ""); code != 404 {
			t.Errorf("admin %s operator webhook = %d", m, code)
		}
	}
	if code, _ := e.do("PATCH", "/v1/webhooks/"+opID, admin, `{"enabled":false}`); code != 404 {
		t.Errorf("admin patch operator webhook = %d", code)
	}
	_, out = e.do("GET", "/v1/webhooks", operator, "")
	if list := out["webhooks"].([]any); len(list) != 2 {
		t.Fatalf("operator list = %v", out)
	}
}

func TestSendingSuspension(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{})
	id, tok := e.create("S")
	send := func() (int, map[string]any) {
		return e.do("POST", "/v1/instances/"+id+"/messages", tok, `{"to":"5511988887777","type":"text","text":"oi"}`)
	}
	if code, _ := send(); code != 202 {
		t.Fatalf("send before suspension = %d", code)
	}
	if code, _ := e.do("PUT", "/v1/operator/instances/"+id+"/sending", admin, `{"suspended":true}`); code != 403 {
		t.Fatalf("admin suspending = %d", code)
	}
	if code, out := e.do("PUT", "/v1/operator/instances/"+id+"/sending", operator, `{"suspended":true,"reason":"billing"}`); code != 200 {
		t.Fatalf("suspend = %d %v", code, out)
	}
	if code, out := send(); code != 403 || errCode(out) != "sending_suspended" {
		t.Fatalf("send while suspended = %d %v", code, out)
	}
	_, out := e.do("GET", "/v1/operator/sending", operator, "")
	if out["suspended"] != false || len(out["instances"].([]any)) != 1 {
		t.Fatalf("status = %v", out)
	}
	e.do("PUT", "/v1/operator/instances/"+id+"/sending", operator, `{"suspended":false}`)
	if code, _ := send(); code != 202 {
		t.Fatalf("send after release = %d", code)
	}
	if code, out := e.do("PUT", "/v1/operator/sending", operator, `{"suspended":true,"reason":"abuse"}`); code != 200 || out["suspended"] != true || out["reason"] != "abuse" {
		t.Fatalf("global suspend = %d %v", code, out)
	}
	if code, out := send(); code != 403 || errCode(out) != "sending_suspended" {
		t.Fatalf("send while globally suspended = %d %v", code, out)
	}
	e.do("PUT", "/v1/operator/sending", operator, `{"suspended":false}`)
	if code, _ := send(); code != 202 {
		t.Fatalf("send after global release = %d", code)
	}
	if code, _ := e.do("PUT", "/v1/operator/instances/inst_nope/sending", operator, `{"suspended":true}`); code != 404 {
		t.Fatalf("unknown instance = %d", code)
	}
	if code, _ := e.do("PUT", "/v1/operator/sending", operator, `{}`); code != 400 {
		t.Fatalf("missing suspended = %d", code)
	}
}

func TestDashboardLoginIsOperatorOnly(t *testing.T) {
	e := newCloudEnv(t, cloudCfg{})
	if code, _ := e.do("POST", "/v1/operator/dashboard-login", admin, ""); code != 403 {
		t.Fatalf("admin = %d", code)
	}
	code, out := e.do("POST", "/v1/operator/dashboard-login", operator, "")
	if code != 201 || out["code"] != "CODE123" || out["url"] != "https://p.example.com/app/#code=CODE123" || out["expires_at"] == nil {
		t.Fatalf("login = %d %v", code, out)
	}
}

func TestNoOperatorRoutesWithoutTokens(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("GET", "/v1/operator/limits", admin, ""); code != 404 {
		t.Fatalf("operator route without operator tokens = %d", code)
	}
	if _, body := e.do("GET", "/v1/me", admin, ""); body["role"] != "admin" {
		t.Fatalf("role = %v", body)
	}
}

type fakeUsage struct{ from, to, id string }

func (f *fakeUsage) Usage(_ context.Context, from, to, id string) ([]store.Usage, error) {
	f.from, f.to, f.id = from, to, id
	return []store.Usage{{InstanceID: "i1", Day: from, MessagesSent: 3}}, nil
}

func TestUsageEndpoint(t *testing.T) {
	fu := &fakeUsage{}
	e := newEnvWith(t, func(o *Options) {
		o.Operator = OperatorOptions{Tokens: []string{operator}, Usage: fu}
	})
	if code, _ := e.do("GET", "/v1/usage", admin, ""); code != 403 {
		t.Fatalf("admin usage = %d", code)
	}
	code, out := e.do("GET", "/v1/usage?from=2026-10-01&to=2026-10-31&instance_id=i1", operator, "")
	if code != 200 || fu.from != "2026-10-01" || fu.to != "2026-10-31" || fu.id != "i1" || len(out["usage"].([]any)) != 1 {
		t.Fatalf("usage = %d %v %+v", code, out, fu)
	}
	for _, q := range []string{"?from=x", "?from=2026-10-31&to=2026-10-01", "?from=2024-01-01&to=2026-01-01"} {
		if code, _ := e.do("GET", "/v1/usage"+q, operator, ""); code != 400 {
			t.Errorf("usage%s = %d", q, code)
		}
	}
	if code, out := e.do("GET", "/v1/usage", operator, ""); code != 200 || out["from"] == nil {
		t.Fatalf("default range = %d %v", code, out)
	}
}
