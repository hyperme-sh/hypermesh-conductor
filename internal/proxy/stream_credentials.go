package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Match credentials in the text an SDK assembles, before writing the event
// which would complete a match. KMP retains only a prefix length per field.
type credentialPattern struct {
	value    string
	fallback []int
}
type streamCredentials struct {
	patterns []credentialPattern
	partial  map[string][]int
	escapes  map[string]string
	invalid  bool
}

func newStreamCredentials(secrets []string) *streamCredentials {
	g := &streamCredentials{partial: map[string][]int{}, escapes: map[string]string{}}
	seen := map[string]bool{}
	for _, s := range secrets {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		if len(s) > 16<<10 {
			g.invalid = true
			break
		}
		f := make([]int, len(s))
		for i, j := 1, 0; i < len(s); i++ {
			for j > 0 && s[j] != s[i] {
				j = f[j-1]
			}
			if s[j] == s[i] {
				j++
			}
			f[i] = j
		}
		g.patterns = append(g.patterns, credentialPattern{s, f})
	}
	return g
}
func (g *streamCredentials) accepts(field, fragment string) bool {
	if fragment == "" {
		return true
	}
	state := g.partial[field]
	if state == nil {
		state = make([]int, len(g.patterns))
	}
	active := false
	for p, pattern := range g.patterns {
		j := state[p]
		for i := 0; i < len(fragment); i++ {
			for j > 0 && pattern.value[j] != fragment[i] {
				j = pattern.fallback[j-1]
			}
			if pattern.value[j] == fragment[i] {
				j++
			}
			if j == len(pattern.value) {
				return false
			}
		}
		state[p] = j
		active = active || j != 0
	}
	if active {
		if _, ok := g.partial[field]; !ok && len(g.partial) >= 256 {
			return false
		}
		g.partial[field] = state
	} else {
		delete(g.partial, field)
	}
	return true
}

// Decode JSON escapes in serialized tool arguments before matching. Retain
// only an incomplete escape (at most one UTF-16 pair) across deltas.
func (g *streamCredentials) acceptsArguments(field, fragment string) bool {
	if !g.accepts(field, fragment) {
		return false
	}
	text := g.escapes[field] + fragment
	delete(g.escapes, field)
	for len(text) > 0 {
		i := strings.IndexByte(text, '\\')
		if i < 0 {
			return g.accepts(field+"/decoded", text)
		}
		if !g.accepts(field+"/decoded", text[:i]) {
			return false
		}
		text = text[i:]
		size := 2
		if len(text) >= 2 && text[1] == 'u' {
			size = 6
			if len(text) >= 6 {
				n, err := strconv.ParseUint(text[2:6], 16, 16)
				if err != nil {
					return false
				}
				if utf16.IsSurrogate(rune(n)) {
					if n > 0xdbff {
						return false
					}
					if len(text) >= 12 {
						lo, err := strconv.ParseUint(text[8:12], 16, 16)
						if text[6:8] != `\u` || err != nil || lo < 0xdc00 || lo > 0xdfff {
							return false
						}
					}
					size = 12
				}
			}
		}
		if len(text) < size {
			if len(g.escapes) >= 256 {
				return false
			}
			g.escapes[field] = text
			return true
		}
		var decoded string
		if json.Unmarshal([]byte("\""+text[:size]+"\""), &decoded) != nil || !g.accepts(field+"/decoded", decoded) {
			return false
		}
		text = text[size:]
	}
	return true
}

func (g *streamCredentials) safeEvents(raw []byte) bool {
	if g.invalid {
		return false
	}
	if len(g.patterns) == 0 {
		return true
	}
	for len(raw) > 0 {
		line, rest, _ := bytes.Cut(raw, []byte{'\n'})
		raw = rest
		payload := ssePayload(line)
		if payload == nil {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(payload, &obj) != nil {
			return false
		}
		index := func(obj map[string]any, key string) int { v, _ := obj[key].(float64); return int(v) }
		str := func(obj map[string]any, key string) string { v, _ := obj[key].(string); return v }
		choices, _ := obj["choices"].([]any)
		for _, v := range choices {
			c, _ := v.(map[string]any)
			delta, _ := c["delta"].(map[string]any)
			base := fmt.Sprintf("chat/%d", index(c, "index"))
			if !g.accepts(base, str(delta, "content")) || !g.accepts(base, str(delta, "refusal")) {
				return false
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, v := range calls {
				call, _ := v.(map[string]any)
				fn, _ := call["function"].(map[string]any)
				field := fmt.Sprintf("%s/call/%d/", base, index(call, "index"))
				for _, k := range []string{"name", "arguments"} {
					if (k == "arguments" && !g.acceptsArguments(field+k, str(fn, k))) || (k == "name" && !g.accepts(field+k, str(fn, k))) {
						return false
					}
				}
			}
		}
		if d, ok := obj["delta"].(map[string]any); ok {
			base := fmt.Sprintf("block/%d/", index(obj, "index"))
			for _, k := range []string{"text", "partial_json", "thinking", "signature"} {
				if (k == "partial_json" && !g.acceptsArguments(base+k, str(d, k))) || (k != "partial_json" && !g.accepts(base+k, str(d, k))) {
					return false
				}
			}
		}
		if d, ok := obj["delta"].(string); ok {
			if len(str(obj, "type")) > 128 {
				return false
			}
			field := fmt.Sprintf("response/%d/%d/%s", index(obj, "output_index"), index(obj, "content_index"), str(obj, "type"))
			if strings.Contains(str(obj, "type"), "arguments") {
				if !g.acceptsArguments(field, d) {
					return false
				}
			} else if !g.accepts(field, d) {
				return false
			}
		}
	}
	return true
}
