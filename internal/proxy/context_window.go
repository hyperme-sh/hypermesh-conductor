package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
)

// Count with the selected provider's tokenizer and template after all rewrites.
// The tokenizer and generation endpoint share the configured origin and auth.
func (p *Proxy) checkContext(upstream *http.Request, tier config.Tier, protocol string, body []byte) (*int, int, error) {
	if tier.InputCounter == "" && tier.ContextWindow == 0 && tier.TaskTokenLimit == 0 {
		return nil, 0, nil
	}
	count, status, err := p.countChatContext(upstream, tier, protocol, body)
	if err != nil && tier.ContextWindow == 0 && tier.TaskTokenLimit == 0 {
		return nil, 0, nil
	}
	return count, status, err
}

func (p *Proxy) countChatContext(upstream *http.Request, tier config.Tier, protocol string, body []byte) (*int, int, error) {
	if protocol != translate.ProtoChat || tier.InputCounter != "vllm" {
		return nil, 400, errors.New("hard context window requires a supported tokenizer for this protocol")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, 400, errors.New("invalid chat request")
	}
	var output int
	for key, raw := range fields {
		switch key {
		case "model", "messages", "tools", "add_generation_prompt", "continue_final_message", "add_special_tokens", "chat_template", "chat_template_kwargs", "response_format":
			// Keep all fields that affect the provider's rendered prompt.
		case "max_tokens", "max_completion_tokens":
			if err := json.Unmarshal(raw, &output); err != nil || output < 1 {
				return nil, 400, errors.New("invalid context output allowance")
			}
			delete(fields, key)
		case "temperature", "top_p", "top_k", "seed", "stream", "stream_options", "n", "best_of", "stop", "frequency_penalty", "presence_penalty", "logprobs", "top_logprobs":
			delete(fields, key)
		default:
			return nil, 400, fmt.Errorf("context counting does not support chat parameter %q", key)
		}
	}
	// Media expansion can depend on the processor and the content retrieved at
	// dispatch time. Only text and tool history have a stable count on this path.
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(fields["messages"], &messages); err != nil {
		return nil, 400, errors.New("invalid chat messages")
	}
	for _, msg := range messages {
		if len(msg.Content) > 0 && msg.Content[0] != '"' && string(msg.Content) != "null" {
			var parts []struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(msg.Content, &parts) != nil {
				return nil, 400, errors.New("invalid message content")
			}
			for _, part := range parts {
				if part.Type != "text" {
					return nil, 400, errors.New("context counting requires text or tool messages")
				}
			}
		}
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, 400, err
	}
	endpoint := *upstream.URL
	if !strings.HasSuffix(endpoint.Path, "/v1/chat/completions") {
		return nil, 503, errors.New("invalid tokenizer endpoint")
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/v1/chat/completions") + "/tokenize"
	req, err := http.NewRequestWithContext(upstream.Context(), http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, 503, errors.New("invalid tokenizer endpoint")
	}
	req.Header = upstream.Header.Clone()
	resp, err := p.upstreamClient(tier.Timeout, false).Do(req)
	unavailable := errors.New("provider input token count unavailable")
	if err != nil {
		return nil, 503, unavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 503, unavailable
	}
	raw, held, err := readReserved(resp.Body, 8<<20)
	defer releaseBody(held)
	var result struct {
		Count *int `json:"count"`
		Max   int  `json:"max_model_len"`
	}
	if err != nil || json.Unmarshal(raw, &result) != nil || result.Count == nil || *result.Count < 0 || result.Max < 1 {
		return nil, 503, unavailable
	}
	if tier.ContextWindow > 0 && (output < 1 || *result.Count > min(tier.ContextWindow, result.Max)-output) {
		return nil, 400, errors.New("counted input and output allowance exceed the effective context window")
	}
	return result.Count, 0, nil
}
