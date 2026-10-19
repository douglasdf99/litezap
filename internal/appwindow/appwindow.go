// Package appwindow is the native window of the LiteZap desktop app on macOS: a Cocoa
// application (Dock icon, app menu, Dock menu) with one WKWebView window that shows the
// dashboard served by the engine running in the same process.
//
// It is a small Objective-C shim (window_darwin.m) instead of a webview library because
// the app needs things those libraries do not expose: hide-on-close with reopen from the
// Dock, a deferred quit that waits for the engine to stop, JavaScript confirm/prompt
// dialogs, file pickers, downloads, and sending other sites to the default browser. It
// also leaves the NSApplication delegate free for the tray (fyne.io/systray, external
// loop mode) to share the main thread.
//
// On other platforms, and without CGO, Supported is false and every function is a no-op:
// the app falls back to the tray plus the browser.
package appwindow

// Menu items the app handles in Go (Options.OnMenu).
const (
	MenuReload     = 1 // Visualizar > Recarregar painel: a fresh one-click login
	MenuBrowser    = 2 // Visualizar > Abrir no navegador
	MenuDataFolder = 3 // Ajuda > Abrir pasta de dados
	MenuLog        = 4 // Ajuda > Ver log
	MenuAI         = 5 // Dock > Conectar a IA
)

// Options configure Run. The callbacks run on the main thread: they must not block.
type Options struct {
	Title         string
	Width, Height int
	// Loading is the text under the spinner until Load is called.
	Loading string
	// StartHidden keeps the window closed at launch (the Dock icon reopens it).
	StartHidden bool
	// IconPNG becomes the Dock icon when the binary runs outside an .app bundle.
	IconPNG []byte
	// OnReady runs once the application finished launching.
	OnReady func()
	// OnQuit runs when the user (Cmd+Q, Dock > Sair) or the system asks the app to quit.
	// The app stays up until Finish is called.
	OnQuit func()
	// OnMenu runs for the Menu* items.
	OnMenu func(id int)
	// OnMessage runs for window.webkit.messageHandlers.litezap.postMessage(text).
	OnMessage func(text string)
}
