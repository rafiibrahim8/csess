// csess lists, forks and moves Claude Code sessions between directories.
// See docs/csess.1 or 'man csess'.
package main

import (
	"os"
	"runtime/debug"

	"github.com/rafiibrahim8/csess/internal/claude"
	"github.com/rafiibrahim8/csess/internal/cli"
)

// version is set by release builds: -ldflags "-X main.version=1.2.3".
var version = ""

func main() {
	os.Exit(cli.Main(claude.LoadConfig, buildVersion()))
}

// buildVersion falls back to the module version for 'go install ...@v1.2.3'.
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
