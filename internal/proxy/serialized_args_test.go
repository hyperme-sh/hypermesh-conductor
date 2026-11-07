package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSerializedArgumentsRedaction(t *testing.T) {
	secret := "demo-credential"
	args := `{"field":"demo-\u0063redential"}`
	body, _ := json.Marshal(map[string]string{"arguments": args})
	var received map[string]string
	if err := json.Unmarshal(redactJSON(body, []string{secret}), &received); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(received["arguments"]), &parsed); err != nil || strings.Contains(parsed["field"], secret) {
		t.Fatal("nested arguments were not safely redacted")
	}
	for i := 1; i < len(args); i++ {
		guard := newStreamCredentials([]string{secret})
		if guard.acceptsArguments("tool", args[:i]) && guard.acceptsArguments("tool", args[i:]) {
			t.Fatalf("missed fragment boundary %d", i)
		}
	}
}
