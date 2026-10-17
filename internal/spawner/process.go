package spawner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
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

func (p *Process) Start(ctx context.Context, id string) (WorkerHandle, error) {
	sock := SocketPath(p.DataDir, id)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(p.DataDir, "run"), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(sock)

	cmd := p.command(id, sock)
	if p.MemoryLimitMB > 0 {
		cmd.Env = append(cmd.Environ(), fmt.Sprintf("GOMEMLIMIT=%dMiB", p.MemoryLimitMB))
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	detach(cmd) // own process group: Ctrl+C on the supervisor must not hit workers directly
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	h := &procHandle{cmd: cmd, done: make(chan struct{}), pidFile: p.pidFile(id)}
	_ = os.WriteFile(h.pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	go func() {
		_ = cmd.Wait()
		_ = os.Remove(h.pidFile)
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
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = h.Kill()
		return nil, fmt.Errorf("dial worker: %w", err)
	}
	h.conn = conn
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
	cmd       *exec.Cmd
	done      chan struct{}
	pidFile   string
	conn      *grpc.ClientConn
	client    workerv1.WorkerClient
	closeOnce sync.Once
}

func (h *procHandle) Client() workerv1.WorkerClient { return h.client }
func (h *procHandle) Done() <-chan struct{}         { return h.done }

func (h *procHandle) closeConn() {
	h.closeOnce.Do(func() {
		if h.conn != nil {
			h.conn.Close()
		}
	})
}

func (h *procHandle) Kill() error {
	h.closeConn()
	err := h.cmd.Process.Kill()
	<-h.done
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (h *procHandle) Close(ctx context.Context) error {
	h.closeConn()
	if runtime.GOOS == "windows" {
		return h.Kill() // no SIGINT for child processes on Windows
	}
	_ = h.cmd.Process.Signal(os.Interrupt)
	select {
	case <-h.done:
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
		b, err := os.ReadFile(f)
		_ = os.Remove(f)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || !isWorkerProcess(pid) {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			log.Warn("killing orphan worker", "pid", pid, "pid_file", f)
			_ = proc.Kill()
		}
	}
}

func isWorkerProcess(pid int) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	cmd := string(out)
	return strings.Contains(cmd, " worker ") && strings.Contains(cmd, "--instance")
}
