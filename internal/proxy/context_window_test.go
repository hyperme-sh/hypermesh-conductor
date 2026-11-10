package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
)

func TestContextAdmissionAtGenerationBoundary(t *testing.T) {
	dir := t.TempDir()
	countResponse := `{"count":28,"max_model_len":32}`
	counts, generations := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing provider credential")
		}
		switch r.URL.Path {
		case "/api/tokenize":
			counts++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if body["model"] != "frontier" || body["tools"] == nil || body["max_tokens"] != nil || len(body["messages"].([]any)) != 2 {
				t.Errorf("incorrect tokenizer input: %v", body)
			}
			fmt.Fprint(w, countResponse)
		case "/api/v1/chat/completions":
			generations++
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":28,"completion_tokens":1}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	cfg := loadTestConfig(t, dir, upstream.URL+"/api/v1", upstream.URL+"/api/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	cfg.Frontier.InputCounter = "vllm"
	cfg.Frontier.ContextWindow = 64
	cfg.Frontier.OutputLimit = 8
	journal := openJournal(t, dir)
	server := New(cfg, nil, journal)
	dispatch := func(extra string, want int) {
		t.Helper()
		req := request("/v1/chat/completions", "code", "lease", "step", "", `{"messages":[{"role":"user","content":"hi"}],`+extra+`}`)
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	}
	dispatch(`"n":2,"max_tokens":4`, 200)
	rows, err := metrics.ReadJournal(journal.Path())
	if err != nil || len(rows) != 1 || rows[0].CountedInputTokens == nil || *rows[0].CountedInputTokens != 28 {
		t.Fatal(rows, err)
	}
	dispatch(`"max_tokens":5`, 400)
	server.Config.Frontier.ContextWindow = 31
	dispatch(`"max_tokens":4`, 400)
	countResponse = `{"count":null,"max_model_len":32}`
	dispatch(`"max_tokens":1`, 503)
	dispatch(`"unsupported_template_option":true`, 400)
	if counts != 4 || generations != 1 {
		t.Fatalf("counts=%d generations=%d", counts, generations)
	}
}

func TestLeaseIDStaysOnGenerationAndLeavesTheTokenizer(t *testing.T) {
	const lease = "22222222-2222-2222-2222-222222222222"
	var tokenBody, genBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/tokenize":
			tokenBody = string(raw)
			fmt.Fprint(w, `{"count":4,"max_model_len":128}`)
		case "/v1/chat/completions":
			genBody = string(raw)
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := loadTestConfig(t, dir, upstream.URL+"/v1", upstream.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	cfg.Cheap.InputCounter = "vllm"
	cfg.Cheap.ContextWindow = 64
	cfg.Cheap.OutputLimit = 8
	req := request("/v1/messages", "parse", "task-1", "lease-count", "", `{"lease_id":"`+lease+`","max_tokens":4,"messages":[{"role":"user","content":"hi"}]}`)
	req.Header.Set("Authorization", "Bearer fixture")
	rec := httptest.NewRecorder()
	New(cfg, nil, openJournal(t, dir)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(tokenBody, "lease_id") || !strings.Contains(genBody, `"lease_id":"`+lease+`"`) {
		t.Fatalf("code %d token %s gen %s body %s", rec.Code, tokenBody, genBody, rec.Body.String())
	}
}
