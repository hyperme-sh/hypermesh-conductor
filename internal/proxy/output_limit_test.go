package proxy

import (
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"testing"
)

func TestRecipeOutputLimit(t *testing.T) {
	for _, tc := range []struct{ proto, body, want string }{
		{translate.ProtoChat, "{}", `{"max_tokens":5}`},
		{translate.ProtoChat, `{"n":2}`, `{"max_tokens":2,"n":2}`},
		{translate.ProtoChat, `{"best_of":3,"n":2}`, `{"best_of":3,"max_tokens":1,"n":2}`},
		{translate.ProtoChat, `{"n":2,"max_completion_tokens":2}`, `{"n":2,"max_completion_tokens":2}`},
		{translate.ProtoChat, `{"n":2,"max_completion_tokens":3}`, ""},
		{translate.ProtoChat, `{"best_of":4,"max_tokens":2}`, ""},
		{translate.ProtoChat, `{"best_of":1,"n":2}`, ""},
		{translate.ProtoChat, `{"n":6}`, ""},
		{translate.ProtoChat, `{"n":null}`, ""},
		{translate.ProtoChat, `{"best_of":1.5}`, ""},
		{translate.ProtoMessages, `{"max_tokens":3}`, `{"max_tokens":3}`},
		{translate.ProtoResponses, "{}", `{"max_output_tokens":5}`},
		{translate.ProtoEmbeddings, "{}", "{}"},
		{translate.ProtoChat, `{"max_completion_tokens":8}`, ""},
		{translate.ProtoChat, `{"max_completion_tokens":3,"max_tokens":4}`, ""},
	} {
		got, err := limitOutput(tc.proto, []byte(tc.body), 5)
		if string(got) != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("%s: %s %v", tc.body, got, err)
		}
	}
}

func TestAnthropicRecipeTranslation(t *testing.T) {
	for _, tc := range []struct {
		protocol, body string
		limit, want    int
	}{
		{translate.ProtoChat, `{"messages":[{"role":"user","content":"summarize"}]}`, 64, 64},
		{translate.ProtoResponses, `{"input":"hello"}`, 64, 64},
		{translate.ProtoResponses, `{"input":"hello","max_output_tokens":8}`, 64, 8},
		{translate.ProtoChat, `{"messages":[{"role":"user","content":"test"}],"max_tokens":65}`, 64, 0},
		{translate.ProtoChat, `{"messages":[{"role":"user","content":"test"}]}`, 0, translate.DefaultMaxTokens},
	} {
		wire, err := translateTierRequest(tc.protocol, config.Tier{Model: "test", Provider: config.ProviderAnthropic, OutputLimit: tc.limit}, []byte(tc.body))
		if tc.want == 0 {
			if err == nil {
				t.Fatal("accepted excessive limit")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Max int `json:"max_tokens"`
		}
		if err = json.Unmarshal(wire.Body, &payload); err != nil || payload.Max != tc.want {
			t.Errorf("expected %d: %s", tc.want, wire.Body)
		}
	}
}
