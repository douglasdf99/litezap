package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/pairing"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/realtime"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/usage"
	"github.com/douglasdf99/litezap/internal/webhook"
	"github.com/douglasdf99/litezap/internal/workertest"
)

const admin = "lza_admin"

type env struct {
	t   *testing.T
	srv *httptest.Server
	sp  *workertest.Spawner
	bus *events.Bus
	st  *store.Store
	rt  *realtime.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, nil)
}

// newEnvWith lets a test adjust the API options (e.g. ProtectOps, Ready).
func newEnvWith(t *testing.T, tweak func(*Options)) *env {
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
	q := queue.New(st, mgr, queue.Options{Defaults: defaults, Log: log, Resolver: resolver, Events: bus})
	msg := &service.Messaging{Store: st, Manager: mgr, Queues: q, Fetcher: &media.Fetcher{AllowPrivate: true},
		Signer:  media.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://example.invalid"),
		DataDir: dir, Defaults: defaults, Resolver: resolver, SignedURLs: true}
	rt := &realtime.Handler{Workers: mgr, Events: bus, Log: log,
		Settings: func(ctx context.Context, id string) (settings.Settings, error) { return msg.Settings(ctx, id) }}
	rt.SetSender(msg)
	msg.OnSettingsChanged = rt.SettingsChanged
	groups := &service.Groups{Store: st, Workers: mgr, Resolver: resolver, Defaults: defaults}            // marco 8
	acct := &service.Account{Messaging: msg, Events: bus}                                                 // marco 9
	social := &service.Social{Store: st, Workers: mgr, Messaging: msg, Fetcher: msg.Fetcher, Events: bus} // marco 10
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log, Events: bus}, q, events.Observer{Bus: bus}, rt,
		&service.GroupEvents{Store: st, Workers: mgr, Events: bus}, acct, &service.SocialEvents{Store: st, Events: bus}})
	if err := disp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Shutdown(ctx)
		mgr.Shutdown(ctx)
		rt.Close()
		disp.Close(ctx)
	})
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: dir, Queues: q, Events: bus}
	opt := Options{AdminToken: admin, Ready: st.Ping, Events: bus,
		Webhooks: &service.Webhooks{Store: st, Dispatcher: disp}, AgentHook: &agenthook.Admin{Store: st}, Groups: groups, Account: acct, Social: social}
	// Marco 11: platform service and pairing links.
	links := &pairing.Links{Store: st, Key: []byte("pairing-key-0123456789abcdef0123"), BaseURL: "https://zap.example.com"}
	opt.Platform = &service.Platform{Store: st, Instances: svc}
	opt.Pairing = links
	opt.PairingPage = pairing.Handler(pairing.PageOptions{Links: links, Instances: svc})
	opt.Operator.Usage = &usage.Recorder{Store: st}
	if tweak != nil {
		tweak(&opt)
	}
	h := New(svc, msg, opt, log)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { bus.CloseSubscribers(); srv.Close() })
	return &env{t: t, srv: srv, sp: sp, bus: bus, st: st, rt: rt}
}

func (e *env) do(method, path, token, body string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (e *env) create(name string) (id, token string) {
	e.t.Helper()
	code, body := e.do("POST", "/v1/instances", admin, `{"name":"`+name+`"}`)
	if code != http.StatusCreated {
		e.t.Fatalf("create = %d %v", code, body)
	}
	inst := body["instance"].(map[string]any)
	return inst["id"].(string), body["token"].(string)
}

func (e *env) waitState(id, want string) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body := e.do("GET", "/v1/instances/"+id, admin, "")
		if body["state"] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("instance %s never reached %s", id, want)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("GET", "/healthz", "", ""); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if code, _ := e.do("GET", "/readyz", "", ""); code != 200 {
		t.Fatalf("readyz = %d", code)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	if code, body := e.do("GET", "/v1/instances", "", ""); code != 401 || errCode(body) != "unauthorized" {
		t.Fatalf("no token = %d %v", code, body)
	}
	if code, _ := e.do("GET", "/v1/instances", "lzi_wrong", ""); code != 401 {
		t.Fatalf("bad token = %d", code)
	}
	id1, tok1 := e.create("A")
	id2, _ := e.create("B")
	if code, _ := e.do("GET", "/v1/instances/"+id1, tok1, ""); code != 200 {
		t.Fatalf("own instance = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances/"+id2, tok1, ""); code != 403 {
		t.Fatalf("other instance = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances", tok1, ""); code != 403 {
		t.Fatalf("list with instance token = %d", code)
	}
	if code, _ := e.do("DELETE", "/v1/instances/"+id1, tok1, ""); code != 403 {
		t.Fatalf("delete with instance token = %d", code)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	code, body := e.do("POST", "/v1/instances", admin, `{"name":"  "}`)
	if code != 400 || errCode(body) != "validation_failed" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestPairingFlow(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Loja")
	e.waitState(id, "qr_pending")

	code, body := e.do("GET", "/v1/instances/"+id+"/qr", tok, "")
	if code != 200 || body["code"] != "QR-API" {
		t.Fatalf("qr = %d %v", code, body)
	}

	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/instances/"+id+"/qr?format=png", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("png = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	if code, body := e.do("POST", "/v1/instances/"+id+"/pair-phone", tok, `{"phone":"123"}`); code != 400 {
		t.Fatalf("bad phone = %d %v", code, body)
	}
	code, body = e.do("POST", "/v1/instances/"+id+"/pair-phone", tok, `{"phone":"+55 11 99999-9999"}`)
	if code != 200 || body["code"] != "PAIR-CODE" {
		t.Fatalf("pair = %d %v", code, body)
	}

	e.sp.Last(id).Emit("connected")
	e.waitState(id, "connected")
	_, body = e.do("GET", "/v1/instances/"+id, tok, "")
	if body["phone"] != "5511999999999" {
		t.Fatalf("phone = %v", body["phone"])
	}
	if code, body := e.do("GET", "/v1/instances/"+id+"/qr", tok, ""); code != 409 || errCode(body) != "instance_not_pairing" {
		t.Fatalf("qr after connect = %d %v", code, body)
	}
}

func TestStopStartRestartLogout(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Ops")
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/stop", tok, ""); code != 200 {
		t.Fatalf("stop = %d", code)
	}
	e.waitState(id, "stopped")
	if code, body := e.do("POST", "/v1/instances/"+id+"/logout", tok, ""); code != 409 || errCode(body) != "instance_not_connected" {
		t.Fatalf("logout while stopped = %d %v", code, body)
	}
	if code, _ := e.do("POST", "/v1/instances/"+id+"/start", tok, ""); code != 200 {
		t.Fatalf("start = %d", code)
	}
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/restart", tok, ""); code != 200 {
		t.Fatalf("restart = %d", code)
	}
	e.waitState(id, "qr_pending")
	if code, _ := e.do("POST", "/v1/instances/"+id+"/logout", tok, ""); code != 200 {
		t.Fatalf("logout = %d", code)
	}
	e.waitState(id, "logged_out")
}

func TestRotateAndDelete(t *testing.T) {
	e := newEnv(t)
	id, oldTok := e.create("Rot")
	code, body := e.do("POST", "/v1/instances/"+id+"/token/rotate", admin, "")
	if code != 200 {
		t.Fatalf("rotate = %d", code)
	}
	newTok := body["token"].(string)
	if code, _ := e.do("GET", "/v1/instances/"+id, oldTok, ""); code != 401 {
		t.Fatalf("old token = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances/"+id, newTok, ""); code != 200 {
		t.Fatalf("new token = %d", code)
	}
	if code, _ := e.do("DELETE", "/v1/instances/"+id, admin, ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, body := e.do("GET", "/v1/instances/"+id, admin, ""); code != 404 || errCode(body) != "instance_not_found" {
		t.Fatalf("after delete = %d %v", code, body)
	}
}

func TestListInstances(t *testing.T) {
	e := newEnv(t)
	e.create("A")
	e.create("B")
	code, body := e.do("GET", "/v1/instances", admin, "")
	list, _ := body["instances"].([]any)
	if code != 200 || len(list) != 2 {
		t.Fatalf("list = %d %v", code, body)
	}
}

// A Start racing a Delete must not leave a worker behind for the deleted instance.
func TestDeleteRacingStartLeavesNoWorker(t *testing.T) {
	for i := 0; i < 5; i++ {
		e := newEnv(t)
		id, _ := e.create("Race")
		e.waitState(id, "qr_pending")

		done := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					req, _ := http.NewRequest("POST", e.srv.URL+"/v1/instances/"+id+"/start", nil)
					req.Header.Set("Authorization", "Bearer "+admin)
					if resp, err := http.DefaultClient.Do(req); err == nil {
						resp.Body.Close()
					}
					select {
					case <-done:
						return
					default:
					}
				}
			}()
		}
		time.Sleep(5 * time.Millisecond)
		code, _ := e.do("DELETE", "/v1/instances/"+id, admin, "")
		time.Sleep(20 * time.Millisecond) // let late Starts land after the delete
		close(done)
		wg.Wait()
		if code != 204 {
			t.Fatalf("delete = %d", code)
		}
		if code, _ := e.do("GET", "/v1/instances/"+id, admin, ""); code != 404 {
			t.Fatalf("after delete = %d", code)
		}
		time.Sleep(20 * time.Millisecond)
		if n := e.sp.Live(id); n != 0 {
			t.Fatalf("iteration %d: %d live worker(s) after delete", i, n)
		}
	}
}

func TestProtectOps(t *testing.T) {
	e := newEnvWith(t, func(o *Options) { o.ProtectOps = true })
	_, tok := e.create("Ops")
	for _, path := range []string{"/healthz", "/readyz"} {
		if code, body := e.do("GET", path, "", ""); code != 401 || errCode(body) != "unauthorized" {
			t.Fatalf("%s without token = %d %v", path, code, body)
		}
		if code, body := e.do("GET", path, tok, ""); code != 403 || errCode(body) != "forbidden" {
			t.Fatalf("%s with instance token = %d %v", path, code, body)
		}
		if code, _ := e.do("GET", path, admin, ""); code != 200 {
			t.Fatalf("%s with admin token = %d", path, code)
		}
	}
}

func TestReadyzUnavailable(t *testing.T) {
	e := newEnvWith(t, func(o *Options) {
		o.Ready = func(context.Context) error { return errors.New("db down") }
	})
	if code, body := e.do("GET", "/readyz", "", ""); code != 503 || body["status"] != "unavailable" {
		t.Fatalf("readyz = %d %v", code, body)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	for _, scheme := range []string{"bearer ", "BEARER ", "Bearer "} {
		req, _ := http.NewRequest("GET", e.srv.URL+"/v1/instances", nil)
		req.Header.Set("Authorization", scheme+admin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("scheme %q = %d", scheme, resp.StatusCode)
		}
	}
	for _, auth := range []string{"Bear", "Basic " + admin, "Bearer"} {
		req, _ := http.NewRequest("GET", e.srv.URL+"/v1/instances", nil)
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("auth %q = %d", auth, resp.StatusCode)
		}
	}
}

func TestSecretsAreNotCached(t *testing.T) {
	e := newEnv(t)
	check := func(method, path, body string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, e.srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+admin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s = %d Cache-Control=%q", method, path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
	check("POST", "/v1/instances", `{"name":"C"}`)
	id, _ := e.create("C2")
	e.waitState(id, "qr_pending")
	check("POST", "/v1/instances/"+id+"/token/rotate", "")
	check("GET", "/v1/instances/"+id+"/qr", "")
}
