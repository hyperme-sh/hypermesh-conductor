package capabilities

import "testing"

func TestRevisionBindingAndUnknown(t *testing.T) {
	r := Record{Model: "model", Runtime: "r1", Protocol: "openai-compatible", Local: true, Features: map[string]string{"chat": "verified", "tools": "unknown"}}
	if r.Require("model", "r1", "openai-compatible", []string{"chat"}, true) != nil {
		t.Fatal("valid record rejected")
	}
	for _, args := range [][3]string{{"other", "r1", "openai-compatible"}, {"model", "r2", "openai-compatible"}, {"model", "r1", "anthropic"}} {
		if r.Require(args[0], args[1], args[2], []string{"chat"}, false) == nil {
			t.Fatal("stale record accepted")
		}
	}
	if r.Require("model", "r1", "openai-compatible", []string{"tools"}, false) == nil {
		t.Fatal("unknown accepted")
	}
	r.Local = false
	if r.Require("model", "r1", "openai-compatible", []string{"chat"}, true) == nil {
		t.Fatal("locality widened")
	}
}
func TestIndependentRequestFeatures(t *testing.T) {
	for _, tc := range []struct{ body, feature string }{{`{"stream":true}`, "streaming"}, {`{"tools":[{}]}`, "tools"}, {`{"response_format":{"type":"json_schema"}}`, "structured_output"}, {`{"messages":[{"content":[{"type":"image_url"}]}]}`, "image"}} {
		required, err := Required("/v1/chat/completions", []byte(tc.body))
		if err != nil || len(required) != 2 || required[1] != tc.feature {
			t.Fatalf("%s: %v %v", tc.feature, required, err)
		}
	}
}

func TestPlainTextFormatsDoNotRequireStructure(t *testing.T) {
	for _, raw := range []string{`{"response_format":{"type":"text"}}`, `{"text":{"format":{"type":"text"}}}`} {
		required, err := Required("/v1/responses", []byte(raw))
		if err != nil || len(required) != 1 || required[0] != "chat" {
			t.Fatalf("%v %v", required, err)
		}
	}
}
