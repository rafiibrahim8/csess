package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAbsPath(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(root+"/real/sub", 0o755)
	os.Symlink(root+"/real", root+"/link")
	for in, want := range map[string]string{
		root + "/link/sub":           root + "/real/sub", // symlinks resolved
		root + "/link/new/../x":      root + "/real/x",   // '..' in the missing part: lexical
		root + "/real/missing/../..": root,               // more '..' than missing parts
		root + "/real//sub/./":       root + "/real/sub", // stray slashes and dots
		root + "/nope/a/b":           root + "/nope/a/b", // nothing exists
		root + "/link/sub/../y":      root + "/real/y",   // existing '..' resolved physically
	} {
		if got, err := AbsPath(in); err != nil || got != want {
			t.Errorf("AbsPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := AbsPath(root + "/a\nb"); err == nil {
		t.Error("newline in path accepted")
	}
}

func TestRewriteFileKeepsModeAndMtime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("old"), 0o600)
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(p, when, when)
	if err := RewriteFile(p, func([]byte) []byte { return []byte("new") }); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != "new" || st.Mode().Perm() != 0o600 || !st.ModTime().Equal(when) {
		t.Errorf("content %q mode %v mtime %v", b, st.Mode(), st.ModTime())
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"/a/b-c.d": "/a/b-c.d", "/a b": "'/a b'", "it's": `'it'\''s'`,
		"/x\x1b]0;t\a'y": `$'/x\x1b]0;t\x07\'y'`, "/\xff": `$'/\xff'`} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
