package compact

import (
	"strings"
	"testing"
)

func TestSummaryKeepsLargeToolIntegers(t *testing.T) {
	tool := `{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"record_id":9007199254740993}}]}`
	raw := []byte(`[` + tool + `,{"role":"user","content":"` + strings.Repeat("word ", 80) + `"}]`)
	plan, err := PlanMessages(raw, 10, 0)
	if err != nil || !plan.Needed {
		t.Fatal(err, plan.Needed)
	}
	if !strings.Contains(plan.SummaryInput, "9007199254740993") || strings.Contains(plan.SummaryInput, "9007199254740992") {
		t.Fatal(plan.SummaryInput)
	}
}

func TestLeadingDeveloperMessageStaysInThePrefix(t *testing.T) {
	dev := `{"role":"developer","content":"Follow these rules"}`
	raw := []byte(`[` + dev + `,{"role":"user","content":"` + strings.Repeat("word ", 80) + `"}]`)
	plan, err := PlanMessages(raw, 10, 0)
	if err != nil || !plan.Needed {
		t.Fatal(err, plan.Needed)
	}
	if len(plan.PrefixRaw) != 1 || !strings.Contains(string(plan.PrefixRaw[0]), "Follow these rules") || strings.Contains(plan.SummaryInput, "Follow these rules") {
		t.Fatal(string(plan.PrefixRaw[0]), plan.SummaryInput)
	}
	out, err := ApplyMessages(plan, "goals and facts")
	if err != nil || !strings.Contains(string(out), `"role":"developer"`) || !strings.Contains(string(out), "Follow these rules") {
		t.Fatal(err, string(out))
	}
}

func TestImageContentIsNotCompactedAway(t *testing.T) {
	image := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"text","text":"diagram"}]}`
	raw := []byte(`[` + image + `,{"role":"user","content":"` + strings.Repeat("word ", 80) + `"}]`)
	plan, err := PlanMessages(raw, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Needed || plan.SummaryInput != "" {
		t.Fatal(plan.Needed, plan.SummaryInput)
	}
	anthropic := `{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}`
	raw = []byte(`[` + anthropic + `,{"role":"user","content":"` + strings.Repeat("word ", 80) + `"}]`)
	plan, err = PlanMessages(raw, 10, 0)
	if err != nil || plan.Needed {
		t.Fatal(err, plan.Needed)
	}
	kept := `[{"role":"user","content":"` + strings.Repeat("word ", 80) + `"},` + image + `]`
	plan, err = PlanMessages([]byte(kept), 10, 1)
	if err != nil || !plan.Needed {
		t.Fatal(err, plan.Needed)
	}
	out, err := ApplyMessages(plan, "goals")
	if err != nil || !strings.Contains(string(out), "image_url") || strings.Contains(plan.SummaryInput, "AAAA") {
		t.Fatal(err, plan.SummaryInput, string(out))
	}
}

func TestPrefixMessagesStayByteStable(t *testing.T) {
	system := `{"role":"system","name":"keep","content":"Stay stable."}`
	raw := []byte(`[` + system + `,{"role":"user","content":"` + strings.Repeat("word ", 80) + `"}]`)
	plan, err := PlanMessages(raw, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Needed {
		t.Fatal("expected compaction")
	}
	out, err := ApplyMessages(plan, "goals and facts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), system) {
		t.Fatal(string(out))
	}
	if strings.Contains(string(out), "word word") {
		t.Fatal("tail leaked into the compacted messages")
	}
	if !strings.Contains(string(out), "goals and facts") {
		t.Fatal(string(out))
	}
}

func TestToolExchangeIsNotSplit(t *testing.T) {
	long := strings.Repeat("word ", 80)
	raw := []byte(`[{"role":"user","content":"` + long + `"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"},{"role":"user","content":"thanks"}]`)
	plan, err := PlanMessages(raw, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Needed {
		t.Fatal("expected compaction")
	}
	recent := string(joinArray(plan.RecentRaw))
	older := string(joinArray(plan.TailRaw[:len(plan.TailRaw)-len(plan.RecentRaw)]))
	if !strings.Contains(recent, `"tool_call_id":"call_1"`) || !strings.Contains(recent, `"id":"call_1"`) {
		t.Fatal(recent)
	}
	if strings.Contains(older, "call_1") {
		t.Fatal(older)
	}
}

func TestAnthropicToolResultReachesTheSummary(t *testing.T) {
	raw := []byte(`[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"balance is 930"}]}]`)
	plan, err := PlanMessages(raw, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Needed || !strings.Contains(plan.SummaryInput, "balance is 930") || !strings.Contains(plan.SummaryInput, "lookup") {
		t.Fatal(plan.Needed, plan.SummaryInput)
	}
}

func TestRecentMessagesAreNotSummarizedAway(t *testing.T) {
	raw := []byte(`[{"role":"user","content":"` + strings.Repeat("word ", 80) + `"}]`)
	plan, err := PlanMessages(raw, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Needed {
		t.Fatal("a single retained message must stay")
	}
}

func TestShortTailIsLeftAlone(t *testing.T) {
	raw := []byte(`[{"role":"user","content":"hi"}]`)
	plan, err := PlanMessages(raw, 1000, 2)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Needed {
		t.Fatal("short tail should stay")
	}
}
func TestUnsupportedOlderContentPreventsCompaction(t *testing.T) {
	for _, block := range []string{
		`{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}`,
		`{"type":"image","source":{"type":"base64","data":"image"}}`,
		`{"type":"tool_result","tool_use_id":"tool","content":[{"type":"image","source":{}}]}`,
		`{"type":"unknown","text":"caption"}`,
	} {
		raw := []byte(`[{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("long ", 80) + `"},` + block + `]},{"role":"user","content":"What was in the image?"}]`)
		plan, err := PlanMessages(raw, 10, 1)
		if err != nil || plan.Needed {
			t.Fatal(err, plan.Needed, block)
		}
	}
}

func TestUnsupportedRecentContentIsRetained(t *testing.T) {
	image := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}`
	raw := []byte(`[{"role":"user","content":"` + strings.Repeat("long ", 80) + `"},` + image + `]`)
	plan, err := PlanMessages(raw, 10, 1)
	if err != nil || !plan.Needed {
		t.Fatal(err, plan.Needed)
	}
	result, err := ApplyMessages(plan, "summary")
	if err != nil || !strings.Contains(string(result), image) {
		t.Fatal(err, string(result))
	}
}

func TestTopLevelAudioAndOpaqueContentPreventCompaction(t *testing.T) {
	for _, message := range []string{
		`{"role":"assistant","content":"spoken words","audio":{"id":"audio-1"}}`,
		`{"role":"user","content":{"image":"opaque"}}`,
	} {
		raw := []byte(`[` + message + `,{"role":"user","content":"` + strings.Repeat("question ", 80) + `"}]`)
		plan, err := PlanMessages(raw, 10, 1)
		if err != nil || plan.Needed {
			t.Fatal(err, plan.Needed)
		}
	}
}
