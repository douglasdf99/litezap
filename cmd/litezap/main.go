package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // IANA zones for settings.timezone even where the OS has none (Windows, slim images)

	"github.com/douglasdf99/litezap/internal/serve"
	"github.com/douglasdf99/litezap/internal/worker"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func usage() {
	fmt.Fprintln(os.Stderr, "usage: litezap <serve|worker|version> [flags]")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default signal handling: a second Ctrl+C terminates immediately
	}()

	var err error
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		cfg := fs.String("config", "litezap.yaml", "config file (optional)")
		fs.Parse(os.Args[2:])
		err = serve.Run(ctx, *cfg, log)
	case "worker":
		fs := flag.NewFlagSet("worker", flag.ExitOnError)
		id := fs.String("instance", "", "instance id")
		sock := fs.String("socket", "", "unix socket path")
		dataDir := fs.String("data-dir", "", "data directory")
		fs.Parse(os.Args[2:])
		err = worker.Run(ctx, worker.Options{
			InstanceID: *id, Socket: *sock, DataDir: *dataDir,
			// Set by the supervisor (env, not a flag: flags are visible to every user in ps).
			ProxyURL: os.Getenv("LITEZAP_WORKER_PROXY_URL"),
			Parent:   os.Stdin, // parent-liveness pipe: exit when the supervisor goes away
		}, log.With("instance_id", *id))
	case "version":
		fmt.Println(version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}
