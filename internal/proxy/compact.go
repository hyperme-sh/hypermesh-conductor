package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/history"
	"github.com/hyperme-sh/hypermesh-conductor/internal/jsonbody"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/notes"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"github.com/hyperme-sh/hypermesh-conductor/internal/usage"
)

func (p *Proxy) maybeCompact(ctx context.Context, client *http.Request, proto string, prepared []byte, taskID, stepID, prefixHash string, selected config.Tier) ([]byte, bool, int, int, error) {
	if p.Journal == nil || p.Config.Compaction.MaxTailTokens <= 0 {
		return prepared, false, 0, 0, nil
	}
	if proto != "openai.chat" && proto != "anthropic.messages" {
		return prepared, false, 0, 0, nil
	}
	obj, err := jsonbody.Parse(prepared)
	if err != nil {
		return prepared, false, 0, 0, nil
	}
	plan, err := compact.PlanMessages(obj.Values["messages"], p.Config.Compaction.MaxTailTokens, p.Config.Compaction.RecentMessages)
	if err != nil || !plan.Needed {
		return prepared, false, 0, 0, nil
	}
	compactionClient := clientForCompaction(client, p.Config.Cheap, selected)
	if err := authorizeCompaction(compactionClient, p.Config.Cheap); err != nil {
		return prepared, false, 0, 0, err
	}
	summary, parsed, latency, measuredPrefix, lookups, hits, cached, err := p.tailSummary(ctx, compactionClient, prefixHash, plan.SummaryInput)
	if !cached {
		compactID := stepID + ".compact"
		record := metrics.StepRecord{
			TaskID: taskID, StepID: compactID, JobClass: "summarize", Tier: "cheap",
			ModelID: p.Config.Cheap.Model, Reason: "compaction of the tail on the cheap tier",
			PrefixSHA256: measuredPrefix, LatencyMS: &latency, UsageStatus: metrics.StatusUncertain,
		}
		if err != nil {
			var denied proxyAuthError
			if errors.As(err, &denied) || errors.Is(err, errBodyBudget) {
				return prepared, false, lookups, hits, err
			}
			msg := "compaction request failed"
			record.Error = &msg
			p.journal(record)
			return prepared, false, lookups, hits, nil
		}
		applyUsage(&record, parsed, p.Config, "cheap")
		p.journal(record)
	}
	if err != nil {
		return prepared, false, lookups, hits, nil
	}
	rewritten, err := compact.ApplyMessages(plan, summary)
	if err != nil {
		return prepared, false, lookups, hits, nil
	}
	if err := obj.Set("messages", json.RawMessage(rewritten)); err != nil {
		return prepared, false, lookups, hits, nil
	}
	next, err := obj.Marshal()
	if err != nil {
		return prepared, false, lookups, hits, nil
	}
	store, err := history.Open(history.PathFor(p.Journal.Path()))
	if err != nil {
		return prepared, false, lookups, hits, nil
	}
	if err := store.Append(history.Record{
		TaskID: taskID, StepID: stepID, PrefixSHA256: prefixHash, Summary: summary,
		OriginalTail: joinRaw(plan.TailRaw),
	}); err != nil {
		return prepared, false, lookups, hits, nil
	}
	return next, true, lookups, hits, nil
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
	needEnv := tier.APIKeyEnv != "" && (tier.Auth == config.AuthEnv || (tier.Auth == config.AuthPassthroughOrEnv && clientKey == ""))
	if !needEnv {
		return nil
	}
	return authorizeProxy(client)
}

func (p *Proxy) tailSummary(ctx context.Context, client *http.Request, prefixHash, tail string) (string, usage.Usage, int, string, int, int, bool, error) {
	lookups, hits := 0, 0
	if p.Notes != nil && p.Notes.Cache != nil {
		lookups = 1
		if cached, ok := p.Notes.Cache.Get("sum:" + notes.SummaryKey(prefixHash, tail)); ok && cached != "" {
			return cached, usage.UncertainUsage(), 0, "", lookups, 1, true, nil
		}
	}
	summary, parsed, latency, measuredPrefix, err := p.cheapSummary(ctx, client, tail)
	if err == nil && p.Notes != nil && p.Notes.Cache != nil {
		p.Notes.Cache.Put("sum:"+notes.SummaryKey(prefixHash, tail), summary)
	}
	return summary, parsed, latency, measuredPrefix, lookups, hits, false, err
}

func (p *Proxy) cheapSummary(ctx context.Context, client *http.Request, tail string) (string, usage.Usage, int, string, error) {
	measuredPrefix := ""
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
		return "", usage.UncertainUsage(), 0, measuredPrefix, err
	}
	wire, err := translateTierRequest(translate.ProtoChat, tier, raw)
	if err != nil {
		return "", usage.UncertainUsage(), 0, measuredPrefix, err
	}
	measuredPrefix, err = hashBody(wire.Protocol, wire.Body)
	if err != nil {
		return "", usage.UncertainUsage(), 0, measuredPrefix, err
	}
	held := len(wire.Body)
	if !reserveBody(held) {
		return "", usage.UncertainUsage(), 0, measuredPrefix, errBodyBudget
	}
	defer func() { releaseBody(held) }()
	verdict, hopErr := p.Hop.Review(aiuditor.WithResponseBudget(ctx, reserveBody, releaseBody), aiuditor.Envelope{
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
	defer verdict.Release()
	bodyToSend := wire.Body
	if hopErr != nil {
		if errors.Is(hopErr, aiuditor.ErrResponseBudget) {
			return "", usage.UncertainUsage(), 0, measuredPrefix, errBodyBudget
		}
		if p.Config.AIU.FailClosed {
			return "", usage.UncertainUsage(), 0, measuredPrefix, hopErr
		}
	} else if verdict.Action == aiuditor.Deny {
		return "", usage.UncertainUsage(), 0, measuredPrefix, fmt.Errorf("aiuditor deny: %s", aiuditor.ReasonText(verdict.Reason))
	} else if verdict.Action == aiuditor.Modify {
		if !json.Valid(verdict.Body) {
			return "", usage.UncertainUsage(), 0, measuredPrefix, fmt.Errorf("aiuditor modify was not JSON")
		}
		forced, err := forceModel(verdict.Body, tier.Model)
		if err != nil {
			return "", usage.UncertainUsage(), 0, measuredPrefix, err
		}
		bodyToSend = forced
		measuredPrefix, err = hashBody(wire.Protocol, forced)
		if err != nil {
			return "", usage.UncertainUsage(), 0, measuredPrefix, err
		}
	}
	bodyToSend, err = limitOutput(wire.Protocol, bodyToSend, tier.OutputLimit)
	if err != nil {
		return "", usage.UncertainUsage(), 0, measuredPrefix, err
	}
	if !replaceReservation(held, len(bodyToSend)) {
		held = 0
		return "", usage.UncertainUsage(), 0, measuredPrefix, errBodyBudget
	}
	held = len(bodyToSend)
	raw = nil
	wire.Body = nil
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tier.BaseURL+"/"+wire.Path, bytes.NewReader(bodyToSend))
	if err != nil {
		return "", usage.UncertainUsage(), 0, measuredPrefix, err
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := applyAuth(req, client, tier); err != nil {
		return "", usage.UncertainUsage(), 0, measuredPrefix, err
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
		return "", usage.UncertainUsage(), elapsedMS(start), measuredPrefix, err
	}
	defer resp.Body.Close()
	body, heldResp, err := readReserved(resp.Body, 1<<20)
	defer releaseBody(heldResp)
	latency := elapsedMS(start)
	if errors.Is(err, errBodyBudget) {
		_ = resp.Body.Close()
		return "", usage.UncertainUsage(), latency, measuredPrefix, err
	}
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err == nil {
			err = fmt.Errorf("provider HTTP %d", resp.StatusCode)
		}
		return "", usage.UncertainUsage(), latency, measuredPrefix, err
	}
	anthropic := tier.Provider == config.ProviderAnthropic || wire.Protocol == translate.ProtoMessages
	parsed := usage.ParseOpenAI(body, false)
	if anthropic {
		parsed = usage.ParseAnthropic(body)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", parsed, latency, measuredPrefix, err
	}
	if !summaryComplete(root, anthropic) || providerError(body) {
		return "", parsed, latency, measuredPrefix, fmt.Errorf("compaction summary was incomplete")
	}
	text := assistantText(root)
	if anthropic {
		text = anthropicSummaryText(root)
	}
	if strings.TrimSpace(text) == "" {
		return "", parsed, latency, measuredPrefix, fmt.Errorf("compaction summary was empty")
	}
	return text, parsed, latency, measuredPrefix, nil
}

// Reject explicit partial, filtered and tool-call finishes. Some local
// OpenAI-compatible endpoints omit finish metadata on a complete response.
func summaryComplete(root map[string]any, anthropic bool) bool {
	if anthropic {
		reason, _ := root["stop_reason"].(string)
		return reason == "" || reason == "end_turn" || reason == "stop_sequence"
	}
	choices, _ := root["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	choice, _ := choices[0].(map[string]any)
	reason, _ := choice["finish_reason"].(string)
	return reason == "" || reason == "stop"
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
