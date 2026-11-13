package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilities"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

type serviceReview struct {
	Event   string           `json:"event"`
	Request aiuditor.Request `json:"request"`
	Routing struct {
		aiuditor.Candidate
		JobClass string `json:"job_class"`
	} `json:"routing"`
	Candidates []aiuditor.Candidate `json:"candidates"`
	Refusal    *struct {
		Model  aiuditor.Candidate `json:"model"`
		Reason string             `json:"reason"`
	} `json:"refusal"`
}

func serviceConfig(t *testing.T, upstream string) (config.Config, string) {
	t.Helper()
	t.Setenv(aiuditor.ServiceTokenEnv, strings.Repeat("s", 32))
	t.Setenv(aiuditor.ServiceTenantEnv, "11111111-1111-4111-8111-111111111111")
	dir := t.TempDir()
	cfg := loadTestConfig(t, dir, upstream+"/v1", upstream+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	cfg.Cheap.PolicyProvider = "local-vendor"
	cfg.Cheap.Locality = "local"
	cfg.Frontier.PolicyProvider = "vendor"
	cfg.Frontier.Locality = "remote"
	// Service failures must remain closed even if an old config requested otherwise.
	cfg.AIU.FailClosed = false
	return cfg, dir
}
func TestProductionPolicySelectsProviderAndRetainsBudgetChecks(t *testing.T) {
	for _, mode := range []string{"route", "deny", "unreachable", "budget", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			var sent atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent.Add(1)
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), `"model":"frontier"`) {
					t.Error("route constraint was not applied")
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
			}))
			defer upstream.Close()
			cfg, dir := serviceConfig(t, upstream.URL)
			if mode == "budget" {
				cfg.Cheap.TaskTokenLimit = 20
			}
			if mode == "metadata" {
				cfg.Cheap.PolicyProvider = ""
			}
			var reviews atomic.Int32
			authority := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reviews.Add(1)
				var env serviceReview
				if json.NewDecoder(r.Body).Decode(&env) != nil || env.Routing.Provider == "" || env.Routing.Locality == "" || len(env.Candidates) == 0 {
					t.Error("missing service metadata")
				}
				if mode == "unreachable" {
					w.WriteHeader(503)
					return
				}
				if mode == "deny" {
					io.WriteString(w, `{"action":"deny","reason":"policy"}`)
					return
				}
				io.WriteString(w, `{"action":"allow","reason":"provider policy","route":{"allow_models":[{"provider":"vendor"}]}}`)
			}))
			defer authority.Close()
			cfg.AIU.Mode = config.AIUModeHTTP
			cfg.AIU.URL = authority.URL + "/api/v1/aiuditor/review"
			p := New(cfg, aiuditor.ProductionHTTP{URL: cfg.AIU.URL, Client: authority.Client()}, openJournal(t, dir))
			result := do(p, "/v1/chat/completions", "parse", "task", "step", "", `{"messages":[{"role":"user","content":"hello"}]}`)
			if mode == "route" {
				if result.Code != 200 || sent.Load() != 1 || reviews.Load() != 2 {
					t.Fatalf("route failed: %d %s sent=%d reviews=%d", result.Code, result.Body.String(), sent.Load(), reviews.Load())
				}
			} else if result.Code < 400 || sent.Load() != 0 {
				t.Fatalf("constraint bypassed: %s %d sent=%d", mode, result.Code, sent.Load())
			}
		})
	}
}
func TestProductionAuxiliaryCallsCarryContractAndStayDenied(t *testing.T) {
	var sent atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	cfg, _ := serviceConfig(t, upstream.URL)
	cfg.Cheap.RuntimeRevision = "ocr-v1"
	cfg.Cheap.Capabilities = &capabilities.Record{Model: cfg.Cheap.Model, Runtime: "ocr-v1", Protocol: cfg.Cheap.Provider, Local: true, Features: map[string]string{"ocr": "verified", "chat": "verified"}}
	cfg.Cheap.OCROutputs = []string{"text"}
	var summary, ocr atomic.Int32
	authority := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env serviceReview
		json.NewDecoder(r.Body).Decode(&env)
		if len(env.Candidates) != 1 || env.Routing.Provider != "local-vendor" || env.Routing.Locality != "local" {
			t.Error("auxiliary metadata missing")
		}
		if env.Routing.JobClass == "summarize" {
			summary.Add(1)
		}
		if env.Routing.JobClass == "extract" {
			ocr.Add(1)
		}
		io.WriteString(w, `{"action":"deny","reason":"auxiliary policy"}`)
	}))
	defer authority.Close()
	cfg.AIU.Mode = config.AIUModeHTTP
	cfg.AIU.URL = authority.URL + "/api/v1/aiuditor/review"
	p := New(cfg, aiuditor.ProductionHTTP{URL: cfg.AIU.URL, Client: authority.Client()}, nil)
	_, _, _, _, err := p.cheapSummary(context.Background(), httptest.NewRequest("POST", "/v1/chat/completions", nil), "private summary", "")
	if err == nil {
		t.Fatal("summary ignored deny")
	}
	result := httptest.NewRecorder()
	p.ServeHTTP(result, httptest.NewRequest("POST", "/v1/ocr", strings.NewReader(`{"document":{"type":"document_url","document_url":"data:application/pdf;base64,cGRm"},"required_outputs":["text"]}`)))
	if result.Code != 403 || sent.Load() != 0 || summary.Load() != 1 || ocr.Load() != 1 {
		t.Fatalf("auxiliary review bypass: status=%d sent=%d summary=%d ocr=%d", result.Code, sent.Load(), summary.Load(), ocr.Load())
	}
}
func TestProductionUpstreamRefusalCannotEscapeRemainingCandidates(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "reroute", true: "deny"}[deny], func(t *testing.T) {
			var frontier, cheap, refusals atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), `"model":"frontier"`) {
					frontier.Add(1)
					io.WriteString(w, `{"choices":[{"message":{"refusal":"no"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":3,"completion_tokens":0}}`)
				} else {
					cheap.Add(1)
					io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
				}
			}))
			defer upstream.Close()
			cfg, dir := serviceConfig(t, upstream.URL)
			cfg.RefusalFallbackTier = "cheap"
			cfg.MaxRefusalAttempts = 2
			authority := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var env serviceReview
				json.NewDecoder(r.Body).Decode(&env)
				action, model := "allow", env.Routing.ModelID
				if env.Event == "upstream_refusal" {
					refusals.Add(1)
					if env.Refusal == nil || env.Refusal.Model.ModelID != "frontier" || len(env.Candidates) != 1 || env.Candidates[0].ModelID != cfg.Cheap.Model {
						t.Error("invalid refusal request")
					}
					if deny {
						io.WriteString(w, `{"action":"deny","reason":"reroute forbidden"}`)
						return
					}
					action, model = "reroute", cfg.Cheap.Model
				}
				response := map[string]any{"action": action, "reason": "policy", "route": map[string]any{"allow_models": []map[string]string{{"model_id": model}}}}
				if action == "reroute" {
					response["allowed_models"] = []string{model}
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer authority.Close()
			cfg.AIU.Mode = config.AIUModeHTTP
			cfg.AIU.URL = authority.URL + "/api/v1/aiuditor/review"
			p := New(cfg, aiuditor.ProductionHTTP{URL: cfg.AIU.URL, Client: authority.Client()}, openJournal(t, dir))
			result := do(p, "/v1/chat/completions", "code", "task", "step", "", `{"messages":[{"role":"user","content":"hello"}]}`)
			if result.Code != 200 || frontier.Load() != 1 || refusals.Load() != 1 || (!deny && cheap.Load() != 1) || (deny && cheap.Load() != 0) {
				t.Fatalf("refusal routing failed: %d %s frontier=%d cheap=%d refusals=%d", result.Code, result.Body.String(), frontier.Load(), cheap.Load(), refusals.Load())
			}
		})
	}
}

func TestHTTPConfigurationDoesNotDefaultToStub(t *testing.T) {
	cfg := config.Config{AIU: config.AIU{Mode: config.AIUModeHTTP, URL: "https://authority.example/api/v1/aiuditor/review"}}
	if !New(cfg, nil, nil).productionReview() {
		t.Fatal("HTTP configuration selected a stub")
	}
}
func TestPolicyMetadataNeverInfersProviderOrLocalityFromProtocol(t *testing.T) {
	tier := config.Tier{Name: "cheap", Model: "m", Provider: config.ProviderOpenAICompatible}
	if _, err := policyCandidate(tier); err == nil {
		t.Fatal("protocol inferred policy identity")
	}
	tier.PolicyProvider = "vendor"
	if _, err := policyCandidate(tier); err == nil {
		t.Fatal("protocol inferred locality")
	}
	tier.Locality = "remote"
	tier.Gateway = true
	candidate, err := policyCandidate(tier)
	if err != nil || candidate.Provider != "hypermesh" {
		t.Fatal("gateway provider mislabeled")
	}
}
