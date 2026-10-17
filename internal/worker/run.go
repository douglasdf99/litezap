package worker

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/grpc"
	_ "modernc.org/sqlite"
)

type Options struct {
	InstanceID string
	Socket     string
	DataDir    string
	// Parent, when non-nil, is a parent-liveness pipe (the supervisor's end is held open
	// for as long as the supervisor lives). Reaching EOF means the supervisor is gone,
	// and the worker shuts down. The worker command passes os.Stdin.
	Parent io.Reader
}

// Run is the body of `litezap worker`. It blocks until ctx is cancelled or serving fails.
func Run(ctx context.Context, opt Options, log *slog.Logger) error {
	if opt.InstanceID == "" || opt.Socket == "" || opt.DataDir == "" {
		return fmt.Errorf("worker: --instance, --socket and --data-dir are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opt.Parent != nil {
		go func() {
			_, _ = io.Copy(io.Discard, opt.Parent)
			cancel()
		}()
	}
	dir := filepath.Join(opt.DataDir, "instances", opt.InstanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// whatsmeow's sqlstore requires foreign keys; modernc registers the "sqlite" driver,
	// so we open the DB ourselves and tell whatsmeow to use the "sqlite3" dialect.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "session.db")+
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open session db: %w", err)
	}
	defer db.Close()
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Noop)
	if err := container.Upgrade(ctx); err != nil {
		return fmt.Errorf("upgrade session db: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}
	cli := whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false))
	srv := NewServer(NewWhatsmeow(cli, log))

	_ = os.Remove(opt.Socket)
	lis, err := net.Listen("unix", opt.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opt.Socket, err)
	}
	g := grpc.NewServer()
	workerv1.RegisterWorkerServer(g, srv)

	stopped := make(chan struct{})
	// Runs before db.Close: cancel so the shutdown goroutine always finishes, then wait for it.
	defer func() { cancel(); <-stopped }()
	go func() {
		defer close(stopped)
		<-ctx.Done()
		srv.Stop()
		// Events streams never finish on their own; give unary calls a moment, then force.
		done := make(chan struct{})
		go func() { g.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			g.Stop()
		}
	}()

	if err := srv.Start(ctx); err != nil {
		g.Stop()
		return fmt.Errorf("start whatsapp client: %w", err)
	}
	log.Info("worker serving", "socket", opt.Socket)
	if err := g.Serve(lis); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
