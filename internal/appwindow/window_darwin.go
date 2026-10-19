//go:build darwin && cgo

package appwindow

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "window_darwin.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// Supported reports whether this build has the native window.
const Supported = true

func init() {
	// Cocoa must run on the main thread: keep the main goroutine on it.
	runtime.LockOSThread()
}

var opts Options

// Run starts the Cocoa application and blocks until it terminates (the process exits
// from inside AppKit; Run never returns normally). Call it from the main goroutine.
func Run(o Options) {
	opts = o
	title := C.CString(o.Title)
	loading := C.CString(o.Loading)
	defer C.free(unsafe.Pointer(title))
	defer C.free(unsafe.Pointer(loading))
	var icon unsafe.Pointer
	if len(o.IconPNG) > 0 {
		icon = C.CBytes(o.IconPNG)
		defer C.free(icon)
	}
	hidden := C.int(0)
	if o.StartHidden {
		hidden = 1
	}
	C.lzw_run(title, C.int(o.Width), C.int(o.Height), loading, hidden, icon, C.int(len(o.IconPNG)))
}

// Load shows url in the window; links outside origin (scheme://host:port) open in the
// default browser.
func Load(url, origin string) {
	u, o := C.CString(url), C.CString(origin)
	defer C.free(unsafe.Pointer(u))
	defer C.free(unsafe.Pointer(o))
	C.lzw_load(u, o)
}

// Show brings the window to the front (reopening it if it was closed).
func Show() { C.lzw_show() }

// Eval runs JavaScript in the dashboard page.
func Eval(js string) {
	s := C.CString(js)
	defer C.free(unsafe.Pointer(s))
	C.lzw_eval(s)
}

// SetStatus replaces the page with a message (isError adds a "Ver log" button), e.g.
// while the engine starts or when it could not.
func SetStatus(text string, isError bool) {
	s := C.CString(text)
	defer C.free(unsafe.Pointer(s))
	e := C.int(0)
	if isError {
		e = 1
	}
	C.lzw_status(s, e)
}

// Terminate asks the app to quit, as Cmd+Q does: OnQuit runs and the app waits for Finish.
func Terminate() { C.lzw_terminate() }

// Finish lets the app exit: it answers a pending quit, or quits right away.
func Finish() { C.lzw_finish() }

//export lzwReady
func lzwReady() {
	if opts.OnReady != nil {
		opts.OnReady()
	}
}

//export lzwQuit
func lzwQuit() {
	if opts.OnQuit != nil {
		opts.OnQuit()
	}
}

//export lzwMenu
func lzwMenu(id C.int) {
	if opts.OnMenu != nil {
		opts.OnMenu(int(id))
	}
}

//export lzwMessage
func lzwMessage(text *C.char) {
	if opts.OnMessage != nil {
		opts.OnMessage(C.GoString(text))
	}
}
