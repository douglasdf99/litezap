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
// Policy note (see docs/guias/ia-por-assinatura): OpenAI documents `codex exec` for
// scripts and says it reuses the saved CLI login, while recommending API keys for
// automation; the ChatGPT Terms of Use forbid extracting output "automatically or
// programmatically". Whether a third-party app may drive a user's ChatGPT-plan login is
// not stated, so the runtime is off until the operator accepts CodexNotice per agent.

import (
	"bufio"
	"bytes"
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
	"runtime"
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

// checkRuntimeNotice guards the switch of an agent to codex_cli: it uses the Codex login
// of the computer running LiteZap, so only the admin may choose it (an instance token
// must not spend the owner's plan), and accept_runtime_notice must confirm CodexNotice.
// In-process callers without a scope (tests, tools) are trusted.
func checkRuntimeNotice(ctx context.Context, prev, next string, in Input) error {
	if next != RuntimeCodexCLI || prev == RuntimeCodexCLI {
		return nil
	}
	if sc, ok := service.ScopeFrom(ctx); ok && !sc.Admin {
		return apierr.New(http.StatusForbidden, "forbidden", "only the admin token can switch an agent to the codex_cli runtime "+
			"(it uses the Codex login of the computer running LiteZap)", false)
	}
	if in.AcceptRuntimeNotice != nil && *in.AcceptRuntimeNotice {
		return nil
	}
	return apierr.New(http.StatusBadRequest, "runtime_notice_required",
		"runtime codex_cli needs accept_runtime_notice: true (read the notice: "+CodexNotice+")", false)
}

// codexDenyFeatures are Codex features turned off for agent runs (when this Codex version
// has them): shell, files, browser, computer use, plugins, hooks, sub-agents... Codex then
// only has the LiteZap MCP tools. The sandbox is read-only and the working folder empty
// anyway.
var codexDenyFeatures = []string{"shell_tool", "unified_exec", "shell_snapshot", "apps", "browser_use",
	"browser_use_external", "in_app_browser", "computer_use", "image_generation", "multi_agent", "multi_agent_v2",
	"plugins", "remote_plugin", "hooks", "memories", "goals", "tool_suggest", "skill_mcp_dependency_install",
	"skill_search", "workspace_dependencies", "code_mode", "code_mode_host", "standalone_web_search", "js_repl"}

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
	Log     *slog.Logger

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

// Status detects Codex (cached for 30s unless refresh).
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
		st.Detail = "Codex CLI não encontrado. Instale (npm install -g @openai/codex ou brew install codex) e rode `codex login`."
		return st
	}
	st.Installed, st.Path = true, bin
	if out, _, err := c.cmd(ctx, bin, 15*time.Second, nil, nil, "--version"); err == nil {
		st.Version = clipText(strings.TrimSpace(out), 80)
	}
	out, errOut, err := c.cmd(ctx, bin, 15*time.Second, nil, nil, "login", "status")
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
	if out, _, err := c.cmd(ctx, bin, 15*time.Second, nil, nil, "features", "list"); err == nil {
		have := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				have[f[0]] = true
			}
		}
		for _, f := range codexDenyFeatures {
			if have[f] {
				st.Disabled = append(st.Disabled, f)
			}
		}
	}
	if !slices.Contains(st.Disabled, "shell_tool") {
		// Older or newer Codex without that switch: still read-only sandbox, empty folder.
		st.Detail = strings.TrimSpace(st.Detail + " Esta versão do Codex não permite desligar o shell: ele fica no sandbox somente leitura, numa pasta vazia.")
	}
	st.Ready = true
	return st
}

// resolve finds the codex executable.
func (c *CodexCLI) resolve() (string, error) {
	if c.Path != "" {
		if isExecutable(c.Path) {
			return c.Path, nil
		}
		return "", fmt.Errorf("%s is not an executable", c.Path)
	}
	if p, err := exec.LookPath("codex"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
		return p, nil
	}
	home, _ := os.UserHomeDir()
	cands := []string{filepath.Join(home, ".local/bin/codex"), "/opt/homebrew/bin/codex", "/usr/local/bin/codex",
		filepath.Join(home, ".volta/bin/codex"), filepath.Join(home, ".bun/bin/codex")}
	nvm, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/*/bin/codex"))
	sort.Sort(sort.Reverse(sort.StringSlice(nvm)))
	for _, p := range append(cands, nvm...) {
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", errors.New("codex not found")
}

// isExecutable accepts a regular executable file that neither it nor its folder can be
// changed by group or others (on Unix), so no other local user can swap what LiteZap runs.
func isExecutable(p string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	fi, err := os.Stat(real)
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	dir, err := os.Stat(filepath.Dir(real))
	return err == nil && fi.Mode().Perm()&0o022 == 0 && (dir.Mode().Perm()&0o022 == 0 || dir.Mode()&os.ModeSticky != 0)
}

// env is the environment of a codex process: only what it needs to find node, its login
// (HOME, CODEX_HOME) and the network. Provider keys (OPENAI_API_KEY, CODEX_API_KEY...) are
// NOT passed, so Codex uses the user's saved login and LiteZap's own secrets stay out.
func (c *CodexCLI) env(bin string, extra ...string) []string {
	path := filepath.Dir(bin)
	if p := os.Getenv("PATH"); p != "" {
		path += string(os.PathListSeparator) + p
	}
	path += ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	env := []string{"PATH=" + path}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "CODEX_HOME", "SSL_CERT_FILE",
		"SSL_CERT_DIR", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// cmd runs codex and returns its stdout and stderr (each capped at 4MB).
func (c *CodexCLI) cmd(ctx context.Context, bin string, timeout time.Duration, stdin []byte, extraEnv []string,
	args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = c.env(bin, extraEnv...)
	cmd.Dir = os.TempDir()
	cmd.WaitDelay = 3 * time.Second
	setProcessGroup(cmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb capped
	out.max, errb.max = 4<<20, 1<<20
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if cctx.Err() != nil {
		err = context.DeadlineExceeded
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	return out.String(), errb.String(), err
}

// capped is a bounded buffer.
type capped struct {
	bytes.Buffer
	max int
}

func (b *capped) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func (c *CodexCLI) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 150 * time.Second
}

// execArgs is the `codex exec` command line of a run.
func (c *CodexCLI) execArgs(st CodexStatus, url, workdir, lastFile string) []string {
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "--cd", workdir, "--output-last-message", lastFile, "--color", "never",
		"-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`,
		"-c", `mcp_servers.litezap.url="` + url + `"`,
		"-c", `mcp_servers.litezap.bearer_token_env_var="` + codexTokenEnv + `"`,
		"-c", "mcp_servers.litezap.required=true",
		"-c", `mcp_servers.litezap.default_tools_approval_mode="approve"`,
		"-c", "mcp_servers.litezap.startup_timeout_sec=20",
		"-c", "mcp_servers.litezap.tool_timeout_sec=60",
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
	m := &runMCP{token: token, ctx: ctx, ex: ex, delim: attendant.NewDelimiter(), max: maxCalls}
	m.url = "http://" + ln.Addr().String() + "/mcp"
	server := sdk.NewServer(&sdk.Implementation{Name: "litezap-agent-run", Version: "1"}, &sdk.ServerOptions{
		Instructions: "LiteZap tools of one agent run. LiteZap enforces the agent's permissions and limits: a refused call is final.",
	})
	for _, s := range specs {
		server.AddTool(&sdk.Tool{Name: s.Name, Description: s.Description, InputSchema: s.Schema}, m.handle)
	}
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
You are running inside LiteZap through the Codex CLI. There is no code or workspace here: do not run shell commands, read or write files, browse the web or plan code changes. Your ONLY tools are the "litezap" MCP tools listed for this run. Everything below the security rules is the task.

`

// cliMaxCalls is the tool calls a codex_cli run may make (the provider runtime bounds
// model turns instead; Codex runs its own loop).
func cliMaxCalls(l Limits) int { return max(4, 3*l.MaxSteps) }

// runCLI performs a run on the Codex CLI. Errors before Codex starts (not installed, not
// logged in) are returned as not_configured; later failures end the run with status failed.
func (r *Runner) runCLI(ctx context.Context, in RunInput) (RunResult, error) {
	a := in.Agent
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

	work, err := os.MkdirTemp("", "litezap-codex-")
	if err != nil {
		run.Status, run.Error = StatusFailed, "codex_error: could not create the run folder"
		return r.finish(ctx, in, run, ex, false), nil
	}
	defer os.RemoveAll(work)
	last := filepath.Join(work, ".last-message.txt")
	bin := cst.Path
	stdout, stderr, runErr := r.Codex.cmd(rctx, bin, r.Codex.timeout()+5*time.Second, []byte(prompt),
		[]string{codexTokenEnv + "=" + srv.token}, r.Codex.execArgs(cst, srv.url, work, last)...)

	ev := parseCodexEvents(stdout)
	run.ModelCalls = max(1, ev.turns)
	run.InputTokens, run.OutputTokens = ev.input, ev.output
	run.Output = ev.lastMessage
	if b, err := os.ReadFile(last); err == nil && strings.TrimSpace(string(b)) != "" {
		run.Output = string(b)
	}
	limited := srv.end() // waits for a call in flight: the executor is ours again
	srv.close()
	switch {
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
	if len(ev.builtin) > 0 {
		note := "warning: Codex used built-in tools (" + strings.Join(ev.builtin, ", ") + ")"
		run.Error = strings.TrimPrefix(strings.TrimSpace(run.Error+" · "+note), "· ")
		r.log().Warn("agents: codex used built-in tools", "instance_id", in.InstanceID, "run_id", run.ID, "tools", ev.builtin)
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
	builtin       []string // Codex's own tools that ran (should never happen)
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
		case "item.completed", "item.started":
			if e.Item == nil {
				continue
			}
			switch e.Item.Type {
			case "agent_message":
				if e.Type == "item.completed" && strings.TrimSpace(e.Item.Text) != "" {
					ev.lastMessage = e.Item.Text
				}
			case "command_execution", "file_change", "web_search":
				if e.Type == "item.started" && !slices.Contains(ev.builtin, e.Item.Type) {
					ev.builtin = append(ev.builtin, e.Item.Type)
				}
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
