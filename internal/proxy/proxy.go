// Package proxy is the localhost OpenAI-compatible and Anthropic-compatible front door.
//
// Each request is classified, reviewed by the AIuditor hop, then forwarded to
// the tier destination. Journals record metrics only.
package proxy

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/jsonbody"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/prefix"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
	"github.com/hyperme-sh/hypermesh-conductor/internal/rules"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
	"github.com/hyperme-sh/hypermesh-conductor/internal/usage"
)

const maxBody = 32 << 20

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

// Proxy serves the local endpoints.
type Proxy struct {
	Config  config.Config
	Hop     aiuditor.Hop
	Journal *metrics.Journal
	Logf    func(string, ...any)
}

// New builds a proxy. A nil hop is replaced with an allow-all stub.
func New(cfg config.Config, hop aiuditor.Hop, journal *metrics.Journal) *Proxy {
	if hop == nil {
		hop = aiuditor.Stub{}
	}
	return &Proxy{Config: cfg, Hop: hop, Journal: journal, Logf: func(string, ...any) {}}
}

// ListenAndServe binds the configured loopback address.
func (p *Proxy) ListenAndServe() error {
	if err := config.ValidateListen(p.Config.Listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", p.Config.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: p}
	return server.Serve(listener)
}

// ServeHTTP implements the local API.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proto, ok := protocolOf(r.Method, r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "unknown path"}})
		return
	}
	if proto == translate.ProtoModels {
		p.serveModels(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "unreadable body"}})
		return
	}
	if len(body) > maxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]any{"message": "request body is too large"}})
		return
	}
	obj, err := jsonbody.Parse(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "request body must be a JSON object"}})
		return
	}
	className, suggest, taskID, stepID, err := p.identity(r, obj, proto)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	obj.DropPrefix("conductor_")
	p.injectPrefix(&obj, proto)
	prepared, err := obj.Marshal()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "request body must be a JSON object"}})
		return
	}
	policy, err := rules.Load(p.Config.RulesFile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	peak := compact.EstimateTokens(string(prepared))
	clientHash, _ := hashBody(proto, prepared)
	configHash, _ := p.Config.Prefix.SHA256()
	prefixMatch := clientHash != "" && clientHash == configHash
	if suggest == "" {
		suggest = rules.Suggest(policy, prefixMatch)
	}
	decision, err := routing.Route(className, suggest, rules.EffectiveClassTiers(p.Config.ClassTiers, policy, className, peak))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	tier, err := p.Config.Endpoint(decision.Tier)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	stream := boolField(obj, "stream")
	prelimHash, _ := hashBody(proto, prepared)
	verdict, hopErr := p.Hop.Review(r.Context(), aiuditor.Envelope{
		Request: aiuditor.Request{
			Protocol: proto,
			Method:   r.Method,
			Path:     r.URL.Path,
			Stream:   stream,
			Headers:  aiuditor.FilterHeaders(r.Header),
			Body:     prepared,
		},
		Routing: aiuditor.Routing{
			JobClass:           decision.JobClass,
			Tier:               decision.Tier,
			ModelID:            tier.Model,
			Reason:             decision.Reason,
			SuggestionTier:     decision.SuggestionTier,
			SuggestionAccepted: decision.SuggestionAccepted,
			PrefixSHA256:       prelimHash,
		},
	})
	base := p.baseRecord(taskID, stepID, decision, tier.Model, prelimHash)
	if prefixMatch {
		yes := true
		base.PrefixCacheMatch = &yes
	}
	if hopErr != nil {
		if p.Config.AIU.FailClosed {
			msg := "aiuditor unreachable"
			base.Error = &msg
			base.UsageStatus = metrics.StatusUncertain
			p.finish(w, r, http.StatusServiceUnavailable, nil, base, nil, false)
			return
		}
	} else if verdict.Action == aiuditor.Deny {
		msg := "aiuditor deny: " + verdict.Reason
		base.Error = &msg
		base.UsageStatus = metrics.StatusUncertain
		p.finish(w, r, http.StatusForbidden, nil, base, nil, false)
		return
	} else if verdict.Action == aiuditor.Modify && len(verdict.Body) > 0 {
		if !json.Valid(verdict.Body) {
			msg := "aiuditor modify was not JSON"
			base.Error = &msg
			base.UsageStatus = metrics.StatusUncertain
			p.finish(w, r, http.StatusBadGateway, nil, base, nil, false)
			return
		}
		prepared = forceStreamFlag(verdict.Body, stream)
		if hash, err := hashBody(proto, prepared); err == nil {
			prelimHash = hash
			base.PrefixSHA256 = hash
		}
	}
	next, didCompact, compactErr := p.maybeCompact(r.Context(), r, proto, prepared, taskID, stepID, prelimHash)
	if compactErr != nil {
		var denied proxyAuthError
		if errors.As(compactErr, &denied) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": compactErr.Error()}})
			return
		}
	}
	if didCompact {
		prepared = next
		yes := true
		base.Compacted = &yes
	}
	forced, err := forceModel(prepared, tier.Model)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	wire, err := translate.Request(proto, tier.Provider, tier.Model, forced)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	if hash, err := hashBody(wire.Protocol, wire.Body); err == nil {
		base.PrefixSHA256 = hash
	}
	secrets, authReq, err := p.upstreamRequest(r, tier, wire, stream)
	if err != nil {
		status := http.StatusBadRequest
		var denied proxyAuthError
		if errors.As(err, &denied) {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	p.forward(w, r, authReq, tier, wire, stream, secrets, base)
}

func (p *Proxy) forward(w http.ResponseWriter, client *http.Request, upstream *http.Request, tier config.Tier, wire translate.Wire, stream bool, secrets []string, record metrics.StepRecord) {
	start := time.Now()
	httpClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:                 nil,
			ResponseHeaderTimeout: tier.Timeout,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		},
	}
	if !stream {
		httpClient.Timeout = tier.Timeout
	}
	resp, err := httpClient.Do(upstream)
	if err != nil {
		ms := elapsedMS(start)
		record.LatencyMS = &ms
		msg := "provider request failed"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		ms := elapsedMS(start)
		record.LatencyMS = &ms
		msg := fmt.Sprintf("provider HTTP %d", resp.StatusCode)
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, resp.StatusCode, redact(raw, secrets), record, secrets, false)
		return
	}
	if stream {
		p.stream(w, client, resp, wire, start, secrets, record)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	ms := elapsedMS(start)
	record.LatencyMS = &ms
	if err == nil && len(raw) > maxBody {
		err = fmt.Errorf("provider response is too large")
	}
	if err != nil {
		msg := "provider request failed"
		if len(raw) > maxBody {
			msg = "provider response is too large"
		}
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	converted, err := translate.Response(protocolOfRequest(client), wire.Protocol, raw)
	if err != nil {
		msg := err.Error()
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		p.finish(w, client, http.StatusBadGateway, nil, record, secrets, false)
		return
	}
	parsed := usage.ParseOpenAI(raw, wire.Protocol == translate.ProtoEmbeddings)
	if parsed.Status != usage.Measured {
		parsed = usage.ParseAnthropic(raw)
	}
	applyUsage(&record, parsed, p.Config, record.Tier)
	p.finish(w, client, http.StatusOK, redact(converted, secrets), record, secrets, false)
}

func (p *Proxy) stream(w http.ResponseWriter, client *http.Request, resp *http.Response, wire translate.Wire, start time.Time, secrets []string, record metrics.StepRecord) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flush(w)
	bridge := translate.NewStream(protocolOfRequest(client), wire.Protocol)
	acc := usage.UncertainUsage()
	buf := make([]byte, 32*1024)
	var pending []byte
	var readErr error
	failed := false
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			pending = append(pending, chunk...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := pending[:i]
				pending = pending[i+1:]
				payload := ssePayload(line)
				if payload != nil {
					if providerError(payload) {
						failed = true
					}
					usage.Absorb(&acc, payload, wire.Protocol == translate.ProtoEmbeddings)
				}
			}
			out := bridge.Consume(chunk)
			if len(out) > 0 {
				_, _ = w.Write(redact(out, secrets))
				flush(w)
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	if len(pending) > 0 {
		if payload := ssePayload(pending); payload != nil {
			if providerError(payload) {
				failed = true
			}
			usage.Absorb(&acc, payload, wire.Protocol == translate.ProtoEmbeddings)
		}
	}
	if tail := bridge.Finish(); len(tail) > 0 {
		_, _ = w.Write(redact(tail, secrets))
		flush(w)
	}
	ms := elapsedMS(start)
	record.LatencyMS = &ms
	if readErr != nil || failed {
		msg := "provider request failed"
		record.Error = &msg
		record.UsageStatus = metrics.StatusUncertain
		record.InputTokens = nil
		record.OutputTokens = nil
		record.CacheHitTokens = nil
		record.CostProxy = nil
		record.CostProxyLabel = nil
	} else {
		applyUsage(&record, acc, p.Config, record.Tier)
	}
	p.journal(record)
	p.logRecord(client, record)
}

func (p *Proxy) finish(w http.ResponseWriter, client *http.Request, status int, body []byte, record metrics.StepRecord, secrets []string, streamed bool) {
	if !streamed {
		if body == nil {
			body = mustJSON(map[string]any{"error": map[string]any{"message": errorText(record)}})
		}
		body = redact(body, secrets)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	p.journal(record)
	p.logRecord(client, record)
}

func (p *Proxy) journal(record metrics.StepRecord) {
	if p.Journal == nil {
		return
	}
	if err := p.Journal.Append(record); err != nil && p.Logf != nil {
		p.Logf("journal write failed")
	}
}

func (p *Proxy) logRecord(client *http.Request, record metrics.StepRecord) {
	if p.Logf == nil {
		return
	}
	in, out := "-", "-"
	if record.InputTokens != nil {
		in = fmt.Sprintf("%d", *record.InputTokens)
	}
	if record.OutputTokens != nil {
		out = fmt.Sprintf("%d", *record.OutputTokens)
	}
	p.Logf("path=%s class=%s tier=%s status=%s input_tokens=%s output_tokens=%s", client.URL.Path, record.JobClass, record.Tier, record.UsageStatus, in, out)
}

func (p *Proxy) serveModels(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []any{
			map[string]any{"id": p.Config.Cheap.Model, "object": "model", "owned_by": routing.Cheap},
			map[string]any{"id": p.Config.Frontier.Model, "object": "model", "owned_by": routing.Frontier},
		},
	})
}

func (p *Proxy) identity(r *http.Request, obj jsonbody.Object, proto string) (className, suggest, taskID, stepID string, err error) {
	className = firstNonEmpty(r.Header.Get("X-Conductor-Class"), rawString(obj.Values["conductor_class"]))
	if className == "" && proto == translate.ProtoEmbeddings {
		className = "embed"
	}
	if className == "" {
		className = p.Config.DefaultClass
	}
	if className == "" {
		return "", "", "", "", fmt.Errorf("job class is empty")
	}
	suggest = firstNonEmpty(r.Header.Get("X-Conductor-Suggest"), rawString(obj.Values["conductor_suggest"]))
	taskID = firstNonEmpty(r.Header.Get("X-Conductor-Task"), rawString(obj.Values["conductor_task"]))
	stepID = firstNonEmpty(r.Header.Get("X-Conductor-Step"), rawString(obj.Values["conductor_step"]))
	if taskID == "" {
		taskID = newID("t")
	}
	if stepID == "" {
		stepID = newID("s")
	}
	if !idPattern.MatchString(taskID) || !idPattern.MatchString(stepID) {
		return "", "", "", "", fmt.Errorf("task_id and step_id must be short stable ids")
	}
	return className, suggest, taskID, stepID, nil
}

func (p *Proxy) injectPrefix(obj *jsonbody.Object, proto string) {
	if p.Config.Prefix.System == "" || proto == translate.ProtoEmbeddings {
		return
	}
	switch proto {
	case translate.ProtoMessages:
		if len(obj.Values["system"]) == 0 || string(obj.Values["system"]) == "null" || string(obj.Values["system"]) == `""` {
			_ = obj.Set("system", p.Config.Prefix.System)
		}
	case translate.ProtoResponses:
		if len(obj.Values["instructions"]) == 0 {
			_ = obj.Set("instructions", p.Config.Prefix.System)
		}
	default:
		messages := decodeArray(obj.Values["messages"])
		hasSystem := false
		for _, item := range messages {
			msg, _ := item.(map[string]any)
			if msg["role"] == "system" {
				hasSystem = true
			}
		}
		if !hasSystem {
			messages = append([]any{map[string]any{"role": "system", "content": p.Config.Prefix.System}}, messages...)
			_ = obj.Set("messages", messages)
		}
	}
	if len(obj.Values["tools"]) == 0 && len(p.Config.Prefix.Tools) > 0 && proto != translate.ProtoResponses {
		_ = obj.Set("tools", translate.ClientTools(proto, p.Config.Prefix.Tools))
	}
}

func (p *Proxy) upstreamRequest(client *http.Request, tier config.Tier, wire translate.Wire, stream bool) ([]string, *http.Request, error) {
	if tier.IncludeUsage && stream && wire.Protocol == translate.ProtoChat && tier.Provider == config.ProviderOpenAI {
		obj, err := jsonbody.Parse(wire.Body)
		if err == nil {
			_ = obj.Set("stream_options", map[string]any{"include_usage": true})
			if raw, err := obj.Marshal(); err == nil {
				wire.Body = raw
			}
		}
	}
	url := tier.BaseURL + "/" + wire.Path
	req, err := http.NewRequestWithContext(client.Context(), http.MethodPost, url, bytes.NewReader(wire.Body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	secrets, err := applyAuth(req, client, tier)
	if err != nil {
		return nil, nil, err
	}
	if tier.Provider == config.ProviderAnthropic || wire.Protocol == translate.ProtoMessages {
		version := client.Header.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
	}
	return secrets, req, nil
}

func applyAuth(dst, src *http.Request, tier config.Tier) ([]string, error) {
	var secrets []string
	clientKey := clientCredential(src, tier.Provider)
	var envKey string
	var err error
	needEnv := tier.Auth == config.AuthEnv || (tier.Auth == config.AuthPassthroughOrEnv && clientKey == "")
	if needEnv {
		if err = authorizeProxy(src); err != nil {
			return nil, err
		}
		envKey, err = tier.Key()
		if err != nil {
			return nil, err
		}
	}
	switch tier.Provider {
	case config.ProviderAnthropic:
		key := clientKey
		if needEnv {
			key = envKey
		}
		if key != "" {
			dst.Header.Set("x-api-key", key)
			secrets = append(secrets, key)
		}
	default:
		key := clientKey
		if needEnv {
			key = envKey
		}
		if key != "" {
			dst.Header.Set("Authorization", "Bearer "+key)
			secrets = append(secrets, key)
		}
	}
	if header := src.Header.Get("Authorization"); header != "" {
		secrets = append(secrets, header)
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			secrets = append(secrets, strings.TrimSpace(header[7:]))
		}
	}
	if key := src.Header.Get("x-api-key"); key != "" {
		secrets = append(secrets, key)
	}
	return secrets, nil
}

func clientCredential(src *http.Request, provider string) string {
	if provider == config.ProviderAnthropic {
		return strings.TrimSpace(src.Header.Get("x-api-key"))
	}
	header := strings.TrimSpace(src.Header.Get("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func (p *Proxy) baseRecord(taskID, stepID string, decision routing.Decision, model, hash string) metrics.StepRecord {
	return metrics.StepRecord{
		TaskID:             taskID,
		StepID:             stepID,
		JobClass:           decision.JobClass,
		Tier:               decision.Tier,
		ModelID:            model,
		Reason:             decision.Reason,
		SuggestionTier:     decision.SuggestionTier,
		SuggestionAccepted: decision.SuggestionAccepted,
		PrefixSHA256:       hash,
		UsageStatus:        metrics.StatusUncertain,
	}
}

func applyUsage(record *metrics.StepRecord, parsed usage.Usage, cfg config.Config, tier string) {
	if parsed.Status != usage.Measured || parsed.Input == nil || parsed.Output == nil {
		record.UsageStatus = metrics.StatusUncertain
		record.InputTokens = nil
		record.OutputTokens = nil
		record.CacheHitTokens = nil
		record.CostProxy = nil
		record.CostProxyLabel = nil
		return
	}
	record.UsageStatus = metrics.StatusMeasured
	record.InputTokens = parsed.Input
	record.OutputTokens = parsed.Output
	record.CacheHitTokens = parsed.Cache
	if rate, ok := cfg.Rate(tier); ok {
		amount := float64(*parsed.Input)/1_000_000*rate.InputPerMillion + float64(*parsed.Output)/1_000_000*rate.OutputPerMillion
		label := fmt.Sprintf("proxy: tokens × published rate (%s); not a charge", rate.Source)
		record.CostProxy = &amount
		record.CostProxyLabel = &label
	}
}

func forceModel(body []byte, model string) ([]byte, error) {
	obj, err := jsonbody.Parse(body)
	if err != nil {
		return nil, err
	}
	obj.DropPrefix("conductor_")
	if err := obj.Set("model", model); err != nil {
		return nil, err
	}
	return obj.Marshal()
}

func hashBody(proto string, body []byte) (string, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", err
	}
	system := ""
	var tools []any
	switch proto {
	case translate.ProtoMessages:
		text, err := translate.SystemText(root["system"])
		if err != nil {
			return "", err
		}
		system = text
		tools, _ = root["tools"].([]any)
	case translate.ProtoResponses:
		system, _ = root["instructions"].(string)
		tools, _ = root["tools"].([]any)
	default:
		if messages, ok := root["messages"].([]any); ok {
			var parts []string
			for _, item := range messages {
				msg, _ := item.(map[string]any)
				if msg["role"] != "system" {
					continue
				}
				if text, ok := msg["content"].(string); ok {
					parts = append(parts, text)
				}
			}
			system = strings.Join(parts, "\n")
		}
		tools, _ = root["tools"].([]any)
	}
	if system == "" && len(tools) == 0 {
		return prefix.HashRequest("", []any{})
	}
	return prefix.HashRequest(system, tools)
}

func protocolOf(method, path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	switch path {
	case "/v1/chat/completions", "/chat/completions":
		return translate.ProtoChat, method == http.MethodPost
	case "/v1/responses", "/responses":
		return translate.ProtoResponses, method == http.MethodPost
	case "/v1/messages", "/messages":
		return translate.ProtoMessages, method == http.MethodPost
	case "/v1/embeddings", "/embeddings":
		return translate.ProtoEmbeddings, method == http.MethodPost
	case "/v1/models", "/models":
		return translate.ProtoModels, method == http.MethodGet
	default:
		return "", false
	}
}

func protocolOfRequest(r *http.Request) string {
	proto, _ := protocolOf(r.Method, r.URL.Path)
	return proto
}

func boolField(obj jsonbody.Object, key string) bool {
	var value bool
	if err := json.Unmarshal(obj.Values[key], &value); err != nil {
		return false
	}
	return value
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func decodeArray(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	var value []any
	_ = json.Unmarshal(raw, &value)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func elapsedMS(start time.Time) int {
	return int((time.Since(start) + time.Millisecond/2) / time.Millisecond)
}

type proxyAuthError struct{ msg string }

func (e proxyAuthError) Error() string { return e.msg }

func authorizeProxy(src *http.Request) error {
	want := strings.TrimSpace(os.Getenv(config.ProxyTokenEnv))
	if want == "" {
		return proxyAuthError{config.ProxyTokenEnv + " is required when a tier uses an environment key"}
	}
	got := strings.TrimSpace(src.Header.Get(config.ProxyTokenHeader))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return proxyAuthError{"proxy token is required to use the configured provider key"}
	}
	return nil
}

func providerError(payload []byte) bool {
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return false
	}
	if kind, _ := obj["type"].(string); kind == "error" {
		return true
	}
	_, ok := obj["error"].(map[string]any)
	return ok
}

func forceStreamFlag(body []byte, stream bool) []byte {
	obj, err := jsonbody.Parse(body)
	if err != nil {
		return body
	}
	if stream {
		if err := obj.Set("stream", true); err != nil {
			return body
		}
	} else {
		obj.Delete("stream")
	}
	raw, err := obj.Marshal()
	if err != nil {
		return body
	}
	return raw
}

func ssePayload(line []byte) []byte {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}
	return []byte(data)
}

func redact(body []byte, secrets []string) []byte {
	if len(body) == 0 {
		return body
	}
	out := body
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(secret), []byte("[redacted]"))
	}
	return out
}

func errorText(record metrics.StepRecord) string {
	if record.Error != nil && *record.Error != "" {
		return *record.Error
	}
	return "request failed"
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func mustJSON(payload any) []byte {
	raw, _ := json.Marshal(payload)
	return raw
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
