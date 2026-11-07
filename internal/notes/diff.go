package notes

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type noteEdits struct {
	drop      bool
	additions []string
}
type patchLine struct {
	kind byte
	text string
}

// MarkdownDiffs creates reviewable, applicable patches against the indexed bytes.
func MarkdownDiffs(dir string, list []Note) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	dir, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	byTitle := map[string][]Note{}
	edits := map[string]*noteEdits{}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, n := range list {
		byTitle[fold(n.Title)] = append(byTitle[fold(n.Title)], n)
		edits[n.Path] = &noteEdits{}
		if strings.ContainsAny(n.ID, "\r\n") {
			return "", fmt.Errorf("cannot propose a link to a multiline note ID")
		}
	}
	for _, group := range byTitle {
		if len(group) < 2 {
			continue
		}
		keep, drop := group[0], group[1]
		if keep.Type != drop.Type {
			continue
		}
		edits[keep.Path].additions = append(edits[keep.Path].additions, "Merged with [["+drop.ID+"]].")
		edits[drop.Path].drop = true
	}
	for _, n := range list {
		linked := knownLinkTargets(n)
		for _, other := range list {
			if other.ID == n.ID {
				continue
			}
			if _, ok := linked[fold(other.ID)]; ok {
				continue
			}
			if _, ok := linked[fold(other.Title)]; ok {
				continue
			}
			if wordHas(n.Body, other.Title) || wordHas(n.Body, other.ID) {
				edits[n.Path].additions = append(edits[n.Path].additions, "See [["+other.ID+"]].")
				break
			}
		}
	}
	var b strings.Builder
	for _, n := range list {
		e := edits[n.Path]
		if !e.drop && len(e.additions) == 0 {
			continue
		}
		raw, err := readRootNote(root, dir, n.Path)
		if err != nil {
			return "", err
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != n.Hash {
			return "", fmt.Errorf("note changed since indexing: %s", n.Path)
		}
		rel, err := filepath.Rel(dir, n.Path)
		if err != nil {
			return "", err
		}
		writeNotePatch(&b, filepath.ToSlash(rel), string(raw), e)
	}
	return b.String(), nil
}

func patchPath(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 32 || c == 127:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func writeNotePatch(b *strings.Builder, path, source string, e *noteEdits) {
	lines := strings.SplitAfter(source, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	nl := "\n"
	if strings.Contains(source, "\r\n") {
		nl = "\r\n"
	}
	var ops []patchLine
	add := func(k byte, t string) { ops = append(ops, patchLine{k, t}) }
	status, end := -1, -1
	if len(lines) > 0 && strings.TrimRight(lines[0], "\r\n") == "---" {
		for i := 1; i < len(lines); i++ {
			line := strings.TrimRight(lines[i], "\r\n")
			if line == "---" {
				end = i
				break
			}
			key, _, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(strings.TrimSpace(key), "status") {
				status = i
			}
		}
	}
	if e.drop && end < 0 {
		for _, v := range []string{"---", "status: dropped", "---"} {
			add('+', v+nl)
		}
	}
	for i, line := range lines {
		if e.drop && end == i && status < 0 {
			add('+', "status: dropped"+nl)
		}
		replacement := line
		if e.drop && end >= 0 && i == status {
			old := strings.TrimRight(line, "\r\n")
			key, value, _ := strings.Cut(old, ":")
			before := withoutYAMLComment(value)
			comment := value[len(before):]
			if comment != "" {
				comment = " " + comment
			}
			replacement = key + ": dropped" + comment + line[len(old):]
		}
		if i == len(lines)-1 && len(e.additions) > 0 && !strings.HasSuffix(replacement, "\n") {
			replacement += nl
		}
		if replacement != line {
			add('-', line)
			add('+', replacement)
		} else {
			add(' ', line)
		}
	}
	for _, v := range e.additions {
		add('+', v+nl)
	}
	old, new := 1, 1
	header := false
	for i := 0; i < len(ops); {
		first := i
		for first < len(ops) && ops[first].kind == ' ' {
			first++
		}
		if first == len(ops) {
			break
		}
		start := max(i, first-3)
		old += start - i
		new += start - i
		stop := first + 1
		for stop < len(ops) {
			next := stop
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			if next == len(ops) || next-stop > 6 {
				stop = min(len(ops), stop+3)
				break
			}
			stop = next + 1
		}
		oldN, newN := 0, 0
		for _, o := range ops[start:stop] {
			if o.kind != '+' {
				oldN++
			}
			if o.kind != '-' {
				newN++
			}
		}
		if !header {
			fmt.Fprintf(b, "--- %s\n+++ %s\n", patchPath("a/"+path), patchPath("b/"+path))
			header = true
		}
		os, ns := old, new
		if oldN == 0 {
			os--
		}
		if newN == 0 {
			ns--
		}
		fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", os, oldN, ns, newN)
		for _, o := range ops[start:stop] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			if !strings.HasSuffix(o.text, "\n") {
				b.WriteString("\n\\ No newline at end of file\n")
			}
		}
		old += oldN
		new += newN
		i = stop
	}
}

// knownLinkTargets is the set of notes this note already points at. Dream
// passes Store.All(), whose Links come from the index, and the body still
// contains the wikilink when that field was not filled.
func knownLinkTargets(note Note) map[string]struct{} {
	linked := map[string]struct{}{}
	add := func(target string) {
		target = fold(target)
		if target == "" {
			return
		}
		linked[target] = struct{}{}
	}
	for _, target := range note.Links {
		add(target)
	}
	for _, target := range wikiLinks(note.Body) {
		add(target)
	}
	return linked
}
