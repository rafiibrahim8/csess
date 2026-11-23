// Package claude knows Claude Code's on-disk layout: where sessions live,
// how folder names are derived from paths, what transcripts contain, and the
// per-project entries in .claude.json.
package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Config says where Claude keeps its state.
type Config struct {
	Dir        string // CLAUDE_CONFIG_DIR or ~/.claude
	Projects   string // <Dir>/projects: one folder of sessions per directory
	History    string // <Dir>/history.jsonl: up-arrow prompt history
	ClaudeJSON string // <CLAUDE_CONFIG_DIR or $HOME>/.claude.json: per-project settings
	// TempRoots are where Claude keeps per-session temp dirs:
	// <root>/<folder name>/<session id>/scratchpad.
	TempRoots []string
}

// LoadConfig honors CLAUDE_CONFIG_DIR the way Claude does.
func LoadConfig() (Config, error) {
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		// Claude keeps .claude.json inside CLAUDE_CONFIG_DIR when that is set.
		dir, err := filepath.Abs(env)
		if err != nil {
			return Config{}, err
		}
		return NewConfig(dir, filepath.Join(dir, ".claude.json")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	return NewConfig(filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")), nil
}

func NewConfig(dir, claudeJSON string) Config {
	uid := "claude-" + strconv.Itoa(os.Getuid())
	roots := []string{filepath.Join("/tmp", uid)}
	if t := filepath.Join(os.TempDir(), uid); t != roots[0] {
		roots = append(roots, t) // macOS: $TMPDIR
	}
	return Config{
		Dir:        dir,
		Projects:   filepath.Join(dir, "projects"),
		History:    filepath.Join(dir, "history.jsonl"),
		ClaudeJSON: claudeJSON,
		TempRoots:  roots,
	}
}

// Scratchpad returns the scratchpad of a session, given its projects folder,
// and the temp root it is in; "" if it has none.
func (c Config) Scratchpad(folder, id string) (dir, root string) {
	for _, r := range c.TempRoots {
		if d := filepath.Join(r, filepath.Base(folder), id, "scratchpad"); fsDir(d) {
			return d, r
		}
	}
	return "", ""
}

func fsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// MaxEnc: Claude truncates longer folder names and appends a hash we can't
// reproduce, so such folders are only found by scanning (see FolderFor).
const MaxEnc = 200

// Enc is Claude Code's folder-name rule: path.replace(/[^a-zA-Z0-9]/g, '-').
// JS regexes without the u flag work on UTF-16 code units, so a character
// outside the BMP (emoji) becomes two dashes. Node decodes invalid UTF-8 in
// the path to one U+FFFD per maximal ill-formed subsequence (one dash each).
func Enc(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRuneInString(p[i:])
		if r == utf8.RuneError && size <= 1 {
			size = maximalSubpart(p[i:])
		}
		switch {
		case r < 0x80 && isAlnum(byte(r)):
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
		i += size
	}
	return b.String()
}

// maximalSubpart is the length of the ill-formed UTF-8 sequence at the start
// of s that a WHATWG decoder (Node's) replaces with a single U+FFFD: the lead
// byte plus the continuation bytes that could still start a valid sequence.
func maximalSubpart(s string) int {
	c := s[0]
	n, lo, hi := 0, byte(0x80), byte(0xBF)
	switch {
	case c >= 0xC2 && c <= 0xDF:
		n = 2
	case c == 0xE0:
		n, lo = 3, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		n = 3
	case c == 0xED:
		n, hi = 3, 0x9F
	case c == 0xF0:
		n, lo = 4, 0x90
	case c >= 0xF1 && c <= 0xF3:
		n = 4
	case c == 0xF4:
		n, hi = 4, 0x8F
	default:
		return 1
	}
	i := 1
	for ; i < n && i < len(s) && s[i] >= lo && s[i] <= hi; i++ {
		lo, hi = 0x80, 0xBF
	}
	return i
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// FolderFor returns the projects folder Claude uses for dir. For names over
// MaxEnc it finds an existing folder whose sessions were started in dir.
func (c Config) FolderFor(dir string) (string, error) {
	e := Enc(dir)
	if len(e) <= MaxEnc {
		return filepath.Join(c.Projects, e), nil
	}
	entries, _ := os.ReadDir(c.Projects)
	for _, ent := range entries {
		if !ent.IsDir() || !strings.HasPrefix(ent.Name(), e[:MaxEnc]) {
			continue
		}
		f := filepath.Join(c.Projects, ent.Name())
		for _, s := range SessionsIn(f) {
			if CwdOf(s.Path) == dir {
				return f, nil
			}
		}
	}
	return "", fmt.Errorf("%s: path too long — Claude hashes the folder names of such paths and no existing folder matches; start claude there once first", dir)
}

// ResolveAll lists the existing dirs under d that the encoded remainder r
// ("-src-my-app") can stand for, walking the real tree: my-app and my/app may
// both exist. Pass d="/" and a full folder name to get every candidate.
func ResolveAll(d, r string) []string {
	if r == "" {
		return []string{d}
	}
	var found []string
	entries, _ := os.ReadDir(d)
	for _, ent := range entries {
		if !ent.IsDir() { // symlinks aren't followed: Claude records physical paths
			continue
		}
		c := filepath.Join(d, ent.Name())
		e := "-" + Enc(ent.Name())
		if r == e {
			found = append(found, c)
		} else if strings.HasPrefix(r, e+"-") {
			found = append(found, ResolveAll(c, r[len(e):])...)
		}
	}
	return found
}

// AllSessions lists the sessions of every folder, newest first.
func (c Config) AllSessions() []Session {
	var all []Session
	folders, _ := os.ReadDir(c.Projects)
	for _, f := range folders {
		all = append(all, SessionsIn(filepath.Join(c.Projects, f.Name()))...)
	}
	SortNewest(all)
	return all
}
