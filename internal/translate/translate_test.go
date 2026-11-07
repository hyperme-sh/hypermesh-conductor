package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestResponsesSchemaIsNotSilentlyDropped(t *testing.T) {
	_, err := Request(ProtoResponses, ProviderAnthropic, "frontier", []byte(`{"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":true}},"input":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "text.format") {
		t.Fatal(err)
	}
	wire, err := Request(ProtoResponses, ProviderAnthropic, "frontier", []byte(`{"text":{"format":{"type":"text"}},"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire.Body), "json_schema") {
		t.Fatal(string(wire.Body))
	}
}

func TestStructuredOutputIsNotSilentlyDropped(t *testing.T) {
	_, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"response_format":{"type":"json_schema","json_schema":{"name":"out","schema":{"type":"object"}}},"messages":[{"role":"user","content":"hi"}]}`))
	if err == nil || !strings.Contains(err.Error(), "response_format") {
		t.Fatal(err)
	}
	wire, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"response_format":{"type":"text"},"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire.Body), "response_format") {
		t.Fatal(string(wire.Body))
	}
}

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

func TestChatDeveloperMessageBecomesAnthropicSystem(t *testing.T) {
	wire, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"messages":[{"role":"developer","content":"Follow these rules"},{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if strings.Contains(text, `"role":"developer"`) || !strings.Contains(text, `"system":"Follow these rules"`) || !strings.Contains(text, `"role":"user"`) {
		t.Fatal(text)
	}
}

func TestAnthropicStreamKeepsInputUsage(t *testing.T) {
	stream := NewStream(ProtoChat, ProtoMessages)
	t.Cleanup(stream.Close)
	input := "" +
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":20}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n"
	out := string(stream.Consume([]byte(input)))
	if !strings.Contains(out, `"prompt_tokens":120`) || !strings.Contains(out, `"completion_tokens":5`) || !strings.Contains(out, `"cached_tokens":20`) {
		t.Fatal(out)
	}
}

func TestAnthropicStreamBecomesChatChunks(t *testing.T) {
	stream := NewStream(ProtoChat, ProtoMessages)
	t.Cleanup(stream.Close)
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
	if !strings.Contains(text, `"tool_calls"`) || !strings.Contains(text, `"id":"call_1"`) || !strings.Contains(text, `"tool_call_id":"call_1"`) || !strings.Contains(text, `"content":null`) || strings.Contains(text, "[tool_use") {
		t.Fatal(text)
	}
	mixed := []byte(`{"id":"m1","content":[{"type":"text","text":"looked up"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":1}}],"stop_reason":"tool_use"}`)
	chatOut, err := Response(ProtoChat, ProtoMessages, mixed)
	if err != nil {
		t.Fatal(err)
	}
	text = string(chatOut)
	if !strings.Contains(text, `"content":"looked up"`) || !strings.Contains(text, `"tool_calls"`) || strings.Contains(text, "[tool_use") {
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
	t.Cleanup(stream.Close)
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
	t.Cleanup(errStream.Close)
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

func TestMessagesUsageSubtractsCachedPromptTokens(t *testing.T) {
	body := []byte(`{"id":"c1","model":"frontier","usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":80}},"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)
	out, err := Response(ProtoMessages, ProtoChat, body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, `"input_tokens":20`) || !strings.Contains(text, `"cache_read_input_tokens":80`) || strings.Contains(text, `"input_tokens":100`) {
		t.Fatal(text)
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
	for _, body := range [][]byte{
		[]byte(`{"parallel_tool_calls":false,"tool_choice":"auto","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"parallel_tool_calls":false,"messages":[{"role":"user","content":"hi"}]}`),
	} {
		wire, err := Request(ProtoChat, ProviderAnthropic, "frontier", body)
		if err != nil || !strings.Contains(string(wire.Body), `"disable_parallel_tool_use":true`) || !strings.Contains(string(wire.Body), `"type":"auto"`) {
			t.Fatal(err, string(wire.Body))
		}
	}
	responses, err := Request(ProtoResponses, ProviderAnthropic, "frontier", []byte(`{"parallel_tool_calls":false,"input":"hi"}`))
	if err != nil || !strings.Contains(string(responses.Body), `"disable_parallel_tool_use":true`) {
		t.Fatal(err, string(responses.Body))
	}
	plain, err := Request(ProtoChat, ProviderAnthropic, "frontier", []byte(`{"tool_choice":"auto","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || strings.Contains(string(plain.Body), "disable_parallel_tool_use") {
		t.Fatal(err, string(plain.Body))
	}
}

func TestChatStreamClosesContentBlocks(t *testing.T) {
	stream := NewStream(ProtoMessages, ProtoChat)
	t.Cleanup(stream.Close)
	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_w\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"
	out := string(stream.Consume([]byte(chunk))) + string(stream.Finish())
	if !strings.Contains(out, `"type":"content_block_stop"`) || strings.Index(out, "content_block_stop") > strings.Index(out, "message_delta") {
		t.Fatal(out)
	}
	if strings.Count(out, "content_block_stop") < 2 {
		t.Fatal(out)
	}
}

func TestTranslatedStreamCapsTotalText(t *testing.T) {
	previous := maxEventBytes
	maxEventBytes = 8
	t.Cleanup(func() { maxEventBytes = previous })
	stream := NewStream(ProtoResponses, ProtoMessages)
	t.Cleanup(stream.Close)
	var out string
	for i := 0; i < 5; i++ {
		out += string(stream.Consume([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"abcd\"}}\n\n")))
	}
	if !stream.TooLarge() || stream.respText.Len() > maxEventBytes || stream.translated > maxEventBytes {
		t.Fatalf("large %v text %d translated %d out %s", stream.TooLarge(), stream.respText.Len(), stream.translated, out)
	}
}

func TestStreamUsageKeepsOnlyCounters(t *testing.T) {
	stream := NewStream(ProtoChat, ProtoMessages)
	t.Cleanup(stream.Close)
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":4")
	for i := 0; i < 100; i++ {
		b.WriteString(fmt.Sprintf(",\"k%d\":%d", i, i))
	}
	b.WriteString("}}}\n\n")
	_ = stream.Consume([]byte(b.String()))
	if stream.TooLarge() || len(stream.chatUsage) != 1 || stream.chatUsage["input_tokens"] == nil {
		t.Fatalf("too %v usage %v", stream.TooLarge(), stream.chatUsage)
	}
}

func TestCrossProtocolToolStateHitsTheCap(t *testing.T) {
	previous := maxEventBytes
	maxEventBytes = 300
	t.Cleanup(func() { maxEventBytes = previous })
	chat := NewStream(ProtoChat, ProtoMessages)
	t.Cleanup(chat.Close)
	var chatOut string
	for i := 1; i <= 6; i++ {
		chatOut += string(chat.Consume([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":" + string(rune('0'+i)) + ",\"content_block\":{\"type\":\"tool_use\",\"id\":\"ab\",\"name\":\"n\"}}\n\n")))
	}
	if !chat.TooLarge() || chat.toolSlots != nil || !strings.Contains(chatOut, "too large") {
		t.Fatalf("chat too %v slots %v out %s", chat.TooLarge(), chat.toolSlots != nil, chatOut)
	}
	messages := NewStream(ProtoMessages, ProtoChat)
	t.Cleanup(messages.Close)
	var msgOut string
	for i := 1; i <= 6; i++ {
		msgOut += string(messages.Consume([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":" + string(rune('0'+i)) + ",\"id\":\"ab\",\"type\":\"function\",\"function\":{\"name\":\"n\",\"arguments\":\"\"}}]}}]}\n\n")))
	}
	if !messages.TooLarge() || messages.chatTools != nil || !strings.Contains(msgOut, "too large") {
		t.Fatalf("messages too %v tools %v out %s", messages.TooLarge(), messages.chatTools != nil, msgOut)
	}
}

func TestToolStartsCountTowardTheStreamCap(t *testing.T) {
	previous := maxEventBytes
	maxEventBytes = 200
	t.Cleanup(func() { maxEventBytes = previous })
	stream := NewStream(ProtoResponses, ProtoMessages)
	t.Cleanup(stream.Close)
	event := func(index int) string {
		return "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":" + string(rune('0'+index)) + ",\"content_block\":{\"type\":\"tool_use\",\"id\":\"ab\",\"name\":\"n\"}}\n\n"
	}
	if out := stream.Consume([]byte(event(1))); stream.TooLarge() || len(stream.respCalls) != 1 {
		t.Fatalf("first too %v calls %d out %s", stream.TooLarge(), len(stream.respCalls), out)
	}
	_ = stream.Consume([]byte(event(2)))
	out := string(stream.Consume([]byte(event(3))))
	if !stream.TooLarge() || stream.respCalls != nil || !strings.Contains(out, "too large") {
		t.Fatalf("too %v calls %v translated %d out %s", stream.TooLarge(), stream.respCalls, stream.translated, out)
	}
}

func TestStreamDropsAnOversizedLine(t *testing.T) {
	previous := maxEventBytes
	maxEventBytes = 16
	t.Cleanup(func() { maxEventBytes = previous })
	stream := NewStream(ProtoMessages, ProtoChat)
	t.Cleanup(stream.Close)
	out := stream.Consume([]byte(strings.Repeat("a", 40)))
	if !stream.TooLarge() || len(stream.buf) != 0 || !strings.Contains(string(out), "too large") {
		t.Fatalf("large %v buf %d out %s", stream.TooLarge(), len(stream.buf), out)
	}
}

func TestChatStreamUsageReachesMessages(t *testing.T) {
	stream := NewStream(ProtoMessages, ProtoChat)
	t.Cleanup(stream.Close)
	finish := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":80}}}\n\n"
	out := string(stream.Consume([]byte(finish))) + string(stream.Consume([]byte(usage)))
	if strings.Contains(out, `"usage":{}`) || !strings.Contains(out, `"input_tokens":20`) || !strings.Contains(out, `"cache_read_input_tokens":80`) || !strings.Contains(out, `"output_tokens":5`) {
		t.Fatal(out)
	}
}

func TestLargeToolIntegersStayExact(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"record_id":9007199254740993}}]}]}`)
	wire, err := Request(ProtoMessages, ProviderOpenAI, "frontier", body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if !strings.Contains(text, "9007199254740993") || strings.Contains(text, "9007199254740992") {
		t.Fatal(text)
	}
	back := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"record_id\":9007199254740993}"}}]}]}`)
	returned, err := Request(ProtoChat, ProviderAnthropic, "frontier", back)
	if err != nil {
		t.Fatal(err)
	}
	text = string(returned.Body)
	if !strings.Contains(text, "9007199254740993") || strings.Contains(text, "9007199254740992") {
		t.Fatal(text)
	}
}

func TestChatStreamKeepsToolCalls(t *testing.T) {
	stream := NewStream(ProtoMessages, ProtoChat)
	t.Cleanup(stream.Close)
	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_w\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"q\\\":1}\"}}]}}]}\n\n"
	out := string(stream.Consume([]byte(chunk)))
	if !strings.Contains(out, `"type":"tool_use"`) || !strings.Contains(out, `"name":"weather"`) || !strings.Contains(out, `"id":"call_w"`) || !strings.Contains(out, `"partial_json":"{\"q\":1}"`) {
		t.Fatal(out)
	}
}

func TestResponsesTranslationKeepsInstructionsAndRefusesState(t *testing.T) {
	body := []byte(`{"instructions":"be brief","input":[{"role":"system","content":"Follow these rules"},{"role":"developer","content":"Stay in scope"},{"role":"user","content":"hi"}]}`)
	wire, err := Request(ProtoResponses, ProviderAnthropic, "frontier", body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	wantSystem := `be brief\nFollow these rules\nStay in scope`
	if strings.Contains(text, `"role":"system"`) || strings.Contains(text, `"role":"developer"`) || !strings.Contains(text, wantSystem) || !strings.Contains(text, `"role":"user"`) {
		t.Fatal(text)
	}
	if _, err := Request(ProtoResponses, ProviderAnthropic, "frontier", []byte(`{"previous_response_id":"resp_prior","input":"what next?"}`)); err == nil {
		t.Fatal("previous_response_id was forwarded")
	}
}

func TestMaxTokensBecomesAnIncompleteResponse(t *testing.T) {
	body := []byte(`{"id":"msg_1","content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":20,"output_tokens":4,"cache_read_input_tokens":80,"cache_creation_input_tokens":50}}`)
	out, err := Response(ProtoResponses, ProtoMessages, body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, `"status":"incomplete"`) || !strings.Contains(text, `"reason":"max_output_tokens"`) || strings.Contains(text, `"status":"completed"`) {
		t.Fatal(text)
	}
	if !strings.Contains(text, `"input_tokens":150`) || !strings.Contains(text, `"cached_tokens":80`) || strings.Contains(text, "cache_creation_input_tokens") || strings.Contains(text, "cache_read_input_tokens") {
		t.Fatal(text)
	}
	stream := NewStream(ProtoResponses, ProtoMessages)
	t.Cleanup(stream.Close)
	var streamed string
	streamed += string(stream.Consume([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")))
	streamed += string(stream.Consume([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n")))
	streamed += string(stream.Consume([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")))
	if !strings.Contains(streamed, `"type":"response.incomplete"`) || !strings.Contains(streamed, `"reason":"max_output_tokens"`) || strings.Contains(streamed, `"type":"response.completed"`) {
		t.Fatal(streamed)
	}
}

func TestResponsesFinishStaysInsideTheCap(t *testing.T) {
	previous := maxEventBytes
	maxEventBytes = 500
	t.Cleanup(func() { maxEventBytes = previous })
	stream := NewStream(ProtoResponses, ProtoMessages)
	t.Cleanup(stream.Close)
	piece := strings.Repeat("a", 20)
	for i := 0; i < 8; i++ {
		chunk := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + piece + "\"}}\n\n"
		if out := stream.Consume([]byte(chunk)); stream.TooLarge() {
			t.Fatal(string(out))
		}
	}
	out := stream.Consume([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	if !stream.TooLarge() || len(out) > maxEventBytes || strings.Contains(string(out), piece+piece) {
		t.Fatalf("too %v len %d out %s", stream.TooLarge(), len(out), out)
	}
}

func TestResponsesSamplingAndStreamUsage(t *testing.T) {
	wire, err := Request(ProtoResponses, ProviderAnthropic, "frontier", []byte(`{"temperature":0,"top_p":0.1,"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire.Body)
	if !strings.Contains(text, `"temperature":0`) || !strings.Contains(text, `"top_p":0.1`) {
		t.Fatal(text)
	}
	stream := NewStream(ProtoResponses, ProtoMessages)
	t.Cleanup(stream.Close)
	var out string
	for _, chunk := range []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":20,\"cache_read_input_tokens\":80,\"cache_creation_input_tokens\":50}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"yo\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	} {
		out += string(stream.Consume([]byte(chunk)))
	}
	if !strings.Contains(out, `"input_tokens":150`) || !strings.Contains(out, `"cached_tokens":80`) || !strings.Contains(out, `"output_tokens":5`) {
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
	t.Cleanup(stream.Close)
	out := string(stream.Consume([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"yo\"}}\n\n")))
	out += string(stream.Consume([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")))
	if !strings.Contains(out, `"type":"response.output_item.added"`) || !strings.Contains(out, `"type":"response.content_part.added"`) || !strings.Contains(out, `"output_index":0`) {
		t.Fatal(out)
	}
	if !strings.Contains(out, `"type":"response.completed"`) || !strings.Contains(out, `"text":"yo"`) || !strings.Contains(out, `"object":"response"`) {
		t.Fatal(out)
	}
}

func TestResponsesOutputKeepsToolCalls(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_w","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use"}`)
	out, err := Response(ProtoResponses, ProtoMessages, body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, `"type":"function_call"`) || !strings.Contains(text, `"name":"weather"`) || !strings.Contains(text, `"call_id":"call_w"`) || !strings.Contains(text, "Paris") || strings.Contains(text, "[tool_use weather]") {
		t.Fatal(text)
	}
	stream := NewStream(ProtoResponses, ProtoMessages)
	t.Cleanup(stream.Close)
	var streamed string
	for _, chunk := range []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_w\",\"name\":\"weather\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	} {
		streamed += string(stream.Consume([]byte(chunk)))
	}
	if !strings.Contains(streamed, `"type":"function_call"`) || !strings.Contains(streamed, "response.function_call_arguments.delta") || !strings.Contains(streamed, "Paris") || !strings.Contains(streamed, `"type":"response.completed"`) {
		t.Fatal(streamed)
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

func TestAnthropicHonorsParallelToolProhibition(t *testing.T) {
	for _, proto := range []string{ProtoChat, ProtoResponses} {
		for _, choice := range []string{"", `,"tool_choice":"auto"`, `,"tool_choice":"required"`} {
			input := `{"parallel_tool_calls":false,"messages":[{"role":"user","content":"hi"}],"input":"hi"` + choice + `}`
			got, err := Request(proto, ProviderAnthropic, "frontier", []byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got.Body), `"disable_parallel_tool_use":true`) {
				t.Fatal(string(got.Body))
			}
		}
	}
}

func TestResponsesStreamPreservesTextToolTextOrder(t *testing.T) {
	stream := NewStream(ProtoResponses, ProtoMessages)
	defer stream.Close()
	var output string
	for _, event := range []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"before"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"lookup"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"after"}}`,
		`{"type":"message_stop"}`,
	} {
		output += string(stream.Consume([]byte("data: " + event + "\n\n")))
	}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &obj); err != nil {
			t.Fatal(err)
		}
		if obj["type"] != "response.completed" {
			continue
		}
		items := obj["response"].(map[string]any)["output"].([]any)
		if len(items) != 3 {
			t.Fatal(items)
		}
		first, middle, last := items[0].(map[string]any), items[1].(map[string]any), items[2].(map[string]any)
		if first["content"].([]any)[0].(map[string]any)["text"] != "before" || middle["type"] != "function_call" || last["content"].([]any)[0].(map[string]any)["text"] != "after" || first["id"] == last["id"] {
			t.Fatal(items)
		}
		return
	}
	t.Fatal("missing terminal response", output)
}

func TestMalformedToolArgumentsAreNotReplaced(t *testing.T) {
	for _, args := range []string{`{"record_id":9007199254740993`, `{} trailing`, `{} {}`, `[]`, `null`} {
		encoded, _ := json.Marshal(args)
		message := `{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"lookup","arguments":` + string(encoded) + `}}]}`
		response := []byte(`{"choices":[{"finish_reason":"length","message":` + message + `}]}`)
		if out, err := Response(ProtoMessages, ProtoChat, response); err == nil {
			t.Fatal(args, string(out))
		}
		request := []byte(`{"messages":[` + message + `]}`)
		if _, err := Request(ProtoChat, ProviderAnthropic, "model", request); err == nil {
			t.Fatal("accepted request", args)
		}
	}
}
