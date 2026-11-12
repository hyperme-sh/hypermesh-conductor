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

func TestEachCapabilityBeforeBackendDispatch(t *testing.T) {
	for _, tc := range []struct{ feature, body string }{
		{"chat", `{"messages":[]}`},
		{"streaming", `{"messages":[],"stream":true}`},
		{"tools", `{"messages":[],"tools":[{"type":"function","function":{"name":"read"}}]}`},
		{"structured_output", `{"messages":[],"response_format":{"type":"json_object"}}`},
		{"image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`},
	} {
		t.Run(tc.feature, func(t *testing.T) {
			calls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.feature == "streaming" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			}))
			defer backend.Close()
			cfg := loadTestConfig(t, t.TempDir(), backend.URL+"/v1", backend.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
			cfg.Prefix, _ = prefix.Build("system", nil)
			tier := &cfg.Frontier
			tier.RuntimeRevision = "r1"
			tier.Capabilities = &capabilities.Record{Model: tier.Model, Runtime: "r1", Protocol: tier.Provider, Features: map[string]string{"chat": "verified", tc.feature: "unknown"}}
			for _, state := range []string{"unknown", "unsupported", "verified"} {
				tier.Capabilities.Features[tc.feature] = state
				rec := do(New(cfg, nil, nil), "/v1/chat/completions", "code", "task", state, "", tc.body)
				if state == "verified" {
					if rec.Code != 200 {
						t.Fatalf("%d %s", rec.Code, rec.Body.String())
					}
				} else if rec.Code != 422 || calls != 0 {
					t.Fatal("unsupported feature reached backend", rec.Code, calls)
				}
			}
			if calls != 1 {
				t.Fatal(calls)
			}
		})
	}
}
