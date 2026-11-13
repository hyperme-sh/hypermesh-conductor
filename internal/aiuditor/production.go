package aiuditor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ServiceTokenEnv = "CONDUCTOR_AIUDITOR_TOKEN"
const ServiceTenantEnv = "CONDUCTOR_AIUDITOR_TENANT_ID"

var ErrProductionReview = errors.New("production model review refused")

type Candidate struct {
	Tier     string `json:"tier"`
	ModelID  string `json:"model_id"`
	Provider string `json:"provider"`
	Locality string `json:"locality"`
}
type ModelSelector struct {
	ModelID  string `json:"model_id,omitempty"`
	Provider string `json:"provider,omitempty"`
}
type RouteConstraint struct {
	AllowModels *[]ModelSelector `json:"allow_models,omitempty"`
	DenyModels  *[]ModelSelector `json:"deny_models,omitempty"`
}

// ProductionHTTP uses only the dedicated service credential, never a caller JWT.
// HTTP remains a legacy fixture adapter; configured HTTP service routing uses this type.
type ProductionHTTP struct {
	URL     string
	Timeout time.Duration
	Client  *http.Client
}

func (ProductionHTTP) ProductionContract() bool { return true }

type authenticatedTransport struct {
	base          http.RoundTripper
	token, tenant string
}

func (t authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("X-Internal-Token", t.token)
	request.Header.Set("X-Tenant-ID", t.tenant)
	return t.base.RoundTrip(request)
}
func (h ProductionHTTP) Review(ctx context.Context, env Envelope) (Verdict, error) {
	verdict, err := h.review(ctx, env)
	if err != nil {
		verdict.Release()
		return Verdict{}, ErrProductionReview
	}
	return verdict, nil
}
func validCandidate(c Candidate) bool {
	return len(c.Tier) > 0 && len(c.Tier) <= 128 && len(c.ModelID) > 0 && len(c.ModelID) <= 256 && len(c.Provider) > 0 && len(c.Provider) <= 128 && (c.Locality == "local" || c.Locality == "remote")
}
func (h ProductionHTTP) review(ctx context.Context, env Envelope) (Verdict, error) {
	endpoint, err := url.Parse(h.URL)
	if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/api/v1/aiuditor/review" {
		return Verdict{}, ErrProductionReview
	}
	ip := net.ParseIP(endpoint.Hostname())
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return Verdict{}, ErrProductionReview
	}
	token := os.Getenv(ServiceTokenEnv)
	tenant, err := uuid.Parse(os.Getenv(ServiceTenantEnv))
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") || err != nil || tenant == uuid.Nil {
		return Verdict{}, ErrProductionReview
	}
	if !validCandidate(env.Selected) || env.Selected.ModelID != env.Routing.ModelID || env.Selected.Tier != env.Routing.Tier || len(env.Metadata) == 0 || len(env.Metadata) > 128 {
		return Verdict{}, ErrProductionReview
	}
	seen := map[string]bool{}
	chosen := false
	for _, candidate := range env.Metadata {
		if !validCandidate(candidate) || seen[candidate.ModelID] {
			return Verdict{}, ErrProductionReview
		}
		seen[candidate.ModelID] = true
		chosen = chosen || candidate == env.Selected
	}
	event := "review"
	var refusal any
	if env.Event == "upstream_refusal" {
		event = env.Event
		if seen[env.Selected.ModelID] {
			return Verdict{}, ErrProductionReview
		}
		refusal = map[string]any{"model": env.Selected, "reason": "upstream refusal"}
	} else if env.Event != "" && env.Event != "review" || !chosen {
		return Verdict{}, ErrProductionReview
	}
	routingRaw, _ := json.Marshal(env.Routing)
	var routing map[string]any
	json.Unmarshal(routingRaw, &routing)
	routing["provider"], routing["locality"] = env.Selected.Provider, env.Selected.Locality
	if env.Request.Headers == nil {
		env.Request.Headers = map[string]string{}
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(env.Request.Body, &body) != nil || body == nil {
		return Verdict{}, ErrProductionReview
	}
	wire := map[string]any{"request_id": uuid.NewString(), "event": event, "purpose": "renter", "request": env.Request, "routing": routing, "candidates": env.Metadata}
	if refusal != nil {
		wire["refusal"] = refusal
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return Verdict{}, ErrProductionReview
	}
	client := http.Client{}
	if h.Client != nil {
		client = *h.Client
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = authenticatedTransport{base, token, tenant.String()}
	// Reuse the bounded transport and aggregate response budget. Only the serialized
	// envelope differs from the explicit legacy adapter.
	legacy := HTTP{URL: h.URL, Timeout: h.Timeout, Client: &client, strict: true}
	verdict, err := legacy.reviewBytes(ctx, env, raw)
	if err != nil {
		return verdict, err
	}
	if verdict.Action == Deny {
		return verdict, nil
	}
	if verdict.Route == nil || (verdict.Route.AllowModels == nil) == (verdict.Route.DenyModels == nil) {
		verdict.Release()
		return Verdict{}, ErrProductionReview
	}
	selectors := verdict.Route.AllowModels
	allow := selectors != nil
	if !allow {
		selectors = verdict.Route.DenyModels
	}
	if len(*selectors) > 128 {
		verdict.Release()
		return Verdict{}, ErrProductionReview
	}
	for _, selector := range *selectors {
		if (selector.ModelID == "") == (selector.Provider == "") || len(selector.ModelID) > 256 || len(selector.Provider) > 128 {
			verdict.Release()
			return Verdict{}, ErrProductionReview
		}
	}
	allowed := []string{}
	for _, candidate := range env.Metadata {
		matched := false
		for _, selector := range *selectors {
			matched = matched || selector.ModelID == candidate.ModelID || selector.Provider == candidate.Provider
		}
		if matched == allow {
			allowed = append(allowed, candidate.ModelID)
		}
	}
	if event == "upstream_refusal" {
		if verdict.Action != "reroute" {
			verdict.Release()
			return Verdict{}, ErrProductionReview
		}
		exact := map[string]bool{}
		for _, id := range verdict.AllowedModels {
			if !seen[id] || exact[id] {
				verdict.Release()
				return Verdict{}, ErrProductionReview
			}
			exact[id] = true
		}
		if len(exact) != len(allowed) {
			verdict.Release()
			return Verdict{}, ErrProductionReview
		}
		for _, id := range allowed {
			if !exact[id] {
				verdict.Release()
				return Verdict{}, ErrProductionReview
			}
		}
	} else if verdict.Action != Allow && verdict.Action != Modify {
		verdict.Release()
		return Verdict{}, ErrProductionReview
	}
	if verdict.Action == Modify && (len(verdict.Body) == 0 || !json.Valid(verdict.Body)) {
		verdict.Release()
		return Verdict{}, ErrProductionReview
	}
	verdict.AllowedModels = allowed
	if len(allowed) == 0 {
		verdict.Action = Deny
		verdict.Reason = "no policy-permitted candidate"
	}
	return verdict, nil
}
