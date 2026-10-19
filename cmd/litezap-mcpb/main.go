// Command litezap-mcpb is the server binary of the LiteZap Claude Desktop extension
// (LiteZap.mcpb). It carries no MCP tools of its own: it finds the installed LiteZap app
// (internal/mcpb.Locator) and runs `<app> mcp` on the same stdin/stdout, so the tools
// always match the installed app and no token is copied anywhere.
//
// It uses only the standard library (no CGO) so it stays small and cross-compiles.
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/douglasdf99/litezap/internal/mcpb"
)

func main() {
	exe, err := mcpb.NewLocator(runtime.GOOS).Find()
	if err != nil {
		// stdout is the MCP channel: the reason goes to stderr (Claude shows it in the log).
		fmt.Fprintln(os.Stderr, "litezap-mcpb:", err)
		os.Exit(1)
	}
	if err := run(exe, []string{"mcp"}); err != nil {
		fmt.Fprintln(os.Stderr, "litezap-mcpb: "+exe+":", err)
		os.Exit(1)
	}
}
