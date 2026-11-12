package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
)

func TestCapturedProviderAndGatewayCredentials(t *testing.T) {
	directCalls, gatewayCalls := 0, 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls++
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer original-provider-key" || !strings.Contains(string(raw), `"model":"original-model"`) {
			t.Error("original provider identity lost")
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer provider.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		if r.Header.Get("Authorization") != "Bearer gateway-only-key" || r.Header.Get("X-Api-Key") != "" {
			t.Error("provider credential reached gateway")
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer gateway.Close()
	dir := t.TempDir()
	cfg := loadTestConfig(t, dir, gateway.URL+"/v1", gateway.URL+"/v1", config.AuthEnv, config.ProviderOpenAICompatible)
	cfg.Cheap.Gateway = true
	cfg.Cheap.Model = "llama-3.1-8b-q4"
	cfg.Captures = map[string]config.Tier{"codex": {Name: "frontier", Direct: true, Auth: config.AuthPassthrough, Provider: config.ProviderOpenAICompatible, BaseURL: provider.URL + "/v1", CredentialOrigin: provider.URL, Model: "$client"}}
	t.Setenv("CONDUCTOR_CHEAP_API_KEY", "gateway-only-key")
	t.Setenv(config.ProxyTokenEnv, "proxy-token")
	journal := openJournal(t, dir)
	p := New(cfg, aiuditor.Stub{}, journal)
	for _, class := range []string{"code", "parse"} {
		req := request("/capture/codex/v1/chat/completions", class, "task", class, "", `{"model":"original-model","messages":[{"role":"user","content":"hello"}]}`)
		req.Header.Set(config.ProxyTokenHeader, "proxy-token")
		req.Header.Set("Authorization", "Bearer original-provider-key")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", class, rec.Code, rec.Body.String())
		}
	}
	if directCalls != 1 || gatewayCalls != 1 {
		t.Fatalf("calls direct=%d gateway=%d", directCalls, gatewayCalls)
	}
	rows, err := metrics.ReadJournal(journal.Path())
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if rows[0].Destination != "direct" || rows[1].Destination != "gateway" {
		t.Fatal(rows)
	}
	for _, origin := range []string{"https://wrong.example", provider.URL} {
		req := request("/capture/codex/v1/chat/completions", "code", "task", "denied", "", `{"model":"original-model","messages":[]}`)
		req.Header.Set("X-Conductor-Credential-Origin", origin)
		if origin == provider.URL {
			p.Config.GatewayEnforcement = true
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("got %d", rec.Code)
		}
	}
	if directCalls != 1 || gatewayCalls != 1 {
		t.Fatal("denied request dispatched")
	}
}
