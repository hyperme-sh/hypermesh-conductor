package rules

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		{TaskID: "task-1", StepID: "s1", Kind: "test_failed"},
		{TaskID: "task-1", StepID: "s2", Kind: "escalation"},
		{TaskID: "task-1", StepID: "s3", Kind: "aiuditor_deny"},
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

func TestSaveActiveDoesNotExposeAPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.json")
	if err := SaveActive(path, Defaults()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var got error
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 80; i++ {
			if _, err := Load(path); err != nil {
				mu.Lock()
				got = err
				mu.Unlock()
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		file := Defaults()
		file.Routing.ClassTiers = map[string]string{"parse": "frontier"}
		for i := 0; i < 80; i++ {
			file.Version = i + 1
			if err := SaveActive(path, file); err != nil {
				mu.Lock()
				got = err
				mu.Unlock()
				return
			}
		}
	}()
	wg.Wait()
	if got != nil {
		t.Fatal(got)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Routing.ClassTiers["parse"] != "frontier" {
		t.Fatal(err, loaded.Routing.ClassTiers)
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
	policy.Routing.EscalateWhenPeakTokensAbove["parse"] = 50
	alias := EffectiveClassTiers(nil, policy, "PARSE", 80)
	if alias["parse"] != "frontier" || alias["PARSE"] == "frontier" {
		t.Fatal(alias)
	}
	parsing := EffectiveClassTiers(nil, policy, "parsing", 80)
	if parsing["parse"] != "frontier" {
		t.Fatal(parsing)
	}
}

func TestDreamDoesNotShareOutcomesAcrossTasks(t *testing.T) {
	in := 10
	first := step("s1", "summarize", "cheap", &in)
	second := step("s1", "summarize", "cheap", &in)
	second.TaskID = "task-b"
	proposal, _, err := Dream(DreamInput{
		Records:  []metrics.StepRecord{first, second},
		Outcomes: []Outcome{{TaskID: "task-1", StepID: "s1", Kind: "test_failed"}},
	}, Defaults(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Routing.EscalateWhenPeakTokensAbove["summarize"] != 0 {
		t.Fatal(proposal.Routing.EscalateWhenPeakTokensAbove)
	}
}

func TestVersionZeroArchiveSurvivesASecondApply(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.json")
	if err := SaveActive(active, Defaults()); err != nil {
		t.Fatal(err)
	}
	write := func(name string, version, limit int) string {
		proposal := Defaults()
		proposal.Version = version
		proposal.Routing.EscalateWhenPeakTokensAbove = map[string]int{"summarize": limit}
		raw, err := Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := Apply(active, write("one.json", 1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := Apply(active, write("two.json", 2, 20)); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(active); err != nil {
		t.Fatal(err)
	}
	restored, err := Load(active)
	if err != nil || restored.Version != 1 || restored.Routing.EscalateWhenPeakTokensAbove["summarize"] != 10 {
		t.Fatal(err, restored.Version, restored.Routing.EscalateWhenPeakTokensAbove)
	}
	if err := Rollback(active); err != nil {
		t.Fatal(err)
	}
	baseline, err := Load(active)
	if err != nil || baseline.Version != 0 {
		t.Fatal(err, baseline.Version)
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
