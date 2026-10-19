// Command mcpb builds LiteZap.mcpb, the Claude Desktop extension (MCP Bundle): a zip
// with manifest.json, icon.png and the litezap-mcpb shim for each platform given. Used by
// cmd/litezap-app/package-macos.sh and the release workflow.
//
//	go run ./scripts/mcpb -version v0.1.0 -darwin dist/litezap-mcpb -windows dist/litezap-mcpb.exe -out dist/LiteZap.mcpb
//	go run ./scripts/mcpb -manifest   # print the manifest only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/douglasdf99/litezap/internal/desktop"
	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/mcpb"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	version := flag.String("version", "dev", "app version (v0.1.0)")
	out := flag.String("out", mcpb.FileName, "output .mcpb")
	darwin := flag.String("darwin", "", "litezap-mcpb for macOS (universal)")
	windows := flag.String("windows", "", "litezap-mcpb.exe for Windows")
	onlyManifest := flag.Bool("manifest", false, "print manifest.json and exit")
	flag.Parse()

	tools, err := listTools()
	if err != nil {
		fail(err)
	}
	var platforms []string
	var files []mcpb.File
	add := func(src, platform, name string) {
		if src == "" {
			return
		}
		b, err := os.ReadFile(src)
		if err != nil {
			fail(err)
		}
		platforms = append(platforms, platform)
		files = append(files, mcpb.File{Name: name, Data: b, Exec: true})
	}
	add(*darwin, "darwin", mcpb.ShimPath)
	add(*windows, "win32", mcpb.ShimPathExe)
	if len(platforms) == 0 && !*onlyManifest {
		fail(fmt.Errorf("give -darwin and/or -windows"))
	}
	m := mcpb.NewManifest(*version, platforms, tools)
	if *darwin == "" && *windows != "" {
		m.Server.EntryPoint = mcpb.ShimPathExe // entry_point must be a file of the bundle
	}
	if *onlyManifest {
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			fail(err)
		}
		fmt.Println(string(b))
		return
	}
	files = append(files, mcpb.File{Name: mcpb.IconPath, Data: desktop.IconPNG(512)})
	f, err := os.Create(*out)
	if err != nil {
		fail(err)
	}
	if err := mcpb.Pack(f, m, files); err != nil {
		f.Close()
		fail(err)
	}
	if err := f.Close(); err != nil {
		fail(err)
	}
	fmt.Printf("built %s (%s, %d tools)\n", *out, strings.Join(platforms, ", "), len(tools))
}

// listTools asks the LiteZap MCP server for its tools. The REST backend is the one
// `litezap-app mcp` uses (so every tool group is registered); it is never called.
func listTools() ([]mcpb.Tool, error) {
	ctx := context.Background()
	b, err := mcp.NewREST("http://127.0.0.1:1", "unused", nil)
	if err != nil {
		return nil, err
	}
	st, ct := sdk.NewInMemoryTransports()
	srv := mcp.NewServer(b, mcp.Options{})
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "mcpb", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	var tools []mcpb.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, mcpb.Tool{Name: t.Name, Description: firstSentence(t.Description)})
	}
	return tools, nil
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	return s
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mcpb:", err)
	os.Exit(1)
}
