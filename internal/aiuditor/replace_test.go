package aiuditor

import (
	"context"
	"strings"
	"testing"
)

func TestReplacementKeepsLargeIntegers(t *testing.T) {
	stub := Stub{Rules: Rules{Replace: []ReplaceRule{{From: "SECRET", To: "[redacted]", Reason: "redact"}}}}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"SECRET"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"record_id":9007199254740993}}]}]}`)
	verdict, err := stub.Review(context.Background(), Envelope{
		Request: Request{Body: body},
		Routing: Routing{JobClass: "parse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(verdict.Body)
	if verdict.Action != Modify || strings.Contains(text, "SECRET") || !strings.Contains(text, "9007199254740993") || strings.Contains(text, "9007199254740992") {
		t.Fatal(verdict.Action, text)
	}
}
