package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/history"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
)

func TestDreamProposesWithoutActivatingOrReadingBodies(t *testing.T) {
	dir := t.TempDir()
	in := 40
	records := []metrics.StepRecord{
		step("s1", "summarize", "cheap", &in),
		step("s2", "summarize", "cheap", &in),
		step("s3", "summarize", "cheap", &in),
	}
	outcomes := []Outcome{
		{StepID: "s1", Kind: "test_failed"},
		{StepID: "s2", Kind: "escalation"},
		{StepID: "s3", Kind: "aiuditor_deny"},
	}
	hist := []history.Record{{StepID: "s1", Summary: "PRIVATE-PROMPT-BODY"}}
	proposal, path, err := Dream(DreamInput{Records: records, Outcomes: outcomes, History: hist, Bodies: false}, Defaults(), filepath.Join(dir, "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "active.json")); !os.IsNotExist(err) {
		t.Fatal("dream activated a policy")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "PRIVATE-PROMPT-BODY") || strings.Contains(string(blob), "fail_closed") {
		t.Fatal(string(blob))
	}
	if proposal.Routing.EscalateWhenPeakTokensAbove["summarize"] != 40 {
		t.Fatal(proposal.Routing.EscalateWhenPeakTokensAbove)
	}
	if len(proposal.Observations) != 1 || !strings.Contains(proposal.Observations[0], "aiuditor denied") {
		t.Fatal(proposal.Observations)
	}
	withBodies, _, err := Dream(DreamInput{Records: records, Outcomes: outcomes, History: hist, Bodies: true}, Defaults(), filepath.Join(dir, "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range withBodies.Evidence {
		if strings.Contains(item.Excerpt, "PRIVATE-PROMPT-BODY") {
			found = true
		}
	}
	if !found {
		t.Fatal("opt-in bodies did not attach the history excerpt")
	}
}

func TestApplyRejectsAIULooseningAndRollbackRestores(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.json")
	if err := SaveActive(active, Defaults()); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema":"hypermesh.conductor.rules.v0","aiuditor":{"fail_closed":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(active, bad); err == nil || !strings.Contains(err.Error(), "AIuditor") {
		t.Fatal(err)
	}
	current, err := Load(active)
	if err != nil || current.Version != 0 {
		t.Fatal(err, current)
	}
	good := filepath.Join(dir, "good.json")
	proposal := Defaults()
	proposal.Version = 1
	proposal.Routing.EscalateWhenPeakTokensAbove = map[string]int{"summarize": 100}
	raw, err := Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(active, good); err != nil {
		t.Fatal(err)
	}
	applied, err := Load(active)
	if err != nil || applied.Routing.EscalateWhenPeakTokensAbove["summarize"] != 100 {
		t.Fatal(err, applied)
	}
	diff := Diff(Defaults(), applied)
	if !strings.Contains(diff, "routing.escalate_when_peak_tokens_above.summarize") {
		t.Fatal(diff)
	}
	if err := Rollback(active); err != nil {
		t.Fatal(err)
	}
	restored, err := Load(active)
	if err != nil || restored.Routing.EscalateWhenPeakTokensAbove["summarize"] != 0 {
		t.Fatal(err, restored.Routing)
	}
}

func TestSuggestCannotUpgrade(t *testing.T) {
	policy := Defaults()
	policy.CachePrefix.PreferOnMatch = "cheap"
	if Suggest(policy, true) != "cheap" {
		t.Fatal("expected a cheap hint")
	}
	tiers := EffectiveClassTiers(nil, Defaults(), "summarize", 100000)
	if tiers["summarize"] != "" {
		t.Fatal("size alone must not escalate")
	}
	policy.Routing.EscalateWhenPeakTokensAbove = map[string]int{"summarize": 50}
	tiers = EffectiveClassTiers(nil, policy, "summarize", 80)
	if tiers["summarize"] != "frontier" {
		t.Fatal(tiers)
	}
}

func step(id, class, tier string, input *int) metrics.StepRecord {
	out := 1
	return metrics.StepRecord{
		TaskID: "task-1", StepID: id, JobClass: class, Tier: tier, ModelID: "m",
		Reason: "policy", InputTokens: input, OutputTokens: &out, PrefixSHA256: "abc",
		UsageStatus: metrics.StatusMeasured,
	}
}
