package proxy

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/media"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

func (p *Proxy) serveOperations(w http.ResponseWriter, r *http.Request) {
	operations := []map[string]any{}
	for _, tier := range []config.Tier{p.Config.Cheap, p.Config.Frontier} {
		if len(tier.OCROutputs) > 0 && tier.Capabilities != nil && tier.Capabilities.Features["ocr"] == "verified" {
			operations = append(operations, map[string]any{"operation": "ocr", "tier": tier.Name, "model": tier.Model, "outputs": tier.OCROutputs, "local": tier.Capabilities.Local})
		}
	}
	writeJSON(w, 200, map[string]any{"operations": operations})
}
func (p *Proxy) serveOCR(w http.ResponseWriter, r *http.Request) {
	tier, err := p.Config.Endpoint(firstNonEmpty(r.Header.Get("X-Conductor-Tier"), "cheap"))
	if err != nil || tier.Gateway || tier.Capabilities == nil || tier.Capabilities.Require(tier.Model, tier.RuntimeRevision, tier.Provider, []string{"ocr"}, false) != nil {
		writeJSON(w, 422, map[string]any{"error": "no verified native OCR endpoint"})
		return
	}
	if tier.TaskTokenLimit > 0 || tier.TaskOutputLimit > 0 {
		writeJSON(w, 422, map[string]any{"error": "OCR cannot guarantee an inference token budget"})
		return
	}
	raw, err := readRequestBody(r.Body)
	defer releaseBody(len(raw))
	if err != nil {
		writeJSON(w, 413, map[string]any{"error": "invalid OCR input size"})
		return
	}
	wire, request, err := media.Prepare(raw, tier.Model, tier.OCROutputs)
	if err != nil {
		writeJSON(w, 422, map[string]any{"error": err.Error()})
		return
	}
	localOnly := request.LocalOnly || r.Header.Get("X-Conductor-Local-Only") == "true"
	if localOnly && (!tier.Capabilities.Local || !localReview(p.Config.AIU)) {
		writeJSON(w, 422, map[string]any{"error": "OCR locality requirements not met"})
		return
	}
	required := append([]string(nil), request.Required...)
	// Review the native request; replacements must pass the same contract again.
	verdict, err := p.review(r.Context(), aiuditor.Envelope{Request: aiuditor.Request{Protocol: "mistral.ocr", Method: "POST", Path: "/v1/ocr", Body: raw}, Routing: aiuditor.Routing{JobClass: "extract", Tier: tier.Name, ModelID: tier.Model}}, tier, []config.Tier{tier}, false)
	defer verdict.Release()
	if err != nil || verdict.Action == aiuditor.Deny {
		writeJSON(w, 403, map[string]any{"error": "OCR denied by policy"})
		return
	}
	if verdict.Action == aiuditor.Modify {
		wire, request, err = media.Prepare(verdict.Body, tier.Model, tier.OCROutputs)
		if err != nil || (request.LocalOnly && (!tier.Capabilities.Local || !localReview(p.Config.AIU))) {
			writeJSON(w, 422, map[string]any{"error": "invalid reviewed OCR request"})
			return
		}
		for _, field := range required {
			if !slices.Contains(request.Required, field) {
				writeJSON(w, 422, map[string]any{"error": "review cannot remove required OCR outputs"})
				return
			}
		}
	} else if verdict.Action != aiuditor.Allow {
		writeJSON(w, 403, map[string]any{"error": "invalid OCR policy verdict"})
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), "POST", tier.BaseURL+"/ocr", bytes.NewReader(wire))
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "invalid OCR endpoint"})
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	secrets, err := applyAuth(upstream, r, tier)
	if err != nil {
		writeJSON(w, 401, map[string]any{"error": "OCR credential rejected"})
		return
	}
	task := firstNonEmpty(r.Header.Get("X-Conductor-Task"), newID("task"))
	step := firstNonEmpty(r.Header.Get("X-Conductor-Step"), newID("step"))
	if !idPattern.MatchString(task) || !idPattern.MatchString(step) {
		writeJSON(w, 400, map[string]any{"error": "invalid OCR correlation ID"})
		return
	}
	record := p.baseRecord(task, step, routing.Decision{JobClass: "extract", Tier: tier.Name, Reason: "native OCR"}, tier.Model, "native-ocr")
	record.Destination = "direct"
	endpoint, _ := url.Parse(tier.BaseURL)
	record.ProviderHost = endpoint.Hostname()
	start := time.Now()
	defer func() { ms := elapsedMS(start); record.LatencyMS = &ms; p.journal(record) }()
	response, err := p.upstreamClient(tier.Timeout, false).Do(upstream)
	if err != nil {
		message := "OCR backend unavailable"
		record.Error = &message
		writeJSON(w, 502, map[string]any{"error": message})
		return
	}
	defer response.Body.Close()
	body, held, err := readReserved(response.Body, maxBody)
	defer releaseBody(held)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "invalid OCR response size"})
		return
	}
	status := response.StatusCode
	if status >= 300 && status < 400 {
		status = 502
		body = []byte(`{"error":"OCR redirect refused"}`)
	}
	if status == 200 && media.ValidateResponse(body, request.Required) != nil {
		status = 502
		body = []byte(`{"error":"OCR output contract not satisfied"}`)
	}
	if status != 200 {
		message := "OCR backend or output contract failed"
		record.Error = &message
	}
	record.UsageStatus = metrics.StatusUncertain
	body = redactJSON(body, secrets)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func localReview(a config.AIU) bool {
	if a.Mode != config.AIUModeHTTP {
		return true
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}
