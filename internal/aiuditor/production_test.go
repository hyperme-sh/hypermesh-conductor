package aiuditor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
"sync/atomic"
	"testing"
)

func productionFixture(t *testing.T) Envelope {
	t.Helper()
	t.Setenv(ServiceTokenEnv, strings.Repeat("s", 32))
	t.Setenv(ServiceTenantEnv, "11111111-1111-4111-8111-111111111111")
	candidate := Candidate{"cheap", "local-model", "local-vendor", "local"}
	return Envelope{Request: Request{Protocol: "openai.chat", Method: "POST", Path: "/v1/chat/completions", Body: json.RawMessage(`{"messages":[]}`)}, Routing: Routing{JobClass: "parse", Tier: "cheap", ModelID: "local-model"}, Selected: candidate, Metadata: []Candidate{candidate, {"frontier", "remote-model", "vendor", "remote"}}}
}
func TestProductionWireAuthAndProviderConstraint(t *testing.T) {
	env := productionFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/aiuditor/review" || r.Header.Get("X-Internal-Token") != strings.Repeat("s", 32) || r.Header.Get("X-Tenant-ID") != "11111111-1111-4111-8111-111111111111" || r.Header.Get("Authorization") != "" {
			t.Error("wrong dedicated authority headers")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid body")
		}
		var candidates []Candidate
		if json.Unmarshal(body["candidates"], &candidates) != nil || len(candidates) != 2 || candidates[0] != env.Selected {
			t.Error("structured ordered candidates missing")
		}
		var routing map[string]any
		json.Unmarshal(body["routing"], &routing)
		if routing["provider"] != "local-vendor" || routing["locality"] != "local" || len(body["request_id"]) < 3 {
			t.Error("missing routing identity")
		}
		w.Write([]byte(`{"action":"allow","reason":"provider rule","route":{"allow_models":[{"provider":"vendor"}]}}`))
	}))
	defer server.Close()
	verdict, err := (ProductionHTTP{URL: server.URL + "/api/v1/aiuditor/review", Client: server.Client()}).Review(context.Background(), env)
	defer verdict.Release()
	if err != nil || verdict.Action != Allow || len(verdict.AllowedModels) != 1 || verdict.AllowedModels[0] != "remote-model" {
		t.Fatal("provider rule lost", err, verdict)
	}
}
func TestProductionRefusesInvalidConstraintsAndServiceFailures(t *testing.T) {
	env := productionFixture(t)
	for _, body := range []string{
		`{"action":"allow","reason":"missing route"}`,
		`{"action":"allow","reason":"both","route":{"allow_models":[],"deny_models":[]}}`,
		`{"action":"allow","reason":"unknown","route":{"allow_models":[],"extra":true}}`,
		`{"action":"allow","reason":"bad selector","route":{"allow_models":[{"provider":"vendor","model_id":"remote-model"}]}}`,
		`{"action":"reroute","reason":"wrong event","allowed_models":["remote-model"],"route":{"allow_models":[{"model_id":"remote-model"}]}}`,
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, err := (ProductionHTTP{URL: server.URL + "/api/v1/aiuditor/review", Client: server.Client()}).Review(context.Background(), env)
		server.Close()
		if !errors.Is(err, ErrProductionReview) {
			t.Fatal("invalid grant accepted", body, err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	if _, err := (ProductionHTTP{URL: server.URL + "/api/v1/aiuditor/review", Client: server.Client()}).Review(context.Background(), env); !errors.Is(err, ErrProductionReview) {
		t.Fatal("service auth failure accepted")
	}
}
func TestProductionRefusalCarriesSelectedModelAndChecksRerouteSet(t *testing.T) {
	env := productionFixture(t)
	env.Event = "upstream_refusal"
	env.Metadata = env.Metadata[1:]
	for _, allowed := range []string{"remote-model", "unlisted"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Event   string `json:"event"`
				Refusal struct {
					Model  Candidate `json:"model"`
					Reason string    `json:"reason"`
				} `json:"refusal"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Event != "upstream_refusal" || body.Refusal.Model != env.Selected || body.Refusal.Reason != "upstream refusal" {
				t.Error("refusal contract changed")
			}
			json.NewEncoder(w).Encode(map[string]any{"action": "reroute", "reason": "policy", "allowed_models": []string{allowed}, "route": map[string]any{"allow_models": []map[string]string{{"model_id": "remote-model"}}}})
		}))
		verdict, err := (ProductionHTTP{URL: server.URL + "/api/v1/aiuditor/review", Client: server.Client()}).Review(context.Background(), env)
		verdict.Release()
		server.Close()
		if allowed == "remote-model" && err != nil {
			t.Fatal(err)
		}
		if allowed == "unlisted" && err == nil {
			t.Fatal("reroute broadened")
		}
	}
}
func TestProductionRequiresIdentityAndProtectedAuthority(t *testing.T) {
	env := productionFixture(t)
	for _, endpoint := range []string{"http://authority.example/api/v1/aiuditor/review", "https://authority.example/old", "https://user:secret@authority.example/api/v1/aiuditor/review"} {
		if _, err := (ProductionHTTP{URL: endpoint}).Review(context.Background(), env); err == nil {
			t.Fatal("unprotected authority accepted")
		}
	}
	t.Setenv(ServiceTokenEnv, "")
	if _, err := (ProductionHTTP{URL: "https://authority.example/api/v1/aiuditor/review"}).Review(context.Background(), env); err == nil {
		t.Fatal("missing identity accepted")
	}
}

func TestProductionRefusesUnauthenticatedLoopbackBeforeSendingSecrets(t *testing.T) {
 env := productionFixture(t)
 var calls atomic.Int32
 server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
 defer server.Close()
 _, err := (ProductionHTTP{URL: server.URL + "/api/v1/aiuditor/review"}).Review(context.Background(), env)
 if !errors.Is(err, ErrProductionReview) || calls.Load() != 0 { t.Fatal("unauthenticated loopback contacted") }
 tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
 defer tlsServer.Close()
 _, err = (ProductionHTTP{URL: tlsServer.URL + "/api/v1/aiuditor/review"}).Review(context.Background(), env)
 if !errors.Is(err, ErrProductionReview) || calls.Load() != 0 { t.Fatal("untrusted certificate accepted") }
}
