// Package aiuditor is the hop between routing and the destination.
//
// This package does not implement AIuditor. It defines the request contract,
// an HTTP client for a configured endpoint, and an in-process stub for tests
// and local runs. Safety rules, plugins, and RBAC belong to AIuditor.
package aiuditor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	Allow  = "allow"
	Deny   = "deny"
	Modify = "modify"
)

// Request is the call under review. Secret headers are already removed.
type Request struct {
	Protocol string            `json:"protocol"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Stream   bool              `json:"stream"`
	Headers  map[string]string `json:"headers"`
	Body     json.RawMessage   `json:"body"`
}

// Routing is the policy decision that AIuditor may not upgrade.
type Routing struct {
	JobClass           string  `json:"job_class"`
	Tier               string  `json:"tier"`
	ModelID            string  `json:"model_id"`
	Reason             string  `json:"reason"`
	SuggestionTier     *string `json:"suggestion_tier"`
	SuggestionAccepted *bool   `json:"suggestion_accepted"`
	PrefixSHA256       string  `json:"prefix_sha256"`
}

// Envelope is the HTTP body posted to AIuditor.
type Envelope struct {
	Request Request `json:"request"`
	Routing Routing `json:"routing"`
}

// Verdict is allow, deny, or modify. Modify carries a replacement JSON body.
type Verdict struct {
	Action string          `json:"action"`
	Reason string          `json:"reason"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// Hop reviews one request.
type Hop interface {
	Review(ctx context.Context, env Envelope) (Verdict, error)
}

// SecretHeaders are removed before a request is shown to AIuditor or a log.
var SecretHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"x-api-key":           {},
	"cookie":              {},
	"set-cookie":          {},
}

// FilterHeaders copies headers that are safe to send to AIuditor.
func FilterHeaders(header http.Header) map[string]string {
	out := map[string]string{}
	for key, values := range header {
		name := strings.ToLower(key)
		if _, secret := SecretHeaders[name]; secret {
			continue
		}
		if strings.HasPrefix(name, "x-conductor-") {
			continue
		}
		if len(values) == 0 {
			continue
		}
		out[name] = values[0]
	}
	return out
}

// Rules is the stub's local stand-in for a policy file.
type Rules struct {
	DenyJobClasses map[string]string `json:"deny_job_classes"`
	Replace        []ReplaceRule     `json:"replace"`
}

// ReplaceRule rewrites exact string values in the JSON body.
type ReplaceRule struct {
	JobClass string `json:"job_class"`
	From     string `json:"from"`
	To       string `json:"to"`
	Reason   string `json:"reason"`
}

// Stub is the in-process no-op hop. With an empty rule set it allows.
type Stub struct {
	Rules Rules
}

// LoadRules reads a stub rules file. An empty path is an allow-all stub.
func LoadRules(path string) (Rules, error) {
	if path == "" {
		return Rules{}, nil
	}
	file, err := osOpen(path)
	if err != nil {
		return Rules{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return Rules{}, err
	}
	var rules Rules
	if err := json.Unmarshal(raw, &rules); err != nil {
		return Rules{}, fmt.Errorf("aiuditor rules file is not JSON")
	}
	return rules, nil
}

// Review applies deny rules, then string replacement, then allows.
func (s Stub) Review(_ context.Context, env Envelope) (Verdict, error) {
	if reason, ok := s.Rules.DenyJobClasses[env.Routing.JobClass]; ok {
		return Verdict{Action: Deny, Reason: reason}, nil
	}
	body := env.Request.Body
	reason := "stub allow"
	changed := false
	for _, rule := range s.Rules.Replace {
		if rule.JobClass != "" && rule.JobClass != env.Routing.JobClass {
			continue
		}
		if rule.From == "" {
			continue
		}
		next, did := replaceStrings(body, rule.From, rule.To)
		if did {
			body = next
			changed = true
			if rule.Reason != "" {
				reason = rule.Reason
			}
		}
	}
	if changed {
		return Verdict{Action: Modify, Reason: reason, Body: body}, nil
	}
	return Verdict{Action: Allow, Reason: reason}, nil
}

func replaceStrings(body json.RawMessage, from, to string) (json.RawMessage, bool) {
	if len(body) == 0 {
		return body, false
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return body, false
	}
	changed := walkReplace(value, from, to)
	if !changed {
		return body, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return body, false
	}
	return raw, true
}

func walkReplace(value any, from, to string) bool {
	switch node := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range node {
			if text, ok := child.(string); ok && strings.Contains(text, from) {
				node[key] = strings.ReplaceAll(text, from, to)
				changed = true
				continue
			}
			if walkReplace(child, from, to) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for i, child := range node {
			if text, ok := child.(string); ok && strings.Contains(text, from) {
				node[i] = strings.ReplaceAll(text, from, to)
				changed = true
				continue
			}
			if walkReplace(child, from, to) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

// HTTP posts the envelope to a configured AIuditor endpoint.
type HTTP struct {
	URL     string
	Timeout time.Duration
	Client  *http.Client
}

// Review calls AIuditor. A non-200 response or a transport error is returned
// to the caller, which fails closed when that is configured.
func (h HTTP) Review(ctx context.Context, env Envelope) (Verdict, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := json.Marshal(env)
	if err != nil {
		return Verdict{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(raw))
	if err != nil {
		return Verdict{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Verdict{}, fmt.Errorf("aiuditor unreachable")
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Verdict{}, fmt.Errorf("aiuditor unreachable")
	}
	if resp.StatusCode != http.StatusOK {
		return Verdict{}, fmt.Errorf("aiuditor HTTP %d", resp.StatusCode)
	}
	var verdict Verdict
	if err := json.Unmarshal(respBody, &verdict); err != nil {
		return Verdict{}, fmt.Errorf("aiuditor response was not JSON")
	}
	switch verdict.Action {
	case Allow, Deny, Modify:
		return verdict, nil
	default:
		return Verdict{}, fmt.Errorf("aiuditor response has an unknown action")
	}
}
