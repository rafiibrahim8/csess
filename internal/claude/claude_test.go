package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnc(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/my.app-x": "-home-u-my-app-x",
		"/a/é":             "-a--",  // BMP char: one UTF-16 unit
		"/a/😀":             "-a---", // astral char: two UTF-16 units, like JS
		"/a\xe2\x82b":      "-a-b",  // invalid UTF-8: one U+FFFD per maximal subpart, like Node
		"/a\xff\xffb":      "-a--b",
		"/a\xf0\x9f\x98":   "-a-",
	} {
		if got := Enc(in); got != want {
			t.Errorf("Enc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSStringRoundTrips(t *testing.T) {
	for _, s := range []string{`/plain/path`, `/we"ird\dir`, "/tab\there", "/ctl\x01", "/ünï/😀"} {
		var back string
		if err := json.Unmarshal([]byte(`"`+JSString(s)+`"`), &back); err != nil || back != s {
			t.Errorf("JSString(%q) doesn't round-trip: %v %q", s, err, back)
		}
	}
	if got := JSString("\x01"); got != `\u0001` { // JSON.stringify's spelling
		t.Errorf("control char: %s", got)
	}
}

func TestMoveDirOnlyTouchesDirAndSubdirs(t *testing.T) {
	in := `{"cwd":"/w/a"} {"cwd":"/w/a/sub"} {"cwd":"/w/ab"} {"cwd":"/w/a-b"} {"realParentDir":"/w/a/x"} {"text":"/w/a"}`
	want := `{"cwd":"/z"} {"cwd":"/z/sub"} {"cwd":"/w/ab"} {"cwd":"/w/a-b"} {"realParentDir":"/z/x"} {"text":"/w/a"}`
	if got := MoveDir([]byte(in), "/w/a", "/z"); string(got) != want {
		t.Errorf("got %s", got)
	}
}

func TestScanTitle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"user","cwd":"/w"}
{"type":"ai-title","aiTitle":"auto"}
{"type":"custom-title","customTitle":"  "}
{"type":"last-prompt","lastPrompt":"prompt"}
`), 0o600)
	if got := Scan(p, true); got.Title != "auto" || got.Cwd != "/w" { // a blank rename doesn't count
		t.Errorf("got %+v", got)
	}
	if got := CleanTitle("a\tb\n\x1b[31mred\x1b[0m  c"); got != "a b [31mred[0m c" {
		t.Errorf("CleanTitle: %q", got)
	}
}

func TestRenameProjects(t *testing.T) {
	in := `{"z":1,"projects":{"/a":{"t":true},"/a/sub":{"u":2},"/a-x":{},"/b":{"stale":1}},` +
		`"githubRepoPaths":{"o/r":["/a/sub","/c"]},"s":"é"}`
	out, n, replaced, err := RenameProjects([]byte(in), "/a", "/b")
	if err != nil || n != 3 || len(replaced) != 1 || replaced[0] != "/b" {
		t.Fatalf("n=%d replaced=%v err=%v", n, replaced, err)
	}
	// key order kept, sibling /a-x untouched, the stale /b entry replaced
	keys := []string{`"z"`, `"projects"`, `"/b": {`, `"t": true`, `"/b/sub"`, `"/a-x"`, `"githubRepoPaths"`, `"s"`}
	last := -1
	for _, k := range keys {
		i := bytes.Index(out, []byte(k))
		if i <= last {
			t.Fatalf("%s missing or out of order in\n%s", k, out)
		}
		last = i
	}
	if bytes.Contains(out, []byte("stale")) || !bytes.Contains(out, []byte(`"/b/sub",`)) {
		t.Errorf("wrong result:\n%s", out)
	}

	same, n, _, _ := RenameProjects([]byte(`{"projects":{"/x":{}}}`), "/a", "/b")
	if n != 0 || string(same) != `{"projects":{"/x":{}}}` {
		t.Errorf("unrelated file changed: %s", same)
	}
}

func TestRenameProjectsEdgeCases(t *testing.T) {
	for _, bad := range []string{``, `[1]`, `{"a":1} {"b":2}`, `{"projects":[]}`} {
		if _, _, _, err := RenameProjects([]byte(bad), "/a", "/b"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	out, _, _, err := RenameProjects([]byte(`{"githubRepoPaths":{"o/r":["/a","/b"]}}`), "/a", "/b")
	if err != nil || bytes.Count(out, []byte(`"/b"`)) != 1 {
		t.Errorf("duplicates after rename: %s %v", out, err)
	}
}

func TestSessionsInOnlyUUIDs(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"11111111-1111-4111-8111-111111111111.jsonl", "agent-a1b2.jsonl", "user.jsonl", ".csess-fork-x.jsonl"} {
		os.WriteFile(filepath.Join(d, n), nil, 0o600)
	}
	if ss := SessionsIn(d); len(ss) != 1 {
		t.Errorf("got %+v", ss)
	}
}

func TestCleanTitleFallsBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"ai-title","aiTitle":"real"}
{"type":"custom-title","customTitle":"\u001b\u0007"}
`), 0o600)
	if got := Scan(p, true).Title; got != "real" {
		t.Errorf("got %q", got)
	}
	if got := CleanTitle("abc\u202edcba\u200b"); got != "abcdcba" {
		t.Errorf("bidi/zero-width kept: %q", got)
	}
}

func TestResolveAll(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(root+"/a/b", 0o755)
	os.MkdirAll(root+"/a-b", 0o755)
	got := ResolveAll("/", Enc(root+"/a/b"))
	if len(got) != 2 {
		t.Errorf("want both a/b and a-b, got %v", got)
	}
}

func TestLoadConfigWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "/x/cfg")
	if c, err := LoadConfig(); err != nil || c.ClaudeJSON != "/x/cfg/.claude.json" {
		t.Errorf("%+v %v", c, err)
	}
}

func TestReplacePathBoundaries(t *testing.T) {
	in := `"/p/-w-a/x" "/mnt/b/p/-w-a/x" "see /p/-w-a/y" "\n/p/-w-a/z" "/p/-w-ab/x"`
	want := `"/p/-w-b/x" "/mnt/b/p/-w-a/x" "see /p/-w-b/y" "\n/p/-w-b/z" "/p/-w-ab/x"`
	if got := ReplacePath([]byte(in), "/p/-w-a", "/p/-w-b", "/"); string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestResolveAllIgnoresSymlinks(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(root+"/a", 0o755)
	os.Symlink(".", root+"/a/x")
	if got := ResolveAll("/", Enc(root+"/a/x/x/x")); len(got) != 0 {
		t.Errorf("followed a symlink loop: %v", got)
	}
}
