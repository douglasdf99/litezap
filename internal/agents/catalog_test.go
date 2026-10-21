package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// Agents use only the instance's own catalog: the owner arguments are not offered and
// are dropped if the model sends them anyway.
func TestCatalogToolsOwnCatalogOnly(t *testing.T) {
	schema := func(props ...string) func() (*jsonschema.Schema, error) {
		return func() (*jsonschema.Schema, error) {
			s := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
			for _, p := range props {
				s.Properties[p] = &jsonschema.Schema{Type: "string"}
			}
			return s, nil
		}
	}
	var got map[string]any
	call := func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		_ = json.Unmarshal(args, &got)
		return json.RawMessage(`{}`), nil
	}
	defs := []ToolDef{
		{Name: "get_catalog", Description: "d", Schema: schema("instance_id", "jid", "limit"), Call: call},
		{Name: "get_product", Description: "d", Schema: schema("jid", "product_id"), Call: call},
		{Name: "send_product", Description: "d", Schema: schema("to", "product_id", "catalog_owner", "body"), Call: call},
	}
	specs, _, err := toolSpecs(defs, map[string]bool{"get_catalog": true, "get_product": true, "send_product": true}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		if strings.Contains(string(s.Schema), `"jid"`) || strings.Contains(string(s.Schema), `"catalog_owner"`) {
			t.Fatalf("%s offers the catalog owner: %s", s.Name, s.Schema)
		}
	}
	args := map[string]any{"to": "x", "product_id": "1", "catalog_owner": "5511", "jid": "5511"}
	dropHidden("send_product", args)
	if _, ok := args["catalog_owner"]; ok {
		t.Fatal("catalog_owner must be dropped")
	}
	dropHidden("get_catalog", args)
	if _, ok := args["jid"]; ok {
		t.Fatal("jid must be dropped")
	}
	_ = got
}
