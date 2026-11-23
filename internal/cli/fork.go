package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/rafiibrahim8/csess/internal/claude"
	"github.com/rafiibrahim8/csess/internal/fsutil"
)

func (a *app) forkCmd() *cobra.Command {
	var session string
	var launch, noMemory bool
	cmd := &cobra.Command{
		Use:   "fork [-l] [<srcdir> | -s <id>] <dir>",
		Short: "Copy a session (and its dir's memory) into <dir>, created if needed",
		Long: `Copy a session into <dir> under a new id. The source is the newest session
started in <srcdir> (default: the current dir), or the one given with -s.`,
		Example: `  csess fork ../experiment          # newest session here; then: cd ../experiment && claude -c
  csess fork ~/work/api ~/scratch/api-try
  csess fork -l -s 20cb319a ~/scratch/try2`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeDirs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("session") && session == "" {
				return errors.New("--session needs an id")
			}
			for _, a := range args {
				if a == "" {
					return errors.New("empty directory argument")
				}
			}
			from := "."
			if len(args) == 2 {
				if session != "" {
					return errors.New("give either a source dir or --session, not both")
				}
				from = args[0]
			}
			if err := a.config(); err != nil {
				return err
			}
			return a.fork(session, args[len(args)-1], forkOpts{launch: launch, memory: !noMemory, from: from})
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "session id or unique prefix, from any dir")
	cmd.Flags().BoolVarP(&launch, "launch", "l", false, "start claude on the copy (default: print the resume command)")
	cmd.Flags().BoolVar(&noMemory, "no-memory", false, "don't copy the source dir's memory/")
	cmd.RegisterFlagCompletionFunc("session", a.completeSessionIDs)
	return cmd
}

// completeSessionIDs offers every non-empty session, newest first, with its title.
func (a *app) completeSessionIDs(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	if a.config() != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, s := range a.cfg.AllSessions() {
		if t := claude.Scan(s.Path, true).Title; t != "" {
			out = append(out, s.ID+"\t"+truncate(t, 80))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

type forkOpts struct {
	launch bool   // exec claude --resume in the target dir
	memory bool   // copy the source dir's memory/
	from   string // without a session id: fork the newest session of this dir ("" = current dir)
}

var (
	interrupted    atomic.Bool // set by a signal during fork
	errInterrupted = errors.New("interrupted")
)

// fork copies a session under a new id into dir's projects folder, with its
// sidecar (subagents, tool results), scratchpad and, unless opts.memory is
// false, its dir's memory/. Everything is copied under temp names first and
// renamed into place at the end, so a failure leaves nothing behind.
func (a *app) fork(sessionID, target string, opts forkOpts) (err error) {
	src, err := a.findSource(sessionID, opts.from)
	if err != nil {
		return err
	}
	srcFolder := filepath.Dir(src.Path)
	oldCwd := claude.CwdOf(src.Path)

	dir, err := fsutil.AbsPath(target)
	if err != nil {
		return err
	}
	dstFolder, err := a.cfg.FolderFor(dir)
	if err != nil {
		return err
	}
	srcMem, dstMem := filepath.Join(srcFolder, "memory"), filepath.Join(dstFolder, "memory")
	mem := memNone
	if opts.memory && srcFolder != dstFolder {
		if mem, err = memoryToCopy(srcMem, dstMem, dir); err != nil {
			return err
		}
	}
	copyMem := mem == memCopy || mem == memCopyOverEmpty

	newID := uuid.NewString()                         // can never collide with sessions already in dstFolder
	srcSide := strings.TrimSuffix(src.Path, ".jsonl") // tool-result paths in the transcript point in here
	dstSide := filepath.Join(dstFolder, newID)
	srcScratch, tmpRoot := a.cfg.Scratchpad(srcFolder, src.ID)
	dstScratch := ""
	if srcScratch != "" {
		dstScratch = filepath.Join(tmpRoot, filepath.Base(dstFolder), newID, "scratchpad")
	}
	// Exact moves for the files that are copied; the cwd move for the rest
	// (it skips these, which may well lie below the cwd).
	moves := [][2]string{{srcSide, dstSide}}
	if srcScratch != "" {
		moves = append(moves, [2]string{srcScratch, dstScratch})
	}
	if mem != memNone { // the fork uses dstMem: copied, or identical already
		moves = append(moves, [2]string{srcMem, dstMem})
	}
	// Claude's own files never move with the cwd (it may be an ancestor of
	// them, e.g. cwd = $HOME); the ones copied get the exact moves above.
	skip := []string{srcMem, a.cfg.Dir}
	if real, err := filepath.EvalSymlinks(a.cfg.Dir); err == nil {
		skip = append(skip, real)
	}
	skip = append(skip, a.cfg.TempRoots...)
	for _, m := range moves {
		skip = append(skip, m[0])
	}
	movePaths := func(b []byte) []byte {
		if oldCwd != "" {
			b = claude.MoveDir(b, oldCwd, claude.NodePath(dir), skip...)
		}
		for _, m := range moves {
			b = claude.ReplacePath(b, m[0], m[1], claude.PathEnd)
		}
		return b
	}
	transcript := func(b []byte) []byte {
		b = movePaths(b)
		return claude.ReplaceBounded(b, `"sessionId":"`+src.ID+`"`, `"sessionId":"`+newID+`"`, "")
	}

	// Ctrl-C stops the copy at the next file and cleans up, instead of
	// leaving temp copies behind. A closed stdout just makes writes fail.
	interrupted.Store(false)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGPIPE)
	defer signal.Stop(sigs)
	go func() {
		for sig := range sigs {
			if sig != syscall.SIGPIPE {
				interrupted.Store(true)
			}
		}
	}()

	// Until the transcript is in place, a failure removes everything created.
	var cleanup []string
	committed := false
	defer func() {
		if err != nil && !committed {
			for i := len(cleanup) - 1; i >= 0; i-- {
				os.RemoveAll(cleanup[i])
			}
		}
	}()
	mkdirTracked := func(p string, mode os.FileMode) error {
		var created []string
		for q := p; !fsutil.Exists(q); q = filepath.Dir(q) {
			created = append(created, q)
		}
		if err := os.MkdirAll(p, mode); err != nil {
			return err
		}
		for i := len(created) - 1; i >= 0; i-- {
			cleanup = append(cleanup, created[i])
		}
		return nil
	}
	if err := mkdirTracked(dstFolder, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dstFolder, ".csess-fork-"+newID)
	cleanup = append(cleanup, tmp, tmp+".jsonl", tmp+"-memory")
	if fsutil.IsDir(srcSide) {
		if err := copyTree(srcSide, tmp, copyOpts{jsonl: transcript, link: movePaths}); err != nil {
			return err
		}
	}
	if copyMem {
		// dereference: a memory file symlinked from dotfiles must not be shared
		if err := copyTree(srcMem, tmp+"-memory", copyOpts{deref: true}); err != nil {
			return err
		}
	}
	if err := copyFile(src.Path, tmp+".jsonl", transcript); err != nil {
		return err
	}
	if dstScratch != "" {
		if err := mkdirTracked(filepath.Dir(dstScratch), 0o700); err != nil {
			return err
		}
		if err := copyTree(srcScratch, dstScratch, copyOpts{link: movePaths}); err != nil {
			return err
		}
	}
	if err := mkdirTracked(dir, 0o755); err != nil {
		return err
	}

	// Into place; the transcript last, since its presence is what makes the session.
	if fsutil.IsDir(tmp) {
		if err := os.Rename(tmp, dstSide); err != nil {
			return err
		}
		cleanup = append(cleanup, dstSide)
	}
	if copyMem {
		if mem == memCopyOverEmpty {
			if err := removeEmptyTree(dstMem); err != nil {
				return err
			}
		}
		if err := os.Rename(tmp+"-memory", dstMem); err != nil {
			return err
		}
		cleanup = append(cleanup, dstMem)
	}
	if interrupted.Load() {
		return errInterrupted
	}
	if err := os.Rename(tmp+".jsonl", dstSide+".jsonl"); err != nil {
		return err
	}
	committed = true  // from here on the fork exists; errors below must not undo it
	signal.Stop(sigs) // claude, exec'd below, gets default signal handling

	if oldCwd == "" {
		oldCwd = "?"
	}
	c := a.outC
	fmt.Fprintf(a.out, "%s %s\n", c.paint("forked", green, bold), c.paint(src.ID, cyan))
	fmt.Fprintf(a.out, "    %s %s\n", c.paint("from", dim), display(oldCwd))
	fmt.Fprintf(a.out, "    %s   %s  %s %s\n", c.paint("to", dim), c.paint(display(dir), bold), c.paint("as", dim), c.paint(newID, cyan))
	var extras []string
	if copyMem {
		extras = append(extras, "memory")
	}
	if dstScratch != "" {
		extras = append(extras, "scratchpad")
	}
	if len(extras) > 0 {
		fmt.Fprintf(a.out, "    %s %s\n", c.paint("with", dim), strings.Join(extras, " and "))
	}
	resume := fmt.Sprintf("cd %s && claude --resume %s", fsutil.ShellQuote(dir), newID)
	if !opts.launch {
		fmt.Fprintf(a.out, "%s  %s\n", c.paint("resume with:", bold), resume)
		return nil
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not on PATH; resume with: %s", resume)
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("%v; resume with: %s", err, resume)
	}
	env := []string{"PWD=" + dir}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PWD=") {
			env = append(env, e)
		}
	}
	if err := syscall.Exec(bin, []string{"claude", "--resume", newID}, env); err != nil {
		return fmt.Errorf("starting %s: %v; resume with: %s", bin, err, resume)
	}
	return nil
}

type memDecision int

const (
	memNone          memDecision = iota // nothing to copy (or --no-memory)
	memCopy                             // target has none: copy
	memCopyOverEmpty                    // target is a dir with no files: replace it
	memSame                             // target already has identical memory
)

// memoryToCopy decides what happens to the source dir's memory/: it goes
// along only if it has files and the target has none or identical ones.
// Different memory in the target is an error, so nothing is ever clobbered.
func memoryToCopy(srcMem, dstMem, dir string) (memDecision, error) {
	if !fsutil.Exists(srcMem) {
		return memNone, nil
	}
	srcFiles, err := treeFiles(srcMem)
	if err != nil {
		return memNone, fmt.Errorf("can't read the source's memory: %v (fork with --no-memory to skip it)", err)
	}
	if len(srcFiles) == 0 {
		return memNone, nil
	}
	if !fsutil.Exists(dstMem) {
		return memCopy, nil
	}
	st, err := os.Lstat(dstMem)
	if err != nil {
		return memNone, err
	}
	if st.Mode()&fs.ModeSymlink != 0 && !fsutil.IsDir(dstMem) {
		return memNone, fmt.Errorf("%s's memory (%s) is a broken symlink; fix or remove it, or fork with --no-memory", display(dir), display(dstMem))
	}
	dstFiles, err := treeFiles(dstMem)
	switch {
	case err != nil:
		return memNone, fmt.Errorf("can't read %s's memory: %v; fork with --no-memory to skip it", display(dir), err)
	case len(dstFiles) == 0 && st.IsDir() && onlyDirs(dstMem):
		return memCopyOverEmpty, nil
	case sameFiles(srcFiles, dstFiles):
		return memSame, nil
	}
	return memNone, fmt.Errorf("%s already has its own memory (%s), different from the source's (%s);\n"+
		"merge them by hand first, or fork with --no-memory", display(dir), display(dstMem), display(srcMem))
}

// onlyDirs reports whether root holds nothing but (empty) directories.
func onlyDirs(root string) bool {
	only := true
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			only = false
			return filepath.SkipAll
		}
		return nil
	})
	return only
}

// removeEmptyTree removes a tree of empty directories, deepest first.
func removeEmptyTree(root string) error {
	var dirs []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Remove(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

// treeFiles reads every file under root (following symlinks, including the
// root's), keyed by relative path.
func treeFiles(root string) (map[string][]byte, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		st, err := os.Stat(p) // follows a symlinked file
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
			return nil // a dangling link: nothing to copy (copyTree skips it too)
		}
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(real, p)
		files[rel], err = os.ReadFile(p)
		return err
	})
	return files, err
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// findSource picks the session to fork: by id (or unique id prefix) from any
// folder, else the newest session started in dir from (default: current dir).
func (a *app) findSource(id, from string) (claude.Session, error) {
	if id == "" {
		if from == "" {
			from = "."
		}
		if !fsutil.IsDir(from) {
			return claude.Session{}, fmt.Errorf("%s is not a directory", display(from))
		}
		here, err := fsutil.AbsPath(from)
		if err != nil {
			return claude.Session{}, err
		}
		folder, err := a.cfg.FolderFor(here)
		if err != nil {
			return claude.Session{}, err
		}
		// The folder may also hold sessions of dirs whose names encode the
		// same (a/b-c and a/b/c); skip those.
		for _, s := range claude.SessionsIn(folder) {
			if c := claude.CwdOf(s.Path); c == claude.NodePath(here) || c == "" {
				return s, nil
			}
		}
		return claude.Session{}, fmt.Errorf("no sessions for %s (see 'csess ls --all', or use --session <id>)", display(here))
	}
	if !sessionIDRe.MatchString(id) {
		return claude.Session{}, fmt.Errorf("not a session id: %q", id)
	}
	var exact, prefix []claude.Session
	for _, s := range a.cfg.AllSessions() {
		if s.ID == id {
			exact = append(exact, s)
		} else if strings.HasPrefix(s.ID, id) {
			prefix = append(prefix, s)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = prefix
	}
	switch len(matches) {
	case 0:
		return claude.Session{}, fmt.Errorf("session %s not found under %s", id, display(a.cfg.Projects))
	case 1:
		return matches[0], nil
	}
	var list strings.Builder
	for _, s := range matches {
		list.WriteString("\n  " + display(s.Path))
	}
	if len(exact) > 1 {
		return claude.Session{}, fmt.Errorf("session %s exists in several project folders:%s\nremove the duplicates (or fork from inside the right dir without --session)", id, list.String())
	}
	return claude.Session{}, fmt.Errorf("%q matches several sessions; give more of the id:%s", id, list.String())
}

// copyFile copies src to dst through rewrite (nil: as is), keeping src's permissions.
func copyFile(src, dst string, rewrite func([]byte) []byte) error {
	if interrupted.Load() {
		return errInterrupted
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if rewrite != nil {
		data = rewrite(data)
	}
	return os.WriteFile(dst, data, st.Mode().Perm())
}

type copyOpts struct {
	jsonl func([]byte) []byte // rewrites *.jsonl transcripts; other files are copied as is
	link  func([]byte) []byte // rewrites symlink targets; nil: copy as is
	deref bool                // copy what symlinks point to instead of the links
}

// copyTree copies a directory recursively; other special files are skipped.
// A symlinked src is followed, so the copy never shares files with it.
func copyTree(src, dst string, o copyOpts) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0 && o.deref:
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return copyFile(p, target, nil)
			}
			return nil // dangling, or a link to a dir: skipped
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if o.link != nil {
				link = string(o.link([]byte(link)))
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular() && strings.HasSuffix(p, ".jsonl"):
			return copyFile(p, target, o.jsonl)
		case d.Type().IsRegular():
			return copyFile(p, target, nil)
		}
		return nil
	})
}
