package api

import (
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/service"
)

// v0.1.1 repair: clearing a chat and removing a contact are off unless
// experimental_app_state_writes; saving a contact (whatsmeow's encoder) stays on.
func TestAppStateWritesGate(t *testing.T) {
	e := newEnvWith(t, func(o *Options) { o.Account.(*service.Account).ExperimentalAppStateWrites = false })
	id, tok := e.connected("Gate")
	f := e.sp.Last(id)
	e.inbound(id, "W1", contact)

	code, v := e.do("POST", "/v1/instances/"+id+"/chats/"+contact+"/clear", tok, "")
	if code != 403 || errCode(v) != "feature_disabled" || !strings.Contains(v["error"].(map[string]any)["message"].(string), "experimental_app_state_writes") {
		t.Fatalf("clear = %d %v", code, v)
	}
	code, v = e.do("POST", "/v1/instances/"+id+"/contacts", tok, `{"phone":"5511999990000@s.whatsapp.net","first_name":"Ana"}`)
	if code != 200 {
		t.Fatalf("save = %d %v", code, v)
	}
	code, v = e.do("DELETE", "/v1/instances/"+id+"/contacts/5511999990000@s.whatsapp.net", tok, "")
	if code != 403 || errCode(v) != "feature_disabled" {
		t.Fatalf("remove = %d %v", code, v)
	}
	for _, a := range f.WA.ChatActions() {
		if a.Action == "clear" {
			t.Fatal("clear reached the worker")
		}
	}
	if acts := f.WA.ContactActions(); len(acts) != 1 || acts[0].Action != "save" {
		t.Fatalf("contact actions = %+v", acts)
	}
}

// POST /app-state/resync is admin only and reports each collection.
func TestAppStateResync(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Resync")
	f := e.sp.Last(id)
	path := "/v1/instances/" + id + "/app-state/resync"

	if code, v := e.do("POST", path, tok, ""); code != 403 {
		t.Fatalf("instance token = %d %v", code, v)
	}
	code, v := e.do("POST", path, admin, "")
	cols, _ := v["collections"].([]any)
	if code != 200 || v["ok"] != true || len(cols) != 5 {
		t.Fatalf("resync all = %d %v", code, v)
	}
	first := cols[0].(map[string]any)
	if first["name"] != "critical_block" || first["ok"] != true || first["version_before"] != float64(7) || first["version_after"] != float64(9) {
		t.Fatalf("first = %v", first)
	}
	code, v = e.do("POST", path, admin, `{"collections":["critical_unblock_low","regular_high"]}`)
	if code != 200 || len(v["collections"].([]any)) != 2 {
		t.Fatalf("resync two = %d %v", code, v)
	}
	if got := strings.Join(f.WA.AppStateResyncs(), ","); !strings.HasSuffix(got, ",critical_unblock_low,regular_high") {
		t.Fatalf("resyncs = %s", got)
	}
	if code, v := e.do("POST", path, admin, `{"collections":["contacts"]}`); code != 400 && code != 422 {
		t.Fatalf("unknown collection = %d %v", code, v)
	}
	if code, v := e.do("POST", path, admin, `{"colections":[]}`); code != 400 {
		t.Fatalf("typo = %d %v", code, v)
	}
}
