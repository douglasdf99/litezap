//go:build !darwin || !cgo

package appwindow

// Supported reports whether this build has the native window.
const Supported = false

// Run is only called when Supported.
func Run(Options) { panic("appwindow: not supported on this platform") }

func Load(url, origin string)       {}
func Show()                         {}
func Eval(js string)                {}
func SetStatus(text string, e bool) {}
func Terminate()                    {}
func Finish()                       {}
