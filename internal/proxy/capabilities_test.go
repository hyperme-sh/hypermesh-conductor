package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilities"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/prefix"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnsupportedCapabilityBeforeDispatch(t *testing.T) {
	head, _ := prefix.Build("system", nil)
	cfg := config.Config{Prefix: head, Frontier: config.Tier{Name: "frontier", Provider: config.ProviderOpenAICompatible,
		BaseURL: "http://127.0.0.1:1/v1", Model: "fixture", RuntimeRevision: "r1", Capabilities: &capabilities.Record{
			Model: "fixture", Runtime: "r1", Protocol: config.ProviderOpenAICompatible, Features: map[string]string{"chat": "verified", "tools": "unsupported"}}}}
	p := New(cfg, nil, nil)
	for _, body := range []string{`{"messages":[],"tools":[{}]}`, `{"messages":[],"stream":true}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("X-Conductor-Class", "code")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != 422 {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	}
}
