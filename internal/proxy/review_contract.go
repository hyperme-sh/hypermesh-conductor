package proxy

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"github.com/hyperme-sh/hypermesh-conductor/internal/aiuditor"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

func (p *Proxy) productionReview() bool {
	contract, ok := p.Hop.(interface{ ProductionContract() bool })
	return ok && contract.ProductionContract()
}
func policyCandidate(tier config.Tier) (aiuditor.Candidate, error) {
	provider, locality := tier.PolicyProvider, tier.Locality
	if tier.Gateway {
		provider = "hypermesh"
	}
	if tier.Capabilities != nil {
		if tier.Capabilities.Validate(tier.Model, tier.RuntimeRevision, tier.Provider) != nil {
			return aiuditor.Candidate{}, aiuditor.ErrProductionReview
		}
		measured := "remote"
		if tier.Capabilities.Local {
			measured = "local"
		}
		if locality != "" && locality != measured {
			return aiuditor.Candidate{}, aiuditor.ErrProductionReview
		}
		locality = measured
	}
	if provider == "" || (locality != "local" && locality != "remote") {
		return aiuditor.Candidate{}, aiuditor.ErrProductionReview
	}
	return aiuditor.Candidate{Tier: tier.Name, ModelID: tier.Model, Provider: provider, Locality: locality}, nil
}
func (p *Proxy) review(ctx context.Context, env aiuditor.Envelope, selected config.Tier, candidates []config.Tier, choose bool) (aiuditor.Verdict, error) {
	if !p.productionReview() {
		return p.Hop.Review(ctx, env)
	}
	var err error
	env.Selected, err = policyCandidate(selected)
	if err != nil {
		return aiuditor.Verdict{}, err
	}
	for _, tier := range candidates {
		candidate, err := policyCandidate(tier)
		if err != nil {
			return aiuditor.Verdict{}, err
		}
		env.Metadata = append(env.Metadata, candidate)
	}
	verdict, err := p.Hop.Review(ctx, env)
	if err == nil && !choose && env.Event != "upstream_refusal" && verdict.Action != aiuditor.Deny && !slices.Contains(verdict.AllowedModels, selected.Model) {
		verdict.Action = aiuditor.Deny
		verdict.Reason = "selected model excluded by policy"
	}
	return verdict, err
}

// Choose before the normal capability, locality, budget, credential and translation
// checks. The final payload is reviewed again and cannot silently switch models.
func (p *Proxy) reviewChoice(r *http.Request, proto string, prepared []byte, stream bool, decision routing.Decision, selected config.Tier) (config.Tier, error) {
	if !p.productionReview() {
		return selected, nil
	}
	flag := r.Header.Get("X-Conductor-Local-Only")
	if (flag != "" && flag != "true" && flag != "false") || (flag == "true" && !localReview(p.Config.AIU)) {
		return config.Tier{}, aiuditor.ErrProductionReview
	}
	candidates := []config.Tier{selected}
	_, captured := r.Context().Value(captureKey{}).(config.Tier)
	retry, _ := r.Context().Value(refusalKey{}).(refusalState)
	if !captured && retry.nextTier == "" {
		for _, other := range []config.Tier{p.Config.Cheap, p.Config.Frontier} {
			if other.Configured() && other.Model != selected.Model {
				candidates = append(candidates, other)
			}
		}
	}
	verdict, err := p.review(aiuditor.WithResponseBudget(r.Context(), reserveBody, releaseBody), aiuditor.Envelope{
		Request: aiuditor.Request{Protocol: proto, Method: r.Method, Path: r.URL.Path, Stream: stream, Headers: aiuditor.FilterHeaders(r.Header), Body: prepared},
		Routing: aiuditor.Routing{JobClass: decision.JobClass, Tier: selected.Name, ModelID: selected.Model, Reason: decision.Reason},
	}, selected, candidates, true)
	defer verdict.Release()
	if err != nil || verdict.Action == aiuditor.Deny {
		return config.Tier{}, aiuditor.ErrProductionReview
	}
	for _, candidate := range candidates {
		if !slices.Contains(verdict.AllowedModels, candidate.Model) {
			continue
		}
		if candidate.Model == selected.Model {
			return selected, nil
		}
		if (selected.TaskTokenLimit > 0 && candidate.TaskTokenLimit != selected.TaskTokenLimit) || (selected.TaskOutputLimit > 0 && candidate.TaskOutputLimit != selected.TaskOutputLimit) {
			return config.Tier{}, aiuditor.ErrProductionReview
		}
		candidate.RequiredCapabilities = append(append([]string{}, candidate.RequiredCapabilities...), selected.RequiredCapabilities...)
		if selected.OutputLimit > 0 && (candidate.OutputLimit == 0 || candidate.OutputLimit > selected.OutputLimit) {
			candidate.OutputLimit = selected.OutputLimit
		}
		// Never forward an original endpoint credential to a policy-selected origin.
		if r.Header.Get("X-Conductor-Credential-Origin") == "" {
			origin, err := url.Parse(selected.BaseURL)
			if err != nil {
				return config.Tier{}, aiuditor.ErrProductionReview
			}
			r.Header.Set("X-Conductor-Credential-Origin", origin.Scheme+"://"+origin.Host)
		}
		return candidate, nil
	}
	return config.Tier{}, aiuditor.ErrProductionReview
}
