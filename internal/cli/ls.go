package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/rafiibrahim8/csess/internal/claude"
	"github.com/rafiibrahim8/csess/internal/fsutil"
)

func (a *app) lsCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ls [<dir>]",
		Short: "List sessions of a dir, newest first ('*' = what 'claude -c' resumes)",
		Example: `  csess ls ~/projects/api
  csess ls --all | grep MISSING`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeDirs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.config(); err != nil {
				return err
			}
			if all {
				if len(args) > 0 {
					return errors.New("--all lists every directory; it takes no <dir>")
				}
				return a.lsAll()
			}
			dir := "."
			if len(args) == 1 {
				if dir = args[0]; dir == "" {
					return errors.New("empty directory argument")
				}
			}
			return a.lsDir(dir)
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "list every dir that has sessions")
	return cmd
}

const timeFmt = "2006-01-02 15:04"

func (a *app) lsDir(target string) error {
	dir, err := fsutil.AbsPath(target)
	if err != nil {
		return err
	}
	folder, err := a.cfg.FolderFor(dir)
	ss := claude.SessionsIn(folder)
	if err != nil || len(ss) == 0 {
		fmt.Fprintf(a.out, "%s %s\n", a.outC.paint("no sessions for", dim), display(dir))
		return nil
	}

	width := 1 << 30 // piped: never cut titles
	if f, ok := a.out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		cols, _, err := term.GetSize(int(f.Fd()))
		if err != nil || cols == 0 {
			cols = 120
		}
		width = max(cols-60, 20)
	}
	c := a.outC
	fmt.Fprintf(a.out, "%s  %s\n", c.paint(display(dir), bold), c.paint("(* = what `claude -c` resumes)", dim))
	fmt.Fprintf(a.out, "  %s\n", c.paint(fmt.Sprintf("%-16s  %-36s  %s", "MODIFIED", "SESSION", "TITLE"), bold))
	for i, s := range ss {
		info := claude.Scan(s.Path, true)
		prefix := ""
		if info.Cwd != "" && info.Cwd != claude.NodePath(dir) {
			// shares the folder with dir because their names encode the same
			prefix = "[from " + display(info.Cwd) + "] "
		}
		title, empty := info.Title, info.Title == ""
		if empty {
			title = "(empty)"
		}
		// cut to width first, then color, so escape codes don't count
		t := truncate(prefix+title, width)
		if len(t) <= len(prefix) {
			t = c.paint(t, dim)
		} else if rest := t[len(prefix):]; empty {
			t = c.paint(prefix, dim) + c.paint(rest, dim)
		} else {
			t = c.paint(prefix, dim) + rest
		}
		mark := " "
		if i == 0 {
			mark = c.paint("*", green, bold)
		}
		fmt.Fprintf(a.out, "%s %s  %s  %s\n", mark, c.paint(fmt.Sprintf("%-16s", s.ModTime.Format(timeFmt)), dim),
			c.paint(fmt.Sprintf("%-36s", s.ID), cyan), t)
	}
	return nil
}

// lsAll prints one row per directory. Sessions are grouped by their recorded
// cwd, not by folder, since dirs whose names encode the same share a folder.
func (a *app) lsAll() error {
	type row struct {
		dir    string // cwd, or the folder name when sessions record none
		known  bool   // dir is a recorded path we can check for MISSING
		folder string // its projects folder name
		n      int
		last   time.Time
	}
	var rows []*row
	entries, _ := os.ReadDir(a.cfg.Projects)
	for _, e := range entries {
		folder := filepath.Join(a.cfg.Projects, e.Name())
		if !fsutil.IsDir(folder) {
			continue
		}
		ss := claude.SessionsIn(folder)
		if len(ss) == 0 {
			if fsutil.IsDir(filepath.Join(folder, "memory")) {
				rows = append(rows, &row{dir: e.Name()})
			}
			continue
		}
		byDir := map[string]*row{}
		for _, s := range ss { // newest first, so a row's first session sets last
			cwd := claude.CwdOf(s.Path)
			key := cwd
			if key == "" {
				key = e.Name()
			}
			r := byDir[key]
			if r == nil {
				r = &row{dir: key, known: cwd != "", folder: e.Name(), last: s.ModTime}
				byDir[key] = r
				rows = append(rows, r)
			}
			r.n++
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].last.After(rows[j].last) })
	c := a.outC
	fmt.Fprintln(a.out, c.paint(fmt.Sprintf("%-16s  %4s  %s", "LAST ACTIVE", "N", "DIRECTORY"), bold))
	for _, r := range rows {
		when, note := "-", ""
		switch {
		case r.n == 0:
			note = "  " + c.paint("(memory only)", dim)
		case r.known && !dirExists(r.dir, r.folder):
			note = "  " + c.paint("MISSING", red, bold)
		}
		if r.n > 0 {
			when = r.last.Format(timeFmt)
		}
		fmt.Fprintf(a.out, "%s  %4d  %s%s\n", c.paint(fmt.Sprintf("%-16s", when), dim), r.n, display(r.dir), note)
	}
	return nil
}

// dirExists checks a cwd as Claude recorded it. A name with invalid UTF-8 is
// recorded with U+FFFD, so look for a real dir that Claude would record so.
func dirExists(cwd, folder string) bool {
	if fsutil.IsDir(cwd) {
		return true
	}
	if !strings.ContainsRune(cwd, utf8.RuneError) {
		return false
	}
	for _, c := range claude.ResolveAll("/", folder) {
		if claude.NodePath(c) == cwd {
			return true
		}
	}
	return false
}
