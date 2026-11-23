package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rafiibrahim8/csess/internal/claude"
	"github.com/rafiibrahim8/csess/internal/fsutil"
)

// ---------------------------------------------------------------- fixtures

// sandbox returns an app whose Claude config lives in a fresh temp dir, and
// that dir's (physical) root for the test's own files.
func sandbox(t *testing.T) (*app, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := claude.NewConfig(root+"/cfg", root+"/cfg/.claude.json")
	cfg.TempRoots = []string{root + "/tmp"}
	mkdir(t, cfg.Projects)
	var out bytes.Buffer
	return &app{cfg: cfg, out: &out, errOut: &out}, root
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// addSession writes a transcript for a session started in dir, with a
// tool-result file in its sidecar dir that the transcript refers to.
func addSession(t *testing.T, a *app, dir, id string, mtime time.Time) string {
	t.Helper()
	folder := filepath.Join(a.cfg.Projects, claude.Enc(dir))
	tr := filepath.Join(folder, id, "tool-results", "r.txt")
	mkdir(t, filepath.Dir(tr))
	os.WriteFile(tr, []byte("result"), 0o600)
	p := filepath.Join(folder, id+".jsonl")
	line := jsonLine(t, map[string]any{"type": "user", "cwd": dir, "sessionId": id, "toolResult": tr})
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
	return p
}

// checkSession asserts the session sits in the folder Claude expects for
// wantCwd, records that cwd, and its tool-result path resolves.
func checkSession(t *testing.T, a *app, id, wantCwd string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, "*", id+".jsonl"))
	if len(m) != 1 {
		t.Fatalf("session %s found %d times: %v", id, len(m), m)
	}
	b, _ := os.ReadFile(m[0])
	var first map[string]any
	line, _, _ := strings.Cut(string(b), "\n")
	json.Unmarshal([]byte(line), &first)
	if got := filepath.Base(filepath.Dir(m[0])); got != claude.Enc(wantCwd) {
		t.Errorf("%s: in folder %s, want %s", id, got, claude.Enc(wantCwd))
	}
	if first["cwd"] != wantCwd {
		t.Errorf("%s: cwd %v, want %s", id, first["cwd"], wantCwd)
	}
	if tr, _ := first["toolResult"].(string); !fsutil.Exists(tr) {
		t.Errorf("%s: tool-result path %s doesn't exist", id, tr)
	}
}

const (
	id1 = "11111111-1111-4111-8111-111111111111"
	id2 = "22222222-2222-4222-8222-222222222222"
	id3 = "33333333-3333-4333-8333-333333333333"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------- mv

// /w/a -> /w/a-b while /w/a/b has sessions: both encode to -w-a-b, so a
// naive move merges a's sessions into b's folder.
func TestMvEncodedNameCollision(t *testing.T) {
	a, root := sandbox(t)
	w := root + "/w"
	mkdir(t, w+"/a/b")
	addSession(t, a, w+"/a", id1, t0)
	addSession(t, a, w+"/a/b", id2, t0)
	if err := a.move(w+"/a", w+"/a-b", false); err != nil {
		t.Fatal(err)
	}
	checkSession(t, a, id1, w+"/a-b")
	checkSession(t, a, id2, w+"/a-b/b")
	if m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, ".csess-mv-*")); len(m) > 0 {
		t.Errorf("staging folders left: %v", m)
	}
}

// A folder shared by two dirs (/w/a/b-c and /w/a/b/c): only ours moves.
func TestMvSplitsSharedFolder(t *testing.T) {
	a, root := sandbox(t)
	w := root + "/w"
	mkdir(t, w+"/a/b/c")
	mkdir(t, w+"/a/b-c")
	addSession(t, a, w+"/a/b/c", id1, t0)
	addSession(t, a, w+"/a/b-c", id2, t0)
	if err := a.move(w+"/a/b", w+"/z", false); err != nil {
		t.Fatal(err)
	}
	checkSession(t, a, id1, w+"/z/c")
	checkSession(t, a, id2, w+"/a/b-c")
}

func TestMvMemoryOnlySubdirFolder(t *testing.T) {
	a, root := sandbox(t)
	w := root + "/w"
	mkdir(t, w+"/a/my-app")
	addSession(t, a, w+"/a", id1, t0)
	mem := filepath.Join(a.cfg.Projects, claude.Enc(w+"/a/my-app"), "memory")
	mkdir(t, mem)
	os.WriteFile(mem+"/MEMORY.md", []byte("x"), 0o600)
	if err := a.move(w+"/a", w+"/b", false); err != nil {
		t.Fatal(err)
	}
	if !fsutil.Exists(filepath.Join(a.cfg.Projects, claude.Enc(w+"/b/my-app"), "memory", "MEMORY.md")) {
		t.Error("memory-only folder of a subdir was not moved")
	}
}

func TestMvMergesIntoExistingFolder(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0)
	addSession(t, a, root+"/b", id2, t0) // stale sessions for the target path
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	checkSession(t, a, id1, root+"/b")
	checkSession(t, a, id2, root+"/b")
}

func TestMvKeepsMtimeOrder(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0.Add(2*time.Hour)) // newest: what claude -c resumes
	addSession(t, a, root+"/a", id2, t0)
	addSession(t, a, root+"/a", id3, t0.Add(time.Hour))
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/b")))
	if len(ss) != 3 || ss[0].ID != id1 || !ss[0].ModTime.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("order/mtimes changed: %+v", ss)
	}
}

func TestMvWeirdPathAndHistory(t *testing.T) {
	a, root := sandbox(t)
	old, newDir := root+`/we"ird\ dir`, root+`/new"name`
	mkdir(t, old)
	addSession(t, a, old, id1, t0)
	os.WriteFile(a.cfg.History, []byte(jsonLine(t, map[string]string{"project": old})+
		jsonLine(t, map[string]string{"project": old + "-sibling"})), 0o600)
	if err := a.move(old, newDir, false); err != nil {
		t.Fatal(err)
	}
	checkSession(t, a, id1, newDir)
	h, _ := os.ReadFile(a.cfg.History)
	var l0, l1 map[string]string
	lines := strings.Split(strings.TrimSpace(string(h)), "\n")
	json.Unmarshal([]byte(lines[0]), &l0)
	json.Unmarshal([]byte(lines[1]), &l1)
	if l0["project"] != newDir || l1["project"] != old+"-sibling" {
		t.Errorf("history: %q / %q", l0["project"], l1["project"])
	}
}

func TestMvClaudeJSONBackupAndMode(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	os.WriteFile(a.cfg.ClaudeJSON, []byte(`{"projects":{"`+root+`/a":{}}}`), 0o600)
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(a.cfg.ClaudeJSON); !bytes.Contains(b, []byte(`"`+root+`/b"`)) {
		t.Errorf("not renamed: %s", b)
	}
	if st, _ := os.Stat(a.cfg.ClaudeJSON); st.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", st.Mode())
	}
	if m, _ := filepath.Glob(a.cfg.ClaudeJSON + ".csess-bak.*"); len(m) != 1 {
		t.Errorf("want one timestamped backup, got %v", m)
	}
}

func TestMvLeavesUnrelatedClaudeJSONAlone(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	orig := []byte(`{"projects":{"/elsewhere":{}}}`)
	os.WriteFile(a.cfg.ClaudeJSON, orig, 0o600)
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a.cfg.ClaudeJSON); !bytes.Equal(got, orig) {
		t.Errorf("file rewritten: %s", got)
	}
	if m, _ := filepath.Glob(a.cfg.ClaudeJSON + ".csess-bak.*"); len(m) != 0 {
		t.Errorf("needless backup %v", m)
	}
}

func TestMvRefusals(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a/in")
	mkdir(t, root+"/taken")
	os.Symlink(root+"/a", root+"/link")
	for name, args := range map[string][2]string{
		"symlink":       {root + "/link/", root + "/x"},
		"target exists": {root + "/a", root + "/taken"},
		"into itself":   {root + "/a", root + "/a/in/x"},
		"config inside": {root + "/cfg", root + "/cfg2"},
		"not a dir":     {root + "/missing", root + "/x"},
	} {
		if err := a.move(args[0], args[1], false); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if !fsutil.IsDir(root+"/a") || !fsutil.Exists(root+"/link") {
		t.Error("something moved")
	}
}

func TestMvDryRunChangesNothing(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0)
	if err := a.move(root+"/a", root+"/b", true); err != nil {
		t.Fatal(err)
	}
	checkSession(t, a, id1, root+"/a")
	if fsutil.Exists(root + "/b") {
		t.Error("dir moved in dry run")
	}
}

// ---------------------------------------------------------------- fork

func TestFork(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	orig := addSession(t, a, root+"/src", id1, t0)
	before, _ := os.ReadFile(orig)
	if err := a.fork(id1[:8], root+"/dst", forkOpts{memory: true}); err != nil { // unique prefix
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst")))
	if len(ss) != 1 || ss[0].ID == id1 {
		t.Fatalf("fork result: %+v", ss)
	}
	checkSession(t, a, ss[0].ID, root+"/dst")
	if b, _ := os.ReadFile(ss[0].Path); bytes.Contains(b, []byte(id1)) {
		t.Error("old session id left in the copy")
	}
	if after, _ := os.ReadFile(orig); !bytes.Equal(before, after) {
		t.Error("original session modified")
	}
}

func TestForkRejectsBadIDs(t *testing.T) {
	a, _ := sandbox(t)
	for _, id := range []string{"../x", "*", "nope"} {
		if err := a.fork(id, t.TempDir(), forkOpts{memory: true}); err == nil {
			t.Errorf("fork(%q) succeeded", id)
		}
	}
}

// ---------------------------------------------------------------- cli

func TestCLIArgs(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/d")
	addSession(t, a, root+"/d", id1, t0)
	run := func(args ...string) error {
		cmd := NewRootCmd(func() (claude.Config, error) { return a.cfg, nil }, "test")
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := run("ls", root+"/d", "--all"); err == nil {
		t.Error("ls <dir> --all accepted")
	}
	if err := run("fork", root+"/f", "--session="+id1); err != nil {
		t.Errorf("options after args, --opt=value: %v", err)
	}
	if err := run("mv", "-n", "--", root+"/d", root+"/e"); err != nil {
		t.Errorf("-- handling: %v", err)
	}
}

// ---------------------------------------------------------------- regressions from adversarial testing

func output(a *app) string { return a.out.(*bytes.Buffer).String() }

func writeSession(t *testing.T, a *app, folderOf, cwd, id string, mtime time.Time) {
	t.Helper()
	folder := filepath.Join(a.cfg.Projects, claude.Enc(folderOf))
	mkdir(t, folder)
	p := filepath.Join(folder, id+".jsonl")
	os.WriteFile(p, []byte(jsonLine(t, map[string]any{"type": "user", "cwd": cwd, "sessionId": id})), 0o600)
	os.Chtimes(p, mtime, mtime)
}

// Dirs sharing a folder name: fork picks the current dir's session, ls --all shows both.
func TestSharedFolderForkAndLsAll(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/w/b/c")
	mkdir(t, root+"/w/b-c")
	writeSession(t, a, root+"/w/b-c", root+"/w/b/c", id1, t0.Add(time.Hour)) // newer, other dir
	writeSession(t, a, root+"/w/b-c", root+"/w/b-c", id2, t0)
	t.Chdir(root + "/w/b-c")
	if err := a.fork("", root+"/out", forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output(a), "forked "+id2) {
		t.Errorf("forked the wrong session:\n%s", output(a))
	}
	a.out.(*bytes.Buffer).Reset()
	a.lsAll()
	if !strings.Contains(output(a), root+"/w/b/c\n") || !strings.Contains(output(a), root+"/w/b-c\n") {
		t.Errorf("ls --all should list both dirs:\n%s", output(a))
	}
}

// The id inside a cwd or the target path must not be rewritten; only "sessionId" is.
func TestForkIDInPaths(t *testing.T) {
	a, root := sandbox(t)
	src := root + "/runs/" + id1
	mkdir(t, src)
	addSession(t, a, src, id1, t0)
	dst := root + "/try-" + id1
	if err := a.fork(id1, dst, forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(dst)))
	if len(ss) != 1 {
		t.Fatalf("got %+v", ss)
	}
	checkSession(t, a, ss[0].ID, dst)
}

func TestForkFailureLeavesNothing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	locked := filepath.Join(a.cfg.Projects, claude.Enc(root+"/src"), id1, "tool-results", "r.txt")
	os.Chmod(locked, 0)
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err == nil {
		t.Fatal("fork of an unreadable sidecar succeeded")
	}
	if fsutil.Exists(root+"/dst") || fsutil.Exists(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"))) {
		t.Error("failed fork left files behind")
	}
}

func TestForkSymlinkedSidecarIsCopied(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	side := filepath.Join(a.cfg.Projects, claude.Enc(root+"/src"), id1)
	os.Rename(side, root+"/elsewhere")
	os.Symlink(root+"/elsewhere", side)
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst")))
	if st, err := os.Lstat(strings.TrimSuffix(ss[0].Path, ".jsonl")); err != nil || !st.IsDir() {
		t.Errorf("fork's sidecar should be a real dir: %v %v", st, err)
	}
}

func TestMvPreflightChangesNothing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	a, root := sandbox(t)
	mkdir(t, root+"/a/b")
	addSession(t, a, root+"/a", id1, t0)
	p := addSession(t, a, root+"/a/b", id2, t0)
	os.Chmod(p, 0)
	if err := a.move(root+"/a", root+"/z", false); err == nil {
		t.Fatal("move with an unreadable transcript succeeded")
	}
	os.Chmod(p, 0o600)
	checkSession(t, a, id1, root+"/a")
	checkSession(t, a, id2, root+"/a/b")

	os.WriteFile(a.cfg.ClaudeJSON, []byte(`{"projects":null`), 0o600) // unparseable
	if err := a.move(root+"/a", root+"/z", false); err == nil {
		t.Fatal("move with a broken .claude.json succeeded")
	}
	if !fsutil.IsDir(root + "/a") {
		t.Error("dir moved although the move was refused")
	}
}

func TestMvKeepsSymlinkedConfigFiles(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	mkdir(t, root+"/dot")
	os.WriteFile(root+"/dot/claude.json", []byte(`{"projects":{"`+root+`/a":{}}}`), 0o600)
	os.Symlink(root+"/dot/claude.json", a.cfg.ClaudeJSON)
	if err := a.move(root+"/a", root+"/z", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(a.cfg.ClaudeJSON); st.Mode()&os.ModeSymlink == 0 {
		t.Error(".claude.json symlink replaced by a file")
	}
	if b, _ := os.ReadFile(root + "/dot/claude.json"); !bytes.Contains(b, []byte(root+"/z")) {
		t.Errorf("link target not updated: %s", b)
	}
}

// A memory-only folder whose name could also be a sibling's isn't taken.
func TestMvLeavesAmbiguousMemoryAlone(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/w/a/b")
	mkdir(t, root+"/w/a-b")
	addSession(t, a, root+"/w/a", id1, t0)
	mem := filepath.Join(a.cfg.Projects, claude.Enc(root+"/w/a-b"), "memory")
	mkdir(t, mem)
	if err := a.move(root+"/w/a", root+"/w/z", false); err != nil {
		t.Fatal(err)
	}
	if !fsutil.IsDir(mem) {
		t.Error("ambiguous memory folder was moved")
	}
}

// Sidecar dirs merge recursively; real conflicts go to a visible folder, not back to the source name.
func TestMvMergeAndConflicts(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0)
	srcMem := filepath.Join(a.cfg.Projects, claude.Enc(root+"/a"), "memory")
	mkdir(t, srcMem)
	os.WriteFile(srcMem+"/MEMORY.md", []byte("mine"), 0o600)
	// target folder already has the same sidecar dir (other file) and a conflicting MEMORY.md
	dst := filepath.Join(a.cfg.Projects, claude.Enc(root+"/z"))
	mkdir(t, filepath.Join(dst, id1, "tool-results"))
	os.WriteFile(filepath.Join(dst, id1, "tool-results", "other.txt"), nil, 0o600)
	mkdir(t, dst+"/memory")
	os.WriteFile(dst+"/memory/MEMORY.md", []byte("theirs"), 0o600)
	if err := a.move(root+"/a", root+"/z", false); err != nil {
		t.Fatal(err)
	}
	checkSession(t, a, id1, root+"/z") // its tool-result merged into the existing sidecar dir
	if fsutil.Exists(filepath.Join(a.cfg.Projects, claude.Enc(root+"/a"))) {
		t.Error("leftovers put back under the old folder name")
	}
	m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, "csess-conflicts-*", "*", "memory", "MEMORY.md"))
	if len(m) != 1 {
		t.Errorf("conflicting MEMORY.md not in a conflicts folder: %v", m)
	}
	if s, _ := filepath.Glob(filepath.Join(a.cfg.Projects, ".csess-mv-*")); len(s) > 0 {
		t.Errorf("staging left: %v", s)
	}
}

func TestMvLongPathWithoutSessions(t *testing.T) {
	a, root := sandbox(t)
	long := root + "/" + strings.Repeat("x", 220)
	mkdir(t, long)
	if err := a.move(long, root+"/short", false); err != nil {
		t.Errorf("needless refusal: %v", err)
	}
}

func TestDisplayAndTruncate(t *testing.T) {
	if got := display("/a  b/c "); got != "/a  b/c " {
		t.Errorf("spaces altered: %q", got)
	}
	if got := display("/x\x1b[2J\n"); !strings.HasPrefix(got, `"`) {
		t.Errorf("control chars not quoted: %q", got)
	}
	if got := truncate("日本語abc", 5); got != "日本" {
		t.Errorf("wide chars: %q", got)
	}
}

// ---------------------------------------------------------------- fork: memory, scratchpad, /rewind paths

func writeMemory(t *testing.T, a *app, dir, content string) string {
	t.Helper()
	mem := filepath.Join(a.cfg.Projects, claude.Enc(dir), "memory")
	mkdir(t, mem)
	os.WriteFile(mem+"/MEMORY.md", []byte(content), 0o600)
	return mem
}

func TestForkCopiesMemoryAndScratchpad(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	writeMemory(t, a, root+"/src", "remember this")
	scratch := filepath.Join(root, "tmp", claude.Enc(root+"/src"), id1, "scratchpad")
	mkdir(t, scratch)
	os.WriteFile(scratch+"/notes.txt", []byte("n"), 0o600)
	// the transcript mentions a scratchpad file and a /rewind backup dir
	p := filepath.Join(a.cfg.Projects, claude.Enc(root+"/src"), id1+".jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(jsonLine(t, map[string]any{"type": "x", "file": scratch + "/notes.txt",
		"snapshot": map[string]any{"realParentDir": root + "/src/pkg"}}))
	f.Close()

	if err := a.fork(id1, root+"/new/deep/dst", forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	if !fsutil.IsDir(root + "/new/deep/dst") {
		t.Error("target dir not created")
	}
	dstFolder := filepath.Join(a.cfg.Projects, claude.Enc(root+"/new/deep/dst"))
	if b, _ := os.ReadFile(dstFolder + "/memory/MEMORY.md"); string(b) != "remember this" {
		t.Errorf("memory not copied: %q", b)
	}
	ss := claude.SessionsIn(dstFolder)
	newScratch := filepath.Join(root, "tmp", claude.Enc(root+"/new/deep/dst"), ss[0].ID, "scratchpad")
	if !fsutil.Exists(newScratch + "/notes.txt") {
		t.Error("scratchpad not copied")
	}
	b, _ := os.ReadFile(ss[0].Path)
	if !bytes.Contains(b, []byte(newScratch+"/notes.txt")) || bytes.Contains(b, []byte(scratch)) {
		t.Errorf("scratchpad path not rewritten:\n%s", b)
	}
	if !bytes.Contains(b, []byte(`"realParentDir":"`+root+`/new/deep/dst/pkg"`)) {
		t.Errorf("/rewind dir not rewritten:\n%s", b)
	}
}

func TestForkNoMemory(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	writeMemory(t, a, root+"/src", "m")
	if err := a.fork(id1, root+"/dst", forkOpts{memory: false}); err != nil {
		t.Fatal(err)
	}
	if fsutil.Exists(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"), "memory")) {
		t.Error("memory copied despite --no-memory")
	}
}

func TestForkRefusesMemoryCollision(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	writeMemory(t, a, root+"/src", "mine")
	dstMem := writeMemory(t, a, root+"/dst", "theirs")
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err == nil {
		t.Fatal("fork over different memory succeeded")
	}
	if b, _ := os.ReadFile(dstMem + "/MEMORY.md"); string(b) != "theirs" {
		t.Error("target memory changed")
	}
	if ss := claude.SessionsIn(filepath.Dir(dstMem)); len(ss) != 0 || fsutil.Exists(root+"/dst") {
		t.Error("refused fork left files behind")
	}
	// identical memory isn't a collision; --no-memory always works
	writeMemory(t, a, root+"/dst", "mine")
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err != nil {
		t.Errorf("identical memory: %v", err)
	}
	writeMemory(t, a, root+"/dst", "theirs")
	if err := a.fork(id1, root+"/dst", forkOpts{memory: false}); err != nil {
		t.Errorf("--no-memory: %v", err)
	}
}

func TestMvRewritesRewindDirs(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	p := addSession(t, a, root+"/a", id1, t0)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(jsonLine(t, map[string]any{"type": "file-history-snapshot",
		"snapshot": map[string]any{"realParentDir": root + "/a/src"}}))
	f.Close()
	if err := a.move(root+"/a", root+"/z", false); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, "*", id1+".jsonl"))
	if b, _ := os.ReadFile(m[0]); !bytes.Contains(b, []byte(`"realParentDir":"`+root+`/z/src"`)) {
		t.Errorf("realParentDir not moved:\n%s", b)
	}
}

// ---------------------------------------------------------------- regressions from round 2

func TestMvPreflightCoversMerge(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	a, root := sandbox(t)
	mkdir(t, root+"/a/sub")
	addSession(t, a, root+"/a", id1, t0)
	addSession(t, a, root+"/a/sub", id2, t0)
	writeMemory(t, a, root+"/a", "m")
	dstMem := filepath.Join(a.cfg.Projects, claude.Enc(root+"/c"), "memory")
	mkdir(t, dstMem)
	os.Chmod(dstMem, 0o555)
	defer os.Chmod(dstMem, 0o755)
	if err := a.move(root+"/a", root+"/c", false); err == nil {
		t.Fatal("merge into a read-only memory/ was not refused")
	}
	checkSession(t, a, id1, root+"/a")
	checkSession(t, a, id2, root+"/a/sub")
}

// Rollback restores transcripts byte for byte, even ones that already
// mention the new path, and moves nothing.
func TestMvRollbackIsExact(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	p := addSession(t, a, root+"/a", id1, t0)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(jsonLine(t, map[string]any{"type": "user", "cwd": root + "/b"}))
	f.Close()
	os.Chtimes(p, t0, t0)
	before, _ := os.ReadFile(p)
	units := []*moveUnit{
		{src: filepath.Dir(p), dst: filepath.Join(a.cfg.Projects, claude.Enc(root+"/b")), whole: true},
		{src: filepath.Join(a.cfg.Projects, "missing"), dst: filepath.Join(a.cfg.Projects, "x"), whole: true}, // staging fails
	}
	mp := movePaths{old: root + "/a", new: root + "/b", nodeOld: root + "/a", nodeNew: root + "/b"}
	if _, undone, err := a.moveSessions(units, mp); err == nil || !undone {
		t.Fatalf("want an undone failure, got undone=%v err=%v", undone, err)
	}
	after, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if !bytes.Equal(before, after) || !st.ModTime().Equal(t0) {
		t.Errorf("transcript changed by rollback:\n%s\n---\n%s", before, after)
	}
	if m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, ".csess-*")); len(m) > 0 {
		t.Errorf("staging left: %v", m)
	}
}

// Node records invalid UTF-8 in a dir name as U+FFFD; mv must still find its sessions.
func TestMvInvalidUTF8Dir(t *testing.T) {
	a, root := sandbox(t)
	old := root + "/caf\xe9"
	mkdir(t, old)
	writeSession(t, a, old, claude.NodePath(old), id1, t0)
	if err := a.move(old, root+"/cafe", false); err != nil {
		t.Fatal(err)
	}
	checkSessionCwd(t, a, id1, root+"/cafe")
}

func checkSessionCwd(t *testing.T, a *app, id, wantCwd string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, claude.Enc(wantCwd), id+".jsonl"))
	if len(m) != 1 {
		t.Fatalf("%s not in the folder for %s", id, wantCwd)
	}
	if c := claude.CwdOf(m[0]); c != wantCwd {
		t.Errorf("cwd %q, want %q", c, wantCwd)
	}
}

func TestMvMemoryOnlyAmbiguousInsideOld(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a/x.y")
	mkdir(t, root+"/a/x-y")
	writeMemory(t, a, root+"/a/x-y", "m")
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	if !fsutil.Exists(filepath.Join(a.cfg.Projects, claude.Enc(root+"/b/x-y"), "memory", "MEMORY.md")) {
		t.Error("memory whose candidates are all inside old was left behind")
	}
}

func TestMvRelativeSymlinkedFolder(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0)
	folder := filepath.Join(a.cfg.Projects, claude.Enc(root+"/a"))
	mkdir(t, a.cfg.Dir+"/store")
	os.Rename(folder, a.cfg.Dir+"/store/a")
	os.Symlink("../store/a", folder)
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	checkSessionCwd(t, a, id1, root+"/b")
}

func TestMvRefusesConfigSymlinkedFromOld(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/dots")
	os.WriteFile(root+"/dots/claude.json", []byte(`{}`), 0o600)
	os.Symlink(root+"/dots/claude.json", a.cfg.ClaudeJSON)
	if err := a.move(root+"/dots", root+"/dotfiles", false); err == nil {
		t.Error("moving the dir .claude.json lives in was not refused")
	}
}

func TestMvBackupNextToRealConfig(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	mkdir(t, root+"/real")
	os.WriteFile(root+"/real/claude.json", []byte(`{"projects":{"`+root+`/a":{}}}`), 0o600)
	os.Symlink(root+"/real/claude.json", a.cfg.ClaudeJSON)
	os.Chmod(a.cfg.Dir, 0o555) // the link's dir is read-only; the real file's isn't
	defer os.Chmod(a.cfg.Dir, 0o755)
	if err := a.move(root+"/a", root+"/b", false); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(root + "/real/claude.json.csess-bak.*"); len(m) != 1 {
		t.Errorf("backup not next to the real file: %v", m)
	}
}

func TestTruncateEmojiPresentation(t *testing.T) {
	if got := truncate(strings.Repeat("❤️", 5), 4); got != "❤️❤️" {
		t.Errorf("got %q", got)
	}
}

// ---------------------------------------------------------------- regressions from round 3

func TestForkRefusesSymlinkedTargetMemory(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	writeMemory(t, a, root+"/src", "mine")
	mkdir(t, root+"/shared")
	os.WriteFile(root+"/shared/MEMORY.md", []byte("shared"), 0o600)
	dstMem := filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"), "memory")
	mkdir(t, filepath.Dir(dstMem))
	os.Symlink(root+"/shared", dstMem)
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err == nil {
		t.Fatal("fork over symlinked different memory succeeded")
	}
	if st, _ := os.Lstat(dstMem); st.Mode()&os.ModeSymlink == 0 {
		t.Error("target memory symlink replaced")
	}
}

func TestForkOddTargetMemoryIsRefusedCleanly(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	writeMemory(t, a, root+"/src", "mine")
	mkdir(t, filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"), "memory", "emptysub"))
	// a memory/ with no files has nothing to clobber: copied over
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"), "memory", "MEMORY.md")); string(b) != "mine" {
		t.Errorf("memory not copied over an empty one: %q", b)
	}
	// a dangling symlink gets its own message
	mkdir(t, filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst2")))
	os.Symlink(root+"/gone", filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst2"), "memory"))
	if err := a.fork(id1, root+"/dst2", forkOpts{memory: true}); err == nil || !strings.Contains(err.Error(), "broken symlink") {
		t.Errorf("want the broken-symlink message, got %v", err)
	}
}

func TestForkFollowsSymlinkedSourceMemory(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	mkdir(t, root+"/dots")
	os.WriteFile(root+"/dots/MEMORY.md", []byte("from dotfiles"), 0o600)
	srcMem := filepath.Join(a.cfg.Projects, claude.Enc(root+"/src"), "memory")
	mkdir(t, srcMem)
	os.Symlink(root+"/dots/MEMORY.md", srcMem+"/MEMORY.md")
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"), "memory", "MEMORY.md")
	st, err := os.Lstat(got)
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("memory file not copied as a real file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "from dotfiles" {
		t.Errorf("content %q", b)
	}
}

func TestForkKeepsSessionWhenLaunchFails(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	writeMemory(t, a, root+"/src", "m")
	t.Setenv("PATH", t.TempDir()) // no claude
	if err := a.fork(id1, root+"/dst", forkOpts{launch: true, memory: true}); err == nil {
		t.Fatal("launch without claude succeeded")
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst")))
	if len(ss) != 1 {
		t.Fatalf("fork removed after the launch failed: %+v", ss)
	}
	checkSession(t, a, ss[0].ID, root+"/dst")
	if !fsutil.Exists(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"), "memory", "MEMORY.md")) {
		t.Error("memory removed after the launch failed")
	}
}

// The scratchpad lies below the cwd (claude run in /tmp's parent): its paths
// must go to the new scratchpad, not to <new dir>/<old scratch path>.
func TestForkScratchpadBelowCwd(t *testing.T) {
	a, root := sandbox(t)
	addSession(t, a, root, id1, t0) // cwd = root, scratch under root/tmp
	scratch := filepath.Join(root, "tmp", claude.Enc(root), id1, "scratchpad")
	mkdir(t, scratch)
	os.Symlink(scratch+"/plan.md", scratch+"/link")
	p := filepath.Join(a.cfg.Projects, claude.Enc(root), id1+".jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(jsonLine(t, map[string]any{"snapshot": map[string]any{"realParentDir": scratch}}))
	f.Close()
	if err := a.fork(id1, root+"/w/new", forkOpts{}); err != nil {
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/w/new")))
	newScratch := filepath.Join(root, "tmp", claude.Enc(root+"/w/new"), ss[0].ID, "scratchpad")
	if b, _ := os.ReadFile(ss[0].Path); !bytes.Contains(b, []byte(`"realParentDir":"`+newScratch+`"`)) {
		t.Errorf("realParentDir not moved to the new scratchpad:\n%s", b)
	}
	if l, _ := os.Readlink(newScratch + "/link"); l != newScratch+"/plan.md" {
		t.Errorf("scratchpad symlink not rewritten: %s", l)
	}
}

func TestMvPreflightSourceWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0)
	srcMem := writeMemory(t, a, root+"/a", "m")
	mkdir(t, filepath.Join(a.cfg.Projects, claude.Enc(root+"/b"), "memory"))
	os.Chmod(srcMem, 0o555)
	defer os.Chmod(srcMem, 0o755)
	if err := a.move(root+"/a", root+"/b", false); err == nil {
		t.Fatal("merge out of a read-only memory/ was not refused")
	}
	checkSession(t, a, id1, root+"/a")
}

func TestMvTargetFolderIsAFile(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/a")
	addSession(t, a, root+"/a", id1, t0)
	os.WriteFile(filepath.Join(a.cfg.Projects, claude.Enc(root+"/b")), []byte("junk"), 0o600)
	if err := a.move(root+"/a", root+"/b", false); err == nil {
		t.Fatal("not refused")
	}
	checkSession(t, a, id1, root+"/a")
}

func TestMvSharedFolderKeepsOtherPaths(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/w/b/c")
	mkdir(t, root+"/w/b-c")
	shared := filepath.Join(a.cfg.Projects, claude.Enc(root+"/w/b-c"))
	writeSession(t, a, root+"/w/b-c", root+"/w/b-c", id2, t0)
	mkdir(t, shared)
	p := filepath.Join(shared, id1+".jsonl")
	os.WriteFile(p, []byte(jsonLine(t, map[string]any{"cwd": root + "/w/b/c", "sessionId": id1, "mem": shared + "/memory/MEMORY.md"})), 0o600)
	if err := a.move(root+"/w/b", root+"/w/z", false); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob(filepath.Join(a.cfg.Projects, claude.Enc(root+"/w/z/c"), id1+".jsonl"))
	if b, _ := os.ReadFile(m[0]); !bytes.Contains(b, []byte(shared+"/memory/MEMORY.md")) {
		t.Errorf("reference to the shared folder's memory rewritten:\n%s", b)
	}
}

func TestLsAllInvalidUTF8NotMissing(t *testing.T) {
	a, root := sandbox(t)
	dir := root + "/a\xffb"
	mkdir(t, dir)
	writeSession(t, a, dir, claude.NodePath(dir), id1, t0)
	a.lsAll()
	if strings.Contains(output(a), "MISSING") {
		t.Errorf("existing dir shown as MISSING:\n%s", output(a))
	}
}

func TestColorModes(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/d")
	addSession(t, a, root+"/d", id1, t0)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := NewRootCmd(func() (claude.Config, error) { return a.cfg, nil }, "test")
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	for mode, want := range map[string]bool{"on": true, "off": false, "auto": false /* a buffer isn't a terminal */} {
		out, err := run("ls", root+"/d", "--color", mode)
		if err != nil || strings.Contains(out, "\x1b[") != want {
			t.Errorf("--color %s: escapes=%v err=%v\n%q", mode, strings.Contains(out, "\x1b["), err, out)
		}
	}
	if _, err := run("ls", "--color", "rainbow"); err == nil {
		t.Error("bad --color value accepted")
	}
	t.Setenv("NO_COLOR", "1")
	if p, _ := colorFor(os.Stdout, "auto"); p {
		t.Error("NO_COLOR ignored")
	}
}

func TestForkFromSourceDir(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	addSession(t, a, root+"/src", id2, t0.Add(time.Hour)) // newest
	var out bytes.Buffer
	run := func(args ...string) error {
		out.Reset()
		cmd := NewRootCmd(func() (claude.Config, error) { return a.cfg, nil }, "test")
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	t.Chdir(t.TempDir()) // the current dir has no sessions: the source must come from the arg
	if err := run("fork", root+"/src", root+"/dst"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "forked "+id2) {
		t.Errorf("didn't fork the newest session of the source dir:\n%s", out.String())
	}
	if ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst"))); len(ss) != 1 {
		t.Fatalf("got %+v", ss)
	}
	if err := run("fork", "-s", id1, root+"/src", root+"/dst2"); err == nil {
		t.Error("source dir together with --session accepted")
	}
	if err := run("fork", root+"/nope", root+"/dst3"); err == nil {
		t.Error("missing source dir accepted")
	}
}

// ---------------------------------------------------------------- regressions from round 4

// cwd = an ancestor of Claude's config (like $HOME): paths under the config
// dir are left alone, and memory refs follow to the target's memory.
func TestForkCwdAboveConfig(t *testing.T) {
	a, root := sandbox(t) // config is root/cfg
	addSession(t, a, root, id1, t0)
	srcMem := writeMemory(t, a, root, "m")
	p := filepath.Join(a.cfg.Projects, claude.Enc(root), id1+".jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(jsonLine(t, map[string]any{"a": map[string]any{"realParentDir": a.cfg.Dir}}))
	f.WriteString(jsonLine(t, map[string]any{"b": map[string]any{"realParentDir": srcMem}}))
	f.Close()
	writeMemory(t, a, root+"/new", "m") // identical memory already there
	if err := a.fork(id1, root+"/new", forkOpts{memory: true}); err != nil {
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/new")))
	b, _ := os.ReadFile(ss[0].Path)
	if !bytes.Contains(b, []byte(`"realParentDir":"`+a.cfg.Dir+`"`)) {
		t.Errorf("config dir path rewritten:\n%s", b)
	}
	if !bytes.Contains(b, []byte(`"realParentDir":"`+filepath.Join(a.cfg.Projects, claude.Enc(root+"/new"), "memory")+`"`)) {
		t.Errorf("memory path not moved to the target's memory:\n%s", b)
	}
}

func TestForkSkipsDanglingMemoryLink(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	mem := writeMemory(t, a, root+"/src", "m")
	os.Symlink(root+"/gone.md", mem+"/old.md")
	if err := a.fork(id1, root+"/dst", forkOpts{memory: true}); err != nil {
		t.Fatalf("dangling link blocked the fork: %v", err)
	}
}

func TestForkSidecarNonTranscriptsByteExact(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/src")
	addSession(t, a, root+"/src", id1, t0)
	raw := `{"cwd":"` + root + `/src/sub","sessionId":"` + id1 + `"}`
	side := filepath.Join(a.cfg.Projects, claude.Enc(root+"/src"), id1, "tool-results", "out.txt")
	os.WriteFile(side, []byte(raw), 0o600)
	if err := a.fork(id1, root+"/dst", forkOpts{}); err != nil {
		t.Fatal(err)
	}
	ss := claude.SessionsIn(filepath.Join(a.cfg.Projects, claude.Enc(root+"/dst")))
	got, _ := os.ReadFile(filepath.Join(strings.TrimSuffix(ss[0].Path, ".jsonl"), "tool-results", "out.txt"))
	if string(got) != raw {
		t.Errorf("tool output changed: %s", got)
	}
}

func TestEmptyArgsRefused(t *testing.T) {
	a, root := sandbox(t)
	mkdir(t, root+"/d")
	run := func(args ...string) error {
		cmd := NewRootCmd(func() (claude.Config, error) { return a.cfg, nil }, "test")
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	for _, args := range [][]string{{"fork", ""}, {"fork", root + "/d", ""}, {"mv", "", "x"}, {"mv", root + "/d", ""}, {"ls", ""}} {
		if err := run(args...); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("%q: %v", args, err)
		}
	}
}

func TestColorArg(t *testing.T) {
	for args, want := range map[string]string{"x": "auto", "--color=off x": "off", "--color on ls": "on", "ls -- --color=off": "auto"} {
		if got := colorArg(strings.Fields(args)); got != want {
			t.Errorf("%q: %s, want %s", args, got, want)
		}
	}
}
