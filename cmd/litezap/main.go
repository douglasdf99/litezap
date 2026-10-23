package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // IANA zones for settings.timezone even where the OS has none (Windows, slim images)

	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/serve"
	"github.com/douglasdf99/litezap/internal/worker"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func usage() {
	fmt.Fprintln(os.Stderr, "usage: litezap <serve|mcp|worker|version> [flags]")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := newLogger(os.Stderr, os.Getenv("LITEZAP_LOG_FORMAT"), os.Args[1] == "worker")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default signal handling: a second Ctrl+C terminates immediately
	}()

	var err error
	switch os.Args[1] {
	case "serve":
		mcp.Version = version
		serve.Version = version
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		cfg := fs.String("config", "litezap.yaml", "config file (optional)")
		fs.Parse(os.Args[2:])
		err = serve.Run(ctx, *cfg, log)
	case "mcp":
		// stdio MCP bridge to a running `litezap serve` (spec 3); stdout is the protocol
		// channel, so logs go to stderr only. Never starts a supervisor.
		fs := flag.NewFlagSet("mcp", flag.ExitOnError)
		fs.Usage = func() {
			fmt.Fprintln(os.Stderr, "usage: litezap mcp\n\nServes the LiteZap MCP tools over stdio, talking to a running `litezap serve`.\n"+
				"Environment: LITEZAP_URL (default http://127.0.0.1:8787), LITEZAP_TOKEN (admin or instance token, required).")
		}
		fs.Parse(os.Args[2:])
		base := os.Getenv("LITEZAP_URL")
		if base == "" {
			base = "http://127.0.0.1:8787"
		}
		err = mcp.RunStdio(ctx, base, os.Getenv("LITEZAP_TOKEN"), version, log)
	case "worker":
		fs := flag.NewFlagSet("worker", flag.ExitOnError)
		id := fs.String("instance", "", "instance id")
		sock := fs.String("socket", "", "unix socket path")
		dataDir := fs.String("data-dir", "", "data directory")
		fs.Parse(os.Args[2:])
		err = worker.Run(ctx, worker.Options{
			InstanceID: *id, Socket: *sock, DataDir: *dataDir,
			// Set by the supervisor (env, not a flag: flags are visible to every user in ps).
			ProxyURL:      os.Getenv("LITEZAP_WORKER_PROXY_URL"),
			Parent:        os.Stdin, // parent-liveness pipe: exit when the supervisor goes away
			ZombieTimeout: envDuration("LITEZAP_WORKER_ZOMBIE_TIMEOUT"),
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
