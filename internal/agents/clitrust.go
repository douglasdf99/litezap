package agents

// How the codex_cli runtime starts Codex safely: which binary it trusts (and its node),
// the private home each process gets, the minimal environment, and the stream watcher
// that kills Codex the moment it uses anything other than LiteZap's tools.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ---- binary trust ----

// trustedDirChain checks dir and every folder above it up to the root: each must be owned
// by root or the current user and not writable by group or others (sticky folders such as
// /tmp are fine), so no other local user can swap a file on the way.
func trustedDirChain(dir string) error {
	dir = filepath.Clean(dir)
	for {
		fi, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return err
			}
			if err := trustedDirChain(real); err != nil {
				return err
			}
		} else if !fi.IsDir() || !checkFilePerms(fi) {
			return fmt.Errorf("%s can be changed by other users", dir)
		}
		up := filepath.Dir(dir)
		if up == dir {
			return nil
		}
		dir = up
	}
}

// trustedExec checks an executable: absolute, a regular file with exec bits, owned by root
// or the current user, not group/other writable, and every folder above both the path and
// its symlink target trusted. It returns the resolved target.
func trustedExec(p string) (string, error) {
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	if err := trustedDirChain(filepath.Dir(p)); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", real)
	}
	if !checkFilePerms(fi) {
		return "", fmt.Errorf("%s can be changed by other users", real)
	}
	if err := trustedDirChain(filepath.Dir(real)); err != nil {
		return "", err
	}
	return real, nil
}

func isExecutable(p string) bool { _, err := trustedExec(p); return err == nil }

// interpreter finds the program a script's shebang runs (codex is usually a node script)
// and checks it like codex itself. A native binary has none ("").
func interpreter(bin, real string) (string, error) {
	f, err := os.Open(real)
	if err != nil {
		return "", err
	}
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	f.Close()
	head = head[:n]
	if !bytes.HasPrefix(head, []byte("#!")) {
		return "", nil
	}
	line, _, _ := bytes.Cut(head[2:], []byte("\n"))
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return "", errors.New("empty shebang")
	}
	if _, err := trustedExec(fields[0]); err != nil {
		return "", fmt.Errorf("shebang %s: %w", fields[0], err)
	}
	if filepath.Base(fields[0]) != "env" {
		return fields[0], nil
	}
	name := ""
	for _, a := range fields[1:] {
		if !strings.HasPrefix(a, "-") {
			name = a
			break
		}
	}
	if name == "" || strings.ContainsRune(name, '/') {
		return "", fmt.Errorf("unsupported shebang %q", string(line))
	}
	// Where npm, nvm, Volta and Homebrew put node next to codex, then the system folders.
	for _, dir := range []string{filepath.Dir(bin), filepath.Dir(real), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		cand := filepath.Join(dir, name)
		if _, err := os.Stat(cand); err != nil {
			continue
		}
		if _, err := trustedExec(cand); err != nil {
			return "", fmt.Errorf("%s: %w", cand, err)
		}
		return cand, nil
	}
	return "", fmt.Errorf("%s (for %s) not found next to codex", name, bin)
}

// trustedPATH re-checks codex (and its interpreter) right before a process starts and
// returns the only folders Codex gets on PATH: codex's, its interpreter's and the system
// folders /usr/bin and /bin when they are trusted.
func trustedPATH(bin string) (string, error) {
	real, err := trustedExec(bin)
	if err != nil {
		return "", fmt.Errorf("codex binary not trusted: %w", err)
	}
	interp, err := interpreter(bin, real)
	if err != nil {
		return "", fmt.Errorf("codex interpreter not trusted: %w", err)
	}
	dirs := []string{filepath.Dir(bin)}
	if interp != "" {
		dirs = append(dirs, filepath.Dir(interp))
	}
	for _, d := range []string{"/usr/bin", "/bin"} {
		if trustedDirChain(d) == nil {
			dirs = append(dirs, d)
		}
	}
	out := []string{}
	for _, d := range dirs {
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return strings.Join(out, string(os.PathListSeparator)), nil
}

// ---- private home ----

// codexHome is the private HOME of one codex process: a 0700 temporary folder holding only
// a copy of the user's auth.json (0600). Codex (and so the model) never sees ~/.codex, the
// LiteZap data folder or anything else in the user's home. Removed afterwards; a token that
// Codex refreshed meanwhile is written back, unless the original changed too.
type codexHome struct {
	dir, codexDir string
	src           string // the user's auth.json
	orig          []byte
	had           bool
}

const maxAuthFile = 1 << 20

func (c *CodexCLI) sourceHome() string {
	if c.CodexHome != "" {
		return c.CodexHome
	}
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func readLimited(p string, n int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > n {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, n)
	}
	return b, nil
}

func (c *CodexCLI) newHome() (*codexHome, error) {
	dir, err := os.MkdirTemp("", "litezap-codex-home-")
	if err != nil {
		return nil, err
	}
	h := &codexHome{dir: dir, codexDir: filepath.Join(dir, ".codex"), src: filepath.Join(c.sourceHome(), "auth.json")}
	fail := func(err error) (*codexHome, error) { os.RemoveAll(dir); return nil, err }
	if err := os.Chmod(dir, 0o700); err != nil {
		return fail(err)
	}
	if err := os.Mkdir(h.codexDir, 0o700); err != nil {
		return fail(err)
	}
	b, err := readLimited(h.src, maxAuthFile)
	switch {
	case err == nil:
		if err := os.WriteFile(filepath.Join(h.codexDir, "auth.json"), b, 0o600); err != nil {
			return fail(err)
		}
		h.orig, h.had = b, true
	case errors.Is(err, fs.ErrNotExist):
		// No file login (or a keyring login): Codex reports it is not logged in.
	default:
		return fail(err)
	}
	return h, nil
}

func (h *codexHome) close() {
	defer os.RemoveAll(h.dir)
	if !h.had {
		return
	}
	b, err := readLimited(filepath.Join(h.codexDir, "auth.json"), maxAuthFile)
	if err != nil || bytes.Equal(b, h.orig) || !json.Valid(b) {
		return
	}
	cur, err := readLimited(h.src, maxAuthFile)
	if err != nil || !bytes.Equal(cur, h.orig) {
		return // the user logged in again meanwhile: theirs wins
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.src), ".auth.json.litezap-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(name, 0o600) != nil || os.Rename(name, h.src) != nil {
		os.Remove(name)
	}
}

// ---- processes ----

// runOpts configure one codex process.
type runOpts struct {
	timeout time.Duration
	stdin   []byte
	env     []string // extra variables (the run token)
	dir     string   // working folder ("" = the private home)
	// onLine sees each stdout line; a non-empty answer is a violation: the process group is
	// killed at once.
	onLine func(line []byte) string
}

var errCodexViolation = errors.New("codex used something other than LiteZap's tools")

// env is the whole environment of a codex process: the trusted PATH, the private home,
// locale, certificates and the proxy variables (so Codex reaches OpenAI through the same
// proxy as the user). Provider keys (OPENAI_API_KEY, CODEX_API_KEY...) and everything else
// are left out.
func codexEnv(path string, h *codexHome, extra ...string) []string {
	env := []string{"PATH=" + path, "HOME=" + h.dir, "CODEX_HOME=" + h.codexDir}
	for _, k := range []string{"USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// run starts codex once: binary and interpreter re-checked, private home, minimal
// environment, own process group, bounded output, killed on timeout or violation.
func (c *CodexCLI) run(ctx context.Context, bin string, o runOpts, args ...string) (stdout, stderr, violation string, err error) {
	path, err := trustedPATH(bin)
	if err != nil {
		return "", "", "", err
	}
	home, err := c.newHome()
	if err != nil {
		return "", "", "", fmt.Errorf("codex home: %w", err)
	}
	defer home.close()
	cctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	kctx, kill := context.WithCancel(cctx)
	defer kill()
	cmd := exec.CommandContext(kctx, bin, args...)
	cmd.Env = codexEnv(path, home, o.env...)
	cmd.Dir = home.dir
	if o.dir != "" {
		cmd.Dir = o.dir
	}
	cmd.WaitDelay = 3 * time.Second
	setProcessGroup(cmd)
	if o.stdin != nil {
		cmd.Stdin = bytes.NewReader(o.stdin)
	}
	var out, errb capped
	out.max, errb.max = 4<<20, 1<<20
	cmd.Stderr = &errb
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", "", err
	}
	if err := cmd.Start(); err != nil {
		return "", "", "", err
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		out.Write(line)
		out.Write([]byte{'\n'})
		if o.onLine != nil && violation == "" {
			if v := o.onLine(line); v != "" {
				violation = v
				kill()
			}
		}
	}
	if sc.Err() != nil {
		if o.onLine != nil && violation == "" {
			violation = "an output line too long to check"
			kill()
		}
		_, _ = io.Copy(io.Discard, pipe)
	}
	err = cmd.Wait()
	switch {
	case violation != "":
		err = errCodexViolation
	case cctx.Err() != nil:
		err = context.DeadlineExceeded
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	return out.String(), errb.String(), violation, err
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

// codexServerName is the name LiteZap's MCP server gets in Codex's config.
const codexServerName = "litezap"

// codexItemViolation is the stream watcher: in `codex exec --json`, an item other than the
// model's text, its reasoning or a call to LiteZap's own MCP server (a shell command, a
// file change, a web search, another MCP server, or a type this version did not have) is
// a violation.
func codexItemViolation(line []byte) string {
	var e struct {
		Type string `json:"type"`
		Item *struct {
			Type   string `json:"type"`
			Server string `json:"server"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &e) != nil || !strings.HasPrefix(e.Type, "item.") || e.Item == nil {
		return ""
	}
	switch e.Item.Type {
	case "agent_message", "reasoning":
		return ""
	case "mcp_tool_call":
		if e.Item.Server == codexServerName {
			return ""
		}
		return fmt.Sprintf("a tool of another MCP server (%q)", clipText(e.Item.Server, 60))
	}
	return fmt.Sprintf("Codex's own %q", clipText(e.Item.Type, 60))
}
