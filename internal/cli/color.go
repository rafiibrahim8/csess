package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// A small, conventional palette: bold for headings, dim for secondary text,
// cyan for session ids, green for success and the 'claude -c' marker,
// yellow for warnings and red for errors.
type style string

const (
	bold   style = "1"
	dim    style = "2"
	red    style = "31"
	green  style = "32"
	yellow style = "33"
	cyan   style = "36"
)

// painter adds ANSI colors to text when enabled.
type painter bool

func (p painter) paint(s string, styles ...style) string {
	if !p || s == "" {
		return s
	}
	codes := make([]string, len(styles))
	for i, st := range styles {
		codes[i] = string(st)
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + s + "\x1b[0m"
}

// colorFor decides whether output to w is colored: "on" and "off" force it;
// "auto" colors a terminal, unless NO_COLOR is set or TERM is dumb.
func colorFor(w io.Writer, mode string) (painter, error) {
	switch mode {
	case "on":
		return true, nil
	case "off":
		return false, nil
	case "auto":
	default:
		return false, fmt.Errorf("--color must be auto, on or off, not %q", mode)
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false, nil
	}
	f, ok := w.(*os.File)
	return painter(ok && term.IsTerminal(int(f.Fd()))), nil
}
