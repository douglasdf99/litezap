// Command litezap-app is the LiteZap tray launcher for non-technical users: double-click
// it and it runs the engine in-process (data in the per-user app folder), shows a tray
// icon with the status, and opens the dashboard in the browser with one-click login.
//
// It lives apart from cmd/litezap because the tray library needs CGO on macOS; the main
// `litezap` binary stays CGO-free. The binary also answers `worker`, since the engine
// re-executes its own executable for each instance, and `mcp`: a stdio MCP server that
// talks to the engine of the running tray app with the admin token of the app folder, so
// "Conectar a IA" can point Claude/Cursor at this executable without copying any token.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones for settings.timezone even where the OS has none

	"fyne.io/systray"
	"github.com/douglasdf99/litezap/internal/dashboard"
	"github.com/douglasdf99/litezap/internal/desktop"
	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/serve"
	"github.com/douglasdf99/litezap/internal/worker"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

const maxLogBytes = 10 << 20

func main() {
	if len(os.Args) > 1 && os.Args[1] == "worker" {
		os.Exit(runWorker(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(runMCP(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	fs := flag.NewFlagSet("litezap-app", flag.ExitOnError)
	noBrowser := fs.Bool("no-browser", false, "do not open the dashboard on start")
	fs.Parse(os.Args[1:])

	dir, err := desktop.AppDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "litezap-app: app folder:", err)
		os.Exit(1)
	}
	logPath := filepath.Join(dir, "litezap.log")
	logFile, err := openLog(logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "litezap-app: log:", err)
		os.Exit(1)
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, logFile), nil))
	log.Info("litezap-app starting", "version", version, "app_dir", dir)

	a := &app{dir: dir, logPath: logPath, log: log, openOnStart: !*noBrowser,
		codes: dashboard.NewLaunchCodes(), stopped: make(chan struct{}), infoc: make(chan serve.Info, 1),
		aiChanged: make(chan struct{}, 1)}
	if conn, err := newConnector(dir); err != nil {
		log.Warn("Conectar a IA disabled", "err", err)
	} else {
		a.ai = conn
	}
	// systray.Run must own the main thread (Cocoa on macOS).
	systray.Run(a.onReady, a.onExit)
}

// openLog appends to the log, starting over (keeping one old copy) past maxLogBytes.
func openLog(p string) (*os.File, error) {
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxLogBytes {
		_ = os.Rename(p, p+".1")
	}
	return os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// newConnector points AI client entries at this executable; when the app folder was
// moved with LITEZAP_APP_DIR, the entry carries it so `mcp` finds the same folder.
func newConnector(dir string) (*desktop.Connector, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if os.Getenv("LITEZAP_APP_DIR") != "" {
		env = map[string]string{"LITEZAP_APP_DIR": dir}
	}
	return desktop.NewConnector(exe, env)
}

// runMCP serves the LiteZap MCP tools over stdio for an AI client (Claude Desktop, Claude
// Code, Cursor). It talks to the engine of the running tray app; it never starts one: a
// second engine on the same folder would fight the tray for the port and the sessions.
// While the app is closed the tools answer "abra o LiteZap".
func runMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: litezap-app mcp\n\nServes the LiteZap MCP tools over stdio, talking to the running LiteZap app\n"+
			"(admin token read from the app folder; LITEZAP_APP_DIR selects another folder).")
	}
	fs.Parse(args)
	// stdout is the protocol channel: logs go to stderr only.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := desktop.AppDir()
	if err != nil {
		log.Error("app folder", "err", err)
		return 1
	}
	eng, err := desktop.LocalEngine(dir)
	if err != nil {
		log.Error("litezap-app mcp: "+err.Error(), "app_dir", dir)
		return 1
	}
	if !eng.Running(ctx, 2*time.Second) {
		log.Warn(desktop.NotRunningMessage, "url", eng.URL)
	}
	b, err := mcp.NewREST(eng.URL, eng.Token, nil)
	if err != nil {
		log.Error("litezap-app mcp", "err", err)
		return 1
	}
	b.SetUnreachableMessage(desktop.NotRunningMessage)
	mcp.Version = version
	if err := mcp.ServeStdio(ctx, b, version, log); err != nil {
		log.Error("fatal", "err", err)
		return 1
	}
	return 0
}

func runWorker(args []string) int {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	id := fs.String("instance", "", "instance id")
	sock := fs.String("socket", "", "unix socket path")
	dataDir := fs.String("data-dir", "", "data directory")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := worker.Run(ctx, worker.Options{
		InstanceID: *id, Socket: *sock, DataDir: *dataDir,
		ProxyURL: os.Getenv("LITEZAP_WORKER_PROXY_URL"),
		Parent:   os.Stdin,
	}, log.With("instance_id", *id))
	if err != nil {
		log.Error("fatal", "err", err)
		return 1
	}
	return 0
}

type app struct {
	dir, logPath string
	log          *slog.Logger
	openOnStart  bool
	codes        *dashboard.LaunchCodes
	stopped      chan struct{} // closed when the engine returned; err holds its error
	err          error
	infoc        chan serve.Info
	cancel       context.CancelFunc
	info         serve.Info
	ai           *desktop.Connector // nil: "Conectar a IA" unavailable
	aiChanged    chan struct{}      // a connection changed (dashboard): refresh the menu
}

func (a *app) onReady() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(desktop.IconICO(64))
	} else {
		systray.SetIcon(desktop.IconPNG(64))
	}
	systray.SetTooltip("LiteZap")
	status := systray.AddMenuItem("LiteZap: iniciando…", "")
	status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem("Abrir painel", "Abre o painel no navegador")
	open.Disable()
	folder := systray.AddMenuItem("Abrir pasta de dados", a.dir)
	logs := systray.AddMenuItem("Ver log", a.logPath)
	if a.ai != nil {
		systray.AddSeparator()
		go a.aiMenu(systray.AddMenuItem("Conectar a IA", "Usa o WhatsApp pelo Claude ou pelo Cursor (MCP)"))
	}
	systray.AddSeparator()
	quit := systray.AddMenuItem("Sair", "Encerra o LiteZap e todas as instâncias")

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	base := desktop.BaseConfig(a.dir)
	var aiClients dashboard.AIConnector
	if a.ai != nil {
		aiClients = notifyingConnector{a.ai, a.aiChanged}
	}
	go func() {
		defer close(a.stopped)
		a.err = serve.RunWith(ctx, serve.Options{
			ConfigPath:  desktop.ConfigPath(a.dir),
			Base:        &base,
			LaunchCodes: a.codes,
			AIClients:   aiClients,
			OnListening: func(i serve.Info) { a.infoc <- i },
		}, a.log)
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		titles := make(chan string, 1)
		stopped := a.stopped
		quitting, running, failed, checking := false, false, false, false
		checkStatus := func() {
			if checking {
				return // the previous check (2s timeout) is still running
			}
			checking = true
			go func() { titles <- a.statusTitle() }()
		}
		beginQuit := func() {
			if failed {
				systray.Quit() // the engine is not running: nothing to stop
				return
			}
			if quitting {
				return
			}
			quitting = true
			status.SetTitle("LiteZap: encerrando…")
			open.Disable()
			quit.Disable()
			a.log.Info("quit requested")
			cancel()
		}
		for {
			select {
			case info := <-a.infoc:
				if quitting {
					continue
				}
				a.info = info
				running = true
				open.Enable()
				checkStatus()
				if a.openOnStart {
					a.openDashboard()
				}
			case title := <-titles:
				checking = false
				if running && !quitting {
					status.SetTitle(title)
					systray.SetTooltip(title + " · " + a.info.URL + dashboard.Prefix)
				}
			case <-open.ClickedCh:
				a.openDashboard()
			case <-folder.ClickedCh:
				a.openPath(a.dir)
			case <-logs.ClickedCh:
				a.openPath(a.logPath)
			case <-quit.ClickedCh:
				beginQuit()
			case <-sigs:
				beginQuit()
			case <-tick.C:
				if running && !quitting {
					checkStatus()
				}
			case <-stopped:
				stopped = nil // closed: stop selecting on it
				err := a.err
				if quitting || err == nil {
					a.log.Info("litezap-app stopped")
					systray.Quit()
					return
				}
				// The engine could not start (e.g. port in use): keep the tray up with the
				// reason; "Ver log" and "Abrir pasta" keep working and Sair just exits.
				a.log.Error("engine stopped", "err", err)
				running, failed = false, true
				open.Disable()
				status.SetTitle("Erro: " + shorten(err.Error(), 70))
				systray.SetTooltip("LiteZap: " + err.Error())
			}
		}
	}()
}

// onExit runs when the tray loop ends (Quit, or the OS ending the session): make sure the
// engine stopped gracefully before the process exits.
func (a *app) onExit() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	select {
	case <-a.stopped:
	case <-time.After(40 * time.Second):
		a.log.Warn("engine did not stop in time")
	}
}

func (a *app) openDashboard() {
	if a.info.URL == "" {
		return
	}
	if !a.info.Dashboard {
		a.log.Warn("dashboard disabled (disable_dashboard); opening the API base URL")
		a.openPath(a.info.URL + "/healthz")
		return
	}
	u := a.info.URL + dashboard.Prefix
	if a.info.OneClickLogin {
		// Single-use, 2-minute code in the fragment: never sent to the server or logs.
		u += "#code=" + a.codes.Mint()
	}
	a.openPath(u)
}

func (a *app) openPath(target string) {
	if err := desktop.Open(target); err != nil {
		a.log.Error("open failed", "err", err)
	}
}

var statusClient = &http.Client{Timeout: 2 * time.Second}

// statusTitle asks the local API how many instances are connected.
func (a *app) statusTitle() string {
	req, _ := http.NewRequest(http.MethodGet, a.info.URL+"/v1/instances", nil)
	req.Header.Set("Authorization", "Bearer "+a.info.AdminToken)
	resp, err := statusClient.Do(req)
	if err != nil {
		return "LiteZap: sem resposta"
	}
	defer resp.Body.Close()
	var body struct {
		Instances []struct {
			State string `json:"state"`
		} `json:"instances"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return "LiteZap: rodando"
	}
	connected := 0
	for _, in := range body.Instances {
		if in.State == "connected" {
			connected++
		}
	}
	return fmt.Sprintf("LiteZap: %d de %d conectada(s)", connected, len(body.Instances))
}

func shorten(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
