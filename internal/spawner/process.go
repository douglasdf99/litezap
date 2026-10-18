package spawner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// CommandFunc builds the worker command for an instance.
type CommandFunc func(instanceID, socket string) *exec.Cmd

// Process starts each worker as a child process of the current executable.
type Process struct {
	DataDir       string
	MemoryLimitMB int64
	Command       CommandFunc   // nil: `<this executable> worker ...`
	StartTimeout  time.Duration // default 10s
	Log           *slog.Logger
	// Env, when set, adds per-instance variables to the worker environment (e.g. its proxy).
	Env func(ctx context.Context, instanceID string) ([]string, error)
}

func (p *Process) pidFile(id string) string { return filepath.Join(p.DataDir, "run", id+".pid") }

func (p *Process) command(id, sock string) *exec.Cmd {
	if p.Command != nil {
		return p.Command(id, sock)
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exec.Command(exe, "worker", "--instance", id, "--socket", sock, "--data-dir", p.DataDir)
}

// proxyVars are dropped from the worker environment: the worker's only proxy is the one
// the supervisor passes explicitly (spec 4.6), never one inherited from our environment
// (including a stale LITEZAP_WORKER_PROXY_URL; the per-instance value is appended after).
// Names are compared case-insensitively, as Windows environment names are.
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "LITEZAP_WORKER_PROXY_URL"}

// supervisorOnlyVars are also dropped: secrets and settings the worker never needs
// (defense in depth; a worker only talks to WhatsApp and its supervisor), and a stale
// watchdog setting (the supervisor appends the current one).
var supervisorOnlyVars = []string{"LITEZAP_ADMIN_TOKEN", "LITEZAP_OPERATOR_TOKEN", "LITEZAP_CONFIG_JSON",
	"LITEZAP_DEFAULT_PROXY_URL", "LITEZAP_WORKER_ZOMBIE_TIMEOUT"}

func isProxyVar(name string) bool {
	for _, v := range append(proxyVars, supervisorOnlyVars...) {
		if strings.EqualFold(name, v) {
			return true
		}
	}
	return false
}

// environ is the worker's environment: ours without proxy variables, plus GOMEMLIMIT and
// p.Env (later entries win).
func (p *Process) environ(ctx context.Context, cmd *exec.Cmd, id string) ([]string, error) {
	var env []string
	for _, kv := range cmd.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !isProxyVar(k) {
			env = append(env, kv)
		}
	}
	if p.MemoryLimitMB > 0 {
		env = append(env, fmt.Sprintf("GOMEMLIMIT=%dMiB", p.MemoryLimitMB))
	}
	if p.Env != nil {
		extra, err := p.Env(ctx, id)
		if err != nil {
			return nil, err
		}
		env = append(env, extra...)
	}
	return env, nil
}

func (p *Process) Start(ctx context.Context, id string) (WorkerHandle, error) {
	sock := SocketPath(p.DataDir, id)
	if err := ensureSocketDir(sock); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(p.DataDir, "run"), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(sock)

	cmd := p.command(id, sock)
	env, err := p.environ(ctx, cmd, id)
	if err != nil {
		return nil, fmt.Errorf("worker env: %w", err)
	}
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	detach(cmd) // own process group: Ctrl+C on the supervisor must not hit workers directly
	// Parent-liveness pipe: the worker reads its stdin and exits on EOF, which happens
	// when this handle closes the writer or the supervisor dies.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("worker stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	h := &procHandle{cmd: cmd, done: make(chan struct{}), pidFile: p.pidFile(id), stdin: stdin}
	if err := os.WriteFile(h.pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil && p.Log != nil {
		p.Log.Warn("cannot write worker pid file", "instance", id, "path", h.pidFile, "err", err)
	}
	go func() {
		_ = cmd.Wait()
		_ = os.Remove(h.pidFile)
		h.closeConn() // a crashed worker must not leak its ClientConn
		close(h.done)
	}()

	timeout := p.StartTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if err := waitForSocket(ctx, sock, h.done, timeout); err != nil {
		_ = h.Kill()
		return nil, err
	}
	// A custom dialer instead of a "unix://" target: url.Parse rejects Windows paths
	// such as C:\Users\..., while AF_UNIX itself works there.
	conn, err := grpc.NewClient("passthrough:///worker",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()), wire.DialOption())
	if err != nil {
		_ = h.Kill()
		return nil, fmt.Errorf("dial worker: %w", err)
	}
	if !h.setConn(conn) { // worker already exited
		return nil, errors.New("worker exited during start")
	}
	h.client = workerv1.NewWorkerClient(conn)
	// The socket file exists from bind(), slightly before listen(); wait until the worker answers.
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() { // abort the readiness wait if the worker dies
		select {
		case <-h.done:
			cancel()
		case <-pctx.Done():
		}
	}()
	if _, err := h.client.Ping(pctx, &workerv1.PingRequest{}, grpc.WaitForReady(true)); err != nil {
		_ = h.Kill()
		return nil, fmt.Errorf("worker not answering: %w", err)
	}
	return h, nil
}

func waitForSocket(ctx context.Context, sock string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return errors.New("worker exited before opening its socket")
		case <-deadline.C:
			return fmt.Errorf("worker socket %s not ready after %s", sock, timeout)
		case <-tick.C:
		}
	}
}

type procHandle struct {
	cmd     *exec.Cmd
	done    chan struct{}
	pidFile string
	client  workerv1.WorkerClient
	stdin   io.Closer

	mu     sync.Mutex
	conn   *grpc.ClientConn
	closed bool
}

func (h *procHandle) Client() workerv1.WorkerClient { return h.client }
func (h *procHandle) Done() <-chan struct{}         { return h.done }

func (h *procHandle) closeConn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.conn != nil {
		h.conn.Close()
	}
}

// setConn stores the client connection; if the handle is already closed (the worker
// exited) the connection is closed immediately and false is returned.
func (h *procHandle) setConn(c *grpc.ClientConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		c.Close()
		return false
	}
	h.conn = c
	return true
}

func (h *procHandle) Kill() error {
	h.closeConn()
	err := h.cmd.Process.Kill()
	<-h.done
	_ = h.stdin.Close()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (h *procHandle) Close(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return h.Kill() // no SIGINT for child processes on Windows
	}
	_ = h.cmd.Process.Signal(os.Interrupt)
	// Keep the connection open while the worker winds down so in-flight RPCs finish.
	select {
	case <-h.done:
		h.closeConn()
		_ = h.stdin.Close()
		return nil
	case <-ctx.Done():
		return h.Kill()
	}
}

// CleanupOrphans kills workers left behind by a previous supervisor that crashed.
// Only PIDs whose command line still looks like a litezap worker are killed.
func CleanupOrphans(dataDir string, log *slog.Logger) {
	files, _ := filepath.Glob(filepath.Join(dataDir, "run", "*.pid"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".pid")
		b, err := os.ReadFile(f)
		_ = os.Remove(f)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || !isWorkerProcess(pid, id, dataDir) {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			log.Warn("killing orphan worker", "pid", pid, "pid_file", f)
			_ = proc.Kill()
			for i := 0; i < 40 && processAlive(proc); i++ {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
}

func processAlive(p *os.Process) bool { return p.Signal(syscall.Signal(0)) == nil }

func isWorkerProcess(pid int, id, dataDir string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	out, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	cmd := string(out)
	return strings.Contains(cmd, " worker ") &&
		// The worker always gets --socket after the id, so the trailing space stops
		// "inst_a" from matching a worker of "inst_ab".
		strings.Contains(cmd, "--instance "+id+" ") &&
		// --data-dir is the last flag, so a suffix match stops "/x/ab" matching "/x/abc".
		strings.HasSuffix(strings.TrimSpace(cmd), " --data-dir "+dataDir)
}
