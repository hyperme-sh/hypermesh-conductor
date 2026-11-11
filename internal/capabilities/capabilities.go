// Package capabilities validates revision-bound endpoint features before dispatch.
package capabilities

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Record struct {
	Model    string            `json:"model"`
	Runtime  string            `json:"runtime_revision"`
	Protocol string            `json:"protocol"`
	Local    bool              `json:"local"`
	Features map[string]string `json:"features"`
}

var known = map[string]bool{"chat": true, "streaming": true, "tools": true, "structured_output": true, "image": true, "embeddings": true, "ocr": true}

func (r Record) Validate(model, runtime, protocol string) error {
	if r.Model != model || r.Runtime == "" || r.Runtime != runtime || r.Protocol != protocol {
		return fmt.Errorf("capability record does not match model, runtime and protocol")
	}
	for name, state := range r.Features {
		if !known[name] || (state != "verified" && state != "unknown" && state != "unsupported") {
			return fmt.Errorf("invalid capability %s", name)
		}
	}
	return nil
}

func (r Record) Require(model, runtime, protocol string, required []string, localOnly bool) error {
	if err := r.Validate(model, runtime, protocol); err != nil {
		return err
	}
	if localOnly && !r.Local {
		return fmt.Errorf("local-only request requires a local endpoint")
	}
	for _, feature := range required {
		if r.Features[feature] != "verified" {
			return fmt.Errorf("endpoint lacks verified %s capability", feature)
		}
	}
	return nil
}

// Required inspects structured request fields, never prompt text.
func Required(path string, body []byte) ([]string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	features := []string{"chat"}
	if strings.HasSuffix(path, "embeddings") {
		return []string{"embeddings"}, nil
	}
	var stream bool
	if raw, ok := obj["stream"]; ok {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, fmt.Errorf("invalid stream flag")
		}
	}
	if stream {
		features = append(features, "streaming")
	}
	for _, key := range []string{"tools", "functions"} {
		if raw := obj[key]; len(raw) > 0 && string(raw) != "[]" && string(raw) != "null" {
			features = append(features, "tools")
			break
		}
	}
	if raw := obj["response_format"]; len(raw) > 0 && string(raw) != "null" {
		features = append(features, "structured_output")
	}
	var text map[string]json.RawMessage
	if json.Unmarshal(obj["text"], &text) == nil && len(text["format"]) > 0 {
		features = append(features, "structured_output")
	}
	var value any
	_ = json.Unmarshal(body, &value)
	if hasImage(value) {
		features = append(features, "image")
	}
	return features, nil
}

func hasImage(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if t, _ := v["type"].(string); t == "image" || t == "image_url" || t == "input_image" {
			return true
		}
		for _, key := range []string{"messages", "content", "input"} {
			if hasImage(v[key]) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasImage(item) {
				return true
			}
		}
	}
	return false
}
