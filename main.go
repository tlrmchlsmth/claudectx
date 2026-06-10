// claudectx is a kubectx-style switcher for per-tool Claude Code and Codex
// CLI profiles: settings, tokens, skills, instructions, and MCP servers,
// each tool switched independently.
package main

import (
	"os"

	"github.com/tlrmchlsmth/claudectx/internal/cli"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
)

// version is overridable at build time: -ldflags "-X main.version=v1.2.3".
var version = "0.2.0"

func main() {
	app := cli.NewApp(paths.FromEnv(), version)
	os.Exit(app.Run(os.Args[1:]))
}
