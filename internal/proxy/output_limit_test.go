package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"testing"
)

func TestRecipeOutputLimit(t *testing.T) {
	for _, tc := range []struct{ proto, body, want string }{
		{translate.ProtoChat, "{}", `{"max_tokens":5}`},
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
