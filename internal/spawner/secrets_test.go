package spawner

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestProcessEnvironDropsSupervisorSecrets(t *testing.T) {
	t.Setenv("LITEZAP_ADMIN_TOKEN", "lza_secret_admin")
	t.Setenv("LITEZAP_OPERATOR_TOKEN", "lzo_secret_operator")
	t.Setenv("LITEZAP_CONFIG_JSON", `{"operator_token":"lzo_secret_json"}`)
	t.Setenv("LITEZAP_WORKER_ZOMBIE_TIMEOUT", "1h")
	t.Setenv("LITEZAP_LOG_FORMAT", "json")
	env, err := (&Process{}).environ(context.Background(), exec.Command("true"), "inst_e")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "ZOMBIE_TIMEOUT=1h") {
		t.Fatal("supervisor-only variables leaked into the worker environment")
	}
	if !strings.Contains(joined, "LITEZAP_LOG_FORMAT=json") {
		t.Fatal("the log format must reach the worker")
	}
}
