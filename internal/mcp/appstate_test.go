package mcp

import (
	"testing"

	"github.com/douglasdf99/litezap/internal/service"
)

// remove_contact and clear_chat are hidden while experimental_app_state_writes is off;
// save_contact stays (whatsmeow's own encoder).
func TestAppStateWriteToolsHiddenWhenDisabled(t *testing.T) {
	names := func(on bool) map[string]bool {
		out := map[string]bool{}
		for _, d := range Catalog(&Local{Account: &service.Account{ExperimentalAppStateWrites: on}}, nil) {
			out[d.Name] = true
		}
		return out
	}
	off, on := names(false), names(true)
	if off["clear_chat"] || off["remove_contact"] || !off["save_contact"] || !off["update_chat"] {
		t.Fatalf("disabled: clear_chat %v remove_contact %v save_contact %v", off["clear_chat"], off["remove_contact"], off["save_contact"])
	}
	if !on["clear_chat"] || !on["remove_contact"] || len(on) != len(off)+2 {
		t.Fatalf("enabled: %d tools vs %d", len(on), len(off))
	}
	// The REST backend (stdio MCP) cannot know the server's config: the tools stay and
	// the API answers feature_disabled, which has a hint.
	if hints["feature_disabled"] == "" || hints["app_state_out_of_sync"] == "" {
		t.Fatal("missing hints")
	}
}
