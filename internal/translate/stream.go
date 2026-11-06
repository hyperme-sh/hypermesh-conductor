package translate

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Stream converts upstream SSE into the client protocol.
// Same-protocol streams are copied through.
type Stream struct {
	client      string
	upstream    string
	buf         []byte
	event       string
	id          string
	opened      bool
	done        bool
	failed      bool
	toolSlots   map[int]int
	nextTool    int
	chatTools   map[int]int
	nextContent int
	respOpened  bool
	respText    strings.Builder
	tooLarge    bool
}

// maxEventBytes is the largest SSE line the bridge will buffer. A provider
// that never sends a newline cannot grow this buffer without limit.
var maxEventBytes = 32 << 20

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
	if s.failed {
		return nil
	}
	if s.passthrough() {
		return chunk
	}
	s.buf = append(s.buf, chunk...)
	var out bytes.Buffer
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) > maxEventBytes {
				return s.failLarge(&out)
			}
			break
		}
		if i > maxEventBytes {
			return s.failLarge(&out)
		}
		line := strings.TrimRight(string(s.buf[:i]), "\r")
		s.buf = s.buf[i+1:]
		out.Write(s.onLine(line))
	}
	return out.Bytes()
}

// TooLarge reports that an upstream event exceeded the SSE line cap.
func (s *Stream) TooLarge() bool { return s.tooLarge }

func (s *Stream) failLarge(out *bytes.Buffer) []byte {
	s.failed = true
	s.tooLarge = true
	s.done = true
	s.buf = nil
	out.Write(s.clientError(map[string]any{
		"error": map[string]any{"message": "provider stream event is too large"},
	}))
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
	if s.client == ProtoResponses && s.upstream == ProtoMessages {
		return s.anthropicToResponses("message_stop", nil)
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
		s.nextContent = 1
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
	if calls, ok := delta["tool_calls"].([]any); ok {
		out.Write(s.chatToolCalls(calls))
	}
	if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
		for i := 0; i < s.nextContent; i++ {
			out.WriteString("event: content_block_stop\n")
			out.Write(sseData(map[string]any{"type": "content_block_stop", "index": i}))
		}
		s.nextContent = 0
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

func (s *Stream) chatToolCalls(calls []any) []byte {
	var out bytes.Buffer
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index, ok := eventIndex(call["index"])
		if !ok {
			index = 0
		}
		if s.chatTools == nil {
			s.chatTools = map[int]int{}
		}
		block, started := s.chatTools[index]
		fn, _ := call["function"].(map[string]any)
		if !started {
			block = s.nextContent
			s.nextContent++
			s.chatTools[index] = block
			name, _ := fn["name"].(string)
			id, _ := call["id"].(string)
			out.WriteString("event: content_block_start\n")
			out.Write(sseData(map[string]any{
				"type": "content_block_start", "index": block,
				"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
			}))
		}
		if args, ok := fn["arguments"].(string); ok && args != "" {
			out.WriteString("event: content_block_delta\n")
			out.Write(sseData(map[string]any{
				"type": "content_block_delta", "index": s.chatTools[index],
				"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
			}))
		}
	}
	return out.Bytes()
}

func (s *Stream) anthropicToResponses(kind string, obj map[string]any) []byte {
	switch kind {
	case "message_start":
		if message, ok := obj["message"].(map[string]any); ok {
			if id, ok := message["id"].(string); ok && id != "" {
				s.id = id
			}
		}
		return s.openResponses()
	case "content_block_delta":
		delta, _ := obj["delta"].(map[string]any)
		if delta["type"] != "text_delta" {
			return nil
		}
		text, _ := delta["text"].(string)
		var out bytes.Buffer
		if !s.respOpened {
			out.Write(s.openResponses())
		}
		s.respText.WriteString(text)
		out.Write(sseData(map[string]any{
			"type":          "response.output_text.delta",
			"output_index":  0,
			"content_index": 0,
			"delta":         text,
		}))
		return out.Bytes()
	case "message_stop":
		s.done = true
		var out bytes.Buffer
		if !s.respOpened {
			out.Write(s.openResponses())
		}
		text := s.respText.String()
		out.Write(sseData(map[string]any{
			"type": "response.output_text.done", "output_index": 0, "content_index": 0, "text": text,
		}))
		out.Write(sseData(map[string]any{
			"type": "response.content_part.done", "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text},
		}))
		out.Write(sseData(map[string]any{
			"type": "response.output_item.done", "output_index": 0,
			"item": s.responsesMessage(text),
		}))
		out.Write(sseData(map[string]any{
			"type":     "response.completed",
			"response": s.responsesObject(text, "completed"),
		}))
		return out.Bytes()
	}
	return nil
}

func (s *Stream) openResponses() []byte {
	if s.respOpened {
		return nil
	}
	s.respOpened = true
	if s.id == "" {
		s.id = "resp"
	}
	var out bytes.Buffer
	out.Write(sseData(map[string]any{
		"type":     "response.created",
		"response": s.responsesObject("", "in_progress"),
	}))
	out.Write(sseData(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"id": s.id, "type": "message", "role": "assistant", "content": []any{}},
	}))
	out.Write(sseData(map[string]any{
		"type": "response.content_part.added", "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": ""},
	}))
	return out.Bytes()
}

func (s *Stream) responsesMessage(text string) map[string]any {
	return map[string]any{
		"id": s.id, "type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func (s *Stream) responsesObject(text, status string) map[string]any {
	return map[string]any{
		"id": s.id, "object": "response", "status": status,
		"output": []any{s.responsesMessage(text)},
	}
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
