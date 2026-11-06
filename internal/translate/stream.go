package translate

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Stream converts upstream SSE into the client protocol.
// Same-protocol streams are copied through.
type Stream struct {
	client    string
	upstream  string
	buf       []byte
	event     string
	id        string
	opened    bool
	done      bool
	failed    bool
	toolSlots map[int]int
	nextTool  int
}

// NewStream starts a bridge. Identical protocols copy bytes unchanged.
func NewStream(clientProto, upstreamProto string) *Stream {
	return &Stream{client: clientProto, upstream: upstreamProto}
}

func (s *Stream) passthrough() bool {
	if s.client == s.upstream {
		return true
	}
	return openaiFamily(s.client) && openaiFamily(s.upstream)
}

func openaiFamily(proto string) bool {
	return proto == ProtoChat || proto == ProtoResponses || proto == ProtoEmbeddings
}

// Consume translates the next upstream chunk into client bytes.
func (s *Stream) Consume(chunk []byte) []byte {
	if s.passthrough() {
		return chunk
	}
	s.buf = append(s.buf, chunk...)
	var out bytes.Buffer
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(s.buf[:i]), "\r")
		s.buf = s.buf[i+1:]
		out.Write(s.onLine(line))
	}
	return out.Bytes()
}

// Finish flushes a closing event when the upstream ended quietly.
func (s *Stream) Finish() []byte {
	if s.passthrough() || s.done || s.failed {
		return nil
	}
	s.done = true
	if s.client == ProtoMessages {
		return []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}
	return []byte("data: [DONE]\n\n")
}

func (s *Stream) onLine(line string) []byte {
	if line == "" || strings.HasPrefix(line, ":") {
		return nil
	}
	if strings.HasPrefix(line, "event:") {
		s.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return nil
	}
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		s.done = true
		if s.client == ProtoMessages {
			return []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
		return []byte("data: [DONE]\n\n")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return nil
	}
	kind, _ := obj["type"].(string)
	if kind == "" {
		kind = s.event
	}
	if kind == "error" || errorObject(obj) {
		s.failed = true
		s.done = true
		return s.clientError(obj)
	}
	switch {
	case s.upstream == ProtoMessages && s.client == ProtoChat:
		return s.anthropicToChat(kind, obj)
	case s.upstream == ProtoChat && s.client == ProtoMessages:
		return s.chatToAnthropic(obj)
	case s.upstream == ProtoMessages && s.client == ProtoResponses:
		return s.anthropicToResponses(kind, obj)
	default:
		return []byte("data: " + data + "\n\n")
	}
}

func (s *Stream) anthropicToChat(kind string, obj map[string]any) []byte {
	switch kind {
	case "message_start":
		if message, ok := obj["message"].(map[string]any); ok {
			s.id, _ = message["id"].(string)
		}
		if s.id == "" {
			s.id = "chatcmpl"
		}
		s.opened = true
		return sseData(chatChunk(s.id, map[string]any{"role": "assistant"}, nil, nil))
	case "content_block_delta":
		delta, _ := obj["delta"].(map[string]any)
		dtype, _ := delta["type"].(string)
		if dtype == "text_delta" {
			text, _ := delta["text"].(string)
			return sseData(chatChunk(s.id, map[string]any{"content": text}, nil, nil))
		}
		if dtype == "input_json_delta" {
			partial, _ := delta["partial_json"].(string)
			return sseData(chatChunk(s.id, map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    s.toolSlot(obj),
					"function": map[string]any{"arguments": partial},
				}},
			}, nil, nil))
		}
	case "content_block_start":
		block, _ := obj["content_block"].(map[string]any)
		if block["type"] == "tool_use" {
			return sseData(chatChunk(s.id, map[string]any{
				"tool_calls": []any{map[string]any{
					"index": s.toolSlot(obj),
					"id":    block["id"],
					"type":  "function",
					"function": map[string]any{
						"name":      block["name"],
						"arguments": "",
					},
				}},
			}, nil, nil))
		}
	case "message_delta":
		finish := "stop"
		if delta, ok := obj["delta"].(map[string]any); ok {
			if reason, ok := delta["stop_reason"].(string); ok {
				finish = mapStop(reason)
			}
		}
		var usage any
		if raw, ok := obj["usage"].(map[string]any); ok {
			usage = usageToOpenAI(raw)
		}
		return sseData(chatChunk(s.id, map[string]any{}, finish, usage))
	case "message_stop":
		s.done = true
		return []byte("data: [DONE]\n\n")
	}
	return nil
}

func (s *Stream) chatToAnthropic(obj map[string]any) []byte {
	var out bytes.Buffer
	if !s.opened {
		s.opened = true
		s.id = "msg"
		if id, ok := obj["id"].(string); ok && id != "" {
			s.id = id
		}
		out.WriteString("event: message_start\n")
		out.Write(sseData(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": s.id, "type": "message", "role": "assistant",
				"content": []any{}, "model": obj["model"],
			},
		}))
		out.WriteString("event: content_block_start\n")
		out.Write(sseData(map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}))
	}
	choices, _ := obj["choices"].([]any)
	if len(choices) == 0 {
		return out.Bytes()
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if text, ok := delta["content"].(string); ok && text != "" {
		out.WriteString("event: content_block_delta\n")
		out.Write(sseData(map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		}))
	}
	if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
		out.WriteString("event: message_delta\n")
		out.Write(sseData(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": mapStopToAnthropic(finish)},
			"usage": map[string]any{},
		}))
		out.WriteString("event: message_stop\n")
		out.Write(sseData(map[string]any{"type": "message_stop"}))
		s.done = true
	}
	return out.Bytes()
}

func (s *Stream) anthropicToResponses(kind string, obj map[string]any) []byte {
	switch kind {
	case "content_block_delta":
		delta, _ := obj["delta"].(map[string]any)
		if delta["type"] == "text_delta" {
			text, _ := delta["text"].(string)
			return sseData(map[string]any{"type": "response.output_text.delta", "delta": text})
		}
	case "message_stop":
		s.done = true
		return sseData(map[string]any{"type": "response.completed"})
	}
	return nil
}

func (s *Stream) toolSlot(obj map[string]any) int {
	index, ok := eventIndex(obj["index"])
	if !ok {
		index = s.nextTool
	}
	if s.toolSlots == nil {
		s.toolSlots = map[int]int{}
	}
	if slot, exists := s.toolSlots[index]; exists {
		return slot
	}
	slot := s.nextTool
	s.nextTool++
	s.toolSlots[index] = slot
	return slot
}

func eventIndex(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

func errorObject(obj map[string]any) bool {
	_, ok := obj["error"].(map[string]any)
	return ok
}

func (s *Stream) clientError(obj map[string]any) []byte {
	message := "provider request failed"
	if errObj, ok := obj["error"].(map[string]any); ok {
		if text, ok := errObj["message"].(string); ok && text != "" {
			message = text
		}
	}
	if s.client == ProtoMessages {
		return append([]byte("event: error\n"), sseData(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": message},
		})...)
	}
	if s.client == ProtoResponses {
		return sseData(map[string]any{"type": "error", "error": map[string]any{"message": message}})
	}
	return sseData(map[string]any{"error": map[string]any{"message": message, "type": "server_error"}})
}

func chatChunk(id string, delta map[string]any, finish any, usage any) map[string]any {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": finish}
	out := map[string]any{
		"id": id, "object": "chat.completion.chunk",
		"choices": []any{choice},
	}
	if usage != nil {
		out["usage"] = usage
	}
	return out
}

func sseData(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return append(append([]byte("data: "), raw...), '\n', '\n')
}
