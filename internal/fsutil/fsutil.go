// Package fsutil has the path and file helpers csess needs beyond the
// standard library: physical paths for dirs that don't exist yet, atomic
// rewrites that keep modification times, and moves across filesystems.
package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

func IsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Exists reports whether p exists, without following a final symlink.
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// Under reports whether p is dir itself or inside it.
func Under(p, dir string) bool {
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

func TrimSlash(p string) string {
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// AbsPath returns the absolute physical path (symlinks resolved, like pwd -P)
// of p, which need not exist. '.' and '..' in the part that doesn't exist yet
// are resolved lexically; in the existing part the kernel resolves them.
func AbsPath(p string) (string, error) {
	if strings.ContainsRune(p, '\n') {
		return "", errors.New("paths containing newlines are not supported")
	}
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = wd + "/" + p
	}
	p = TrimSlash(p)
	var rest []string
	skip := 0
	for !IsDir(p) {
		i := strings.LastIndexByte(p, '/')
		base := p[i+1:]
		p = TrimSlash(p[:i])
		if p == "" {
			p = "/"
		}
		switch {
		case base == "" || base == ".":
		case base == "..":
			skip++
		case skip > 0:
			skip--
		default:
			rest = append([]string{base}, rest...)
		}
	}
	for ; skip > 0; skip-- {
		p += "/.."
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if real, err = filepath.Abs(real); err != nil {
		return "", err
	}
	return filepath.Join(append([]string{real}, rest...)...), nil
}

// RewriteFile applies f to the file's contents and atomically replaces it if
// anything changed, keeping its mode and modification time (Claude picks the
// session for 'claude -c' by recency, so mtimes must survive a rewrite).
func RewriteFile(path string, f func([]byte) []byte) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := f(data)
	if bytes.Equal(out, data) {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := WriteAtomic(path, out, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(path, time.Time{}, st.ModTime())
}

// WriteAtomic writes data to a temp file next to path and renames it over
// path. A symlinked path is followed, so the link itself stays a link.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".csess-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Pending is a rewritten file written next to the original but not yet
// swapped in, so a batch of rewrites can be applied all-or-nothing.
type Pending struct {
	Path, tmp string
	mtime     time.Time
}

// PrepareRewrite writes f(contents of path) to a hidden temp file next to
// path, with the same mode. It returns nil if f changes nothing.
func PrepareRewrite(path string, f func([]byte) []byte) (*Pending, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := f(data)
	if bytes.Equal(out, data) {
		return nil, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".csess-new-*")
	if err != nil {
		return nil, err
	}
	_, werr := tmp.Write(out)
	cerr := tmp.Chmod(st.Mode().Perm())
	if err := errors.Join(werr, cerr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return &Pending{Path: path, tmp: tmp.Name(), mtime: st.ModTime()}, nil
}

// Commit swaps the rewritten file in, keeping the original's mtime.
func (p *Pending) Commit() error {
	if err := os.Rename(p.tmp, p.Path); err != nil {
		return err
	}
	return os.Chtimes(p.Path, time.Time{}, p.mtime)
}

// Discard removes the rewritten file, leaving the original untouched.
func (p *Pending) Discard() { os.Remove(p.tmp) }

// Move renames src to dst, falling back to mv(1) across filesystems.
func Move(src, dst string) error {
	err := os.Rename(src, dst)
	if errors.Is(err, syscall.EXDEV) {
		out, err := exec.Command("mv", "--", src, dst).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mv %s %s: %v: %s", src, dst, err, bytes.TrimSpace(out))
		}
		return nil
	}
	return err
}

// ShellQuote quotes s for bash/zsh, only when needed. Text with control
// characters or invalid UTF-8 uses $'...' with escapes, so the result is
// safe to print to a terminal and still pastes back as the same path.
func ShellQuote(s string) string {
	safe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%,", r)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	printable := utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) && r != ' ' }) < 0
	if printable {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1, r < 0x80 && !unicode.IsPrint(r):
			fmt.Fprintf(&b, `\x%02x`, s[i])
			size = 1
		case r == '\'' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case !unicode.IsPrint(r):
			for j := 0; j < size; j++ {
				fmt.Fprintf(&b, `\x%02x`, s[i+j])
			}
		default:
			b.WriteRune(r)
		}
		i += size
	}
	b.WriteByte('\'')
	return b.String()
}
