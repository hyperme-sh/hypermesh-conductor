// Package translate converts between OpenAI-compatible and Anthropic requests
// when a client and the selected destination do not speak the same protocol.
// Same-protocol calls are forwarded unchanged.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/jsonbody"
)

const (
	ProtoChat       = "openai.chat"
	ProtoResponses  = "openai.responses"
	ProtoMessages   = "anthropic.messages"
	ProtoEmbeddings = "openai.embeddings"
	ProtoModels     = "openai.models"

	ProviderOpenAI           = "openai"
	ProviderAnthropic        = "anthropic"
	ProviderOpenAICompatible = "openai-compatible"

	DefaultMaxTokens = 4096
)

// Wire is the body and path to send upstream.
type Wire struct {
	Path     string
	Body     []byte
	Protocol string
}

// Request builds the upstream request for a client body.
func Request(clientProto, provider, model string, body []byte) (Wire, error) {
	upstream := upstreamProtocol(provider)
	if clientProto == ProtoEmbeddings {
		if upstream == ProtoMessages {
			return Wire{}, fmt.Errorf("embed has no anthropic destination")
		}
		rewritten, err := setModelPreserve(body, model)
		if err != nil {
			return Wire{}, err
		}
		return Wire{Path: "embeddings", Body: rewritten, Protocol: ProtoEmbeddings}, nil
	}
	if sameFamily(clientProto, upstream) {
		rewritten, err := setModelPreserve(body, model)
		if err != nil {
			return Wire{}, err
		}
		return Wire{Path: pathFor(clientProto), Body: rewritten, Protocol: clientProto}, nil
	}
	switch {
	case clientProto == ProtoChat && upstream == ProtoMessages:
		raw, err := chatToAnthropic(body, model)
		if err != nil {
			return Wire{}, err
		}
		return Wire{Path: "messages", Body: raw, Protocol: ProtoMessages}, nil
	case clientProto == ProtoMessages && upstream != ProtoMessages:
		raw, err := anthropicToChat(body, model)
		if err != nil {
			return Wire{}, err
		}
		return Wire{Path: "chat/completions", Body: raw, Protocol: ProtoChat}, nil
	case clientProto == ProtoResponses && upstream == ProtoMessages:
		raw, err := responsesToAnthropic(body, model)
		if err != nil {
			return Wire{}, err
		}
		return Wire{Path: "messages", Body: raw, Protocol: ProtoMessages}, nil
	default:
		return Wire{}, fmt.Errorf("cannot translate %s to %s", clientProto, provider)
	}
}

// Response converts an upstream body back to the client protocol.
func Response(clientProto, upstreamProto string, body []byte) ([]byte, error) {
	if clientProto == upstreamProto || clientProto == ProtoEmbeddings {
		return body, nil
	}
	switch {
	case clientProto == ProtoChat && upstreamProto == ProtoMessages:
		return anthropicResponseToChat(body)
	case clientProto == ProtoMessages && upstreamProto == ProtoChat:
		return chatResponseToAnthropic(body)
	case clientProto == ProtoResponses && upstreamProto == ProtoMessages:
		return anthropicResponseToResponses(body)
	default:
		return body, nil
	}
}

func upstreamProtocol(provider string) string {
	if provider == ProviderAnthropic {
		return ProtoMessages
	}
	return ProtoChat
}

func sameFamily(clientProto, upstream string) bool {
	if clientProto == ProtoMessages {
		return upstream == ProtoMessages
	}
	return upstream != ProtoMessages
}

func pathFor(proto string) string {
	switch proto {
	case ProtoMessages:
		return "messages"
	case ProtoResponses:
		return "responses"
	case ProtoEmbeddings:
		return "embeddings"
	default:
		return "chat/completions"
	}
}

func setModelPreserve(body []byte, model string) ([]byte, error) {
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

func chatToAnthropic(body []byte, model string) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("request body is not JSON")
	}
	messages, _ := req["messages"].([]any)
	system, converted, err := splitChatMessages(messages)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":      model,
		"max_tokens": maxTokens(req),
		"messages":   converted,
	}
	if system != "" {
		out["system"] = system
	}
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		out["tools"] = openaiToolsToAnthropic(tools)
	}
	if stream, ok := req["stream"].(bool); ok && stream {
		out["stream"] = true
	}
	return json.Marshal(out)
}

func anthropicToChat(body []byte, model string) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("request body is not JSON")
	}
	messages := []any{}
	if system, ok := req["system"].(string); ok && system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	rawMessages, _ := req["messages"].([]any)
	for _, item := range rawMessages {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		role, _ := msg["role"].(string)
		content, err := anthropicContentToText(msg["content"])
		if err != nil {
			return nil, err
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	out := map[string]any{"model": model, "messages": messages}
	if max, ok := asInt(req["max_tokens"]); ok {
		out["max_tokens"] = max
	}
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		out["tools"] = anthropicToolsToOpenAI(tools)
	}
	if stream, ok := req["stream"].(bool); ok {
		out["stream"] = stream
	}
	return json.Marshal(out)
}

func responsesToAnthropic(body []byte, model string) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("request body is not JSON")
	}
	messages := []any{}
	switch input := req["input"].(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": input})
	case []any:
		for _, item := range input {
			msg, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unsupported content for protocol translation")
			}
			role, _ := msg["role"].(string)
			if role == "" {
				role = "user"
			}
			text, err := contentToText(msg["content"])
			if err != nil {
				return nil, err
			}
			if text == "" {
				if direct, ok := msg["text"].(string); ok {
					text = direct
				}
			}
			messages = append(messages, map[string]any{"role": role, "content": text})
		}
	default:
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
	out := map[string]any{"model": model, "max_tokens": maxTokens(req), "messages": messages}
	if instructions, ok := req["instructions"].(string); ok && instructions != "" {
		out["system"] = instructions
	}
	if stream, ok := req["stream"].(bool); ok && stream {
		out["stream"] = true
	}
	return json.Marshal(out)
}

func splitChatMessages(messages []any) (string, []any, error) {
	var systemParts []string
	out := []any{}
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("unsupported content for protocol translation")
		}
		role, _ := msg["role"].(string)
		text, err := contentToText(msg["content"])
		if err != nil {
			return "", nil, err
		}
		switch role {
		case "system":
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "tool":
			id, _ := msg["tool_call_id"].(string)
			out = append(out, map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type":        "tool_result",
					"tool_use_id": id,
					"content":     text,
				}},
			})
		case "assistant":
			blocks := []any{}
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, call := range calls {
					obj, _ := call.(map[string]any)
					fn, _ := obj["function"].(map[string]any)
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					var parsed any
					if args != "" && json.Unmarshal([]byte(args), &parsed) == nil {
						// parsed object
					} else {
						parsed = map[string]any{}
					}
					id, _ := obj["id"].(string)
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": id, "name": name, "input": parsed})
				}
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": ""})
			}
			if len(blocks) == 1 {
				if textBlock, ok := blocks[0].(map[string]any); ok && textBlock["type"] == "text" {
					out = append(out, map[string]any{"role": "assistant", "content": text})
					continue
				}
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		default:
			out = append(out, map[string]any{"role": role, "content": text})
		}
	}
	return strings.Join(systemParts, "\n"), out, nil
}

func contentToText(content any) (string, error) {
	switch value := content.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []any:
		var parts []string
		for _, part := range value {
			obj, ok := part.(map[string]any)
			if !ok {
				return "", fmt.Errorf("unsupported content for protocol translation")
			}
			kind, _ := obj["type"].(string)
			switch kind {
			case "text", "input_text", "output_text":
				text, _ := obj["text"].(string)
				parts = append(parts, text)
			case "":
				if text, ok := obj["text"].(string); ok {
					parts = append(parts, text)
				}
			default:
				return "", fmt.Errorf("unsupported content for protocol translation")
			}
		}
		return strings.Join(parts, ""), nil
	default:
		return "", fmt.Errorf("unsupported content for protocol translation")
	}
}

func anthropicContentToText(content any) (string, error) {
	switch value := content.(type) {
	case string:
		return value, nil
	case []any:
		var parts []string
		for _, part := range value {
			obj, ok := part.(map[string]any)
			if !ok {
				return "", fmt.Errorf("unsupported content for protocol translation")
			}
			kind, _ := obj["type"].(string)
			switch kind {
			case "text":
				text, _ := obj["text"].(string)
				parts = append(parts, text)
			case "tool_result":
				text, err := contentToText(obj["content"])
				if err != nil {
					if direct, ok := obj["content"].(string); ok {
						text = direct
					} else {
						return "", err
					}
				}
				parts = append(parts, text)
			case "tool_use":
				name, _ := obj["name"].(string)
				parts = append(parts, "[tool_use "+name+"]")
			default:
				return "", fmt.Errorf("unsupported content for protocol translation")
			}
		}
		return strings.Join(parts, ""), nil
	default:
		return contentToText(content)
	}
}

func openaiToolsToAnthropic(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		obj, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := obj["function"].(map[string]any)
		if fn == nil {
			if _, ok := obj["name"].(string); ok {
				out = append(out, obj)
			}
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		schema := fn["parameters"]
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{"name": name, "description": desc, "input_schema": schema})
	}
	return out
}

func anthropicToolsToOpenAI(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		obj, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name, _ := obj["name"].(string)
		desc, _ := obj["description"].(string)
		schema := obj["input_schema"]
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  schema,
			},
		})
	}
	return out
}

func anthropicResponseToChat(body []byte) ([]byte, error) {
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("provider response was not JSON")
	}
	text, err := anthropicContentToText(msg["content"])
	if err != nil {
		return nil, err
	}
	message := map[string]any{"role": "assistant", "content": text}
	if calls := anthropicToolCalls(msg["content"]); len(calls) > 0 {
		message["tool_calls"] = calls
		if text == "" {
			message["content"] = nil
		}
	}
	out := map[string]any{
		"id":     msg["id"],
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": mapStop(stringField(msg, "stop_reason")),
		}},
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		out["usage"] = usageToOpenAI(usage)
	}
	return json.Marshal(out)
}

func chatResponseToAnthropic(body []byte) ([]byte, error) {
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("provider response was not JSON")
	}
	text := ""
	var toolBlocks []any
	if choices, ok := msg["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		text, _ = contentToText(message["content"])
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, call := range calls {
				obj, _ := call.(map[string]any)
				fn, _ := obj["function"].(map[string]any)
				args, _ := fn["arguments"].(string)
				var parsed any = map[string]any{}
				if args != "" {
					_ = json.Unmarshal([]byte(args), &parsed)
				}
				toolBlocks = append(toolBlocks, map[string]any{
					"type":  "tool_use",
					"id":    obj["id"],
					"name":  fn["name"],
					"input": parsed,
				})
			}
		}
	}
	content := []any{}
	if text != "" || len(toolBlocks) == 0 {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	content = append(content, toolBlocks...)
	out := map[string]any{
		"id":          msg["id"],
		"type":        "message",
		"role":        "assistant",
		"content":     content,
		"stop_reason": "end_turn",
		"model":       msg["model"],
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		out["usage"] = usageToAnthropic(usage)
	}
	return json.Marshal(out)
}

func anthropicResponseToResponses(body []byte) ([]byte, error) {
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("provider response was not JSON")
	}
	text, err := anthropicContentToText(msg["content"])
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id":     msg["id"],
		"object": "response",
		"status": "completed",
		"output": []any{map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text",
				"text": text,
			}},
		}},
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		out["usage"] = usageToAnthropic(usage)
	}
	return json.Marshal(out)
}

func anthropicToolCalls(content any) []any {
	blocks, ok := content.([]any)
	if !ok {
		return nil
	}
	var calls []any
	for _, block := range blocks {
		obj, ok := block.(map[string]any)
		if !ok || obj["type"] != "tool_use" {
			continue
		}
		raw, _ := json.Marshal(obj["input"])
		calls = append(calls, map[string]any{
			"id":   obj["id"],
			"type": "function",
			"function": map[string]any{
				"name":      obj["name"],
				"arguments": string(raw),
			},
		})
	}
	return calls
}

func usageToOpenAI(usage map[string]any) map[string]any {
	out := map[string]any{}
	if n, ok := asInt(usage["input_tokens"]); ok {
		out["prompt_tokens"] = n
	}
	if n, ok := asInt(usage["prompt_tokens"]); ok {
		out["prompt_tokens"] = n
	}
	if n, ok := asInt(usage["output_tokens"]); ok {
		out["completion_tokens"] = n
	}
	if n, ok := asInt(usage["completion_tokens"]); ok {
		out["completion_tokens"] = n
	}
	if n, ok := asInt(usage["cache_read_input_tokens"]); ok {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": n}
	}
	return out
}

func usageToAnthropic(usage map[string]any) map[string]any {
	out := map[string]any{}
	if n, ok := asInt(usage["prompt_tokens"]); ok {
		out["input_tokens"] = n
	}
	if n, ok := asInt(usage["input_tokens"]); ok {
		out["input_tokens"] = n
	}
	if n, ok := asInt(usage["completion_tokens"]); ok {
		out["output_tokens"] = n
	}
	if n, ok := asInt(usage["output_tokens"]); ok {
		out["output_tokens"] = n
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if n, ok := asInt(details["cached_tokens"]); ok {
			out["cache_read_input_tokens"] = n
		}
	}
	return out
}

func maxTokens(req map[string]any) int {
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if n, ok := asInt(req[key]); ok && n > 0 {
			return n
		}
	}
	return DefaultMaxTokens
}

func mapStop(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence", "":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

func stringField(obj map[string]any, key string) string {
	value, _ := obj[key].(string)
	return value
}

func asInt(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
