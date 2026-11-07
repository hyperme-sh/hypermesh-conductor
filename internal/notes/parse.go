// Package notes is the on-disk linked-note index.
//
// Markdown in a configured directory is the source of truth. The SQLite index
// is derived from those files and can be rebuilt. Note bodies stay on this
// machine except for the neighborhood copied into a model request.
package notes

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Types are the note verbs. They are labels for local notes, not imported code.
var Types = map[string]string{
	"goal": "goal", "goals": "goal",
	"step": "steps", "steps": "steps",
	"done": "done",
	"drop": "drop",
	"fact": "fact",
	"note": "note",
	"find": "find",
	"ask":  "ask",
	"show": "show",
}

var linkPattern = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]`)

// Note is one markdown file.
type Note struct {
	ID     string
	Path   string
	Title  string
	Type   string
	Status string
	Order  int
	Tags   []string
	Body   string
	Hash   string
	Links  []string
	// idFromName is true when the file omitted an id and the basename was used.
	idFromName bool
}

// Parse reads Obsidian-style markdown: YAML frontmatter, a title, and wikilinks.
func Parse(path string, data []byte) Note {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	sum := sha256.Sum256(data)
	meta, body := splitFrontmatter(text)
	title := titleOf(body, path)
	id := strings.TrimSpace(meta["id"])
	fromName := false
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		fromName = true
	}
	kind := Types[strings.ToLower(strings.TrimSpace(meta["type"]))]
	if kind == "" {
		kind = "note"
		if raw := strings.TrimSpace(meta["type"]); raw != "" {
			kind = strings.ToLower(raw)
		}
	}
	status := strings.ToLower(strings.TrimSpace(meta["status"]))
	if status == "" {
		status = "open"
	}
	order := 0
	for _, key := range []string{"order", "step_order"} {
		if raw := strings.TrimSpace(meta[key]); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				order = n
			}
		}
	}
	return Note{
		ID: id, Path: path, Title: title, Type: kind, Status: status, Order: order,
		Tags: parseTags(meta["tags"]), Body: strings.TrimSpace(body),
		Hash: hex.EncodeToString(sum[:]), Links: wikiLinks(text), idFromName: fromName,
	}
}

func splitFrontmatter(text string) (map[string]string, string) {
	if !strings.HasPrefix(text, "---\n") {
		return map[string]string{}, text
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return map[string]string{}, text
	}
	meta := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		meta[strings.ToLower(strings.TrimSpace(key))] = yamlScalar(value)
	}
	body := rest[end+4:]
	body = strings.TrimPrefix(body, "\n")
	return meta, body
}

// yamlScalar decodes one frontmatter value. Quoted scalars are values, not
// the quote characters stored around them.
func yamlScalar(raw string) string {
	value := strings.TrimSpace(withoutYAMLComment(raw))
	if len(value) < 2 {
		return value
	}
	switch value[0] {
	case '"':
		if value[len(value)-1] != '"' {
			return value
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	case '\'':
		if value[len(value)-1] != '\'' {
			return value
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	default:
		return value
	}
}

// A separated # starts a comment outside a quoted scalar. Quoting is only
// special at the beginning of a scalar, including items in a flow sequence.
func withoutYAMLComment(raw string) string {
	var quote byte
	start := true
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if quote != 0 {
			if quote == '"' && ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				if quote == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if ch == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return raw[:i]
		}
		if ch == ' ' || ch == '\t' {
			continue
		}
		if start && (ch == '"' || ch == '\'') {
			quote = ch
		}
		start = ch == '[' || ch == ','
	}
	return raw
}

func parseTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if raw == "" {
		return nil
	}
	var tags []string
	for _, part := range strings.Split(raw, ",") {
		tag := strings.Trim(strings.TrimSpace(part), `"'`)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func titleOf(body, path string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func wikiLinks(text string) []string {
	seen := map[string]struct{}{}
	var links []string
	for _, match := range linkPattern.FindAllStringSubmatch(text, -1) {
		target := strings.TrimSpace(match[1])
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		links = append(links, target)
	}
	return links
}

// Closed reports whether a status means the note is finished.
func Closed(status string) bool {
	switch strings.ToLower(status) {
	case "done", "closed", "dropped", "drop":
		return true
	default:
		return false
	}
}

// KnownType reports whether the type is one of the note verbs.
func KnownType(kind string) bool {
	_, ok := Types[kind]
	return ok
}

func fold(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}

func wordHas(text, needle string) bool {
	if needle == "" {
		return false
	}
	hay := fold(text)
	needle = fold(needle)
	start := 0
	for {
		i := strings.Index(hay[start:], needle)
		if i < 0 {
			return false
		}
		at := start + i
		before := at == 0 || !unicode.IsLetter(rune(hay[at-1])) && !unicode.IsDigit(rune(hay[at-1]))
		afterAt := at + len(needle)
		after := afterAt >= len(hay) || !unicode.IsLetter(rune(hay[afterAt])) && !unicode.IsDigit(rune(hay[afterAt]))
		if before && after {
			return true
		}
		start = at + len(needle)
		if start >= len(hay) {
			return false
		}
	}
}
