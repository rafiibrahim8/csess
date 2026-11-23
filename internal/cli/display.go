package cli

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// display makes a path or name from disk safe to print: shown as is when it
// is plain printable text (spaces included, so it can be copied back), else
// Go-quoted so escape sequences, newlines and bidi tricks are visible
// instead of acting on the terminal.
func display(s string) string {
	if utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return s
	}
	return strconv.Quote(s)
}

// truncate cuts s to at most n terminal columns (wide CJK characters take
// two, and so does a character followed by the emoji variation selector).
func truncate(s string, n int) string {
	cols := 0
	rs := []rune(s)
	for i, r := range rs {
		w := runeWidth(r)
		if w == 1 && i+1 < len(rs) && rs[i+1] == '\uFE0F' {
			w = 2
		}
		if cols+w > n {
			return string(rs[:i])
		}
		cols += w
	}
	return s
}

func runeWidth(r rune) int {
	switch {
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case width.LookupRune(r).Kind() == width.EastAsianWide, width.LookupRune(r).Kind() == width.EastAsianFullwidth:
		return 2
	}
	return 1
}
