// Package usage reads provider token reports.
//
// Missing, partial, negative, or wrongly typed counts stay uncertain.
// A reported zero stays zero. Cache-hit tokens stay nil unless the provider
// reports them.
package usage

import "encoding/json"

const (
	Measured  = "measured"
	Uncertain = "uncertain"
)

// Usage is the token report for one provider response.
type Usage struct {
	Input  *int
	Output *int
	Cache  *int
	Status string
}

// UncertainUsage is an explicit missing report.
func UncertainUsage() Usage { return Usage{Status: Uncertain} }

// ParseOpenAI reads a chat, responses, or embeddings JSON body.
// embeddings reports output tokens as zero when the provider omits them and
// the prompt count is present, because that API does not generate completions.
func ParseOpenAI(payload []byte, embeddings bool) Usage {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return UncertainUsage()
	}
	return fromMap(root, embeddings)
}

// ParseAnthropic reads a Messages API JSON body.
func ParseAnthropic(payload []byte) Usage {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return UncertainUsage()
	}
	if message, ok := root["message"].(map[string]any); ok {
		if usage, ok := message["usage"].(map[string]any); ok {
			parsed := fromUsageObject(usage, false)
			if parsed.Status == Measured {
				return parsed
			}
		}
	}
	return fromMap(root, false)
}

// Absorb folds one JSON payload into an accumulator.
// A chunk may carry only input tokens or only output tokens. Malformed counts
// are ignored so a later good report can still be measured.
func Absorb(dst *Usage, payload []byte, embeddings bool) {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return
	}
	if message, ok := root["message"].(map[string]any); ok {
		if usage, ok := message["usage"].(map[string]any); ok {
			applyPartial(dst, usage, embeddings)
		}
	}
	if response, ok := root["response"].(map[string]any); ok {
		if usage, ok := response["usage"].(map[string]any); ok {
			applyPartial(dst, usage, embeddings)
		}
	}
	if usage, ok := root["usage"].(map[string]any); ok {
		applyPartial(dst, usage, embeddings)
	}
	if dst.Input != nil && dst.Output != nil {
		dst.Status = Measured
		return
	}
	if dst.Status == "" {
		dst.Status = Uncertain
	}
}

func applyPartial(dst *Usage, usage map[string]any, embeddings bool) {
	input, inputOK := firstCount(usage, "prompt_tokens", "input_tokens")
	output, outputOK := firstCount(usage, "completion_tokens", "output_tokens")
	if !inputOK && keyPresent(usage, "prompt_tokens", "input_tokens") {
		return
	}
	if !outputOK && keyPresent(usage, "completion_tokens", "output_tokens") {
		return
	}
	cache, cacheOK, badCache := cacheCount(usage)
	if badCache {
		return
	}
	if embeddings && !outputOK && inputOK {
		zero := 0
		output = &zero
		outputOK = true
	}
	if inputOK && dst.Input == nil {
		dst.Input = input
	}
	if outputOK {
		dst.Output = output
	}
	if cacheOK {
		dst.Cache = cache
	}
}

func fromMap(root map[string]any, embeddings bool) Usage {
	usage, ok := root["usage"].(map[string]any)
	if !ok {
		return UncertainUsage()
	}
	return fromUsageObject(usage, embeddings)
}

func fromUsageObject(usage map[string]any, embeddings bool) Usage {
	input, inputOK := firstCount(usage, "prompt_tokens", "input_tokens")
	output, outputOK := firstCount(usage, "completion_tokens", "output_tokens")
	cache, cacheOK, badCache := cacheCount(usage)
	if badCache || (!inputOK && keyPresent(usage, "prompt_tokens", "input_tokens")) || (!outputOK && keyPresent(usage, "completion_tokens", "output_tokens")) {
		return UncertainUsage()
	}
	if embeddings && !outputOK && inputOK {
		zero := 0
		output = &zero
		outputOK = true
	}
	if !inputOK || !outputOK {
		return UncertainUsage()
	}
	out := Usage{Input: input, Output: output, Status: Measured}
	if cacheOK {
		out.Cache = cache
	}
	return out
}

func firstCount(obj map[string]any, keys ...string) (*int, bool) {
	for _, key := range keys {
		value, ok := obj[key]
		if !ok || value == nil {
			continue
		}
		count, ok := nonNegInt(value)
		if !ok {
			return nil, false
		}
		return &count, true
	}
	return nil, false
}

func keyPresent(obj map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := obj[key]; ok && value != nil {
			return true
		}
	}
	return false
}

func cacheCount(usage map[string]any) (*int, bool, bool) {
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if value, ok := details["cached_tokens"]; ok && value != nil {
			count, good := nonNegInt(value)
			if !good {
				return nil, false, true
			}
			return &count, true, false
		}
	}
	if value, ok := usage["prompt_cache_hit_tokens"]; ok && value != nil {
		count, good := nonNegInt(value)
		if !good {
			return nil, false, true
		}
		return &count, true, false
	}
	if value, ok := usage["cache_read_input_tokens"]; ok && value != nil {
		count, good := nonNegInt(value)
		if !good {
			return nil, false, true
		}
		return &count, true, false
	}
	return nil, false, false
}

func nonNegInt(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		if n < 0 || n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil || i < 0 {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}
