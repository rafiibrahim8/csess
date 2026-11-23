// Package cli implements the csess commands.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rafiibrahim8/csess/internal/claude"
)

// app is what every command works with.
type app struct {
	load   func() (claude.Config, error) // called once, on first use
	cfg    claude.Config
	out    io.Writer
	errOut io.Writer
	outC   painter // colors for out
	errC   painter // colors for errOut
}

// config loads Claude's config on first use, so --version, help and
// completion work even where it can't be found (e.g. HOME unset).
func (a *app) config() error {
	if a.cfg.Projects != "" {
		return nil
	}
	cfg, err := a.load()
	a.cfg = cfg
	return err
}

func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.errOut, "%s "+format+"\n", append([]any{a.errC.paint("csess:", yellow, bold)}, args...)...)
}

// Main runs csess with the given arguments' source (os.Args) and returns
// the exit code. load supplies Claude's config.
func Main(load func() (claude.Config, error), version string) int {
	a, root := newApp(load, version)
	if err := root.Execute(); err != nil {
		if a.errOut == nil { // failed before flags were applied: find --color ourselves
			a.errOut = os.Stderr
			a.errC, _ = colorFor(os.Stderr, colorArg(os.Args[1:]))
		}
		fmt.Fprintf(a.errOut, "%s %v\n", a.errC.paint("csess:", red, bold), err)
		return 1
	}
	return 0
}

// NewRootCmd builds the csess command tree. load supplies Claude's config.
func NewRootCmd(load func() (claude.Config, error), version string) *cobra.Command {
	_, root := newApp(load, version)
	return root
}

func newApp(load func() (claude.Config, error), version string) (*app, *cobra.Command) {
	a := &app{load: load}
	var colorMode string
	root := &cobra.Command{
		Use:     "csess",
		Short:   "List, fork and move Claude Code sessions between directories",
		Version: version,
		Long: `List, fork and move Claude Code sessions between directories.
Claude ties each session to the dir it started in; csess keeps them following
the dir when you rename it. See csess(1) for details.`,
		SilenceErrors: true, // Main prints errors with a "csess:" prefix
		SilenceUsage:  true, // runtime errors shouldn't dump the usage text
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.out, a.errOut = cmd.OutOrStdout(), cmd.ErrOrStderr()
			var err error
			if a.outC, err = colorFor(a.out, colorMode); err != nil {
				return err
			}
			a.errC, _ = colorFor(a.errOut, colorMode)
			return nil
		},
	}
	// 'completion <shell>' still works (packages use it) but isn't listed in
	// help or offered by tab completion.
	root.CompletionOptions.HiddenDefaultCmd = true
	root.PersistentFlags().StringVar(&colorMode, "color", "auto", "color output: auto (when a terminal), on, off")
	root.RegisterFlagCompletionFunc("color", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"auto", "on", "off"}, cobra.ShellCompDirectiveNoFileComp
	})
	root.AddCommand(a.lsCmd(), a.forkCmd(), a.mvCmd())
	return a, root
}

// completeDirs makes positional args complete as directories.
func completeDirs(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// colorArg finds the --color value in raw arguments (last one wins), for
// errors raised before cobra has parsed the flags.
func colorArg(args []string) string {
	mode := "auto"
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "--color="); ok {
			mode = v
		} else if a == "--color" && i+1 < len(args) {
			mode = args[i+1]
		}
	}
	return mode
}
