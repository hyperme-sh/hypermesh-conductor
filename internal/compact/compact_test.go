package compact

import (
	"strings"
	"testing"
)

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
