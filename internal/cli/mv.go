package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/rafiibrahim8/csess/internal/claude"
	"github.com/rafiibrahim8/csess/internal/fsutil"
)

func (a *app) mvCmd() *cobra.Command {
	var dry bool
	cmd := &cobra.Command{
		Use:   "mv [-n] <olddir> <newdir>",
		Short: "Rename a dir and move its sessions, prompt history and settings along",
		Example: `  csess mv --dry-run ~/work/app ~/work/app-v2
  csess mv ~/work/app ~/work/app-v2`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeDirs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.config(); err != nil {
				return err
			}
			return a.move(args[0], args[1], dry)
		},
	}
	cmd.Flags().BoolVarP(&dry, "dry-run", "n", false, "show the plan, change nothing")
	return cmd
}

// moveUnit is one projects folder (or part of one) that has to follow the dir.
type moveUnit struct {
	src, dst string   // projects folders
	whole    bool     // every session in src is ours: move the folder itself
	items    []string // otherwise: the entries to move (<id>.jsonl and <id>/)
	ours     int      // sessions moving
	others   int      // sessions staying (other dirs whose names encode the same)
	tmp      string   // staging folder during the move, next to src
}

// paths of a move: the real dirs, and the same as Claude records them in
// transcripts and config (they differ only for names with invalid UTF-8).
type movePaths struct {
	old, new         string
	nodeOld, nodeNew string
}

func (a *app) move(oldArg, newArg string, dry bool) error {
	p, err := a.checkMove(oldArg, newArg)
	if err != nil {
		return err
	}
	units, err := a.planMove(p)
	if err != nil {
		return err
	}
	c := a.outC
	arrow := c.paint("->", dim)
	fmt.Fprintf(a.out, "%s  %s %s %s\n", c.paint("move dir:", bold), display(p.old), arrow, c.paint(display(p.new), bold))
	for _, u := range units {
		note := ""
		if !u.whole {
			note = "  " + c.paint(fmt.Sprintf("(%d of %d sessions; the rest belong to other dirs and stay, as does memory/)", u.ours, u.ours+u.others), dim)
		}
		fmt.Fprintf(a.out, "%s  %s %s %s%s\n", c.paint("sessions:", bold), filepath.Base(u.src), arrow, filepath.Base(u.dst), note)
	}
	if err := a.preflight(units, p); err != nil {
		return fmt.Errorf("%v\n(nothing was changed)", err)
	}
	if dry {
		fmt.Fprintln(a.out, c.paint("(dry run — nothing changed)", yellow))
		return nil
	}

	// From here on, dying would leave things half-moved. With SIGPIPE ignored,
	// a closed stdout (csess mv ... | head) only makes writes fail.
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGPIPE}
	signal.Ignore(sigs...)
	defer signal.Reset(sigs...)

	// 1. the real directory (most likely step to fail, so do it first)
	created, err := mkdirParents(filepath.Dir(p.new))
	if err != nil {
		removeDirs(created)
		return err
	}
	if err := fsutil.Move(p.old, p.new); err != nil {
		removeDirs(created)
		return err
	}
	// 2. sessions, with the paths inside them; undone (with step 1) if staging fails
	notes, undone, err := a.moveSessions(units, p)
	if err != nil && undone {
		if uerr := fsutil.Move(p.new, p.old); uerr != nil {
			return fmt.Errorf("%v\nand moving %s back to %s failed: %v", err, display(p.new), display(p.old), uerr)
		}
		removeDirs(created)
		return fmt.Errorf("%v\n(everything was put back)", err)
	}
	if err != nil { // the dir did move: still update history and settings
		err = fmt.Errorf("%v\n%s was moved to %s, but not all of its sessions were (see above)", err, display(p.old), display(p.new))
	}
	for _, n := range notes {
		fmt.Fprintln(a.out, c.paint(n, dim))
	}
	// 3. prompt history (up-arrow) is keyed by project path
	if fsutil.Exists(a.cfg.History) {
		err := fsutil.RewriteFile(a.cfg.History, func(b []byte) []byte {
			return claude.ReplaceBounded(b, `"project":"`+claude.JSString(p.nodeOld), `"project":"`+claude.JSString(p.nodeNew), `"/`)
		})
		if err != nil {
			a.warn("failed to update %s: %v", a.cfg.History, err)
		}
	}
	// 4. global config: per-project trust, allowed tools, local MCP servers
	return errors.Join(err, a.updateClaudeJSON(p))
}

// checkMove resolves both paths and refuses moves that would lose or break state.
func (a *app) checkMove(oldArg, newArg string) (p movePaths, err error) {
	if oldArg == "" || newArg == "" {
		return p, errors.New("empty directory argument")
	}
	arg := fsutil.TrimSlash(oldArg)
	if st, err := os.Lstat(arg); err == nil && st.Mode()&fs.ModeSymlink != 0 {
		link, _ := os.Readlink(arg)
		real, _ := fsutil.AbsPath(arg)
		return p, fmt.Errorf("%s is a symlink (-> %s). Sessions live under the real path:\n"+
			"  csess mv %s <newdir>   moves the target, or\n"+
			"  mv %s <newname>        renames just the link", display(arg), display(link), fsutil.ShellQuote(real), fsutil.ShellQuote(arg))
	}
	if !fsutil.IsDir(arg) {
		return p, fmt.Errorf("%s is not a directory", display(arg))
	}
	if p.old, err = fsutil.AbsPath(arg); err != nil {
		return p, err
	}
	if p.new, err = fsutil.AbsPath(newArg); err != nil {
		return p, err
	}
	p.nodeOld, p.nodeNew = claude.NodePath(p.old), claude.NodePath(p.new)
	switch {
	case p.old == "/":
		return p, errors.New("refusing to move /")
	case fsutil.Exists(p.new):
		return p, fmt.Errorf("%s already exists (refusing: mv would nest old inside it)", display(p.new))
	case fsutil.Under(p.new, p.old):
		return p, errors.New("can't move a directory inside itself")
	}
	// Claude's own files must not move with the dir, nor live in it via a
	// symlink (dotfiles): they would point at the old path afterwards.
	for _, c := range []string{a.cfg.Dir, a.cfg.ClaudeJSON, a.cfg.History} {
		real, err := filepath.EvalSymlinks(c)
		if errors.Is(err, fs.ErrNotExist) {
			continue // absent, or a dangling link: nothing that could move
		}
		if err != nil {
			return p, err
		}
		if fsutil.Under(real, p.old) {
			return p, fmt.Errorf("%s contains Claude's own config (%s); refusing to move it", display(p.old), display(real))
		}
	}
	busy, err := claude.RunningIn(p.old)
	if err != nil {
		a.warn("can't list processes (%v); not checking for running claude sessions", err)
	}
	if len(busy) > 0 {
		return p, fmt.Errorf("Claude is running inside %s — exit it first:\n  %s", display(p.old), strings.Join(busy, "\n  "))
	}
	return p, nil
}

// planMove finds the projects folders holding sessions of old and its subdirs.
// Every session is classified by its own recorded cwd, so a folder shared with
// a different dir (a/b-c and a/b/c both encode to -a-b-c) is split correctly.
func (a *app) planMove(p movePaths) ([]*moveUnit, error) {
	encOld := claude.Enc(p.old)
	mayHold := func(name string) bool { // could this folder hold sessions of old or its subdirs?
		if len(encOld) > claude.MaxEnc { // hashed names: only the prefix is predictable
			return strings.HasPrefix(name, encOld[:claude.MaxEnc])
		}
		return name == encOld || strings.HasPrefix(name, encOld+"-")
	}
	entries, _ := os.ReadDir(a.cfg.Projects)
	var units []*moveUnit
	for _, e := range entries {
		name := e.Name()
		folder := filepath.Join(a.cfg.Projects, name)
		if !mayHold(name) || !fsutil.IsDir(folder) {
			continue
		}
		var mine, unknown []claude.Session
		others := 0
		dstNames := map[string]bool{}
		for _, s := range claude.SessionsIn(folder) {
			switch c := claude.CwdOf(s.Path); {
			case c == "":
				unknown = append(unknown, s)
			case fsutil.Under(c, p.nodeOld):
				mine = append(mine, s)
				dstNames[claude.Enc(p.nodeNew+c[len(p.nodeOld):])] = true
			default:
				others++
			}
		}
		if len(mine) == 0 && others == 0 {
			// No session says whose folder this is (only memory/, or transcripts
			// without a cwd): take it only if its name can't mean any dir
			// outside old. Dirs inside old all map to the same target name.
			cands := claude.ResolveAll("/", name)
			var inside, outside []string
			for _, c := range cands {
				if fsutil.Under(c, p.old) {
					inside = append(inside, c)
				} else {
					outside = append(outside, c)
				}
			}
			switch {
			case len(inside) > 0 && len(outside) == 0:
				dstNames[claude.Enc(p.new+inside[0][len(p.old):])] = true
			case len(inside) > 0:
				a.warn("left %s in place: it could belong to any of %s", display(name), display(strings.Join(cands, ", ")))
				continue
			case len(cands) == 0 && len(name) > claude.MaxEnc:
				a.warn("left %s in place: its name is hashed and it has no session saying which dir it belongs to", display(name))
				continue
			default:
				continue // not ours
			}
		}
		if len(dstNames) == 0 {
			continue // a sibling such as old-foo, not ours
		}
		if len(dstNames) > 1 {
			return nil, fmt.Errorf("sessions in %s map to several target folders; can't move them safely", display(folder))
		}
		if others == 0 { // sessions without a cwd go along with the folder
			mine, unknown = append(mine, unknown...), nil
		}
		var dstName string
		for n := range dstNames {
			dstName = n
		}
		if len(dstName) > claude.MaxEnc {
			return nil, fmt.Errorf("%s: target folder name would exceed %d chars, which Claude hashes; not supported", dstName, claude.MaxEnc)
		}
		u := &moveUnit{src: folder, dst: filepath.Join(a.cfg.Projects, dstName),
			whole: others == 0, ours: len(mine), others: others + len(unknown)}
		if !u.whole {
			for _, s := range mine {
				u.items = append(u.items, s.ID+".jsonl")
				if fsutil.Exists(filepath.Join(folder, s.ID)) {
					u.items = append(u.items, s.ID)
				}
			}
		}
		units = append(units, u)
	}
	return units, nil
}

// preflight checks, before anything moves, that every step can succeed:
// each dir that gets written to or moved to another parent is writable,
// transcripts are readable, and .claude.json parses.
func (a *app) preflight(units []*moveUnit, p movePaths) error {
	writable := func(path string) error {
		if err := unix.Access(path, unix.W_OK); err != nil {
			return fmt.Errorf("can't write to %s: %v", display(path), err)
		}
		return nil
	}
	if len(units) > 0 {
		if err := writable(a.cfg.Projects); err != nil {
			return err
		}
	}
	for _, u := range units {
		// What lands in dst: the whole folder's contents, or the moved items.
		srcs := []string{u.src}
		if !u.whole {
			if err := writable(u.src); err != nil {
				return err
			}
			srcs = nil
			for _, it := range u.items {
				item := filepath.Join(u.src, it)
				srcs = append(srcs, item)
				if fsutil.IsDir(item) { // moved to the staging folder: a new parent
					if err := writable(item); err != nil {
						return err
					}
				}
			}
		}
		for _, s := range srcs {
			if err := checkTranscripts(s, writable); err != nil {
				return err
			}
		}
		if !fsutil.Exists(u.dst) {
			continue
		}
		if !fsutil.IsDir(u.dst) {
			return fmt.Errorf("%s is in the way: a file where the target folder should be", display(u.dst))
		}
		if u.whole {
			if err := checkMerge(u.src, u.dst, writable); err != nil {
				return err
			}
			continue
		}
		if err := writable(u.dst); err != nil {
			return err
		}
		for _, s := range srcs {
			if to := filepath.Join(u.dst, filepath.Base(s)); fsutil.IsDir(s) && fsutil.IsDir(to) {
				if err := checkMerge(s, to, writable); err != nil {
					return err
				}
			}
		}
	}
	data, err := os.ReadFile(a.cfg.ClaudeJSON)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if _, _, _, err := claude.RenameProjects(data, p.nodeOld, p.nodeNew); err != nil {
		return fmt.Errorf("can't parse %s (%v); fix it first", display(a.cfg.ClaudeJSON), err)
	}
	real, err := filepath.EvalSymlinks(a.cfg.ClaudeJSON)
	if err != nil {
		return err
	}
	return writable(filepath.Dir(real)) // the new file and its backup go there
}

// checkMerge mirrors mergeDir: dst dirs that receive entries must be
// writable, src dirs that entries leave too, and dirs that move into a new
// parent themselves.
func checkMerge(src, dst string, writable func(string) error) error {
	if err := writable(dst); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		if err := writable(src); err != nil {
			return err
		}
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case !fsutil.Exists(to):
			if e.IsDir() {
				if err := writable(from); err != nil {
					return err
				}
			}
		case e.IsDir() && fsutil.IsDir(to):
			if err := checkMerge(from, to, writable); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkTranscripts makes sure every transcript under root can be read and
// rewritten in place (its dir must be writable for the atomic replace).
func checkTranscripts(root string, writable func(string) error) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || !strings.HasSuffix(p, ".jsonl") {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		f.Close()
		return writable(filepath.Dir(p))
	})
}

// moveSessions moves every unit to its target in three phases:
//  1. stage: rename each unit aside, next to its source (same parent, so a
//     relative symlink still resolves). Staging all of them first matters:
//     a target name can equal another source's name (/w/a -> /w/a-b while
//     /w/a/b has sessions: both are -w-a-b).
//  2. rewrite: write the updated transcripts next to the originals, then
//     swap them all in. Until the swap the originals are untouched.
//  3. place: rename or merge each unit into its target.
//
// A failure in 1 or 2 puts everything back exactly and returns undone=true.
// Placing can't be undone, but preflight checked it; if it fails anyway the
// remaining units are still placed and stranded ones are named.
func (a *app) moveSessions(units []*moveUnit, p movePaths) (notes []string, undone bool, err error) {
	var staged []*moveUnit
	rollback := func(cause error) ([]string, bool, error) {
		for i := len(staged) - 1; i >= 0; i-- {
			if err := unstage(staged[i]); err != nil {
				a.warn("couldn't put %s back (it is in %s): %v", staged[i].src, staged[i].tmp, err)
				return nil, false, cause
			}
		}
		return nil, true, cause
	}
	suffix, err := randomSuffix()
	if err != nil {
		return nil, true, err
	}
	for i, u := range units {
		u.tmp = filepath.Join(a.cfg.Projects, ".csess-mv-"+suffix+"-"+strconv.Itoa(i))
		if err := stageUnit(u); err != nil {
			return rollback(err)
		}
		staged = append(staged, u)
	}

	var pending []*fsutil.Pending
	discard := func() {
		for _, pd := range pending {
			pd.Discard()
		}
	}
	for _, u := range units {
		pds, err := prepareRewrites(u, p)
		pending = append(pending, pds...)
		if err != nil {
			discard()
			return rollback(err)
		}
	}
	for i, pd := range pending {
		if err := pd.Commit(); err != nil {
			// Renames in a dir checked writable don't fail in practice; if one
			// does, earlier swaps can't be taken back without the originals.
			for _, rest := range pending[i:] {
				rest.Discard()
			}
			var where []string
			for _, u := range units {
				where = append(where, u.tmp+" (for "+u.dst+")")
			}
			return nil, false, fmt.Errorf("updating %s: %v\nthe sessions are stranded in:\n  %s", pd.Path, err, strings.Join(where, "\n  "))
		}
	}

	var conflicts []*moveUnit
	var placeErrs []error
	for _, u := range units {
		if !fsutil.Exists(u.dst) {
			if err := os.Rename(u.tmp, u.dst); err != nil {
				placeErrs = append(placeErrs, fmt.Errorf("sessions for %s are stranded in %s: %v", u.dst, u.tmp, err))
			}
			continue
		}
		notes = append(notes, "merged into existing "+u.dst)
		if err := mergeDir(u.tmp, u.dst); err != nil {
			placeErrs = append(placeErrs, fmt.Errorf("sessions for %s are partly stranded in %s: %v", u.dst, u.tmp, err))
			continue
		}
		if os.Remove(u.tmp) != nil {
			conflicts = append(conflicts, u)
		}
	}
	if len(conflicts) > 0 {
		a.reportConflicts(conflicts)
	}
	return notes, false, errors.Join(placeErrs...)
}

func randomSuffix() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// stageUnit moves a unit's files to u.tmp; on failure it puts them back.
func stageUnit(u *moveUnit) error {
	if u.whole {
		return os.Rename(u.src, u.tmp)
	}
	st, err := os.Stat(u.src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(u.tmp, st.Mode().Perm()); err != nil {
		return err
	}
	for _, it := range u.items {
		if err := os.Rename(filepath.Join(u.src, it), filepath.Join(u.tmp, it)); err != nil {
			unstage(u)
			return err
		}
	}
	return nil
}

func unstage(u *moveUnit) error {
	if u.whole {
		return os.Rename(u.tmp, u.src)
	}
	for _, it := range u.items {
		if p := filepath.Join(u.tmp, it); fsutil.Exists(p) {
			if err := os.Rename(p, filepath.Join(u.src, it)); err != nil {
				return err
			}
		}
	}
	return os.Remove(u.tmp)
}

// prepareRewrites writes updated copies of a staged unit's transcripts:
// recorded dirs (cwd, /rewind backups) and paths into the session folder
// (tool results, subagents, memory) point at the new locations.
func prepareRewrites(u *moveUnit, p movePaths) ([]*fsutil.Pending, error) {
	root, err := filepath.EvalSymlinks(u.tmp)
	if err != nil {
		return nil, err
	}
	var out []*fsutil.Pending
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		pd, err := fsutil.PrepareRewrite(path, func(b []byte) []byte {
			b = claude.MoveDir(b, p.nodeOld, p.nodeNew)
			if u.whole {
				return claude.ReplacePath(b, u.src, u.dst, "/")
			}
			// a shared folder's other files (memory/, other sessions) stay put
			for _, it := range u.items {
				id := strings.TrimSuffix(it, ".jsonl")
				b = claude.ReplacePath(b, filepath.Join(u.src, id), filepath.Join(u.dst, id), claude.PathEnd)
			}
			return b
		})
		if pd != nil {
			out = append(out, pd)
		}
		return err
	})
	return out, err
}

// mergeDir moves everything from src into dst, recursing into directories
// both have. Files that already exist in dst are left in src.
func mergeDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case !fsutil.Exists(to):
			if err := os.Rename(from, to); err != nil {
				return err
			}
		case e.IsDir() && fsutil.IsDir(to):
			if err := mergeDir(from, to); err != nil {
				return err
			}
			os.Remove(from) // only succeeds if everything moved
		}
	}
	return nil
}

// reportConflicts moves files that would have overwritten existing ones to a
// visible folder (never back under a name some dir's sessions use) and lists them.
func (a *app) reportConflicts(units []*moveUnit) {
	base := filepath.Join(a.cfg.Projects, "csess-conflicts-"+time.Now().Format("20060102-150405"))
	dir := base
	for n := 2; os.Mkdir(dir, 0o700) != nil; n++ {
		if n > 100 {
			a.warn("some files already existed in the target folders; they are in the folders below")
			dir = ""
			break
		}
		dir = fmt.Sprintf("%s-%d", base, n)
	}
	if dir != "" {
		a.warn("some files already existed in the target folders and were not overwritten.\n"+
			"Merge them by hand (incl. memory/MEMORY.md); they are in %s:", display(dir))
	}
	for i, u := range units {
		left := u.tmp
		if dir != "" {
			to := filepath.Join(dir, strconv.Itoa(i)+"-for-"+filepath.Base(u.dst))
			if os.Rename(u.tmp, to) == nil {
				left = to
			}
		}
		filepath.WalkDir(left, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				fmt.Fprintf(a.errOut, "    %s\n", display(p))
			}
			return nil
		})
	}
}

// mkdirParents creates dir and its missing parents, returning the ones it
// created (deepest first) so a failed move can remove them again.
func mkdirParents(dir string) ([]string, error) {
	var created []string
	for p := dir; !fsutil.Exists(p); p = filepath.Dir(p) {
		created = append(created, p)
	}
	return created, os.MkdirAll(dir, 0o755)
}

func removeDirs(dirs []string) {
	for _, d := range dirs {
		os.Remove(d)
	}
}

func (a *app) updateClaudeJSON(p movePaths) error {
	path, err := filepath.EvalSymlinks(a.cfg.ClaudeJSON) // a symlinked file stays a link
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(a.out, a.outC.paint("done.", green, bold))
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, n, replaced, err := claude.RenameProjects(data, p.nodeOld, p.nodeNew)
	if err != nil {
		return fmt.Errorf("couldn't update %s (left unchanged): %v", path, err)
	}
	if n == 0 {
		fmt.Fprintf(a.out, "%s %s\n", a.outC.paint("done.", green, bold),
			a.outC.paint(fmt.Sprintf("(no entries for %s in %s)", display(p.old), a.cfg.ClaudeJSON), dim))
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	bak := path + ".csess-bak." + time.Now().Format("20060102-150405")
	if fsutil.Exists(bak) {
		bak += fmt.Sprintf(".%d", os.Getpid())
	}
	if err := os.WriteFile(bak, data, st.Mode().Perm()); err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(path, out, st.Mode().Perm()); err != nil {
		return fmt.Errorf("couldn't update %s: %v (backup at %s)", path, err, bak)
	}
	for _, k := range replaced {
		a.warn("replaced the existing settings for %s in %s (the old ones are in the backup)", display(k), a.cfg.ClaudeJSON)
	}
	entries := "entries"
	if n == 1 {
		entries = "entry"
	}
	fmt.Fprintf(a.out, "%s updated %d %s in %s %s\n", a.outC.paint("done.", green, bold), n, entries, a.cfg.ClaudeJSON,
		a.outC.paint("(backup at "+bak+")", dim))
	return nil
}
