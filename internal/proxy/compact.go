package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
	"github.com/hyperme-sh/hypermesh-conductor/internal/history"
	"github.com/hyperme-sh/hypermesh-conductor/internal/jsonbody"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/usage"
)

func (p *Proxy) maybeCompact(ctx context.Context, proto string, prepared []byte, taskID, stepID, prefixHash string) ([]byte, bool) {
	if p.Journal == nil || p.Config.Compaction.MaxTailTokens <= 0 {
		return prepared, false
	}
	if proto != "openai.chat" && proto != "anthropic.messages" {
		return prepared, false
	}
	obj, err := jsonbody.Parse(prepared)
	if err != nil {
		return prepared, false
	}
	plan, err := compact.PlanMessages(obj.Values["messages"], p.Config.Compaction.MaxTailTokens, p.Config.Compaction.RecentMessages)
	if err != nil || !plan.Needed {
		return prepared, false
	}
	summary, parsed, latency, err := p.cheapSummary(ctx, plan.SummaryInput)
	compactID := stepID + ".compact"
	record := metrics.StepRecord{
		TaskID: taskID, StepID: compactID, JobClass: "summarize", Tier: "cheap",
		ModelID: p.Config.Cheap.Model, Reason: "compaction of the tail on the cheap tier",
		PrefixSHA256: prefixHash, LatencyMS: &latency, UsageStatus: metrics.StatusUncertain,
	}
	if err != nil {
		msg := "compaction request failed"
		record.Error = &msg
		p.journal(record)
		return prepared, false
	}
	applyUsage(&record, parsed, p.Config, "cheap")
	p.journal(record)
	rewritten, err := compact.ApplyMessages(plan, summary)
	if err != nil {
		return prepared, false
	}
	if err := obj.Set("messages", json.RawMessage(rewritten)); err != nil {
		return prepared, false
	}
	next, err := obj.Marshal()
	if err != nil {
		return prepared, false
	}
	store, err := history.Open(history.PathFor(p.Journal.Path()))
	if err == nil {
		_ = store.Append(history.Record{
			TaskID: taskID, StepID: stepID, PrefixSHA256: prefixHash, Summary: summary,
			OriginalTail: joinRaw(plan.TailRaw),
		})
	}
	return next, true
}

func (p *Proxy) cheapSummary(ctx context.Context, tail string) (string, usage.Usage, int, error) {
	tier := p.Config.Cheap
	payload := map[string]any{
		"model": tier.Model,
		"messages": []any{
			map[string]string{"role": "system", "content": "Summarize the conversation tail as goals, steps, facts, and notes."},
			map[string]string{"role": "user", "content": tail},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", usage.UncertainUsage(), 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tier.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", usage.UncertainUsage(), 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tier.APIKeyEnv != "" {
		key, err := tier.Key()
		if err != nil {
			return "", usage.UncertainUsage(), 0, err
		}
		if key != "" {
			if tier.Provider == "anthropic" {
				req.Header.Set("x-api-key", key)
			} else {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
	}
	client := &http.Client{
		Timeout:       tier.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := elapsedMS(start)
	if err != nil {
		return "", usage.UncertainUsage(), latency, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= 400 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", usage.UncertainUsage(), latency, err
	}
	parsed := usage.ParseOpenAI(body, false)
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", parsed, latency, err
	}
	text := assistantText(root)
	if strings.TrimSpace(text) == "" {
		text = "summary unavailable"
	}
	return text, parsed, latency, nil
}

func assistantText(root map[string]any) string {
	choices, _ := root["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	text, _ := message["content"].(string)
	return text
}

func joinRaw(items []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}
