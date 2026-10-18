package api

import (
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
)

// A read key never sees pairing secrets on the event stream (a QR would let its holder
// link their own phone), and a revoked key's open stream is cut at the next heartbeat.
func TestReadKeyStreamHidesQRAndEndsOnRevoke(t *testing.T) {
	e := newEnvWith(t, func(o *Options) { o.SSEHeartbeat = 20 * time.Millisecond })
	id, tok := e.create("S")
	base := "/v1/instances/" + id
	_, out := e.do("POST", base+"/api-keys", tok, `{"name":"bi","scopes":["read"]}`)
	key := out["key"].(string)
	keyID := out["api_key"].(map[string]any)["id"].(string)

	for _, path := range []string{base + "/events", base + "/events?types=instance.qr,instance.pair_code,instance.status"} {
		c, code := e.sse(path, key)
		if code != 200 {
			t.Fatalf("stream %s = %d", path, code)
		}
		c.next("") // connected
		e.bus.Publish(id, events.TypeInstanceQR, events.InstanceQR{Code: "SECRET-QR"})
		e.bus.Publish(id, events.TypeInstancePairCode, events.PairCode{Code: "SECRET-CODE"})
		e.bus.Publish(id, events.TypeInstanceStatus, events.InstanceStatus{State: "qr_pending"})
		deadline := time.After(3 * time.Second)
	loop:
		for {
			select {
			case ev, ok := <-c.evs:
				if !ok {
					t.Fatal("stream ended")
				}
				if ev.typ == events.TypeInstanceQR || ev.typ == events.TypeInstancePairCode {
					t.Fatalf("read key got %s: %s", ev.typ, ev.data)
				}
				if ev.typ == events.TypeInstanceStatus {
					break loop
				}
			case <-deadline:
				t.Fatal("no instance.status")
			}
		}
		c.close()
	}
	// The instance token still gets the QR.
	ci, _ := e.sse(base+"/events", tok)
	ci.next("")
	e.bus.Publish(id, events.TypeInstanceQR, events.InstanceQR{Code: "QR"})
	ci.next(events.TypeInstanceQR)

	c, _ := e.sse(base+"/events", key)
	c.next("")
	if code, _ := e.do("DELETE", base+"/api-keys/"+keyID, tok, ""); code != 204 {
		t.Fatalf("revoke = %d", code)
	}
	ev := c.next("error")
	if ev.data == "" {
		t.Fatal("no error event")
	}
}

// An API key must also satisfy the instance's allowlist.
func TestKeyHonorsInstanceAllowlist(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("K")
	base := "/v1/instances/" + id
	_, out := e.do("POST", base+"/api-keys", tok, `{"name":"bi","scopes":["read"]}`)
	key := out["key"].(string)
	if code, _ := e.do("GET", base, key, ""); code != 200 {
		t.Fatalf("before = %d", code)
	}
	e.do("PATCH", base, admin, `{"ip_allowlist":["10.0.0.0/8"]}`)
	if code, _ := e.do("GET", base, key, ""); code != 403 {
		t.Fatalf("key outside the instance allowlist = %d", code)
	}
}

// Rotating the instance token revokes the keys minted with it, not the admin's.
func TestRotateRevokesInstanceMintedKeys(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("R")
	base := "/v1/instances/" + id
	_, out := e.do("POST", base+"/api-keys", tok, `{"name":"by-instance","scopes":["read"]}`)
	byInstance := out["key"].(string)
	_, out = e.do("POST", base+"/api-keys", admin, `{"name":"by-admin","scopes":["read"]}`)
	byAdmin := out["key"].(string)
	if code, _ := e.do("POST", base+"/token/rotate", admin, ""); code != 200 {
		t.Fatalf("rotate = %d", code)
	}
	if code, _ := e.do("GET", base, byInstance, ""); code != 401 {
		t.Fatalf("instance-minted key after rotate = %d", code)
	}
	if code, _ := e.do("GET", base, byAdmin, ""); code != 200 {
		t.Fatalf("admin-minted key after rotate = %d", code)
	}
}
