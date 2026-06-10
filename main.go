// claudectx is a kubectx-style switcher for paired Claude Code + Codex CLI
// contexts: settings, tokens, skills, instructions, and MCP servers.
package main

import (
	"os"

	"github.com/tlrmchlsmth/claudectx/internal/cli"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
)

// version is overridable at build time: -ldflags "-X main.version=v1.2.3".
var version = "0.1.0"

func main() {
	app := cli.NewApp(paths.FromEnv(), version)
	os.Exit(app.Run(os.Args[1:]))
}
