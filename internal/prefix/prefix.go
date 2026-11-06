// Package prefix keeps the system prompt and tool definitions canonical.
//
// The hash covers that prefix only. Task ids, step ids, and step text stay
// outside it. Tail compaction is a later change.
package prefix

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Prefix is the cacheable system prompt and tool list.
type Prefix struct {
	System string
	Tools  []any
}

// Material is the canonical JSON of the prefix.
func (p Prefix) Material() ([]byte, error) {
	payload := map[string]any{"system": p.System, "tools": p.Tools}
	return Canonical(payload)
}

// SHA256 is the hex digest of Material.
func (p Prefix) SHA256() (string, error) {
	raw, err := p.Material()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Build validates a configured prefix. Tool definitions use OpenAI function names.
func Build(system string, tools []any) (Prefix, error) {
	if system == "" {
		return Prefix{}, fmt.Errorf("prefix system prompt must be a non-empty string")
	}
	if tools == nil {
		tools = []any{}
	}
	normalized := make([]any, 0, len(tools))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		obj, ok := tool.(map[string]any)
		if !ok {
			return Prefix{}, fmt.Errorf("each tool definition must be an object")
		}
		canon, err := canonicalize(obj)
		if err != nil {
			return Prefix{}, err
		}
		asMap := canon.(map[string]any)
		name := functionName(asMap)
		if name == "" {
			return Prefix{}, fmt.Errorf("each tool needs a function name")
		}
		names = append(names, name)
		normalized = append(normalized, asMap)
	}
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, ok := seen[name]; ok {
			return Prefix{}, fmt.Errorf("tool function names must be unique")
		}
		seen[name] = struct{}{}
	}
	sort.Slice(normalized, func(i, j int) bool {
		return functionName(normalized[i].(map[string]any)) < functionName(normalized[j].(map[string]any))
	})
	return Prefix{System: system, Tools: normalized}, nil
}

// HashRequest hashes the system text and tools that will actually be sent.
// Tools may be OpenAI function objects or Anthropic tool objects.
func HashRequest(system string, tools []any) (string, error) {
	if tools == nil {
		tools = []any{}
	}
	normalized := make([]any, 0, len(tools))
	for _, tool := range tools {
		canon, err := canonicalize(tool)
		if err != nil {
			return "", err
		}
		normalized = append(normalized, canon)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return requestToolName(normalized[i]) < requestToolName(normalized[j])
	})
	raw, err := Canonical(map[string]any{"system": system, "tools": normalized})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func functionName(tool map[string]any) string {
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return name
}

func requestToolName(tool any) string {
	obj, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if name := functionName(obj); name != "" {
		return name
	}
	name, _ := obj["name"].(string)
	return name
}

// Canonical encodes JSON with sorted object keys, compact separators, and
// unescaped HTML. Equivalent values share bytes.
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := appendValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func canonicalize(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, bool, float64, int, int64, json.Number:
		return x, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, val := range x {
			next, err := canonicalize(val)
			if err != nil {
				return nil, err
			}
			out[key] = next
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			next, err := canonicalize(val)
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	default:
		return nil, fmt.Errorf("prefix value of type %T is not JSON", v)
	}
}

func appendValue(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			raw, err := marshalScalar(key)
			if err != nil {
				return err
			}
			buf.Write(raw)
			buf.WriteByte(':')
			if err := appendValue(buf, x[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := appendValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case nil:
		buf.WriteString("null")
		return nil
	default:
		raw, err := marshalScalar(x)
		if err != nil {
			return fmt.Errorf("prefix value of type %T is not JSON", v)
		}
		buf.Write(raw)
		return nil
	}
}

func marshalScalar(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
