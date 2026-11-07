package proxy

import (
	"encoding/json"
	"fmt"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
)

// Anthropic translation supplies a required max_tokens default. Apply the
// recipe first so that default cannot mask an omitted or malformed client limit.
func translateTierRequest(protocol string, tier config.Tier, body []byte) (translate.Wire, error) {
	if tier.Provider == config.ProviderAnthropic && (protocol == translate.ProtoChat || protocol == translate.ProtoResponses) {
		var err error
		body, err = limitOutput(protocol, body, tier.OutputLimit)
		if err != nil {
			return translate.Wire{}, err
		}
	}
	return translate.Request(protocol, tier.Provider, tier.Model, body)
}

// Apply to the final provider payload, including AIuditor rewrites.
func limitOutput(protocol string, body []byte, limit int) ([]byte, error) {
	if limit == 0 || protocol == translate.ProtoEmbeddings {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("invalid generation request")
	}
	field := "max_tokens"
	switch protocol {
	case translate.ProtoChat:
		// Divide before comparing so the aggregate check cannot overflow.
		n, err := candidateCount(obj, "n", 1)
		if err != nil {
			return nil, err
		}
		generated, err := candidateCount(obj, "best_of", n)
		if err != nil {
			return nil, err
		}
		if generated < n || generated > limit {
			return nil, fmt.Errorf("generated candidate count must be between n and the recipe output limit")
		}
		limit /= generated
		if _, ok := obj["max_completion_tokens"]; ok {
			field = "max_completion_tokens"
		}
	case translate.ProtoResponses:
		field = "max_output_tokens"
	case translate.ProtoMessages:
	default:
		return nil, fmt.Errorf("recipe output limit unsupported for %s", protocol)
	}
	for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if raw, ok := obj[name]; ok {
			var count int
			if name != field || json.Unmarshal(raw, &count) != nil || count < 1 || count > limit {
				return nil, fmt.Errorf("invalid %s for recipe output limit %d", name, limit)
			}
		}
	}
	if _, exists := obj[field]; exists {
		return body, nil
	}
	obj[field], _ = json.Marshal(limit)
	return json.Marshal(obj)
}

func candidateCount(obj map[string]json.RawMessage, key string, fallback int) (int, error) {
	raw, ok := obj[key]
	if !ok {
		return fallback, nil
	}
	var count int
	if err := json.Unmarshal(raw, &count); err != nil || count < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return count, nil
}
