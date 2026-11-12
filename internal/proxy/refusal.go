package proxy

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/usage"
)

type refusalKey struct{}
type refusalState struct {
	body     []byte
	tried    []string
	nextTier string
}

func refusalSignal(raw []byte) (refused, content bool) {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	if reason, _ := obj["stop_reason"].(string); reason == "refusal" || reason == "content_filter" {
		refused = true
	}
	if kind, _ := obj["type"].(string); strings.Contains(kind, "refusal") {
		refused = true
	}
	if kind, _ := obj["type"].(string); kind == "response.function_call_arguments.delta" {
		content = true
	}
	if item, ok := obj["item"].(map[string]any); ok && item["type"] == "function_call" {
		content = true
	}
	if kind, _ := obj["type"].(string); kind == "response.output_text.delta" {
		if text, _ := obj["delta"].(string); text != "" {
			content = true
		}
	}
	if e, ok := obj["error"].(map[string]any); ok {
		switch e["code"] {
		case "content_filter", "content_policy_violation", "policy_violation":
			refused = true
		}
	}
	if choices, ok := obj["choices"].([]any); ok {
		for _, choice := range choices {
			c, _ := choice.(map[string]any)
			if c["finish_reason"] == "refusal" || c["finish_reason"] == "content_filter" {
				refused = true
			}
			for _, key := range []string{"message", "delta"} {
				m, _ := c[key].(map[string]any)
				if text, _ := m["refusal"].(string); text != "" {
					refused = true
				}
				if text, _ := m["content"].(string); text != "" {
					content = true
				}
				if calls, _ := m["tool_calls"].([]any); len(calls) > 0 {
					content = true
				}
			}
		}
	}
	for _, key := range []string{"delta", "content_block"} {
		if d, ok := obj[key].(map[string]any); ok {
			if text, _ := d["text"].(string); text != "" {
				content = true
			}
			if d["type"] == "tool_use" || d["type"] == "input_json_delta" {
				content = true
			}
			if d["stop_reason"] == "refusal" {
				refused = true
			}
		}
	}
	return
}

type replayBody struct {
	io.Reader
	io.Closer
}

// Inspect only the prefix before visible text/tool content. The original bytes
// are replayed unchanged if rerouting is denied. Buffered bytes are bounded.
func sniffRefusal(body io.ReadCloser) (io.ReadCloser, []byte, bool) {
	reader := bufio.NewReader(body)
	prefix := make([]byte, 0, 1<<20)
	event := make([]byte, 0, 1<<20)
	var refusal []byte
	for len(prefix) < 1<<20 {
		line, err := reader.ReadSlice('\n')
		prefix = append(prefix, line...)
		event = append(event, line...)
		if err == bufio.ErrBufferFull {
			continue
		}
		data := bytes.TrimSpace(event)
		event = event[:0]
		if bytes.HasPrefix(data, []byte("data:")) {
			raw := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
			refused, content := refusalSignal(raw)
			if content {
				break
			}
			if refused {
				refusal = append([]byte(nil), raw...)
				break
			}
			if refusalTerminal(raw) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return &replayBody{Reader: io.MultiReader(bytes.NewReader(prefix), reader), Closer: body}, refusal, len(refusal) > 0
}

func (p *Proxy) rerouteRefusal(w http.ResponseWriter, client *http.Request, tier config.Tier, raw []byte, record metrics.StepRecord) bool {
	state, _ := client.Context().Value(refusalKey{}).(refusalState)
	tried := append(append([]string(nil), state.tried...), tier.Model)
	w.Header().Set("X-Conductor-Attempted-Models", strings.Join(tried, ","))
	max := p.Config.MaxRefusalAttempts
	if max == 0 {
		max = 2
	}
	nextName := p.Config.RefusalFallbackTier
	if len(state.body) == 0 || len(tried) >= max || nextName == "" {
		return false
	}
	next, err := p.Config.Endpoint(nextName)
	if _, captured := client.Context().Value(captureKey{}).(config.Tier); captured && nextName == "frontier" {
		return false
	}
	if err != nil || next.Model == tier.Model {
		return false
	}
	for _, model := range tried {
		if model == next.Model {
			return false
		}
	}
	// A fallback cannot escape an existing hard task limit.
	if (tier.TaskTokenLimit > 0 && next.TaskTokenLimit != tier.TaskTokenLimit) || (tier.TaskOutputLimit > 0 && next.TaskOutputLimit != tier.TaskOutputLimit) {
		return false
	}
	verdict, err := p.Hop.Review(client.Context(), aiuditor.Envelope{Event: "upstream_refusal", Candidates: []string{next.Model},
		Request: aiuditor.Request{Protocol: protocolOfRequest(client), Method: "POST", Path: client.URL.Path, Body: state.body},
		Routing: aiuditor.Routing{JobClass: record.JobClass, Tier: tier.Name, ModelID: tier.Model, Reason: "upstream refusal"}})
	defer verdict.Release()
	if err != nil || verdict.Action != "reroute" {
		return false
	}
	allowed := false
	for _, model := range verdict.AllowedModels {
		if model == next.Model {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	parsed := usage.ParseOpenAI(raw, false)
	if parsed.Status != usage.Measured {
		parsed = usage.ParseAnthropic(raw)
	}
	applyUsage(&record, parsed, p.Config, record.Tier)
	reason := "upstream refusal"
	record.Error = &reason
	p.recordDispatch(client, record)
	ctx := context.WithValue(client.Context(), budgetContextKey{}, (*sql.DB)(nil))
	ctx = context.WithValue(ctx, refusalKey{}, refusalState{body: state.body, tried: tried, nextTier: nextName})
	retry := client.Clone(ctx)
	retry.Body = io.NopCloser(bytes.NewReader(state.body))
	retry.ContentLength = int64(len(state.body))
	step := record.StepID
	if len(step) > 180 {
		step = step[:180]
	}
	retry.Header.Set("X-Conductor-Task", record.TaskID)
	retry.Header.Set("X-Conductor-Step", step+".retry")
	// Credentials remain scoped to the original endpoint across tiers.
	if retry.Header.Get("X-Conductor-Credential-Origin") == "" {
		if origin, err := url.Parse(tier.BaseURL); err == nil {
			retry.Header.Set("X-Conductor-Credential-Origin", origin.Scheme+"://"+origin.Host)
		}
	}
	p.ServeHTTP(w, retry)
	return true
}

// markRefusal records a bounded category, never the provider refusal text.
func markRefusal(record *metrics.StepRecord) {
	reason := "upstream refusal"
	record.Error = &reason
}

func refusalTerminal(raw []byte) bool {
	if bytes.Equal(raw, []byte("[DONE]")) {
		return true
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	if _, ok := obj["error"]; ok {
		return true
	}
	switch obj["type"] {
	case "response.completed", "response.failed", "response.incomplete", "message_stop", "error":
		return true
	}
	return false
}
