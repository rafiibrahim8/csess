package claude

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// JSString escapes s exactly like JavaScript's JSON.stringify (without the
// quotes), so paths can be matched byte-for-byte inside Claude's JSON files.
func JSString(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// replace replaces each occurrence of needle with repl for which ok(i, end)
// is true, where data[i:end] is the occurrence.
func replace(data []byte, needle, repl string, ok func(i, end int) bool) []byte {
	n := []byte(needle)
	var out []byte
	start, from, replaced := 0, 0, false
	for {
		i := bytes.Index(data[from:], n)
		if i < 0 {
			break
		}
		i += from
		end := i + len(n)
		if ok(i, end) {
			out = append(out, data[start:i]...)
			out = append(out, repl...)
			start, from, replaced = end, end, true
		} else {
			from = i + 1
		}
	}
	if !replaced {
		return data
	}
	return append(out, data[start:]...)
}

// ReplaceBounded replaces needle with repl where the byte after needle is in
// boundary (any byte if boundary is empty), so /w/a doesn't match /w/ab.
func ReplaceBounded(data []byte, needle, repl, boundary string) []byte {
	return replace(data, needle, repl, func(_, end int) bool {
		return boundary == "" || end < len(data) && strings.IndexByte(boundary, data[end]) >= 0
	})
}

// PathEnd lists the bytes that can follow a path inside a transcript's text.
const PathEnd = "/.\"\\ ,:;)'`"

// ReplacePath replaces the JSON-escaped path old with to wherever it appears
// as a whole path or a prefix of one: the byte before must not continue a
// path (so /mnt/x/home/a doesn't match /home/a), and the byte after must be
// in right (e.g. "/" so /w/a doesn't match /w/ab) or the end of data.
func ReplacePath(data []byte, old, to, right string) []byte {
	return replace(data, JSString(old), JSString(to), func(i, end int) bool {
		leftOK := i == 0 || !pathByte(data[i-1]) || i >= 2 && data[i-2] == '\\' // after \n, \t, ...
		return leftOK && (end == len(data) || strings.IndexByte(right, data[end]) >= 0)
	})
}

func pathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._-~/", c) >= 0 || c >= 0x80
}

// NodePath is p as Claude (Node.js) records it: invalid UTF-8 decoded to one
// U+FFFD per maximal ill-formed subsequence. Compare transcripts against this.
func NodePath(p string) string {
	if utf8.ValidString(p) {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRuneInString(p[i:])
		if r == utf8.RuneError && size <= 1 {
			size = maximalSubpart(p[i:])
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// dirKeys are the transcript fields that hold a directory the session works
// in: its working dir, and the dir of each file /rewind has a backup of.
var dirKeys = []string{"cwd", "realParentDir"}

// MoveDir rewrites those fields where they are old or below it, except
// values inside one of the skip dirs (which get their own exact rewrite).
func MoveDir(data []byte, old, to string, skip ...string) []byte {
	for _, k := range dirKeys {
		prefix := `"` + k + `":"`
		data = replace(data, prefix+JSString(old), prefix+JSString(to), func(i, end int) bool {
			if end >= len(data) || data[end] != '"' && data[end] != '/' {
				return false
			}
			value := data[i+len(prefix):]
			for _, s := range skip {
				if e := JSString(s); bytes.HasPrefix(value, []byte(e)) &&
					len(value) > len(e) && (value[len(e)] == '"' || value[len(e)] == '/') {
					return false
				}
			}
			return true
		})
	}
	return data
}
