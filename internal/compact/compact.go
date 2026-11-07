// Package compact replaces a long conversation tail with a short summary.
//
// The system prompt and tool definitions stay outside the tail. Their bytes
// are not rewritten. The original tail is returned so the caller can store it.
package compact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// EstimateTokens is a local size signal. It is not a provider token count.
func EstimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	tokens := runes / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

// Plan describes one compaction decision.
type Plan struct {
	Needed       bool
	TailTokens   int
	PrefixRaw    []json.RawMessage
	TailRaw      []json.RawMessage
	RecentRaw    []json.RawMessage
	SummaryInput string
}

// PlanMessages splits a chat messages array. Leading system messages are the
// prefix and stay raw. The tail is compacted when its estimate exceeds maxTail.
func PlanMessages(raw json.RawMessage, maxTail, recent int) (Plan, error) {
	items, err := decodeArray(raw)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	seenUser := false
	for _, item := range items {
		role := roleOf(item)
		if (role == "system" || role == "developer") && !seenUser {
			plan.PrefixRaw = append(plan.PrefixRaw, item)
			continue
		}
		seenUser = true
		plan.TailRaw = append(plan.TailRaw, item)
	}
	plan.TailTokens = EstimateTokens(textOfAll(plan.TailRaw))
	if maxTail <= 0 || plan.TailTokens <= maxTail || len(plan.TailRaw) == 0 {
		return plan, nil
	}
	plan.Needed = true
	if recent < 0 {
		recent = 0
	}
	if recent > len(plan.TailRaw) {
		recent = len(plan.TailRaw)
	}
	split := len(plan.TailRaw) - recent
	if split < 0 {
		split = 0
	}
	if split > len(plan.TailRaw) {
		split = len(plan.TailRaw)
	}
	for split > 0 && split < len(plan.TailRaw) && isToolResult(plan.TailRaw[split]) {
		split--
	}
	older := plan.TailRaw[:split]
	if containsUnsummarized(older) {
		plan.Needed = false
		plan.RecentRaw = nil
		plan.SummaryInput = ""
		return plan, nil
	}
	if len(older) == 0 {
		plan.Needed = false
		plan.RecentRaw = nil
		plan.SummaryInput = ""
		return plan, nil
	}
	plan.RecentRaw = plan.TailRaw[split:]
	plan.SummaryInput = textOfAll(older)
	return plan, nil
}

// ApplyMessages returns a messages array whose prefix raw bytes are unchanged.
func ApplyMessages(plan Plan, summary string) (json.RawMessage, error) {
	if !plan.Needed {
		return nil, fmt.Errorf("compaction was not needed")
	}
	summaryMsg, err := json.Marshal(map[string]string{
		"role":    "user",
		"content": "Compacted tail:\n" + summary,
	})
	if err != nil {
		return nil, err
	}
	parts := append([]json.RawMessage{}, plan.PrefixRaw...)
	parts = append(parts, summaryMsg)
	parts = append(parts, plan.RecentRaw...)
	return joinArray(parts), nil
}

func decodeArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return nil, fmt.Errorf("messages must be an array")
	}
	var items []json.RawMessage
	for dec.More() {
		var item json.RawMessage
		if err := dec.Decode(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func isToolResult(raw json.RawMessage) bool {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	if role, _ := obj["role"].(string); role == "tool" {
		return true
	}
	if _, ok := obj["tool_call_id"].(string); ok {
		return true
	}
	return contentHasType(obj["content"], "tool_result")
}

func contentHasType(content any, kind string) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, block := range blocks {
		obj, _ := block.(map[string]any)
		if obj["type"] == kind {
			return true
		}
	}
	return false
}

func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func roleOf(raw json.RawMessage) string {
	obj, err := decodeObject(raw)
	if err != nil {
		return ""
	}
	role, _ := obj["role"].(string)
	return role
}

func textOfAll(items []json.RawMessage) string {
	var buf bytes.Buffer
	for _, item := range items {
		obj, err := decodeObject(item)
		if err != nil {
			continue
		}
		role, _ := obj["role"].(string)
		buf.WriteString("[message " + role + "]\n")
		if calls, ok := obj["tool_calls"].([]any); ok {
			for _, call := range calls {
				item, _ := call.(map[string]any)
				fn, _ := item["function"].(map[string]any)
				name, _ := fn["name"].(string)
				id, _ := item["id"].(string)
				args, _ := fn["arguments"].(string)
				buf.WriteString("[tool_use " + name + " " + id + "] " + args + "\n")
			}
		}
		if id, ok := obj["tool_call_id"].(string); ok {
			buf.WriteString("[tool_result " + id + "] ")
		}
		writeSummaryContent(&buf, obj["content"])
		if refusal, ok := obj["refusal"].(string); role == "assistant" && ok && refusal != "" {
			buf.WriteString("\n[refusal] " + refusal)
		}
		buf.WriteString("\n[/message]\n")
	}
	return buf.String()
}

func containsUnsummarized(items []json.RawMessage) bool {
	for _, item := range items {
		obj, err := decodeObject(item)
		if err != nil {
			return true
		}
		if _, audio := obj["audio"]; audio || contentUnsummarized(obj["content"]) {
			return true
		}
	}
	return false
}

func contentUnsummarized(content any) bool {
	blocks, ok := content.([]any)
	if !ok {
		switch content.(type) {
		case nil, string:
			return false
		default:
			return true
		}
	}
	for _, part := range blocks {
		block, ok := part.(map[string]any)
		if !ok {
			return true
		}
		switch block["type"] {
		case "text", "":
			if _, ok := block["text"].(string); !ok {
				return true
			}
		case "tool_use":
			if _, ok := block["input"]; !ok {
				return true
			}
		case "tool_result":
			if contentUnsummarized(block["content"]) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func writeSummaryContent(buf *bytes.Buffer, content any) {
	switch value := content.(type) {
	case string:
		buf.WriteString(value)
		buf.WriteByte('\n')
	case []any:
		for _, part := range value {
			block, _ := part.(map[string]any)
			switch block["type"] {
			case "tool_use":
				name, _ := block["name"].(string)
				id, _ := block["id"].(string)
				raw, _ := json.Marshal(block["input"])
				buf.WriteString("[tool_use " + name + " " + id + "] " + string(raw) + "\n")
			case "tool_result":
				id, _ := block["tool_use_id"].(string)
				buf.WriteString("[tool_result " + id + "] ")
				writeSummaryContent(buf, block["content"])
			default:
				if text, ok := block["text"].(string); ok {
					buf.WriteString(text)
					buf.WriteByte('\n')
				}
			}
		}
	}
}

func joinArray(items []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}
