package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/history"
	"github.com/hyperme-sh/hypermesh-conductor/internal/jsonbody"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"github.com/hyperme-sh/hypermesh-conductor/internal/usage"
)

func (p *Proxy) maybeCompact(ctx context.Context, client *http.Request, proto string, prepared []byte, taskID, stepID, prefixHash string, selected config.Tier) ([]byte, bool, error) {
	if p.Journal == nil || p.Config.Compaction.MaxTailTokens <= 0 {
		return prepared, false, nil
	}
	if proto != "openai.chat" && proto != "anthropic.messages" {
		return prepared, false, nil
	}
	obj, err := jsonbody.Parse(prepared)
	if err != nil {
		return prepared, false, nil
	}
	plan, err := compact.PlanMessages(obj.Values["messages"], p.Config.Compaction.MaxTailTokens, p.Config.Compaction.RecentMessages)
	if err != nil || !plan.Needed {
		return prepared, false, nil
	}
	compactionClient := clientForCompaction(client, p.Config.Cheap, selected)
	if err := authorizeCompaction(compactionClient, p.Config.Cheap); err != nil {
		return prepared, false, err
	}
	summary, parsed, latency, err := p.cheapSummary(ctx, compactionClient, plan.SummaryInput)
	compactID := stepID + ".compact"
	record := metrics.StepRecord{
		TaskID: taskID, StepID: compactID, JobClass: "summarize", Tier: "cheap",
		ModelID: p.Config.Cheap.Model, Reason: "compaction of the tail on the cheap tier",
		PrefixSHA256: prefixHash, LatencyMS: &latency, UsageStatus: metrics.StatusUncertain,
	}
	if err != nil {
		var denied proxyAuthError
		if errors.As(err, &denied) {
			return prepared, false, err
		}
		msg := "compaction request failed"
		record.Error = &msg
		p.journal(record)
		return prepared, false, nil
	}
	applyUsage(&record, parsed, p.Config, "cheap")
	p.journal(record)
	rewritten, err := compact.ApplyMessages(plan, summary)
	if err != nil {
		return prepared, false, nil
	}
	if err := obj.Set("messages", json.RawMessage(rewritten)); err != nil {
		return prepared, false, nil
	}
	next, err := obj.Marshal()
	if err != nil {
		return prepared, false, nil
	}
	store, err := history.Open(history.PathFor(p.Journal.Path()))
	if err != nil {
		return prepared, false, nil
	}
	if err := store.Append(history.Record{
		TaskID: taskID, StepID: stepID, PrefixSHA256: prefixHash, Summary: summary,
		OriginalTail: joinRaw(plan.TailRaw),
	}); err != nil {
		return prepared, false, nil
	}
	return next, true, nil
}

// clientForCompaction keeps a caller credential on the cheap call only when
// that call uses the same origin as the selected tier. A frontier bearer is
// not forwarded to a different cheap endpoint.
func clientForCompaction(client *http.Request, cheap, selected config.Tier) *http.Request {
	if cheap.BaseURL == selected.BaseURL && cheap.Provider == selected.Provider {
		return client
	}
	clone := client.Clone(client.Context())
	clone.Header.Del("Authorization")
	clone.Header.Del("x-api-key")
	return clone
}

func authorizeCompaction(client *http.Request, tier config.Tier) error {
	if !tier.UsesEnvKey() {
		return nil
	}
	clientKey := clientCredential(client, tier.Provider)
	needEnv := tier.Auth == config.AuthEnv || (tier.Auth == config.AuthPassthroughOrEnv && clientKey == "")
	if !needEnv {
		return nil
	}
	return authorizeProxy(client)
}

func (p *Proxy) cheapSummary(ctx context.Context, client *http.Request, tail string) (string, usage.Usage, int, error) {
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
	wire, err := translate.Request(translate.ProtoChat, tier.Provider, tier.Model, raw)
	if err != nil {
		return "", usage.UncertainUsage(), 0, err
	}
	verdict, hopErr := p.Hop.Review(ctx, aiuditor.Envelope{
		Request: aiuditor.Request{
			Protocol: wire.Protocol,
			Method:   http.MethodPost,
			Path:     "/" + wire.Path,
			Headers:  aiuditor.FilterHeaders(client.Header),
			Body:     wire.Body,
		},
		Routing: aiuditor.Routing{
			JobClass: "summarize", Tier: "cheap", ModelID: tier.Model,
			Reason: "compaction of the tail on the cheap tier",
		},
	})
	bodyToSend := wire.Body
	if hopErr != nil {
		if p.Config.AIU.FailClosed {
			return "", usage.UncertainUsage(), 0, hopErr
		}
	} else if verdict.Action == aiuditor.Deny {
		return "", usage.UncertainUsage(), 0, fmt.Errorf("aiuditor deny: %s", verdict.Reason)
	} else if verdict.Action == aiuditor.Modify {
		if !json.Valid(verdict.Body) {
			return "", usage.UncertainUsage(), 0, fmt.Errorf("aiuditor modify was not JSON")
		}
		forced, err := forceModel(verdict.Body, tier.Model)
		if err != nil {
			return "", usage.UncertainUsage(), 0, err
		}
		bodyToSend = forced
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tier.BaseURL+"/"+wire.Path, bytes.NewReader(bodyToSend))
	if err != nil {
		return "", usage.UncertainUsage(), 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := applyAuth(req, client, tier); err != nil {
		return "", usage.UncertainUsage(), 0, err
	}
	if tier.Provider == config.ProviderAnthropic || wire.Protocol == translate.ProtoMessages {
		version := client.Header.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
	}
	start := time.Now()
	resp, err := p.upstreamClient(tier.Timeout, false).Do(req)
	if err != nil {
		return "", usage.UncertainUsage(), elapsedMS(start), err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	latency := elapsedMS(start)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err == nil {
			err = fmt.Errorf("provider HTTP %d", resp.StatusCode)
		}
		return "", usage.UncertainUsage(), latency, err
	}
	anthropic := tier.Provider == config.ProviderAnthropic || wire.Protocol == translate.ProtoMessages
	parsed := usage.ParseOpenAI(body, false)
	if anthropic {
		parsed = usage.ParseAnthropic(body)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", parsed, latency, err
	}
	text := assistantText(root)
	if anthropic {
		text = anthropicSummaryText(root)
	}
	if strings.TrimSpace(text) == "" {
		return "", parsed, latency, fmt.Errorf("compaction summary was empty")
	}
	return text, parsed, latency, nil
}

func anthropicSummaryText(root map[string]any) string {
	switch content := root["content"].(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, item := range content {
			obj, _ := item.(map[string]any)
			if obj["type"] == "text" {
				if text, ok := obj["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
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
