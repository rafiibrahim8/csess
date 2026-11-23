package claude

import (
	"fmt"
	"os"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/rafiibrahim8/csess/internal/fsutil"
)

// RunningIn lists this user's running claude processes whose working dir is
// dir or below it, as "pid N in <cwd>".
func RunningIn(dir string) ([]string, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	var busy []string
	for _, p := range procs {
		if !isClaude(p) {
			continue
		}
		if uids, err := p.Uids(); err != nil || len(uids) == 0 || uint32(uids[0]) != uid {
			continue
		}
		if cwd, err := p.Cwd(); err == nil && fsutil.Under(cwd, dir) {
			busy = append(busy, fmt.Sprintf("pid %d in %s", p.Pid, cwd))
		}
	}
	return busy, nil
}

// isClaude: the native install runs as "claude"; an npm install as node
// running @anthropic-ai/claude-code.
func isClaude(p *process.Process) bool {
	name, err := p.Name()
	if err != nil {
		return false
	}
	if name == "claude" {
		return true
	}
	if name != "node" {
		return false
	}
	cmd, err := p.Cmdline()
	return err == nil && strings.Contains(cmd, "claude-code")
}
