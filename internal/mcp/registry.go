package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolDef is one entry of the tool table: what MCP serves, callable in process (the
// LiteZap agents run their tool-use loop over it, without the MCP transport). The input is
// validated against the same JSON schema, with the same defaults, and the handler is the
// MCP one, so a call here behaves exactly like the MCP tool call.
type ToolDef struct {
	Name        string
	Title       string
	Description string
	// ReadOnly and Destructive mirror the MCP annotations.
	ReadOnly    bool
	Destructive bool

	schema func() (*jsonschema.Schema, *jsonschema.Resolved, error)
	call   func(ctx context.Context, in json.RawMessage) (json.RawMessage, error)
}

// InputSchema is the tool's input JSON schema (the one MCP advertises).
func (d ToolDef) InputSchema() (*jsonschema.Schema, error) {
	s, _, err := d.schema()
	return s, err
}

// Call runs the tool with JSON arguments and returns its structured output. A validation
// failure or a backend error comes back as an error whose text is what an MCP client would
// see in the tool error (with the same hints).
func (d ToolDef) Call(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return d.call(ctx, args)
}

// Catalog returns the tool table over b without creating an MCP server: the same tools,
// schemas and handlers that NewServer serves.
func Catalog(b Backend, log *slog.Logger) []ToolDef {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	t := &tools{b: b, log: log}
	t.register(nil)
	return t.defs
}

func newToolDef[In, Out any](t *tools, tool *sdk.Tool, h func(context.Context, In) (*sdk.CallToolResult, Out, error)) ToolDef {
	d := ToolDef{Name: tool.Name, Title: tool.Title, Description: tool.Description}
	if a := tool.Annotations; a != nil {
		d.ReadOnly = a.ReadOnlyHint
		d.Destructive = a.DestructiveHint != nil && *a.DestructiveHint
	}
	given, _ := tool.InputSchema.(*jsonschema.Schema)
	// Resolved on first use: an MCP server built per token never pays for it.
	var once sync.Once
	var sch *jsonschema.Schema
	var res *jsonschema.Resolved
	var serr error
	d.schema = func() (*jsonschema.Schema, *jsonschema.Resolved, error) {
		once.Do(func() {
			sch = given
			if sch == nil { // derived from In, as the SDK does
				if sch, serr = jsonschema.For[In](nil); serr != nil {
					return
				}
			}
			res, serr = sch.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		})
		return sch, res, serr
	}
	log, name := t.log, tool.Name
	d.call = func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		_, resolved, err := d.schema()
		if err != nil {
			return nil, fmt.Errorf("input schema: %w", err)
		}
		args, err := applyInput(raw, resolved)
		if err != nil {
			return nil, fmt.Errorf("validating \"arguments\": %v", err)
		}
		var in In
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		_, out, err := h(ctx, in)
		if err != nil {
			return nil, toolError(log, name, err)
		}
		return json.Marshal(out)
	}
	return d
}

// applyInput validates tool arguments and fills their defaults (the SDK's input handling).
func applyInput(raw json.RawMessage, resolved *jsonschema.Resolved) (json.RawMessage, error) {
	v := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("unmarshaling arguments: %w", err)
		}
	}
	if v == nil {
		return nil, errors.New("arguments must be an object")
	}
	var val any = v
	if err := resolved.ApplyDefaults(&val); err != nil {
		return nil, fmt.Errorf("applying schema defaults:\n%w", err)
	}
	if err := resolved.Validate(&val); err != nil {
		return nil, err
	}
	return json.Marshal(val)
}
