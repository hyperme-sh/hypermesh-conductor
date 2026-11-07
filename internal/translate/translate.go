// Package translate converts between OpenAI-compatible and Anthropic requests
// when a client and the selected destination do not speak the same protocol.
// Same-protocol calls are forwarded unchanged.
package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	if err := decodeJSON(body, &req); err != nil {
		return nil, fmt.Errorf("request body is not JSON")
	}
	if err := rejectStrictTools(req); err != nil {
		return nil, err
	}
	if err := rejectResponseFormat(req); err != nil {
		return nil, err
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
	if err := applySampling(out, req, true); err != nil {
		return nil, err
	}
	if err := applyToolChoice(out, req, true); err != nil {
		return nil, err
	}
	if stream, ok := req["stream"].(bool); ok && stream {
		out["stream"] = true
	}
	return json.Marshal(out)
}

func anthropicToChat(body []byte, model string) ([]byte, error) {
	var req map[string]any
	if err := decodeJSON(body, &req); err != nil {
		return nil, fmt.Errorf("request body is not JSON")
	}
	messages := []any{}
	system, err := SystemText(req["system"])
	if err != nil {
		return nil, err
	}
	if system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	rawMessages, _ := req["messages"].([]any)
	converted, err := anthropicMessagesToOpenAI(rawMessages)
	if err != nil {
		return nil, err
	}
	messages = append(messages, converted...)
	out := map[string]any{"model": model, "messages": messages}
	if max, ok := asInt(req["max_tokens"]); ok {
		out["max_tokens"] = max
	}
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		out["tools"] = anthropicToolsToOpenAI(tools)
	}
	if err := applySampling(out, req, false); err != nil {
		return nil, err
	}
	if err := applyToolChoice(out, req, false); err != nil {
		return nil, err
	}
	if stream, ok := req["stream"].(bool); ok {
		out["stream"] = stream
	}
	return json.Marshal(out)
}

func responsesToAnthropic(body []byte, model string) ([]byte, error) {
	var req map[string]any
	if err := decodeJSON(body, &req); err != nil {
		return nil, fmt.Errorf("request body is not JSON")
	}
	if err := rejectStrictTools(req); err != nil {
		return nil, err
	}
	if prior, ok := req["previous_response_id"].(string); ok && strings.TrimSpace(prior) != "" {
		return nil, fmt.Errorf("previous_response_id cannot be translated")
	}
	if err := rejectResponsesFormat(req); err != nil {
		return nil, err
	}
	var systemParts []string
	if instructions, ok := req["instructions"].(string); ok && instructions != "" {
		systemParts = append(systemParts, instructions)
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
			instruction, isInstruction, err := InstructionText(msg)
			if err != nil {
				return nil, err
			}
			if isInstruction {
				if instruction != "" {
					systemParts = append(systemParts, instruction)
				}
				continue
			}
			converted, err := responsesItemToAnthropic(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, converted...)
		}
	default:
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
	out := map[string]any{"model": model, "max_tokens": maxTokens(req), "messages": messages}
	if len(systemParts) > 0 {
		out["system"] = strings.Join(systemParts, "\n")
	}
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		converted, err := responsesToolsToAnthropic(tools)
		if err != nil {
			return nil, err
		}
		out["tools"] = converted
	}
	if err := applyToolChoice(out, req, true); err != nil {
		return nil, err
	}
	if err := applySampling(out, req, true); err != nil {
		return nil, err
	}
	if stream, ok := req["stream"].(bool); ok && stream {
		out["stream"] = true
	}
	return json.Marshal(out)
}

func responsesToolsToAnthropic(tools []any) ([]any, error) {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		obj, ok := tool.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		if _, nested := obj["function"].(map[string]any); nested {
			out = append(out, openaiToolsToAnthropic([]any{obj})...)
			continue
		}
		name, _ := obj["name"].(string)
		if obj["type"] != "function" || name == "" {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		schema := obj["parameters"]
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		desc, _ := obj["description"].(string)
		out = append(out, map[string]any{"name": name, "description": desc, "input_schema": schema})
	}
	return out, nil
}

func rejectResponsesFormat(req map[string]any) error {
	text, ok := req["text"].(map[string]any)
	if !ok || text == nil {
		return nil
	}
	format, ok := text["format"]
	if !ok || format == nil {
		return nil
	}
	spec, ok := format.(map[string]any)
	if !ok {
		return fmt.Errorf("unsupported text.format for protocol translation")
	}
	kind, _ := spec["type"].(string)
	if kind == "" || kind == "text" {
		return nil
	}
	return fmt.Errorf("unsupported text.format %s for protocol translation", kind)
}

func rejectResponseFormat(req map[string]any) error {
	format, ok := req["response_format"]
	if !ok || format == nil {
		return nil
	}
	obj, ok := format.(map[string]any)
	if !ok {
		return fmt.Errorf("unsupported response_format for protocol translation")
	}
	kind, _ := obj["type"].(string)
	if kind == "" || kind == "text" {
		return nil
	}
	return fmt.Errorf("unsupported response_format %s for protocol translation", kind)
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
		case "system", "developer":
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
			if refusal, ok := msg["refusal"].(string); ok && refusal != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": refusal})
			}
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, call := range calls {
					obj, _ := call.(map[string]any)
					fn, _ := obj["function"].(map[string]any)
					name, _ := fn["name"].(string)
					parsed, err := responsesArguments(fn["arguments"])
					if err != nil {
						return "", nil, err
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
					out = append(out, map[string]any{"role": "assistant", "content": textBlock["text"]})
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

func anthropicMessagesToOpenAI(rawMessages []any) ([]any, error) {
	out := []any{}
	for _, item := range rawMessages {
		msg, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		role, _ := msg["role"].(string)
		switch content := msg["content"].(type) {
		case string:
			out = append(out, map[string]any{"role": role, "content": content})
		case nil:
			if role == "assistant" {
				out = append(out, map[string]any{"role": "assistant", "content": nil})
				continue
			}
			out = append(out, map[string]any{"role": role, "content": ""})
		case []any:
			if role == "assistant" {
				message, err := assistantBlocksToOpenAI(content)
				if err != nil {
					return nil, err
				}
				out = append(out, message)
				continue
			}
			pieces, err := userBlocksToOpenAI(content)
			if err != nil {
				return nil, err
			}
			out = append(out, pieces...)
		default:
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
	}
	return out, nil
}

func assistantBlocksToOpenAI(blocks []any) (map[string]any, error) {
	var texts []string
	var calls []any
	for _, part := range blocks {
		obj, ok := part.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		kind, _ := obj["type"].(string)
		switch kind {
		case "text":
			text, _ := obj["text"].(string)
			texts = append(texts, text)
		case "tool_use":
			raw, err := json.Marshal(obj["input"])
			if err != nil {
				return nil, err
			}
			if obj["input"] == nil {
				raw = []byte("{}")
			}
			id, _ := obj["id"].(string)
			name, _ := obj["name"].(string)
			calls = append(calls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(raw),
				},
			})
		default:
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
	}
	text := strings.Join(texts, "")
	message := map[string]any{"role": "assistant", "content": text}
	if len(calls) > 0 {
		message["tool_calls"] = calls
		if text == "" {
			message["content"] = nil
		}
	}
	return message, nil
}

func userBlocksToOpenAI(blocks []any) ([]any, error) {
	var texts []string
	out := []any{}
	flush := func() {
		if len(texts) == 0 {
			return
		}
		out = append(out, map[string]any{"role": "user", "content": strings.Join(texts, "")})
		texts = nil
	}
	for _, part := range blocks {
		obj, ok := part.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		kind, _ := obj["type"].(string)
		switch kind {
		case "text":
			text, _ := obj["text"].(string)
			texts = append(texts, text)
		case "tool_result":
			flush()
			text, err := contentToText(obj["content"])
			if err != nil {
				return nil, err
			}
			id, _ := obj["tool_use_id"].(string)
			out = append(out, map[string]any{"role": "tool", "tool_call_id": id, "content": text})
		default:
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
	}
	flush()
	if len(out) == 0 {
		out = append(out, map[string]any{"role": "user", "content": ""})
	}
	return out, nil
}

// InstructionText extracts system/developer text from a message for translation and prefix hashing.
func InstructionText(msg map[string]any) (string, bool, error) {
	kind, _ := msg["type"].(string)
	if kind != "" && kind != "message" {
		return "", false, nil
	}
	role, _ := msg["role"].(string)
	if role != "system" && role != "developer" {
		return "", false, nil
	}
	text, err := contentToText(msg["content"])
	if err != nil {
		return "", false, err
	}
	if text == "" {
		if direct, ok := msg["text"].(string); ok {
			text = direct
		}
	}
	return text, true, nil
}

func responsesItemToAnthropic(msg map[string]any) ([]any, error) {
	kind, _ := msg["type"].(string)
	switch kind {
	case "function_call":
		name, _ := msg["name"].(string)
		id, _ := msg["call_id"].(string)
		if id == "" {
			id, _ = msg["id"].(string)
		}
		input, err := responsesArguments(msg["arguments"])
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "tool_use", "id": id, "name": name, "input": input,
			}},
		}}, nil
	case "function_call_output":
		id, _ := msg["call_id"].(string)
		output, err := responsesOutput(msg["output"])
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": id, "content": output,
			}},
		}}, nil
	case "", "message":
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
		return []any{map[string]any{"role": role, "content": text}}, nil
	default:
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
}

func responsesArguments(value any) (any, error) {
	switch args := value.(type) {
	case nil:
		return map[string]any{}, nil
	case string:
		if args == "" {
			return map[string]any{}, nil
		}
		var parsed any
		if err := decodeJSON([]byte(args), &parsed); err != nil {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		if _, ok := parsed.(map[string]any); !ok {
			return nil, fmt.Errorf("unsupported content for protocol translation")
		}
		return parsed, nil
	case map[string]any:
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
}

func responsesOutput(value any) (string, error) {
	switch output := value.(type) {
	case nil:
		return "", nil
	case string:
		return output, nil
	default:
		raw, err := json.Marshal(output)
		if err != nil {
			return "", fmt.Errorf("unsupported content for protocol translation")
		}
		return string(raw), nil
	}
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
	if err := decodeJSON(body, &msg); err != nil {
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
	if err := decodeJSON(body, &msg); err != nil {
		return nil, fmt.Errorf("provider response was not JSON")
	}
	text := ""
	var toolBlocks []any
	if choices, ok := msg["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		text, _ = contentToText(message["content"])
		if refusal, ok := message["refusal"].(string); ok {
			text += refusal
		}
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, call := range calls {
				obj, _ := call.(map[string]any)
				fn, _ := obj["function"].(map[string]any)
				parsed, err := responsesArguments(fn["arguments"])
				if err != nil {
					return nil, err
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
		"stop_reason": mapStopToAnthropic(finishReason(msg)),
		"model":       msg["model"],
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		out["usage"] = usageToAnthropic(usage)
	}
	return json.Marshal(out)
}

func anthropicResponseToResponses(body []byte) ([]byte, error) {
	var msg map[string]any
	if err := decodeJSON(body, &msg); err != nil {
		return nil, fmt.Errorf("provider response was not JSON")
	}
	output, err := anthropicContentToResponsesOutput(msg["content"])
	if err != nil {
		return nil, err
	}
	status, details := responsesStatus(stringField(msg, "stop_reason"))
	out := map[string]any{
		"id":     msg["id"],
		"object": "response",
		"status": status,
		"output": output,
	}
	if details != nil {
		out["incomplete_details"] = details
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		out["usage"] = usageToResponses(usage)
	}
	return json.Marshal(out)
}

func responsesStatus(stopReason string) (string, map[string]any) {
	if stopReason == "max_tokens" {
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	}
	return "completed", nil
}

func usageToResponses(usage map[string]any) map[string]any {
	out := map[string]any{}
	if n, ok := asInt(usage["output_tokens"]); ok {
		out["output_tokens"] = n
	}
	if n, ok := asInt(usage["completion_tokens"]); ok {
		out["output_tokens"] = n
	}
	if prompt, ok := asInt(usage["prompt_tokens"]); ok {
		out["input_tokens"] = prompt
		if cached, ok := openAICachedTokens(usage); ok {
			out["input_tokens_details"] = map[string]any{"cached_tokens": cached}
		}
		return out
	}
	input, hasInput := asInt(usage["input_tokens"])
	read, hasRead := asInt(usage["cache_read_input_tokens"])
	created, hasCreated := asInt(usage["cache_creation_input_tokens"])
	if hasInput || hasRead || hasCreated {
		total := 0
		if hasInput {
			total += input
		}
		if hasRead {
			total += read
		}
		if hasCreated {
			total += created
		}
		out["input_tokens"] = total
	}
	if hasRead {
		out["input_tokens_details"] = map[string]any{"cached_tokens": read}
	}
	return out
}

func anthropicContentToResponsesOutput(content any) ([]any, error) {
	switch value := content.(type) {
	case string:
		return []any{responsesTextMessage(value)}, nil
	case nil:
		return []any{responsesTextMessage("")}, nil
	case []any:
		var output []any
		var texts []string
		flush := func() {
			if len(texts) == 0 {
				return
			}
			output = append(output, responsesTextMessage(strings.Join(texts, "")))
			texts = nil
		}
		for _, part := range value {
			obj, ok := part.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("provider response was not JSON")
			}
			switch obj["type"] {
			case "text":
				text, _ := obj["text"].(string)
				texts = append(texts, text)
			case "tool_use":
				flush()
				item, err := responsesFunctionCall(obj)
				if err != nil {
					return nil, err
				}
				output = append(output, item)
			default:
				return nil, fmt.Errorf("unsupported content for protocol translation")
			}
		}
		flush()
		if len(output) == 0 {
			output = append(output, responsesTextMessage(""))
		}
		return output, nil
	default:
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
}

func responsesTextMessage(text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []any{map[string]any{
			"type": "output_text",
			"text": text,
		}},
	}
}

func responsesFunctionCall(block map[string]any) (map[string]any, error) {
	name, _ := block["name"].(string)
	id, _ := block["id"].(string)
	if name == "" {
		return nil, fmt.Errorf("unsupported content for protocol translation")
	}
	raw := []byte("{}")
	if block["input"] != nil {
		encoded, err := json.Marshal(block["input"])
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	return map[string]any{
		"type":      "function_call",
		"id":        id,
		"call_id":   id,
		"name":      name,
		"arguments": string(raw),
		"status":    "completed",
	}, nil
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
	if _, hasPrompt := usage["prompt_tokens"]; hasPrompt {
		if n, ok := asInt(usage["prompt_tokens"]); ok {
			out["prompt_tokens"] = n
		}
	} else if n, ok := asInt(usage["input_tokens"]); ok {
		if read, ok := asInt(usage["cache_read_input_tokens"]); ok {
			n += read
		}
		if created, ok := asInt(usage["cache_creation_input_tokens"]); ok {
			n += created
		}
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
	cached, hasCached := openAICachedTokens(usage)
	if n, ok := asInt(usage["prompt_tokens"]); ok {
		if hasCached && cached <= n {
			n -= cached
		}
		out["input_tokens"] = n
	} else if n, ok := asInt(usage["input_tokens"]); ok {
		out["input_tokens"] = n
	}
	if n, ok := asInt(usage["completion_tokens"]); ok {
		out["output_tokens"] = n
	}
	if n, ok := asInt(usage["output_tokens"]); ok {
		out["output_tokens"] = n
	}
	if hasCached {
		out["cache_read_input_tokens"] = cached
	} else if n, ok := asInt(usage["cache_read_input_tokens"]); ok {
		out["cache_read_input_tokens"] = n
	}
	return out
}

func openAICachedTokens(usage map[string]any) (int, bool) {
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		details, ok := usage[key].(map[string]any)
		if !ok {
			continue
		}
		if n, ok := asInt(details["cached_tokens"]); ok {
			return n, true
		}
	}
	return 0, false
}

func applySampling(dst, src map[string]any, toAnthropic bool) error {
	if toAnthropic {
		if err := rejectSampling(src, "frequency_penalty", "presence_penalty", "logit_bias", "logprobs", "top_logprobs", "n", "seed"); err != nil {
			return err
		}
	} else if err := rejectSampling(src, "top_k"); err != nil {
		return err
	}
	for _, key := range []string{"temperature", "top_p"} {
		if err := copySamplingNumber(dst, src, key); err != nil {
			return err
		}
	}
	if toAnthropic {
		if err := copySamplingNumber(dst, src, "top_k"); err != nil {
			return err
		}
		return copyStop(dst, src["stop"])
	}
	if value, ok := src["stop_sequences"]; ok {
		dst["stop"] = value
	}
	return nil
}

func rejectSampling(src map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, ok := src[key]; ok {
			return fmt.Errorf("unsupported sampling control %s for protocol translation", key)
		}
	}
	return nil
}

func copySamplingNumber(dst, src map[string]any, key string) error {
	value, ok := src[key]
	if !ok || value == nil {
		return nil
	}
	switch value.(type) {
	case float64, int, json.Number:
		dst[key] = value
		return nil
	default:
		return fmt.Errorf("unsupported sampling control %s for protocol translation", key)
	}
}

func copyStop(dst map[string]any, value any) error {
	switch stop := value.(type) {
	case nil:
		return nil
	case string:
		if stop != "" {
			dst["stop_sequences"] = []any{stop}
		}
		return nil
	case []any:
		dst["stop_sequences"] = stop
		return nil
	default:
		return fmt.Errorf("unsupported sampling control stop for protocol translation")
	}
}

func applyToolChoice(dst, src map[string]any, toAnthropic bool) error {
	if toAnthropic {
		choice, err := openAIToolChoiceToAnthropic(src["tool_choice"], parallelDisabled(src))
		if err != nil {
			return err
		}
		if choice != nil {
			dst["tool_choice"] = choice
		}
		return nil
	}
	value, ok := src["tool_choice"]
	if !ok || value == nil {
		return nil
	}
	choice, err := anthropicToolChoiceToOpenAI(value)
	if err != nil {
		return err
	}
	dst["tool_choice"] = choice
	return nil
}

func parallelDisabled(src map[string]any) bool {
	disabled, ok := src["parallel_tool_calls"].(bool)
	return ok && !disabled
}

func openAIToolChoiceToAnthropic(value any, disableParallel bool) (any, error) {
	if value == nil {
		if !disableParallel {
			return nil, nil
		}
		return map[string]any{"type": "auto", "disable_parallel_tool_use": true}, nil
	}
	switch choice := value.(type) {
	case string:
		var kind string
		switch choice {
		case "none":
			kind = "none"
		case "auto":
			kind = "auto"
		case "required":
			kind = "any"
		default:
			return nil, fmt.Errorf("unsupported tool_choice %s for protocol translation", choice)
		}
		out := map[string]any{"type": kind}
		if disableParallel && kind != "none" {
			out["disable_parallel_tool_use"] = true
		}
		return out, nil
	case map[string]any:
		name, _ := choice["name"].(string)
		fn, _ := choice["function"].(map[string]any)
		if name == "" {
			name, _ = fn["name"].(string)
		}
		if choice["type"] == "function" && name != "" {
			out := map[string]any{"type": "tool", "name": name}
			if disableParallel {
				out["disable_parallel_tool_use"] = true
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("unsupported tool_choice for protocol translation")
}

func anthropicToolChoiceToOpenAI(value any) (any, error) {
	switch choice := value.(type) {
	case string:
		switch choice {
		case "none", "auto":
			return choice, nil
		default:
			return nil, fmt.Errorf("unsupported tool_choice %s for protocol translation", choice)
		}
	case map[string]any:
		if disabled, ok := choice["disable_parallel_tool_use"].(bool); ok && disabled {
			return nil, fmt.Errorf("unsupported tool_choice for protocol translation")
		}
		kind, _ := choice["type"].(string)
		switch kind {
		case "none":
			return "none", nil
		case "auto":
			return "auto", nil
		case "any":
			return "required", nil
		case "tool":
			name, _ := choice["name"].(string)
			if name == "" {
				return nil, fmt.Errorf("unsupported tool_choice for protocol translation")
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
		}
		if _, extra := choice["disable_parallel_tool_use"]; extra {
			return nil, fmt.Errorf("unsupported tool_choice for protocol translation")
		}
	}
	return nil, fmt.Errorf("unsupported tool_choice for protocol translation")
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
	case "refusal":
		return "content_filter"
	default:
		return reason
	}
}

func mapStopToAnthropic(reason string) string {
	switch reason {
	case "tool_calls", "tool_use":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	case "content_filter", "refusal":
		return "refusal"
	case "stop", "end_turn", "stop_sequence", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func finishReason(msg map[string]any) string {
	choices, _ := msg["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	reason, _ := choice["finish_reason"].(string)
	return reason
}

// SystemText reads an Anthropic system prompt. A string is kept. A block
// array contributes its text blocks. Any other block is refused.
func SystemText(system any) (string, error) {
	switch value := system.(type) {
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
			if kind != "" && kind != "text" {
				return "", fmt.Errorf("unsupported content for protocol translation")
			}
			text, _ := obj["text"].(string)
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ""), nil
	default:
		return "", fmt.Errorf("unsupported content for protocol translation")
	}
}

// ClientTools converts configured OpenAI function tools into the client protocol.
func ClientTools(proto string, tools []any) []any {
	if proto == ProtoMessages {
		return openaiToolsToAnthropic(tools)
	}
	return tools
}

func decodeJSON(raw []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
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

func rejectStrictTools(req map[string]any) error {
	tools, _ := req["tools"].([]any)
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			fn = tool
		}
		if strict, _ := fn["strict"].(bool); strict {
			return fmt.Errorf("strict tool schemas cannot be translated to Anthropic")
		}
	}
	return nil
}
