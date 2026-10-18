package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/webhook"
)

func TestWebhookCRUDAndScopes(t *testing.T) {
	e := newEnv(t)
	a, tokA := e.create("A")
	b, _ := e.create("B")

	if code, body := e.do("POST", "/v1/webhooks", admin, `{"url":"ftp://x","events":["*"]}`); code != 400 {
		t.Fatalf("bad url = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/webhooks", admin, `{"url":"https://x.test/h","events":["message.nope"]}`); code != 400 {
		t.Fatalf("bad filter = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/webhooks", admin, `{"url":"https://x.test/h","events":["*"],"version":2}`); code != 400 {
		t.Fatalf("bad version = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/webhooks", admin, `{"url":"https://x.test/h","events":["*"],"instance_id":"inst_nope"}`); code != 404 || errCode(body) != "instance_not_found" {
		t.Fatalf("unknown instance = %d %v", code, body)
	}

	code, global := e.do("POST", "/v1/webhooks", admin, `{"url":"https://x.test/global","events":["*"]}`)
	if code != 201 || !strings.HasPrefix(global["secret"].(string), "whsec_") || global["instance_id"] != nil || global["version"].(float64) != 1 {
		t.Fatalf("global = %d %v", code, global)
	}
	gid := global["id"].(string)
	code, own := e.do("POST", "/v1/webhooks", tokA, `{"url":"https://x.test/a","events":["message.*"],"secret":"0123456789abcdef0123"}`)
	if code != 201 || own["instance_id"] != a || own["secret"] != "0123456789abcdef0123" {
		t.Fatalf("instance webhook = %d %v", code, own)
	}
	aid := own["id"].(string)
	if code, body := e.do("POST", "/v1/webhooks", tokA, `{"url":"https://x.test/b","events":["*"],"instance_id":"`+b+`"}`); code != 403 {
		t.Fatalf("instance token registering for another instance = %d %v", code, body)
	}

	// The instance token sees only its own webhooks; others look missing.
	if code, body := e.do("GET", "/v1/webhooks/"+gid, tokA, ""); code != 404 || errCode(body) != "not_found" {
		t.Fatalf("instance reading global = %d %v", code, body)
	}
	if code, _ := e.do("DELETE", "/v1/webhooks/"+gid, tokA, ""); code != 404 {
		t.Fatalf("instance deleting global = %d", code)
	}
	_, list := e.do("GET", "/v1/webhooks", tokA, "")
	if hooks := list["webhooks"].([]any); len(hooks) != 1 || hooks[0].(map[string]any)["id"] != aid {
		t.Fatalf("instance list = %v", list)
	}
	_, list = e.do("GET", "/v1/webhooks", admin, "")
	if hooks := list["webhooks"].([]any); len(hooks) != 2 {
		t.Fatalf("admin list = %v", list)
	}
	_, list = e.do("GET", "/v1/webhooks?instance_id="+a, admin, "")
	if hooks := list["webhooks"].([]any); len(hooks) != 1 {
		t.Fatalf("admin list filtered = %v", list)
	}

	// Secrets are never read back.
	code, got := e.do("GET", "/v1/webhooks/"+aid, tokA, "")
	if code != 200 || got["secret"] != nil || got["url"] != "https://x.test/a" {
		t.Fatalf("get = %d %v", code, got)
	}
	code, got = e.do("PATCH", "/v1/webhooks/"+aid, tokA, `{"enabled":false,"events":["instance.*","message.status"]}`)
	if code != 200 || got["enabled"] != false || got["secret"] != nil || len(got["events"].([]any)) != 2 {
		t.Fatalf("patch = %d %v", code, got)
	}
	code, got = e.do("PATCH", "/v1/webhooks/"+aid, tokA, `{"secret":""}`)
	if code != 200 || !strings.HasPrefix(got["secret"].(string), "whsec_") {
		t.Fatalf("rotate = %d %v", code, got)
	}
	if code, body := e.do("PATCH", "/v1/webhooks/"+aid, tokA, `{"instance_id":"`+b+`"}`); code != 400 {
		t.Fatalf("moving a webhook = %d %v", code, body)
	}
	if code, _ := e.do("DELETE", "/v1/webhooks/"+aid, tokA, ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := e.do("GET", "/v1/webhooks/"+aid, admin, ""); code != 404 {
		t.Fatalf("deleted webhook = %d", code)
	}
}

type hookReceiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hdrs   []http.Header
	bodies [][]byte
}

func newHookReceiver(t *testing.T) *hookReceiver {
	r := &hookReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hdrs = append(r.hdrs, req.Header.Clone())
		r.bodies = append(r.bodies, b)
		r.mu.Unlock()
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *hookReceiver) find(typ string) (http.Header, []byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, h := range r.hdrs {
		if h.Get(webhook.HeaderEventType) == typ {
			return h, r.bodies[i], true
		}
	}
	return nil, nil, false
}

func TestWebhookReceivesMessageAndDeadLetterRetry(t *testing.T) {
	e := newEnv(t)
	rcv := newHookReceiver(t)
	id, tok := e.create("Loja")
	e.waitState(id, "qr_pending")
	code, wh := e.do("POST", "/v1/webhooks", tok, `{"url":"`+rcv.srv.URL+`","events":["message.received"]}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, wh)
	}
	wid, secret := wh["id"].(string), wh["secret"].(string)
	e.sp.Last(id).Emit("connected")
	e.waitState(id, "connected")
	e.sp.Last(id).EmitMessage(&workerv1.Message{Id: "IN1", Chat: "5511988887777@s.whatsapp.net",
		Sender: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "text", Text: "oi"})

	var hdr http.Header
	var body []byte
	deadline := time.Now().Add(3 * time.Second)
	for ok := false; !ok; hdr, body, ok = rcv.find("message.received") {
		if time.Now().After(deadline) {
			t.Fatal("webhook not delivered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := webhook.Verify(secret, hdr.Get(webhook.HeaderSignature), body, time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var env struct {
		ID, Type   string
		InstanceID string `json:"instance_id"`
		Version    int
		Data       struct{ Text, Source, Chat string }
	}
	_ = json.Unmarshal(body, &env)
	if env.Data.Text != "oi" || env.Data.Source != "contact" || env.Version != 1 || hdr.Get(webhook.HeaderEventID) == "" {
		t.Fatalf("envelope = %s", body)
	}
	if _, _, ok := rcv.find("instance.status"); ok {
		t.Fatal("filtered event delivered")
	}
	eventuallyAPI(t, "delivered listed", func() bool {
		_, page := e.do("GET", "/v1/webhooks/"+wid+"/deliveries?status=delivered", tok, "")
		ds, _ := page["deliveries"].([]any)
		return len(ds) == 1 && ds[0].(map[string]any)["event"].(map[string]any)["type"] == "message.received"
	})

	// Dead-letter: list and retry.
	ctx := context.Background()
	if _, err := e.st.InsertDeliveries(ctx, []store.Delivery{{ID: "dlv_dead", WebhookID: wid, EventID: "evt_dead",
		EventType: "message.received", Payload: []byte(`{"id":"evt_dead"}`), NextAttemptAt: time.Now().Add(time.Hour),
		ExpiresAt: time.Now().Add(time.Hour)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishDelivery(ctx, "dlv_dead", store.DeliveryFailed, 9, time.Now(), 500, "http 500", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, page := e.do("GET", "/v1/webhooks/"+wid+"/deliveries?status=failed", tok, "")
	if ds, _ := page["deliveries"].([]any); len(ds) != 1 || ds[0].(map[string]any)["last_error"] != "http 500" {
		t.Fatalf("dead-letter = %v", page)
	}
	if code, body := e.do("GET", "/v1/webhooks/"+wid+"/deliveries?status=nope", tok, ""); code != 400 {
		t.Fatalf("bad status = %d %v", code, body)
	}
	code, d := e.do("POST", "/v1/webhooks/"+wid+"/deliveries/dlv_dead/retry", tok, "")
	if code != 200 || (d["status"] != "pending" && d["status"] != "delivered") {
		t.Fatalf("retry = %d %v", code, d)
	}
	eventuallyAPI(t, "retried delivery delivered", func() bool {
		_, d := e.do("GET", "/v1/webhooks/"+wid+"/deliveries?status=delivered", tok, "")
		ds, _ := d["deliveries"].([]any)
		return len(ds) == 2
	})
	if code, body := e.do("POST", "/v1/webhooks/"+wid+"/deliveries/dlv_dead/retry", tok, ""); code != 409 {
		t.Fatalf("retry of a delivered delivery = %d %v", code, body)
	}
	if code, _ := e.do("POST", "/v1/webhooks/"+wid+"/deliveries/dlv_nope/retry", tok, ""); code != 404 {
		t.Fatalf("retry of unknown delivery = %d", code)
	}
}

func eventuallyAPI(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type sseEvent struct {
	id, typ, data string
	comment       string
}

type sseClient struct {
	t      *testing.T
	resp   *http.Response
	cancel context.CancelFunc
	evs    chan sseEvent
}

func (e *env) sse(path, token string) (*sseClient, int) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		e.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, resp.StatusCode
	}
	c := &sseClient{t: e.t, resp: resp, cancel: cancel, evs: make(chan sseEvent, 64)}
	go func() {
		defer close(c.evs)
		br := bufio.NewReader(resp.Body)
		var cur sseEvent
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				if cur != (sseEvent{}) {
					c.evs <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, ":"):
				cur.comment = strings.TrimSpace(line[1:])
			case strings.HasPrefix(line, "id: "):
				cur.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.typ = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[6:]
			}
		}
	}()
	e.t.Cleanup(c.close)
	return c, 200
}

func (c *sseClient) close() { c.cancel(); c.resp.Body.Close() }

// next returns the next event of type typ (skipping others and comments).
func (c *sseClient) next(typ string) sseEvent {
	c.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-c.evs:
			if !ok {
				c.t.Fatalf("stream ended waiting for %s", typ)
			}
			if ev.typ == typ || (typ == "" && ev.comment != "" && ev.typ == "") {
				return ev
			}
		case <-timeout:
			c.t.Fatalf("no %q event", typ)
		}
	}
}

func TestSSEStreams(t *testing.T) {
	e := newEnvWith(t, func(o *Options) { o.SSEHeartbeat = 20 * time.Millisecond })
	_, tokOther := e.create("Outra")
	all, _ := e.sse("/v1/events", admin)
	if _, code := e.sse("/v1/events", tokOther); code != 403 {
		t.Fatalf("instance token on /v1/events = %d", code)
	}
	if code, _ := e.do("GET", "/v1/events?types=nope", admin, ""); code != 400 {
		t.Fatalf("bad filter = %d", code)
	}

	id, tok := e.create("Loja")
	if _, code := e.sse("/v1/instances/"+id+"/events", tokOther); code != 403 {
		t.Fatalf("other instance's stream = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances/inst_nope/events", admin, ""); code != 404 {
		t.Fatalf("unknown instance stream = %d", code)
	}
	var env struct {
		ID         string `json:"id"`
		InstanceID string `json:"instance_id"`
		Data       struct {
			Code  string `json:"code"`
			State string `json:"state"`
		} `json:"data"`
	}
	for env.InstanceID != id { // the other instance's QR may come first
		qr := all.next("instance.qr")
		if err := json.Unmarshal([]byte(qr.data), &env); err != nil || env.Data.Code != "QR-API" || qr.id != env.ID {
			t.Fatalf("qr event = %+v (%v)", qr, err)
		}
	}
	e.waitState(id, "qr_pending")

	inst, _ := e.sse("/v1/instances/"+id+"/events?types=instance.pair_code,message.*", tok)
	if hb := inst.next(""); hb.comment != "ping" && hb.comment != "connected" {
		t.Fatalf("heartbeat = %+v", hb)
	}
	if code, body := e.do("POST", "/v1/instances/"+id+"/pair-phone", tok, `{"phone":"+55 11 99999-9999"}`); code != 200 {
		t.Fatalf("pair = %d %v", code, body)
	}
	pc := inst.next("instance.pair_code")
	if !strings.Contains(pc.data, `"code":"PAIR-CODE"`) {
		t.Fatalf("pair code event = %+v", pc)
	}
	e.sp.Last(id).Emit("connected")
	st := all.next("instance.status")
	for !strings.Contains(st.data, `"state":"connected"`) {
		st = all.next("instance.status")
	}
	e.sp.Last(id).EmitMessage(&workerv1.Message{Id: "IN9", Chat: "5511988887777@s.whatsapp.net",
		Sender: "5511988887777@s.whatsapp.net", TimestampMs: 1, Type: "text", Text: "olá"})
	m := inst.next("message.received")
	if !strings.Contains(m.data, `"text":"olá"`) {
		t.Fatalf("message event = %+v", m)
	}
	// The instance stream filtered instance.status out.
	select {
	case ev := <-inst.evs:
		if ev.typ == "instance.status" {
			t.Fatalf("filtered type delivered: %+v", ev)
		}
	default:
	}

	// Server shutdown ends the streams.
	e.bus.CloseSubscribers()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-all.evs:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream not closed by CloseSubscribers")
		}
	}
}
