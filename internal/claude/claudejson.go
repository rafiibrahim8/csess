package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/rafiibrahim8/csess/internal/fsutil"
)

// RenameProjects rewrites the keys of .projects (and paths in
// .githubRepoPaths) of a .claude.json from dir old to dir to, including
// entries for subdirs. Key order and all other values are kept; the result is
// indented the way Claude writes the file. n is the number of entries
// changed; replaced lists stale entries that a moved one took the place of.
func RenameProjects(data []byte, old, to string) (out []byte, n int, replaced []string, err error) {
	top, err := readObject(data)
	if err != nil {
		return nil, 0, nil, err
	}
	move := func(p string) (string, bool) {
		if fsutil.Under(p, old) {
			return to + p[len(old):], true
		}
		return p, false
	}
	for i := range top {
		switch top[i].key {
		case "projects":
			proj, err := readObject(top[i].val)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("projects: %v", err)
			}
			moved := map[string]bool{}
			for j := range proj {
				if k, ok := move(proj[j].key); ok {
					proj[j].key, proj[j].moved = k, true
					moved[k] = true
					n++
				}
			}
			kept := proj[:0]
			for _, m := range proj {
				if !m.moved && moved[m.key] { // a stale entry the moved one replaces
					replaced = append(replaced, m.key)
					continue
				}
				kept = append(kept, m)
			}
			top[i].val = writeObject(kept)
		case "githubRepoPaths":
			repos, err := readObject(top[i].val)
			if err != nil {
				continue
			}
			for j := range repos {
				var paths []string
				if json.Unmarshal(repos[j].val, &paths) != nil {
					continue
				}
				changed, seen, kept := false, map[string]bool{}, paths[:0]
				for _, p := range paths {
					p, ok := move(p)
					changed = changed || ok
					if !seen[p] { // old and new may both have been listed
						seen[p] = true
						kept = append(kept, p)
					}
				}
				if changed {
					repos[j].val = writeStrings(kept)
					n++
				}
			}
			top[i].val = writeObject(repos)
		}
	}
	if n == 0 {
		return data, 0, nil, nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, writeObject(top), "", "  "); err != nil {
		return nil, 0, nil, err
	}
	if bytes.HasSuffix(data, []byte("\n")) {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), n, replaced, nil
}

// member is one key of a JSON object, with its value kept raw.
type member struct {
	key   string
	val   json.RawMessage
	moved bool
}

// readObject splits a JSON object into its members, in order.
func readObject(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var ms []member
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		ms = append(ms, member{key: t.(string), val: raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return ms, nil
}

func writeObject(ms []member) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + JSString(m.key) + `":`)
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func writeStrings(ss []string) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + JSString(s) + `"`)
	}
	b.WriteByte(']')
	return b.Bytes()
}
