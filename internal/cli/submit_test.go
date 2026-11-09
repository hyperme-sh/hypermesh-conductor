package cli

import (
	"context"
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/proxy"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitNamedRoute(t *testing.T) {
	t.Setenv("TEST_PEER_GRANT", "fixture")
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/peer/prompt-submit" || r.Method != "POST" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fixture" {
			w.WriteHeader(403)
			return
		}
		var v map[string]string
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil || len(v) != 3 || v["destination"] != "lab" || v["class"] != "summarize" || v["prompt"] != "sample" {
			t.Errorf("unexpected envelope %v", v)
		}
		w.Write([]byte("accepted"))
	}))
	defer server.Close()
	route := submissionRoute{server.URL, []string{"summarize"}, "TEST_PEER_GRANT"}
	var out strings.Builder
	invoke := func(class string) error {
		return submit(context.Background(), server.Client().Transport, route, "lab", class, strings.NewReader("sample"), &out)
	}
	if err := invoke("code"); err == nil || calls != 0 {
		t.Fatal("egress denial failed")
	}
	if err := invoke("summarize"); err != nil || out.String() != "accepted" {
		t.Fatalf("submit: %v %s", err, out.String())
	}
	t.Setenv("TEST_PEER_GRANT", "revoked")
	if err := invoke("summarize"); err == nil || calls != 2 {
		t.Fatal("destination denial failed")
	}
}
func TestSubmitRedirectDenied(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	t.Setenv("TEST_PEER_GRANT", "fixture")
	err := submit(context.Background(), origin.Client().Transport, submissionRoute{origin.URL, []string{"code"}, "TEST_PEER_GRANT"}, "lab", "code", strings.NewReader("sample"), io.Discard)
	if err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestPeerReceiverScopedDispatch(t *testing.T) {
	t.Setenv("INBOUND_GRANT", "allowed")
	calls := 0
	grant := peerGrant{"lab", []string{"summarize"}, "INBOUND_GRANT"}
	server := httptest.NewTLSServer(grant.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Conductor-Class") != "summarize" {
			t.Error("unexpected local dispatch")
		}
		var b struct{ Messages []map[string]string }
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Messages) != 1 || b.Messages[0]["content"] != "sample" || b.Messages[0]["role"] != "user" {
			t.Error("unexpected local request")
		}
		w.Write([]byte("ok"))
	})))
	defer server.Close()
	t.Setenv("OUTBOUND_GRANT", "allowed")
	route := submissionRoute{server.URL, []string{"summarize", "code"}, "OUTBOUND_GRANT"}
	for _, c := range []struct {
		dest, class string
		allow       bool
	}{{"other", "summarize", false}, {"lab", "code", false}, {"lab", "summarize", true}} {
		err := submit(context.Background(), server.Client().Transport, route, c.dest, c.class, strings.NewReader("sample"), io.Discard)
		if (err == nil) != c.allow {
			t.Errorf("%+v: %v", c, err)
		}
	}
	t.Setenv("INBOUND_GRANT", "replacement")
	if submit(context.Background(), server.Client().Transport, route, "lab", "summarize", strings.NewReader("sample"), io.Discard) == nil || calls != 1 {
		t.Fatal("revoked or denied submission dispatched")
	}
}

func TestPeerIngressAllocatesLocalBudgetTask(t *testing.T) {
	t.Setenv("PEER_GRANT", "fixture")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	cfg, err := config.Load(filepath.Join("..", "..", "examples", "endpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cheap = config.Tier{Name: "cheap", Provider: config.ProviderOpenAICompatible, Model: "test", BaseURL: upstream.URL + "/v1", Auth: config.AuthPassthrough, OutputLimit: 8, TaskOutputLimit: 8}
	cfg.Journal = filepath.Join(t.TempDir(), "journal.jsonl")
	journal, err := metrics.OpenJournal(cfg.Journal)
	if err != nil {
		t.Fatal(err)
	}
	handler := peerGrant{"lab", []string{"summarize"}, "PEER_GRANT"}.handler(proxy.New(cfg, nil, journal))
	seen := map[string]bool{}
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/peer/prompt-submit", strings.NewReader(`{"destination":"lab","class":"summarize","prompt":"sample"}`))
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("X-Conductor-Task", "caller-supplied")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		id := rec.Header().Get("X-Conductor-Task")
		if rec.Code != 200 || id == "" || id == "caller-supplied" || seen[id] {
			t.Fatalf("status %d task %q: %s", rec.Code, id, rec.Body.String())
		}
		seen[id] = true
	}
}

func TestLivePeerGrantRevocation(t *testing.T) {
	t.Setenv("LIVE_PEER_TEST", "fixture")
	path := filepath.Join(t.TempDir(), "grant.json")
	grant := []byte(`{"destination":"lab","allowed_classes":["summarize"],"token_env":"LIVE_PEER_TEST"}`)
	if err := os.WriteFile(path, grant, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler, err := loadPeerIngress(path, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	if err != nil {
		t.Fatal(err)
	}
	check := func(status int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/peer/prompt-submit", strings.NewReader(`{"destination":"lab","class":"summarize","prompt":"same"}`))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("status %d want %d", w.Code, status)
		}
	}
	check(200)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check(403)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	check(403)
	if calls != 1 {
		t.Fatalf("dispatched %d times", calls)
	}
}
