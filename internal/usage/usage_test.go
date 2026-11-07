package usage

import "testing"

func TestOpenAICachePrefersCachedTokens(t *testing.T) {
	parsed := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":20,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":16},"prompt_cache_hit_tokens":1}}`), false)
	if parsed.Status != Measured || *parsed.Input != 20 || *parsed.Output != 5 || *parsed.Cache != 16 {
		t.Fatalf("%+v", parsed)
	}
}

func TestCacheFallbackAndZero(t *testing.T) {
	fallback := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":4}}`), false)
	if fallback.Cache == nil || *fallback.Cache != 4 {
		t.Fatal(fallback)
	}
	zero := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":0}}}`), false)
	if zero.Cache == nil || *zero.Cache != 0 {
		t.Fatal(zero)
	}
}

func TestMissingAndPartialStayNull(t *testing.T) {
	missing := ParseOpenAI([]byte(`{"choices":[{"message":{"content":"ok"}}]}`), false)
	if missing.Status != Uncertain || missing.Input != nil || missing.Cache != nil {
		t.Fatal(missing)
	}
	partial := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":12}}`), false)
	if partial.Status != Uncertain || partial.Input != nil {
		t.Fatal(partial)
	}
	bad := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":"12","completion_tokens":1}}`), false)
	if bad.Status != Uncertain {
		t.Fatal(bad)
	}
	negative := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`), false)
	if negative.Status != Uncertain {
		t.Fatal(negative)
	}
}

func TestResponsesUsageDetailsCachedTokens(t *testing.T) {
	parsed := ParseOpenAI([]byte(`{"usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":80}}}`), false)
	if parsed.Status != Measured || *parsed.Input != 100 || *parsed.Output != 5 || parsed.Cache == nil || *parsed.Cache != 80 {
		t.Fatalf("%+v", parsed)
	}
	acc := UncertainUsage()
	Absorb(&acc, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":80}}}}`), false)
	if acc.Status != Measured || *acc.Input != 100 || *acc.Output != 5 || acc.Cache == nil || *acc.Cache != 80 {
		t.Fatalf("%+v", acc)
	}
}

func TestEmbeddingsOmitCompletion(t *testing.T) {
	parsed := ParseOpenAI([]byte(`{"usage":{"prompt_tokens":8,"total_tokens":8}}`), true)
	if parsed.Status != Measured || *parsed.Input != 8 || *parsed.Output != 0 || parsed.Cache != nil {
		t.Fatalf("%+v", parsed)
	}
}

func TestAnthropicUsageAndStreamFold(t *testing.T) {
	parsed := ParseAnthropic([]byte(`{"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":1}}`))
	if parsed.Status != Measured || *parsed.Input != 4 || *parsed.Output != 2 || *parsed.Cache != 1 {
		t.Fatalf("%+v", parsed)
	}
	cached := ParseAnthropic([]byte(`{"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":90,"cache_creation_input_tokens":20}}`))
	if cached.Status != Measured || *cached.Input != 120 || *cached.Output != 2 || *cached.Cache != 90 {
		t.Fatalf("%+v", cached)
	}
	acc := UncertainUsage()
	Absorb(&acc, []byte(`{"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":1}}}`), false)
	Absorb(&acc, []byte(`{"type":"message_delta","usage":{"output_tokens":5}}`), false)
	if acc.Status != Measured || *acc.Input != 3 || *acc.Output != 5 {
		t.Fatalf("%+v", acc)
	}
	folded := UncertainUsage()
	Absorb(&folded, []byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":90,"cache_creation_input_tokens":20}}}`), false)
	Absorb(&folded, []byte(`{"type":"message_delta","usage":{"output_tokens":2}}`), false)
	if folded.Status != Measured || *folded.Input != 120 || *folded.Output != 2 || *folded.Cache != 90 {
		t.Fatalf("%+v", folded)
	}
}

func TestCacheOnlyInputDoesNotBecomeMeasuredUsage(t *testing.T) {
	for _, body := range []string{`{"usage":{"cache_read_input_tokens":80,"output_tokens":5}}`, `{"usage":{"cache_creation_input_tokens":80,"output_tokens":5}}`} {
		parsed := ParseOpenAI([]byte(body), false)
		if parsed.Status != Uncertain || parsed.Input != nil {
			t.Fatalf("%+v", parsed)
		}
	}
}
