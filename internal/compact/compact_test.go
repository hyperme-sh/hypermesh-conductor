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
