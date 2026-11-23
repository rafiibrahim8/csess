package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Session is one transcript: <Projects>/<folder>/<ID>.jsonl, plus an optional
// <ID>/ sidecar dir with subagent transcripts and tool results.
type Session struct {
	Path    string
	ID      string
	ModTime time.Time
}

// Session ids are UUIDs; the file name is the id.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SessionsIn lists the sessions of one projects folder, newest first — the
// first one is what 'claude -c' resumes.
func SessionsIn(folder string) []Session {
	entries, _ := os.ReadDir(folder)
	var ss []Session
	for _, e := range entries {
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".jsonl")
		if !ok || !uuidRe.MatchString(id) || !e.Type().IsRegular() {
			continue // not a session: legacy agent-*.jsonl sidechains, temp files, ...
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ss = append(ss, Session{filepath.Join(folder, name), id, info.ModTime()})
	}
	SortNewest(ss)
	return ss
}

func SortNewest(ss []Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		if !ss[i].ModTime.Equal(ss[j].ModTime) {
			return ss[i].ModTime.After(ss[j].ModTime)
		}
		return ss[i].Path < ss[j].Path
	})
}

// Info is what a transcript tells us beyond its file name.
type Info struct {
	Cwd   string // first recorded working dir = the dir the session was started in
	Title string // /rename title > Claude's auto title > last prompt; "" if none
}

// line holds the top-level fields we care about in a transcript line.
type line struct {
	Type        string  `json:"type"`
	Cwd         *string `json:"cwd"`
	CustomTitle string  `json:"customTitle"`
	AiTitle     string  `json:"aiTitle"`
	LastPrompt  string  `json:"lastPrompt"`
}

var (
	markCwd   = []byte(`"cwd":`)
	markTitle = [][]byte{[]byte(`"custom-title"`), []byte(`"ai-title"`), []byte(`"last-prompt"`)}
)

// Scan reads a transcript. With wantTitle=false it stops at the first cwd.
// Lines are only JSON-decoded when a cheap byte search says they're relevant.
func Scan(path string, wantTitle bool) Info {
	f, err := os.Open(path)
	if err != nil {
		return Info{}
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var info Info
	var custom, ai, prompt string
	for {
		raw, err := r.ReadBytes('\n')
		needCwd := info.Cwd == "" && bytes.Contains(raw, markCwd)
		needTitle := wantTitle && containsAny(raw, markTitle)
		if needCwd || needTitle {
			var l line
			if json.Unmarshal(raw, &l) == nil {
				if info.Cwd == "" && l.Cwd != nil {
					info.Cwd = *l.Cwd
				}
				// A title that is blank once cleaned doesn't hide the next-best one.
				switch l.Type {
				case "custom-title":
					custom = orKeep(CleanTitle(l.CustomTitle), custom)
				case "ai-title":
					ai = orKeep(CleanTitle(l.AiTitle), ai)
				case "last-prompt":
					prompt = orKeep(CleanTitle(l.LastPrompt), prompt)
				}
			}
		}
		if err != nil || !wantTitle && info.Cwd != "" {
			break
		}
	}
	for _, t := range []string{custom, ai, prompt} {
		if t != "" {
			info.Title = t
			break
		}
	}
	return info
}

func containsAny(b []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(b, n) {
			return true
		}
	}
	return false
}

// CwdOf returns the dir the session was started in, or "".
func CwdOf(path string) string { return Scan(path, false).Cwd }

// CleanTitle makes text safe for one terminal line: whitespace runs become
// one space; control and format characters (incl. the ESC of ANSI sequences
// and bidi overrides) and invalid UTF-8 are dropped.
func CleanTitle(t string) string {
	t = strings.ToValidUTF8(t, "")
	var b strings.Builder
	space := false
	for _, r := range t {
		switch {
		case unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError:
			// Cf: bidi overrides and zero-width chars, which can disguise text
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// orKeep returns s, or prev when s is empty.
func orKeep(s, prev string) string {
	if s == "" {
		return prev
	}
	return s
}
