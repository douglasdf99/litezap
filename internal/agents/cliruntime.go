package agents

// The codex_cli runtime (opt-in): per run, LiteZap starts the user's own Codex CLI
// (`codex exec`, installed and logged in by the user with their ChatGPT plan) instead of
// calling an AI provider's API. Codex only gets LiteZap's tools, through a run-scoped MCP
// endpoint that this file serves on the loopback interface for the run's lifetime: every
// tool call goes through the same executor as the provider runtime (permission groups,
// trigger-chat-only sends and reads, caps, mentions-all refusal, modes, guard), so the
// model, whoever runs it, cannot do more than the agent's permissions. Codex never sees an
// instance or admin token: the run token only reaches this one endpoint, which only offers
// the agent's tools and dies with the run.
//
// It fails closed: only a checked Codex version range, only when every dangerous Codex
// feature can be disabled, in a private home, and the run is killed as soon as Codex
// reports using anything but LiteZap's tools (clitrust.go).
//
// Policy note (see docs/guias/ia-por-assinatura): OpenAI documents `codex exec` for
// scripts and says it reuses the saved CLI login, while recommending API keys for
// automation; the ChatGPT Terms of Use forbid extracting output "automatically or
// programmatically". Whether a third-party app may drive a user's ChatGPT-plan login is
// not stated, so the runtime is off until the admin accepts CodexNotice per agent.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Runtimes.
const (
	// RuntimeProvider runs the agent on the instance's AI provider (API key; the default).
	RuntimeProvider = "provider"
	// RuntimeCodexCLI runs the agent on the user's own Codex CLI (`codex exec`).
	RuntimeCodexCLI = "codex_cli"
)

// CodexNotice must be accepted (accept_runtime_notice) when an agent switches to codex_cli.
const CodexNotice = "Este agente vai usar o Codex CLI instalado e logado neste computador, com a SUA conta " +
	"(assinatura ChatGPT ou chave da OpenAI). O LiteZap roda o `codex exec` sozinho a cada gatilho. A OpenAI documenta " +
	"o `codex exec` para scripts, mas recomenda chave de API para automação, e os Termos de Uso do ChatGPT proíbem " +
	"extrair respostas de forma automática ou programática; não está claro se um app de terceiros pode usar o seu " +
	"plano assim. Você decide e responde por isso: o uso conta nos limites do seu plano e pode ser bloqueado pela OpenAI. " +
	"O conteúdo das conversas vai para a OpenAI. As permissões e limites do agente continuam valendo."

// requireCodexAdmin refuses a non-admin caller: a codex_cli agent uses the Codex login of
// the computer running LiteZap, so only the admin may create, change, run or simulate one
// (an instance token must not spend the owner's plan nor reprogram what runs on it).
// In-process callers without a scope (the agent hook, tests) are trusted.
func requireCodexAdmin(ctx context.Context) error {
	if sc, ok := service.ScopeFrom(ctx); ok && !sc.Admin {
		return apierr.New(http.StatusForbidden, "forbidden", "only the admin token can create, change, run or simulate an agent "+
			"with the codex_cli runtime (it uses the Codex login of the computer running LiteZap)", false)
	}
	return nil
}

// checkRuntimeNotice guards a create or change: touching a codex_cli agent (before or
// after) needs the admin, and switching to codex_cli also accept_runtime_notice.
func checkRuntimeNotice(ctx context.Context, prev, next string, in Input) error {
	if next != RuntimeCodexCLI && prev != RuntimeCodexCLI {
		return nil
	}
	if err := requireCodexAdmin(ctx); err != nil {
		return err
	}
	if next != RuntimeCodexCLI || prev == RuntimeCodexCLI {
		return nil
	}
	if in.AcceptRuntimeNotice != nil && *in.AcceptRuntimeNotice {
		return nil
	}
	return apierr.New(http.StatusBadRequest, "runtime_notice_required",
		"runtime codex_cli needs accept_runtime_notice: true (read the notice: "+CodexNotice+")", false)
}

// codexCriticalFeatures must all be listed by `codex features list` (and get disabled);
// otherwise the runtime is not ready. Any other listed feature starting with one of
// codexRiskyPrefixes (and not "removed") is disabled too.
var codexCriticalFeatures = []string{"shell_tool", "unified_exec", "apps", "plugins", "hooks", "browser_use",
	"computer_use", "multi_agent"}

var codexRiskyPrefixes = []string{"shell_", "unified_exec", "multi_agent", "browser_use", "computer_use", "code_mode",
	"js_repl", "apply_patch", "exec_", "plugin", "remote_plugin", "hooks", "apps", "in_app_browser", "image_generation",
	"memories", "goals", "tool_suggest", "skill_", "workspace_dependencies", "standalone_web_search",
	"request_permissions", "artifact", "chronicle", "external_agent"}

// codexDisabled returns the features to turn off, or an error when a critical one is not
// listed (this Codex could still have it on under another name).
func codexDisabled(featuresOut string) ([]string, error) {
	stage := map[string]string{}
	var order []string
	for _, line := range strings.Split(featuresOut, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		st := ""
		if len(f) > 2 {
			st = strings.Join(f[1:len(f)-1], " ")
		}
		if _, dup := stage[f[0]]; !dup {
			order = append(order, f[0])
		}
		stage[f[0]] = st
	}
	var missing []string
	for _, c := range codexCriticalFeatures {
		if st, ok := stage[c]; !ok || st == "removed" {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("esta versão do Codex não lista os recursos %s", strings.Join(missing, ", "))
	}
	out := []string{}
	for _, name := range order {
		if stage[name] == "removed" {
			continue
		}
		if slices.Contains(codexCriticalFeatures, name) ||
			slices.ContainsFunc(codexRiskyPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			out = append(out, name)
		}
	}
	return out, nil
}

// Codex versions this runtime was checked against (flags, config keys, feature names,
// --json events): [codexMinVersion, codexMaxVersion).
var (
	codexMinVersion = [3]int{0, 145, 0}
	codexMaxVersion = [3]int{0, 200, 0}
)

// parseCodexVersion reads "codex-cli 0.145.0".
func parseCodexVersion(s string) ([3]int, bool) {
	var v [3]int
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) != 2 || f[0] != "codex-cli" {
		return v, false
	}
	core, _, _ := strings.Cut(f[1], "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func supportedCodexVersion(s string) bool {
	v, ok := parseCodexVersion(s)
	return ok && !versionLess(v, codexMinVersion) && versionLess(v, codexMaxVersion)
}

// codexTokenEnv carries the run token to Codex (never on the command line).
const codexTokenEnv = "LITEZAP_AGENT_RUN_TOKEN"

// CodexCLI runs `codex exec`. It never sends a model request outside a run: detection
// only runs `codex --version`, `codex login status` and `codex features list`.
type CodexCLI struct {
	// Path is the codex executable ("" = PATH, then the usual install folders).
	Path string
	// Model, when set, is passed as --model ("" = the Codex default of the user's plan).
	Model string
	// Timeout bounds one run (default 150s; the agent hook allows 3 minutes).
	Timeout time.Duration
	// CodexHome is where the user's Codex login lives ("" = $CODEX_HOME, else ~/.codex).
	// Only its auth.json is copied into each process's private home.
	CodexHome string
	Log       *slog.Logger

	mu       sync.Mutex
	cached   CodexStatus
	cachedAt time.Time
}

// NewCodexCLI is the runtime of the app (LITEZAP_CODEX_PATH overrides the lookup).
func NewCodexCLI(log *slog.Logger) *CodexCLI {
	return &CodexCLI{Path: strings.TrimSpace(os.Getenv("LITEZAP_CODEX_PATH")), Log: log}
}

// CodexStatus is what the dashboard shows of the local Codex CLI.
type CodexStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	LoggedIn  bool   `json:"logged_in"`
	// Auth: chatgpt (plan login, fixed cost) | api_key (billed per use) | unknown.
	Auth string `json:"auth,omitempty"`
	// Ready: an agent with runtime codex_cli can run.
	Ready  bool   `json:"ready"`
	Detail string `json:"detail,omitempty"`
	// Disabled are the Codex features turned off in agent runs.
	Disabled  []string  `json:"disabled_features"`
	Notice    string    `json:"notice"`
	CheckedAt time.Time `json:"checked_at"`
}

const codexStatusTTL = 30 * time.Second

// Status detects Codex (cached for 30s unless refresh). Every process start re-checks the
// binary anyway (trustedPATH).
func (c *CodexCLI) Status(ctx context.Context, refresh bool) CodexStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && !c.cachedAt.IsZero() && time.Since(c.cachedAt) < codexStatusTTL {
		return c.cached
	}
	st := c.detect(ctx)
	c.cached, c.cachedAt = st, time.Now()
	return st
}

func (c *CodexCLI) detect(ctx context.Context) CodexStatus {
	st := CodexStatus{Disabled: []string{}, Notice: CodexNotice, CheckedAt: time.Now().UTC()}
	bin, err := c.resolve()
	if err != nil {
		st.Detail = "Codex CLI não encontrado (ou instalado numa pasta que outros usuários podem alterar). Instale " +
			"(npm install -g @openai/codex ou brew install codex) e rode `codex login`."
		return st
	}
	st.Installed, st.Path = true, bin
	if _, err := trustedPATH(bin); err != nil {
		st.Detail = "O LiteZap não confia neste Codex: " + err.Error()
		return st
	}
	quick := runOpts{timeout: 15 * time.Second}
	if out, _, _, err := c.run(ctx, bin, quick, "--version"); err == nil {
		st.Version = clipText(strings.TrimSpace(out), 80)
	}
	if !supportedCodexVersion(st.Version) {
		st.Detail = fmt.Sprintf("Versão do Codex não suportada (%q): o LiteZap foi verificado com codex-cli de %d.%d.%d até antes de %d.%d.%d. "+
			"Instale uma versão dessa faixa ou atualize o LiteZap.", st.Version, codexMinVersion[0], codexMinVersion[1],
			codexMinVersion[2], codexMaxVersion[0], codexMaxVersion[1], codexMaxVersion[2])
		return st
	}
	out, errOut, _, err := c.run(ctx, bin, quick, "login", "status")
	text := strings.ToLower(out + "\n" + errOut)
	switch {
	case err != nil && strings.Contains(text, "not logged in"):
		st.Detail = "O Codex está instalado, mas não está logado: rode `codex login` no Terminal e entre com a sua conta ChatGPT."
		return st
	case err != nil:
		st.Detail = "Não foi possível ver o login do Codex (`codex login status` falhou)."
		return st
	}
	st.LoggedIn = true
	switch {
	case strings.Contains(text, "chatgpt"):
		st.Auth = "chatgpt"
	case strings.Contains(text, "api key") || strings.Contains(text, "api_key"):
		st.Auth = "api_key"
		st.Detail = "O Codex está logado com uma chave de API: o uso é cobrado por chamada, não pela assinatura. Para usar o plano, rode `codex logout` e `codex login`."
	default:
		st.Auth = "unknown"
	}
	fout, _, _, err := c.run(ctx, bin, quick, "features", "list")
	if err != nil {
		st.Detail = strings.TrimSpace(st.Detail + " Não foi possível listar os recursos do Codex (`codex features list` falhou): " +
			"sem isso o LiteZap não consegue desligar o shell e os arquivos, então não roda.")
		return st
	}
	disabled, err := codexDisabled(fout)
	if err != nil {
		st.Detail = strings.TrimSpace(st.Detail + " " + err.Error() + ": o LiteZap não consegue desligar todos os recursos " +
			"perigosos desta versão, então não roda.")
		return st
	}
	st.Disabled = disabled
	st.Ready = true
	return st
}

// resolve finds a trusted codex executable.
func (c *CodexCLI) resolve() (string, error) {
	if c.Path != "" {
		if isExecutable(c.Path) {
			return c.Path, nil
		}
		return "", fmt.Errorf("%s is not a trusted executable", c.Path)
	}
	home, _ := os.UserHomeDir()
	var cands []string
	if p, err := exec.LookPath("codex"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			cands = append(cands, abs)
		}
	}
	cands = append(cands, filepath.Join(home, ".local/bin/codex"), "/opt/homebrew/bin/codex", "/usr/local/bin/codex",
		filepath.Join(home, ".volta/bin/codex"), filepath.Join(home, ".bun/bin/codex"))
	nvm, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/*/bin/codex"))
	sort.Sort(sort.Reverse(sort.StringSlice(nvm)))
	for _, p := range append(cands, nvm...) {
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", errors.New("codex not found")
}

func (c *CodexCLI) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 150 * time.Second
}

// execArgs is the `codex exec` command line of a run.
func (c *CodexCLI) execArgs(st CodexStatus, url, workdir, lastFile string) []string {
	srv := "mcp_servers." + codexServerName + "."
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "--cd", workdir, "--output-last-message", lastFile, "--color", "never",
		"-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`,
		"-c", srv + `url="` + url + `"`,
		"-c", srv + `bearer_token_env_var="` + codexTokenEnv + `"`,
		"-c", srv + "required=true",
		"-c", srv + `default_tools_approval_mode="approve"`,
		"-c", srv + "startup_timeout_sec=20",
		"-c", srv + "tool_timeout_sec=60",
	}
	for _, f := range st.Disabled {
		args = append(args, "--disable", f)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	return append(args, "-") // the prompt comes on stdin
}

// ---- the run-scoped MCP endpoint ----

// runMCP serves one run's tools over MCP (streamable HTTP) on 127.0.0.1, behind a bearer
// token that exists only for that run. Every call goes through the run's executor.
type runMCP struct {
	url   string
	host  string // 127.0.0.1:port, the only Host accepted
	token string
	srv   *http.Server

	ctx   context.Context // the run's
	ex    *executor
	delim string
	max   int // tool calls per run

	mu      sync.Mutex
	calls   int
	limited bool
	done    bool // the run ended: no call may touch the executor any more
}

func newRunToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "lzr_" + base64.RawURLEncoding.EncodeToString(b)
}

// startRunMCP listens on a random loopback port. Close it when the run ends.
func startRunMCP(ctx context.Context, ex *executor, specs []attendant.ToolSpec, maxCalls int, token string) (*runMCP, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	m := &runMCP{token: token, ctx: ctx, ex: ex, delim: attendant.NewDelimiter(), max: maxCalls, host: ln.Addr().String()}
	m.url = "http://" + m.host + "/mcp"
	server := sdk.NewServer(&sdk.Implementation{Name: "litezap-agent-run", Version: "1"}, &sdk.ServerOptions{
		Instructions: "LiteZap tools of one agent run. LiteZap enforces the agent's permissions and limits: a refused call is final.",
	})
	for _, s := range specs {
		server.AddTool(&sdk.Tool{Name: s.Name, Description: s.Description, InputSchema: s.Schema}, m.handle)
	}
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only Codex, on this exact address: no browser (Origin), no other Host (DNS rebinding).
		if r.Host != m.host || r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !secure.Equal(strings.TrimSpace(got), m.token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	m.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = m.srv.Serve(ln) }()
	return m, nil
}

// end hands the executor back to the run: later calls are refused. It reports whether the
// tool call limit was hit.
func (m *runMCP) end() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = true
	return m.limited
}

func (m *runMCP) close() { _ = m.srv.Close() }

// handle runs one MCP tool call through the executor, one at a time.
func (m *runMCP) handle(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "refused by LiteZap: the run has ended"}}, IsError: true}, nil
	}
	m.calls++
	name, args := "", json.RawMessage(nil)
	if req != nil && req.Params != nil {
		name, args = req.Params.Name, req.Params.Arguments
	}
	var content string
	var isErr bool
	if m.calls > m.max {
		m.limited = true
		st := Step{N: m.calls, Tool: name, Args: clipText(string(args), maxStepText), Outcome: OutcomeRefused,
			Result: fmt.Sprintf("refused by LiteZap: at most %d tool calls per run", m.max)}
		m.ex.steps = append(m.ex.steps, st)
		content, isErr = st.Result+". Stop now and write your summary.", true
	} else {
		content, isErr = m.ex.exec(m.ctx, m.calls, attendant.ToolCall{ID: "mcp_" + strconv.Itoa(m.calls), Name: name, Input: args})
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: wrapToolOutput(content, m.delim)}}, IsError: isErr}, nil
}

// ---- the run ----

// codexPreamble tells Codex where it is: no workspace, only the LiteZap tools.
const codexPreamble = `# Environment
You are running inside LiteZap through the Codex CLI. There is no code or workspace here: do not run shell commands, read or write files, browse the web or plan code changes. Your ONLY tools are the "litezap" MCP tools listed for this run; using anything else stops the run. Everything below the security rules is the task.

`

// cliMaxCalls is the tool calls a codex_cli run may make (the provider runtime bounds
// model turns instead; Codex runs its own loop).
func cliMaxCalls(l Limits) int { return max(4, 3*l.MaxSteps) }

// maxLastMessage bounds the final message read back from Codex.
const maxLastMessage = 64 << 10

// runCLI performs a run on the Codex CLI. Errors before Codex starts (not admin, not
// installed, not logged in) are returned; later failures end the run with status failed.
func (r *Runner) runCLI(ctx context.Context, in RunInput) (RunResult, error) {
	a := in.Agent
	if err := requireCodexAdmin(ctx); err != nil {
		return RunResult{}, err
	}
	if r.Codex == nil {
		return RunResult{}, &agenthook.CallError{Reason: "not_configured", Detail: "the codex_cli runtime is not available in this LiteZap"}
	}
	cst := r.Codex.Status(ctx, false)
	if !cst.Ready {
		return RunResult{}, &agenthook.CallError{Reason: "not_configured", Detail: "Codex CLI: " + cst.Detail}
	}
	specs, byName, err := toolSpecs(r.toolsFor(in.InstanceID), a.allowedTools(), a.has(GroupReadOther))
	if err != nil {
		return RunResult{}, err
	}
	trigger := in.Chat
	if trigger != "" {
		if j, err := service.ChatJID(trigger); err == nil {
			trigger = j
		}
	}
	now := r.now()
	model := "codex-cli"
	if r.Codex.Model != "" {
		model = "codex-cli/" + r.Codex.Model
	}
	run := store.AgentRun{ID: secure.NewID("arun_"), InstanceID: in.InstanceID, AgentID: a.ID, AgentName: a.Name,
		Trigger: in.Trigger, ChatJID: in.Chat, MessageID: in.MessageID, Mode: a.Mode, Status: StatusRunning,
		Model: model, StartedAt: now}
	if !in.DryRun {
		if err := r.Store.InsertAgentRun(ctx, run); err != nil {
			return RunResult{}, err
		}
	}
	canary := attendant.NewCanary()
	rctx, cancel := context.WithTimeout(ctx, r.Codex.timeout())
	defer cancel()
	ex := &executor{r: r, instanceID: in.InstanceID, agent: a, mode: a.Mode, runID: run.ID, trigger: trigger,
		dryRun: in.DryRun, guard: in.Guard, defs: byName, canary: canary, recipients: map[string]bool{}}
	token := newRunToken()
	ex.leaks = func(text, c string) bool { return codexLeaks(text, c, token) }
	srv, err := startRunMCP(rctx, ex, specs, cliMaxCalls(a.Limits), token)
	if err != nil {
		run.Status, run.Error = StatusFailed, "codex_error: could not open the run's tool endpoint"
		return r.finish(ctx, in, run, ex, false), nil
	}
	defer srv.close()

	loc := time.UTC
	if r.Location != nil {
		if l := r.Location(ctx, in.InstanceID); l != nil {
			loc = l
		}
	}
	prompt := systemPrompt(a, a.Mode, in.DryRun, canary)
	prompt = strings.Replace(prompt, "# Security rules", codexPreamble+"# Security rules", 1)
	prompt = strings.Replace(prompt, "You have at most "+strconv.Itoa(a.Limits.MaxSteps)+" steps (model turns) in this run.",
		"You have at most "+strconv.Itoa(cliMaxCalls(a.Limits))+" tool calls in this run.", 1)
	prompt += "\n# This run\n" + userTurn(in, now, loc) + "\n"

	work, err := os.MkdirTemp("", "litezap-codex-run-")
	if err != nil {
		run.Status, run.Error = StatusFailed, "codex_error: could not create the run folder"
		return r.finish(ctx, in, run, ex, false), nil
	}
	defer os.RemoveAll(work)
	last := filepath.Join(work, ".last-message.txt")
	stdout, stderr, violation, runErr := r.Codex.run(rctx, cst.Path, runOpts{timeout: r.Codex.timeout() + 5*time.Second,
		stdin: []byte(prompt), env: []string{codexTokenEnv + "=" + srv.token}, dir: work, onLine: codexItemViolation},
		r.Codex.execArgs(cst, srv.url, work, last)...)

	ev := parseCodexEvents(stdout)
	run.ModelCalls = max(1, ev.turns)
	run.InputTokens, run.OutputTokens = ev.input, ev.output
	run.Output = ev.lastMessage
	if b, err := readLimited(last, maxLastMessage); err == nil && strings.TrimSpace(string(b)) != "" {
		run.Output = string(b)
	}
	limited := srv.end() // waits for a call in flight: the executor is ours again
	srv.close()
	switch {
	case violation != "":
		run.Status = StatusFailed
		run.Error = "codex_violation: Codex tried to use " + violation + "; LiteZap stopped it (only LiteZap's tools are allowed)"
		r.log().Warn("agents: codex violation", "instance_id", in.InstanceID, "run_id", run.ID, "what", violation)
	case errors.Is(runErr, context.DeadlineExceeded) || rctx.Err() != nil:
		run.Status, run.Error = StatusFailed, "timeout: the Codex CLI did not finish in "+r.Codex.timeout().String()
	case ex.stopped != nil:
		run.Status, run.Error = StatusDiscarded, ex.stopped.Error()
	case runErr != nil || ev.failure != "":
		why := ev.failure
		if why == "" {
			why = lastLine(stderr)
		}
		if why == "" {
			why = runErr.Error()
		}
		run.Status, run.Error = StatusFailed, "codex_error: "+clipText(maskPhones(why), 500)
	case limited:
		run.Status, run.Error = StatusStepLimit, fmt.Sprintf("tool call limit reached (%d calls)", cliMaxCalls(a.Limits))
	default:
		run.Status = StatusCompleted
	}
	return r.finish(ctx, in, run, ex, codexLeaks(run.Output, canary, srv.token)), nil
}

// codexLeaks reports text that echoes the canary or the run token.
func codexLeaks(text, canary, token string) bool {
	return (canary != "" && strings.Contains(text, canary)) || (token != "" && strings.Contains(text, token))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// codexEvents is what a run keeps of `codex exec --json` (JSON Lines on stdout).
type codexEvents struct {
	turns         int
	input, output int64
	lastMessage   string
	failure       string
}

func parseCodexEvents(out string) codexEvents {
	var ev codexEvents
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
			Item *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "turn.completed":
			ev.turns++
			if e.Usage != nil {
				ev.input += e.Usage.InputTokens
				ev.output += e.Usage.OutputTokens
			}
		case "turn.failed":
			ev.turns++
			if e.Error != nil {
				ev.failure = e.Error.Message
			}
		case "error":
			ev.failure = e.Message
		case "item.completed":
			if e.Item != nil && e.Item.Type == "agent_message" && strings.TrimSpace(e.Item.Text) != "" {
				ev.lastMessage = e.Item.Text
			}
		}
	}
	return ev
}

// ---- runtimes API ----

// RuntimesView is GET /agents/runtimes: what can run agents on this computer.
type RuntimesView struct {
	// Provider is the instance's AI provider (API key), configured in the Atendente IA tab.
	Provider map[string]any `json:"provider"`
	// CodexCLI is the local Codex CLI (opt-in runtime codex_cli); nil when unavailable.
	CodexCLI *CodexStatus `json:"codex_cli"`
	// ClaudeCode explains the Claude subscription path: it is not a runtime LiteZap runs.
	ClaudeCode map[string]any `json:"claude_code"`
}

// Runtimes reports the runtimes (refresh re-detects Codex).
func (r *Runner) Runtimes(ctx context.Context, instanceID string, refresh bool) (RuntimesView, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return RuntimesView{}, err
	}
	v := RuntimesView{Provider: map[string]any{"runtime": RuntimeProvider}}
	if r.Codex != nil {
		st := r.Codex.Status(ctx, refresh)
		if sc, ok := service.ScopeFrom(ctx); ok && !sc.Admin {
			st.Path = "" // a local path is the admin's business
		}
		v.CodexCLI = &st
	}
	v.ClaudeCode = map[string]any{
		"runtime": false,
		"how":     "Abra o Claude Code (com a sua assinatura Claude), instale o plugin LiteZap e rode /litezap:whatsapp-plantao; para repetir, /loop 3m /litezap:whatsapp-plantao. O LiteZap não chama a sua assinatura Claude sozinho: a Anthropic não permite que apps de terceiros usem o login do claude.ai.",
		"docs":    "https://litezap.com/docs/guias/ia-por-assinatura/",
	}
	return v, nil
}
