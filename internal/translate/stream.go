package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// Stream converts upstream SSE into the client protocol.
// Same-protocol streams are copied through.
type Stream struct {
	held         int
	closed       bool
	client       string
	upstream     string
	buf          []byte
	event        string
	id           string
	opened       bool
	done         bool
	failed       bool
	toolSlots    map[int]int
	nextTool     int
	chatTools    map[int]int
	chatUsage    map[string]any
	nextContent  int
	respOpened   bool
	respText     strings.Builder
	textOpen     bool
	textIndex    int
	closedTexts  []responseText
	nextOut      int
	respCalls    []responseCall
	respStop     string
	finishSeen   bool
	finishReason string
	translated   int
	tooLarge     bool
}

type responseText struct {
	index int
	text  string
}

type responseCall struct {
	block int
	index int
	id    string
	name  string
	args  strings.Builder
}

// maxEventBytes bounds one SSE line and the translated output retained for
// one response, including tool-call names and ids.
var maxEventBytes = 32 << 20

// Shared across every translating stream, including incomplete events and IDs.
var maxRetainedStreamBytes int64 = 64 << 20
var retainedStreamBytes atomic.Int64

func (s *Stream) resizeHeld(n int) bool {
	delta := int64(n - s.held)
	if delta > 0 {
		for {
			used := retainedStreamBytes.Load()
			if delta > maxRetainedStreamBytes-used {
				return false
			}
			if retainedStreamBytes.CompareAndSwap(used, used+delta) {
				break
			}
		}
	} else {
		retainedStreamBytes.Add(delta)
	}
	s.held = n
	return true
}

// Include provider-controlled metadata as well as accumulated content.
func (s *Stream) retainedSize() int {
	n := len(s.buf) + s.translated + len(s.id) + len(s.event) + len(s.respStop) + len(s.finishReason)
	var usageSize func(map[string]any) int
	usageSize = func(values map[string]any) int {
		total := 0
		for key, value := range values {
			total += len(key) + 32
			switch v := value.(type) {
			case json.Number:
				total += len(v)
			case map[string]any:
				total += usageSize(v)
			}
		}
		return total
	}
	return n + usageSize(s.chatUsage)
}

// Closed reports that a terminal event released the retained provider bytes.
func (s *Stream) Closed() bool { return s.closed }

// Close releases every retained provider byte, including on client disconnect.
func (s *Stream) Close() {
	s.buf = nil
	s.respText.Reset()
	s.respCalls = nil
	s.closedTexts = nil
	s.toolSlots = nil
	s.chatTools = nil
	s.chatUsage = nil
	s.id, s.event, s.respStop, s.finishReason = "", "", "", ""
	s.resizeHeld(0)
	s.closed = true
}

// callMetaBytes is the retained cost of one tool-call record aside from its
// name and id. Tool starts have to count, or a stream of empty calls grows
// without hitting the text cap.
const callMetaBytes = 64

// NewStream starts a bridge. Identical protocols copy bytes unchanged.
func NewStream(clientProto, upstreamProto string) *Stream {
	return &Stream{client: clientProto, upstream: upstreamProto}
}

// Passthrough reports that upstream bytes are copied to the client.
func (s *Stream) Passthrough() bool { return s.passthrough() }

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
	if s.failed || s.closed {
		return nil
	}
	if s.passthrough() {
		return chunk
	}
	var out bytes.Buffer
	if len(s.buf)+len(chunk) > maxEventBytes || !s.resizeHeld(s.held+len(chunk)) {
		return s.failLarge(&out)
	}
	s.buf = append(s.buf, chunk...)
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
		if s.done || s.failed {
			s.Close()
			break
		}
		needed := s.retainedSize()
		if needed > maxEventBytes || (needed > s.held && !s.resizeHeld(needed)) {
			return s.failLarge(&out)
		}
	}
	if !s.closed {
		// Do not retain a consumed chunk's backing array outside its reservation.
		s.buf = bytes.Clone(s.buf)
		if !s.resizeHeld(s.retainedSize()) {
			return s.failLarge(&out)
		}
	}
	return out.Bytes()
}

// TooLarge reports that an upstream event exceeded the SSE line cap.
func (s *Stream) TooLarge() bool { return s.tooLarge }

func (s *Stream) noteTranslated(n int) bool {
	if s.tooLarge || s.translated+n > maxEventBytes || !s.resizeHeld(s.held+n) {
		return false
	}
	s.translated += n
	return true
}

func (s *Stream) failLarge(out *bytes.Buffer) []byte {
	defer s.Close()
	s.failed = true
	s.tooLarge = true
	s.done = true
	s.buf = nil
	s.respText.Reset()
	s.respCalls = nil
	s.closedTexts = nil
	s.toolSlots = nil
	s.chatTools = nil
	out.Write(s.clientError(map[string]any{
		"error": map[string]any{"message": "provider stream event is too large"},
	}))
	return out.Bytes()
}

// Abort tells the client the upstream ended in failure. It does not emit a
// successful completion.
func (s *Stream) Abort() []byte {
	defer s.Close()
	if s.done || s.failed {
		return nil
	}
	s.failed = true
	s.done = true
	return s.clientError(map[string]any{"error": map[string]any{"message": "provider request failed"}})
}

// Finish flushes a closing event when the upstream ended quietly.
func (s *Stream) Finish() []byte {
	defer s.Close()
	if s.passthrough() || s.done || s.failed {
		return nil
	}
	if s.client == ProtoMessages && s.upstream == ProtoChat {
		return s.closeAnthropic()
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
		s.event = strings.Clone(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		return nil
	}
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		if s.client == ProtoMessages && s.upstream == ProtoChat && !s.done {
			return s.closeAnthropic()
		}
		s.done = true
		if s.client == ProtoMessages {
			return []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
		return []byte("data: [DONE]\n\n")
	}
	var obj map[string]any
	if err := decodeJSON([]byte(data), &obj); err != nil {
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
			if usage, ok := message["usage"].(map[string]any); ok {
				s.chatUsage = mergeUsage(nil, usage)
			}
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
			slot, ok := s.noteToolSlot(obj, "", "")
			if !ok {
				return s.failLarge(&bytes.Buffer{})
			}
			return sseData(chatChunk(s.id, map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    slot,
					"function": map[string]any{"arguments": partial},
				}},
			}, nil, nil))
		}
	case "content_block_start":
		block, _ := obj["content_block"].(map[string]any)
		if block["type"] == "tool_use" {
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			slot, ok := s.noteToolSlot(obj, id, name)
			if !ok {
				return s.failLarge(&bytes.Buffer{})
			}
			return sseData(chatChunk(s.id, map[string]any{
				"tool_calls": []any{map[string]any{
					"index": slot,
					"id":    id,
					"type":  "function",
					"function": map[string]any{
						"name":      name,
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
		if raw, ok := obj["usage"].(map[string]any); ok {
			s.chatUsage = mergeUsage(s.chatUsage, raw)
		}
		var usage any
		if len(s.chatUsage) > 0 {
			usage = usageToOpenAI(s.chatUsage)
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
	if usage, ok := obj["usage"].(map[string]any); ok && len(usage) > 0 {
		s.chatUsage = mergeUsage(s.chatUsage, usage)
	}
	choices, _ := obj["choices"].([]any)
	if len(choices) == 0 {
		if s.finishSeen && len(s.chatUsage) > 0 {
			out.Write(s.closeAnthropic())
		}
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
		s.finishSeen = true
		s.finishReason = finish
		for i := 0; i < s.nextContent; i++ {
			out.WriteString("event: content_block_stop\n")
			out.Write(sseData(map[string]any{"type": "content_block_stop", "index": i}))
		}
		s.nextContent = 0
		if len(s.chatUsage) > 0 {
			out.Write(s.closeAnthropic())
		}
	}
	return out.Bytes()
}

func (s *Stream) closeAnthropic() []byte {
	if s.done {
		return nil
	}
	var out bytes.Buffer
	for i := 0; i < s.nextContent; i++ {
		out.WriteString("event: content_block_stop\n")
		out.Write(sseData(map[string]any{"type": "content_block_stop", "index": i}))
	}
	s.nextContent = 0
	reason := s.finishReason
	if reason == "" {
		reason = "stop"
	}
	usage := map[string]any{}
	if len(s.chatUsage) > 0 {
		usage = usageToAnthropic(s.chatUsage)
	}
	out.WriteString("event: message_delta\n")
	out.Write(sseData(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": mapStopToAnthropic(reason)},
		"usage": usage,
	}))
	out.WriteString("event: message_stop\n")
	out.Write(sseData(map[string]any{"type": "message_stop"}))
	s.done = true
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
			name, _ := fn["name"].(string)
			id, _ := call["id"].(string)
			if !s.noteTranslated(len(id) + len(name) + callMetaBytes) {
				return s.failLarge(&out)
			}
			block = s.nextContent
			s.nextContent++
			s.chatTools[index] = block
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
			if usage, ok := message["usage"].(map[string]any); ok {
				s.chatUsage = mergeUsage(s.chatUsage, usage)
			}
		}
		return s.ensureResponse()
	case "content_block_start":
		block, _ := obj["content_block"].(map[string]any)
		if block["type"] != "tool_use" {
			return nil
		}
		var out bytes.Buffer
		if s.textOpen {
			if !s.noteTranslated(3 * s.respText.Len()) {
				return s.failLarge(&out)
			}
			out.Write(s.closeResponseText())
		}
		out.Write(s.openFunctionCall(obj, block))
		return out.Bytes()
	case "content_block_delta":
		delta, _ := obj["delta"].(map[string]any)
		switch delta["type"] {
		case "text_delta":
			text, _ := delta["text"].(string)
			var out bytes.Buffer
			if !s.textOpen {
				out.Write(s.openResponses())
			}
			if !s.noteTranslated(len(text)) {
				return s.failLarge(&out)
			}
			s.respText.WriteString(text)
			out.Write(sseData(map[string]any{
				"type":          "response.output_text.delta",
				"output_index":  s.textIndex,
				"content_index": 0,
				"delta":         text,
			}))
			return out.Bytes()
		case "input_json_delta":
			return s.appendFunctionArgs(obj, delta)
		default:
			return nil
		}
	case "message_delta":
		if delta, ok := obj["delta"].(map[string]any); ok {
			if reason, ok := delta["stop_reason"].(string); ok && reason != "" {
				s.respStop = reason
			}
		}
		if usage, ok := obj["usage"].(map[string]any); ok {
			s.chatUsage = mergeUsage(s.chatUsage, usage)
		}
		return nil
	case "message_stop":
		s.done = true
		return s.finishResponses()
	}
	return nil
}

func (s *Stream) openFunctionCall(obj, block map[string]any) []byte {
	var out bytes.Buffer
	out.Write(s.ensureResponse())
	blockIndex, ok := eventIndex(obj["index"])
	if !ok {
		blockIndex = len(s.respCalls)
	}
	id, _ := block["id"].(string)
	name, _ := block["name"].(string)
	if !s.noteTranslated(len(id) + len(name) + callMetaBytes) {
		return s.failLarge(&out)
	}
	call := responseCall{block: blockIndex, index: s.allocOut(), id: id, name: name}
	s.respCalls = append(s.respCalls, call)
	out.Write(sseData(map[string]any{
		"type":         "response.output_item.added",
		"output_index": call.index,
		"item": map[string]any{
			"type": "function_call", "id": id, "call_id": id, "name": name, "arguments": "", "status": "in_progress",
		},
	}))
	return out.Bytes()
}

func (s *Stream) appendFunctionArgs(obj, delta map[string]any) []byte {
	partial, _ := delta["partial_json"].(string)
	blockIndex, ok := eventIndex(obj["index"])
	if !ok {
		return nil
	}
	for i := range s.respCalls {
		if s.respCalls[i].block != blockIndex {
			continue
		}
		if !s.noteTranslated(len(partial)) {
			var out bytes.Buffer
			return s.failLarge(&out)
		}
		s.respCalls[i].args.WriteString(partial)
		return sseData(map[string]any{
			"type":         "response.function_call_arguments.delta",
			"item_id":      s.respCalls[i].id,
			"output_index": s.respCalls[i].index,
			"delta":        partial,
		})
	}
	return nil
}

func (s *Stream) allocOut() int {
	n := s.nextOut
	s.nextOut++
	return n
}

func (s *Stream) finishResponses() []byte {
	text := s.respText.String()
	// The terminal events repeat the text and each tool's arguments. Charge
	// that expansion before buffering it.
	extra := 3 * len(text)
	for i := range s.respCalls {
		extra += 2 * s.respCalls[i].args.Len()
	}
	if extra > 0 && !s.noteTranslated(extra) {
		return s.failLarge(&bytes.Buffer{})
	}
	var out bytes.Buffer
	if !s.respOpened {
		out.Write(s.openResponses())
	}
	out.Write(s.closeResponseText())
	for i := range s.respCalls {
		call := &s.respCalls[i]
		args := call.args.String()
		out.Write(sseData(map[string]any{
			"type": "response.function_call_arguments.done", "item_id": call.id,
			"output_index": call.index, "arguments": args,
		}))
		out.Write(sseData(map[string]any{
			"type": "response.output_item.done", "output_index": call.index,
			"item": functionCallItem(call.id, call.name, args, "completed"),
		}))
	}
	status, details := responsesStatus(s.respStop)
	event := "response.completed"
	if status != "completed" {
		event = "response.incomplete"
	}
	response := s.responsesObject(text, status)
	if details != nil {
		response["incomplete_details"] = details
	}
	if len(s.chatUsage) > 0 {
		response["usage"] = usageToResponses(s.chatUsage)
	}
	out.Write(sseData(map[string]any{
		"type":     event,
		"response": response,
	}))
	return out.Bytes()
}

func (s *Stream) closeResponseText() []byte {
	var out bytes.Buffer
	text := s.respText.String()
	if s.textOpen {
		out.Write(sseData(map[string]any{
			"type": "response.output_text.done", "output_index": s.textIndex, "content_index": 0, "text": text,
		}))
		out.Write(sseData(map[string]any{
			"type": "response.content_part.done", "output_index": s.textIndex, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text},
		}))
		out.Write(sseData(map[string]any{
			"type": "response.output_item.done", "output_index": s.textIndex,
			"item": s.responsesMessage(text),
		}))
	}
	if s.textOpen {
		s.closedTexts = append(s.closedTexts, responseText{s.textIndex, text})
		s.textOpen = false
		s.respText.Reset()
	}
	return out.Bytes()
}

func (s *Stream) ensureResponse() []byte {
	if s.respOpened {
		return nil
	}
	s.respOpened = true
	if s.id == "" {
		s.id = "resp"
	}
	return sseData(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id": s.id, "object": "response", "status": "in_progress", "output": []any{},
		},
	})
}

func (s *Stream) openResponses() []byte {
	if s.textOpen {
		return nil
	}
	var out bytes.Buffer
	out.Write(s.ensureResponse())
	s.textOpen = true
	s.textIndex = s.allocOut()
	out.Write(sseData(map[string]any{
		"type": "response.output_item.added", "output_index": s.textIndex,
		"item": map[string]any{"id": s.textID(s.textIndex), "type": "message", "role": "assistant", "content": []any{}},
	}))
	out.Write(sseData(map[string]any{
		"type": "response.content_part.added", "output_index": s.textIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": ""},
	}))
	return out.Bytes()
}

func (s *Stream) textID(index int) string { return fmt.Sprintf("%s_message_%d", s.id, index) }

func (s *Stream) responsesMessage(text string) map[string]any {
	return s.responsesMessageAt(text, s.textIndex)
}

func (s *Stream) responsesMessageAt(text string, index int) map[string]any {
	return map[string]any{
		"id": s.textID(index), "type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func (s *Stream) responsesObject(text, status string) map[string]any {
	return map[string]any{
		"id": s.id, "object": "response", "status": status,
		"output": s.responseOutput(text),
	}
}

func (s *Stream) responseOutput(text string) []any {
	type slot struct {
		index int
		item  any
	}
	var slots []slot
	for _, text := range s.closedTexts {
		slots = append(slots, slot{text.index, s.responsesMessageAt(text.text, text.index)})
	}
	if s.textOpen {
		slots = append(slots, slot{s.textIndex, s.responsesMessage(text)})
	}
	for i := range s.respCalls {
		call := &s.respCalls[i]
		slots = append(slots, slot{call.index, functionCallItem(call.id, call.name, call.args.String(), "completed")})
	}
	for i := 1; i < len(slots); i++ {
		for j := i; j > 0 && slots[j].index < slots[j-1].index; j-- {
			slots[j], slots[j-1] = slots[j-1], slots[j]
		}
	}
	out := make([]any, 0, len(slots))
	for _, item := range slots {
		out = append(out, item.item)
	}
	if len(out) == 0 {
		out = append(out, s.responsesMessage(text))
	}
	return out
}

func functionCallItem(id, name, args, status string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": id, "call_id": id, "name": name, "arguments": args, "status": status,
	}
}

func (s *Stream) noteToolSlot(obj map[string]any, id, name string) (int, bool) {
	index, ok := eventIndex(obj["index"])
	if !ok {
		index = s.nextTool
	}
	if s.toolSlots == nil {
		s.toolSlots = map[int]int{}
	}
	if slot, exists := s.toolSlots[index]; exists {
		return slot, true
	}
	if !s.noteTranslated(len(id) + len(name) + callMetaBytes) {
		return 0, false
	}
	slot := s.nextTool
	s.nextTool++
	s.toolSlots[index] = slot
	return slot, true
}

func eventIndex(value any) (int, bool) {
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

func mergeUsage(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		switch key {
		case "prompt_tokens_details", "input_tokens_details":
			details, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := details["cached_tokens"]; ok && usageNumber(n) {
				out[key] = map[string]any{"cached_tokens": n}
			}
		default:
			if usageCounter(key) && usageNumber(value) {
				out[key] = value
			}
		}
	}
	return out
}

func usageCounter(key string) bool {
	switch key {
	case "input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "cache_read_input_tokens", "cache_creation_input_tokens":
		return true
	default:
		return false
	}
}

func usageNumber(value any) bool {
	switch value.(type) {
	case float64, int, int64, json.Number:
		return true
	default:
		return false
	}
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
