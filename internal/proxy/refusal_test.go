package proxy

import (
	"bytes"
	"context"
	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefusalDetection(t *testing.T) {
	for _, raw := range []string{`{"choices":[{"finish_reason":"content_filter"}]}`, `{"choices":[{"message":{"refusal":"no"}}]}`, `{"error":{"code":"content_policy_violation"}}`} {
		r, _ := refusalSignal([]byte(raw))
		if !r {
			t.Fatal(raw)
		}
	}
	r, c := refusalSignal([]byte(`{"choices":[{"delta":{"content":"hello"}}]}`))
	if r || !c {
		t.Fatal("content not recognized")
	}
}
func TestStreamRefusalOnlyBeforeContent(t *testing.T) {
	refusal := "data: {\"choices\":[{\"delta\":{\"refusal\":\"no\"}}]}\n\n"
	content := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	for _, tc := range []struct {
		raw  string
		want bool
	}{{refusal, true}, {content + refusal, false}} {
		body, _, got := sniffRefusal(io.NopCloser(bytes.NewBufferString(tc.raw)))
		if got != tc.want {
			t.Fatal("incorrect retry boundary")
		}
		all, _ := io.ReadAll(body)
		if string(all) != tc.raw {
			t.Fatal("stream bytes changed")
		}
	}
}

type refusalHop struct {
	deny   bool
	events int
}

func (h *refusalHop) Review(_ context.Context, e aiuditor.Envelope) (aiuditor.Verdict, error) {
	if e.Event == "upstream_refusal" {
		h.events++
		if h.deny {
			return aiuditor.Verdict{Action: aiuditor.Deny}, nil
		}
		return aiuditor.Verdict{Action: "reroute", AllowedModels: e.Candidates}, nil
	}
	return aiuditor.Verdict{Action: aiuditor.Allow}, nil
}
func TestRefusalRerouteAndDenyContract(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{true: "deny", false: "reroute"}[deny], func(t *testing.T) {
			calls := map[string]int{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if bytes.Contains(raw, []byte(`"model":"frontier"`)) {
					calls["frontier"]++
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","refusal":"no"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":3,"completion_tokens":0}}`)
				} else {
					calls["cheap"]++
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
				}
			}))
			defer upstream.Close()
			dir := t.TempDir()
			cfg := loadTestConfig(t, dir, upstream.URL+"/v1", upstream.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
			cfg.Cheap.Model = "cheap"
			cfg.Frontier.Model = "frontier"
			cfg.RefusalFallbackTier = "cheap"
			cfg.MaxRefusalAttempts = 2
			hop := &refusalHop{deny: deny}
			p := New(cfg, hop, openJournal(t, dir))
			req := request("/v1/chat/completions", "code", "task", "step", "", `{"conductor_task":"body-task","messages":[{"role":"user","content":"ordinary request"}]}`)
			req.Header.Del("X-Conductor-Task")
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != 200 || calls["frontier"] != 1 || hop.events != 1 {
				t.Fatalf("%d %v %d %s", rec.Code, calls, hop.events, rec.Body.String())
			}
			if !deny && rec.Header().Get("X-Conductor-Task") != "body-task" {
				t.Fatal("retry changed task", rec.Header())
			}
			if deny && calls["cheap"] != 0 || !deny && calls["cheap"] != 1 {
				t.Fatal(calls)
			}
		})
	}
}
