package proxy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

type captureKey struct{}

func (p *Proxy) prepareCapture(w http.ResponseWriter, request **http.Request) bool {
	r := *request
	if !strings.HasPrefix(r.URL.Path, "/capture/") {
		return true
	}
	id, path, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/capture/"), "/")
	tier, found := p.Config.Captures[id]
	if !ok || !found || !config.CaptureID.MatchString(id) {
		http.Error(w, "unknown captured tool", 404)
		return false
	}
	path = "/" + path
	if _, ok := protocolOf(r.Method, path); !ok {
		http.Error(w, "unsupported captured operation", 404)
		return false
	}
	if p.Config.GatewayEnforcement {
		http.Error(w, "direct capture forbidden by policy", 403)
		return false
	}
	// The configured ingress path binds the original credential to its provider.
	// An explicit conflicting binding is rejected rather than overwritten.
	origins := r.Header.Values("X-Conductor-Credential-Origin")
	if len(origins) > 1 || (len(origins) == 1 && origins[0] != tier.CredentialOrigin) {
		http.Error(w, "credential origin mismatch", 403)
		return false
	}
	next := r.Clone(context.WithValue(r.Context(), captureKey{}, tier))
	next.URL.Path, next.URL.RawPath = path, ""
	next.Header.Set("X-Conductor-Credential-Origin", tier.CredentialOrigin)
	*request = next
	return true
}

func (p *Proxy) captureTier(r *http.Request, selected config.Tier, model string) (config.Tier, error) {
	captured, ok := r.Context().Value(captureKey{}).(config.Tier)
	if !ok {
		return selected, nil
	}
	if selected.Name != "frontier" {
		// The source provider credential must never become a gateway credential.
		if selected.Auth != config.AuthEnv {
			return config.Tier{}, fmt.Errorf("captured alternate routes require their own environment credential")
		}
		return selected, nil
	}
	if os.Getenv("CONDUCTOR_ACCOUNTING_URL") != "" {
		return config.Tier{}, fmt.Errorf("captured providers cannot use device-bound host accounting")
	}
	if captured.Model == "$client" {
		if model == "" || len(model) > 200 || strings.ContainsAny(model, "\r\n") {
			return config.Tier{}, fmt.Errorf("captured request requires a model")
		}
		captured.Model = model
	}
	// Existing hard limits cannot disappear when a captured model is selected.
	if selected.RecipeDigest != "" {
		if selected.Model != captured.Model || selected.InputCounter != captured.InputCounter {
			return config.Tier{}, fmt.Errorf("captured model does not match the active recipe")
		}
		captured.RecipeDigest, captured.OutputLimit = selected.RecipeDigest, selected.OutputLimit
		captured.ContextWindow, captured.TaskTokenLimit = selected.ContextWindow, selected.TaskTokenLimit
		captured.TaskOutputLimit = selected.TaskOutputLimit
		captured.RequiredCapabilities = selected.RequiredCapabilities
	}
	return captured, nil
}
