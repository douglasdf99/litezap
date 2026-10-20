package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/service"
)

// TestCatalogIsTheMCPToolTable checks that the in-process table (used by the agents) has
// exactly the tools MCP serves, with the same schemas, and that a call through it behaves
// like the MCP call: schema validation, defaults and hinted errors.
func TestCatalogIsTheMCPToolTable(t *testing.T) {
	e := newEnv(t, nil)
	id, token := e.connected("cat")
	cs := e.client(modeLocal, token)
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &Local{Instances: e.svc, Messaging: e.msg, Events: e.bus, Groups: e.grp, Caller: service.Scope{InstanceID: id},
		Account: e.acct, Social: e.soc, Attendant: e.att}
	defs := Catalog(b, nil)
	if len(defs) != len(listed.Tools) {
		t.Fatalf("catalog has %d tools, MCP lists %d", len(defs), len(listed.Tools))
	}
	byName := map[string]ToolDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	for _, tool := range listed.Tools {
		d, ok := byName[tool.Name]
		if !ok {
			t.Fatalf("MCP tool %s is not in the catalog", tool.Name)
		}
		s, err := d.InputSchema()
		if err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		var a, b any
		ja, _ := json.Marshal(s)
		jb, _ := json.Marshal(tool.InputSchema)
		_ = json.Unmarshal(ja, &a)
		_ = json.Unmarshal(jb, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s schema differs:\ncatalog %s\nmcp     %s", tool.Name, ja, jb)
		}
		if d.ReadOnly != (tool.Annotations != nil && tool.Annotations.ReadOnlyHint) {
			t.Fatalf("%s read-only flag differs", tool.Name)
		}
	}
	ctx := context.Background()
	// Validation: a required field is missing.
	if _, err := byName["get_messages"].Call(ctx, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "validating") {
		t.Fatalf("missing chat = %v", err)
	}
	// Defaults and a real call.
	out, err := byName["list_chats"].Call(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var page service.ChatPage
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatalf("list_chats output %s: %v", out, err)
	}
	// Scope: an instance token never reaches another instance, with the MCP hint.
	if _, err := byName["list_chats"].Call(ctx, json.RawMessage(`{"instance_id":"inst_other"}`)); err == nil ||
		!strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("other instance = %v", err)
	}
	// A send through the table goes through the queue like MCP's.
	out, err = byName["send_message"].Call(ctx, json.RawMessage(`{"to":"5511988887777","text":"oi","wait":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var sent sendOutput
	if err := json.Unmarshal(out, &sent); err != nil || sent.Message.ID == "" {
		t.Fatalf("send_message output %s: %v", out, err)
	}
}
