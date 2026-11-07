package metrics

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

func row(overrides func(*StepRecord)) StepRecord {
	in, out, cache, latency := 100, 10, 25, 5
	record := StepRecord{
		TaskID: "task-1", StepID: "step-1", JobClass: "parse", Tier: routing.Cheap,
		ModelID: "local-small", Reason: "policy tier for parse is cheap",
		InputTokens: &in, OutputTokens: &out, CacheHitTokens: &cache, LatencyMS: &latency,
		PrefixSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		UsageStatus:  StatusMeasured,
	}
	if overrides != nil {
		overrides(&record)
	}
	return record
}

func TestSummarySplitAndCacheRate(t *testing.T) {
	in2, out2, cache2, lat2 := 50, 20, 10, 7
	in3, out3, cache3, lat3 := 5, 1, 0, 3
	summary := Summarize([]StepRecord{
		row(nil),
		row(func(r *StepRecord) {
			r.StepID = "s2"
			r.JobClass = "code"
			r.Tier = routing.Frontier
			r.ModelID = "frontier"
			r.InputTokens = &in2
			r.OutputTokens = &out2
			r.CacheHitTokens = &cache2
			r.LatencyMS = &lat2
		}),
		row(func(r *StepRecord) {
			r.TaskID = "task-2"
			r.StepID = "s3"
			r.InputTokens = &in3
			r.OutputTokens = &out3
			r.CacheHitTokens = &cache3
			r.LatencyMS = &lat3
		}),
	})
	if summary.Schema != SchemaSummary || summary.NumbersPolicy != NumbersPolicy {
		t.Fatal(summary.Schema)
	}
	if summary.Accuracy.FullHistory != nil || summary.Accuracy.EndOfConversationCompaction != nil || summary.Accuracy.SingleActionCompaction != nil {
		t.Fatal("accuracy must stay null")
	}
	if len(summary.Tasks) != 2 || summary.Tasks[0].TaskID != "task-1" {
		t.Fatal(summary.Tasks)
	}
	first := summary.Tasks[0]
	if *first.InputTokens != 150 || *first.OutputTokens != 30 || *first.PeakContextTokens != 100 {
		t.Fatalf("tokens %+v", first.Block)
	}
	if first.ByTier[routing.Cheap].Steps != 1 || *first.ByTier[routing.Cheap].InputTokens != 100 {
		t.Fatal(first.ByTier)
	}
	if *first.ByTier[routing.Frontier].InputTokens != 50 || first.ByTier[routing.Frontier].Steps != 1 {
		t.Fatal(first.ByTier[routing.Frontier])
	}
	if math.Abs(*first.CacheHitRate-(35.0/150.0)) > 1e-9 {
		t.Fatal(*first.CacheHitRate)
	}
	if *first.PrefixReuseRate != 1 || *first.LatencyMS != 12 {
		t.Fatalf("reuse %v latency %v", first.PrefixReuseRate, first.LatencyMS)
	}
	if *summary.Tasks[1].CacheHitRate != 0 || *summary.Totals.InputTokens != 155 || *summary.Totals.ByTier[routing.Frontier].OutputTokens != 20 {
		t.Fatalf("%+v", summary.Totals)
	}
	if *summary.Totals.PeakContextTokens != 100 {
		t.Fatal(*summary.Totals.PeakContextTokens)
	}
}

func TestOmittedCacheAndUncertainUsage(t *testing.T) {
	omitted := Summarize([]StepRecord{
		row(func(r *StepRecord) { r.CacheHitTokens = nil }),
		row(func(r *StepRecord) { r.StepID = "s2"; five := 5; r.CacheHitTokens = &five }),
	})
	if omitted.Totals.CacheHitRate != nil || omitted.Totals.CacheHitTokens != nil || !omitted.Totals.TokensComplete {
		t.Fatalf("%+v", omitted.Totals.Block)
	}
	if *omitted.Totals.InputTokens != 200 {
		t.Fatal(*omitted.Totals.InputTokens)
	}
	uncertain := Summarize([]StepRecord{
		row(func(r *StepRecord) {
			in, out, cache := 80, 8, 8
			r.InputTokens, r.OutputTokens, r.CacheHitTokens = &in, &out, &cache
		}),
		row(func(r *StepRecord) {
			r.StepID = "s2"
			r.Tier = routing.Frontier
			r.JobClass = "review"
			r.ModelID = "frontier"
			r.UsageStatus = StatusUncertain
			r.InputTokens, r.OutputTokens, r.CacheHitTokens, r.CostProxy = nil, nil, nil, nil
			msg := "provider HTTP 500"
			r.Error = &msg
		}),
	})
	if uncertain.Totals.InputTokens != nil || uncertain.Totals.PeakContextTokens != nil || uncertain.Totals.CacheHitRate != nil || uncertain.Totals.CostProxy != nil {
		t.Fatal("uncertain totals must stay null")
	}
	if *uncertain.Totals.MeasuredInputTokens != 80 || uncertain.Totals.UncertainSteps != 1 {
		t.Fatal(uncertain.Totals)
	}
	if uncertain.Totals.ByTier[routing.Frontier].InputTokens != nil || uncertain.Totals.ByTier[routing.Frontier].Steps != 1 {
		t.Fatal(uncertain.Totals.ByTier[routing.Frontier])
	}
}

func TestCostProxyAndPrefixReuse(t *testing.T) {
	label := "proxy: tokens × published rate (card); not a charge"
	two, four := 2.0, 4.0
	priced := Summarize([]StepRecord{
		row(func(r *StepRecord) { r.CostProxy = &two; r.CostProxyLabel = &label }),
		row(func(r *StepRecord) {
			r.StepID = "s2"
			r.Tier = routing.Frontier
			r.JobClass = "plan"
			r.ModelID = "frontier"
			r.CostProxy = &four
			r.CostProxyLabel = &label
		}),
	})
	if *priced.Totals.CostProxy != 6 || *priced.Totals.CostPerStep != 3 || *priced.Totals.CostProxyLabel != label {
		t.Fatalf("%v %v %v", priced.Totals.CostProxy, priced.Totals.CostPerStep, priced.Totals.CostProxyLabel)
	}
	mixed := Summarize([]StepRecord{
		row(func(r *StepRecord) { r.CostProxy = &two; r.CostProxyLabel = &label }),
		row(func(r *StepRecord) { r.StepID = "s2" }),
	})
	if mixed.Totals.CostProxy != nil || mixed.Totals.CostPerStep != nil {
		t.Fatal("partial cost must stay null")
	}
	if Summarize([]StepRecord{row(nil)}).Totals.PrefixReuseRate != nil {
		t.Fatal("one step has no reuse rate")
	}
	other := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	split := Summarize([]StepRecord{row(nil), row(func(r *StepRecord) { r.StepID = "s2"; r.PrefixSHA256 = other })})
	if *split.Totals.PrefixReuseRate != 0.5 {
		t.Fatal(*split.Totals.PrefixReuseRate)
	}
}

func TestCacheOverInputIsRejectedPerStep(t *testing.T) {
	badIn, badCache := 10, 11
	okIn, okCache := 100, 0
	summary := Summarize([]StepRecord{
		row(func(r *StepRecord) {
			r.InputTokens = &badIn
			r.CacheHitTokens = &badCache
		}),
		row(func(r *StepRecord) {
			r.StepID = "s2"
			r.Tier = routing.Frontier
			r.JobClass = "code"
			r.ModelID = "frontier"
			r.InputTokens = &okIn
			r.CacheHitTokens = &okCache
		}),
	})
	if summary.Totals.CacheReportComplete || summary.Totals.CacheHitRate != nil || summary.Totals.CacheHitTokens != nil || summary.Totals.MeasuredCacheHitTokens != nil {
		t.Fatalf("totals %+v", summary.Totals.Block)
	}
	if *summary.Totals.InputTokens != 110 {
		t.Fatal(*summary.Totals.InputTokens)
	}
	cheap := summary.Totals.ByTier[routing.Cheap]
	if cheap.CacheReportComplete || cheap.CacheHitRate != nil {
		t.Fatalf("cheap %+v", cheap)
	}
	frontier := summary.Totals.ByTier[routing.Frontier]
	if !frontier.CacheReportComplete || frontier.CacheHitRate == nil || *frontier.CacheHitRate != 0 {
		t.Fatalf("frontier %+v", frontier)
	}
}

func TestEmptyJournalAndRoundTrip(t *testing.T) {
	empty := Summarize(nil)
	if len(empty.Tasks) != 0 || empty.Totals.Steps != 0 || empty.Totals.InputTokens != nil || empty.Totals.ByTier[routing.Frontier].InputTokens != nil {
		t.Fatalf("%+v", empty.Totals)
	}
	dir := t.TempDir()
	journal, err := OpenJournal(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	original := row(nil)
	if err := journal.Append(original); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadJournal(journal.path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].TaskID != original.TaskID || *loaded[0].InputTokens != *original.InputTokens || loaded[0].CacheHitTokens == nil || *loaded[0].CacheHitTokens != 25 {
		t.Fatalf("%+v", loaded[0])
	}
	raw, _ := json.Marshal(original.Wire())
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	accuracy := payload["accuracy"].(map[string]any)
	if accuracy["context_mode"] != nil {
		t.Fatal(accuracy)
	}
	bad := filepath.Join(dir, "bad.jsonl")
	if err := writeFile(bad, "{}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournal(bad); err == nil {
		t.Fatal("expected schema failure")
	}
}

func TestRecordRules(t *testing.T) {
	bad := row(func(r *StepRecord) { r.InputTokens = nil; r.OutputTokens = nil })
	if err := bad.Validate(); err == nil {
		t.Fatal("measured null tokens")
	}
	zero := 0
	uncertain := row(func(r *StepRecord) {
		r.UsageStatus = StatusUncertain
		r.InputTokens, r.OutputTokens, r.CacheHitTokens = &zero, &zero, &zero
	})
	if err := uncertain.Validate(); err == nil {
		t.Fatal("uncertain zeros")
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestJournalRotationBoundsDiskAndPreservesRecentRows(t *testing.T) {
	old := maxJournalBytes
	maxJournalBytes = 1500
	t.Cleanup(func() { maxJournalBytes = old })
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	for i := 0; i < 20; i++ {
		j, err := OpenJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Append(row(nil)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1"} {
		info, err := os.Stat(p)
		if err != nil || info.Size() > maxJournalBytes {
			t.Fatal(err, info)
		}
	}
	rows, err := ReadJournal(path)
	if err != nil || len(rows) == 0 || len(rows) >= 20 {
		t.Fatal(err, len(rows))
	}
}
