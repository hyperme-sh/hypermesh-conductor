package translate

import (
	"strings"
	"testing"
)

func TestChatRequestBecomesAnthropicMessages(t *testing.T) {
	wire, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"model":"other","max_tokens":32,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if wire.Path != "messages" || wire.Protocol != ProtoMessages {
		t.Fatalf("%+v", wire)
	}
	text := string(wire.Body)
	if !strings.Contains(text, `"model":"frontier"`) || !strings.Contains(text, `"system":"be brief"`) || !strings.Contains(text, `"max_tokens":32`) {
		t.Fatal(text)
	}
}

func TestAnthropicStreamBecomesChatChunks(t *testing.T) {
	stream := NewStream(ProtoChat, ProtoMessages)
	input := "" +
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"yo\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	out := string(stream.Consume([]byte(input)))
	if !strings.Contains(out, `"content":"yo"`) || !strings.Contains(out, "data: [DONE]") {
		t.Fatal(out)
	}
}

func TestToolCallsSurviveTranslation(t *testing.T) {
	chat := []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	wire, err := Request(ProtoChat, ProviderAnthropic, "frontier", chat)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if !strings.Contains(text, `"type":"tool_use"`) || !strings.Contains(text, `"id":"call_1"`) || !strings.Contains(text, `"tool_use_id":"call_1"`) {
		t.Fatal(text)
	}
	anthropic := []byte(`{"system":"be brief","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]}`)
	back, err := Request(ProtoMessages, ProviderOpenAI, "frontier", anthropic)
	if err != nil {
		t.Fatal(err)
	}
	text = string(back.Body)
	if !strings.Contains(text, `"tool_calls"`) || !strings.Contains(text, `"id":"call_1"`) || !strings.Contains(text, `"tool_call_id":"call_1"`) || !strings.Contains(text, `"content":null`) {
		t.Fatal(text)
	}
}

func TestSystemBlocksAndStopReasons(t *testing.T) {
	wire, err := Request(ProtoMessages, ProviderOpenAI, "frontier", []byte(`{"system":[{"type":"text","text":"be brief"}],"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !strings.Contains(string(wire.Body), `"role":"system"`) || !strings.Contains(string(wire.Body), "be brief") {
		t.Fatal(err, string(wire.Body))
	}
	if _, err := Request(ProtoMessages, ProviderOpenAI, "frontier", []byte(`{"system":[{"type":"image","source":{}}],"messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("image system block was accepted")
	}
	tool, err := Response(ProtoMessages, ProtoChat, []byte(`{"id":"c1","model":"frontier","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`))
	if err != nil || !strings.Contains(string(tool), `"stop_reason":"tool_use"`) {
		t.Fatal(err, string(tool))
	}
	length, err := Response(ProtoMessages, ProtoChat, []byte(`{"id":"c1","model":"frontier","choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`))
	if err != nil || !strings.Contains(string(length), `"stop_reason":"max_tokens"`) {
		t.Fatal(err, string(length))
	}
}

func TestStreamToolIndexesAndErrorEvent(t *testing.T) {
	stream := NewStream(ProtoChat, ProtoMessages)
	var out string
	chunks := []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_a\",\"name\":\"one\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\":1}\"}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_b\",\"name\":\"two\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"y\\\":2}\"}}\n\n",
	}
	for _, chunk := range chunks {
		out += string(stream.Consume([]byte(chunk)))
	}
	if !strings.Contains(out, `"id":"call_a"`) || !strings.Contains(out, `"id":"call_b"`) || strings.Count(out, `"index":1`) != 2 {
		t.Fatal(out)
	}
	if strings.Contains(out, `"id":"call_b"`) && !strings.Contains(out, `"index":1`) {
		t.Fatal(out)
	}
	errStream := NewStream(ProtoChat, ProtoMessages)
	_ = errStream.Consume([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	failed := string(errStream.Consume([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"overloaded\"}}\n\n")))
	if !strings.Contains(failed, `"message":"overloaded"`) || len(errStream.Finish()) != 0 {
		t.Fatal(failed)
	}
}

func TestSamplingControlsCrossTheProtocolBoundary(t *testing.T) {
	body := []byte(`{"temperature":0,"top_p":0.8,"messages":[{"role":"user","content":"hi"}]}`)
	wire, err := Request(ProtoChat, ProviderAnthropic, "frontier", body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if !strings.Contains(text, `"temperature":0`) || !strings.Contains(text, `"top_p":0.8`) {
		t.Fatal(text)
	}
	back, err := Request(ProtoMessages, ProviderOpenAI, "frontier", []byte(`{"temperature":0,"top_p":0.8,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	text = string(back.Body)
	if !strings.Contains(text, `"temperature":0`) || !strings.Contains(text, `"top_p":0.8`) {
		t.Fatal(text)
	}
	if _, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"frequency_penalty":1,"messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("frequency_penalty was dropped")
	}
	if _, err := Request(ProtoMessages, ProviderOpenAI, "frontier", []byte(`{"top_k":10,"messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("top_k was dropped")
	}
}

func TestToolChoiceCrossesTheProtocolBoundary(t *testing.T) {
	none, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"tool_choice":"none","tools":[{"type":"function","function":{"name":"lookup"}}],"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !strings.Contains(string(none.Body), `"tool_choice":{"type":"none"}`) {
		t.Fatal(err, string(none.Body))
	}
	forced, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"tool_choice":{"type":"function","function":{"name":"lookup"}},"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !strings.Contains(string(forced.Body), `"type":"tool"`) || !strings.Contains(string(forced.Body), `"name":"lookup"`) {
		t.Fatal(err, string(forced.Body))
	}
	back, err := Request(ProtoMessages, ProviderOpenAI, "frontier", []byte(`{"tool_choice":{"type":"any"},"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !strings.Contains(string(back.Body), `"tool_choice":"required"`) {
		t.Fatal(err, string(back.Body))
	}
	if _, err := Request(ProtoMessages, ProviderOpenAI, "frontier", []byte(`{"tool_choice":{"type":"auto","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("parallel tool constraint was dropped")
	}
}

func TestChatStreamClosesContentBlocks(t *testing.T) {
	stream := NewStream(ProtoMessages, ProtoChat)
	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_w\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"
	out := string(stream.Consume([]byte(chunk)))
	if !strings.Contains(out, `"type":"content_block_stop"`) || strings.Index(out, "content_block_stop") > strings.Index(out, "message_delta") {
		t.Fatal(out)
	}
	if strings.Count(out, "content_block_stop") < 2 {
		t.Fatal(out)
	}
}

func TestStreamDropsAnOversizedLine(t *testing.T) {
	previous := maxEventBytes
	maxEventBytes = 16
	t.Cleanup(func() { maxEventBytes = previous })
	stream := NewStream(ProtoMessages, ProtoChat)
	out := stream.Consume([]byte(strings.Repeat("a", 40)))
	if !stream.TooLarge() || len(stream.buf) != 0 || !strings.Contains(string(out), "too large") {
		t.Fatalf("large %v buf %d out %s", stream.TooLarge(), len(stream.buf), out)
	}
}

func TestChatStreamKeepsToolCalls(t *testing.T) {
	stream := NewStream(ProtoMessages, ProtoChat)
	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_w\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"q\\\":1}\"}}]}}]}\n\n"
	out := string(stream.Consume([]byte(chunk)))
	if !strings.Contains(out, `"type":"tool_use"`) || !strings.Contains(out, `"name":"weather"`) || !strings.Contains(out, `"id":"call_w"`) || !strings.Contains(out, `"partial_json":"{\"q\":1}"`) {
		t.Fatal(out)
	}
}

func TestResponsesToolsAndStreamLifecycle(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"weather","description":"Forecast","parameters":{"type":"object"}}],"tool_choice":"required","input":"hi"}`)
	wire, err := Request(ProtoResponses, ProviderAnthropic, "frontier", body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if !strings.Contains(text, `"name":"weather"`) || !strings.Contains(text, `"input_schema"`) || !strings.Contains(text, `"tool_choice":{"type":"any"}`) {
		t.Fatal(text)
	}
	stream := NewStream(ProtoResponses, ProtoMessages)
	out := string(stream.Consume([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"yo\"}}\n\n")))
	out += string(stream.Consume([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")))
	if !strings.Contains(out, `"type":"response.output_item.added"`) || !strings.Contains(out, `"type":"response.content_part.added"`) || !strings.Contains(out, `"output_index":0`) {
		t.Fatal(out)
	}
	if !strings.Contains(out, `"type":"response.completed"`) || !strings.Contains(out, `"text":"yo"`) || !strings.Contains(out, `"object":"response"`) {
		t.Fatal(out)
	}
}

func TestResponsesToolItemsBecomeAnthropicBlocks(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"},{"type":"function_call_output","call_id":"call_1","output":"balance is 930"}]}`)
	wire, err := Request(ProtoResponses, ProviderAnthropic, "frontier", body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if !strings.Contains(text, `"type":"tool_use"`) || !strings.Contains(text, `"tool_use_id":"call_1"`) || !strings.Contains(text, "balance is 930") {
		t.Fatal(text)
	}
	if _, err := Request(ProtoResponses, ProviderAnthropic, "frontier", []byte(`{"input":[{"type":"reasoning","summary":"secret"}]}`)); err == nil {
		t.Fatal("unsupported responses item was forwarded")
	}
}

func TestSameProtocolPreservesNestedBytes(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"a & b"}],"stream":false}`)
	wire, err := Request(ProtoChat, ProviderOpenAICompatible, "local-small", body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire.Body), `"content":"a & b"`) {
		t.Fatal(string(wire.Body))
	}
	if !strings.Contains(string(wire.Body), `"model":"local-small"`) {
		t.Fatal(string(wire.Body))
	}
}
