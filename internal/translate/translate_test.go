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
