// Package metrics records per-step usage and summarizes a journal.
//
// Unmeasured fields stay null. Uncertain usage is never stored as zero.
// Accuracy under compaction modes stays null; those scores belong to
// hypermesh-bench issue 12.
package metrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

const (
	SchemaStep      = "hypermesh.conductor.step.v0"
	SchemaSummary   = "hypermesh.conductor.efficiency.v0"
	NumbersPolicy   = "Unmeasured fields stay null. Lost or uncertain usage stays uncertain and is never recorded as zero. A cost figure is a labeled proxy from published rates, or null. This record is not a savings measurement. Compaction-mode accuracy stays null and belongs to hypermesh-bench issue 12."
	StatusMeasured  = "measured"
	StatusUncertain = "uncertain"
)

// Accuracy is the bench issue 12 block. Values stay null in this repository.
type Accuracy struct {
	ContextMode                 *string `json:"context_mode"`
	FullHistory                 *string `json:"full_history"`
	EndOfConversationCompaction *string `json:"end_of_conversation_compaction"`
	SingleActionCompaction      *string `json:"single_action_compaction"`
}

// StepRecord is one journal row. Token fields are nil when unmeasured.
type StepRecord struct {
	TaskID             string   `json:"task_id"`
	StepID             string   `json:"step_id"`
	JobClass           string   `json:"job_class"`
	Tier               string   `json:"tier"`
	ModelID            string   `json:"model_id"`
	Reason             string   `json:"reason"`
	SuggestionTier     *string  `json:"suggestion_tier"`
	SuggestionAccepted *bool    `json:"suggestion_accepted"`
	InputTokens        *int     `json:"input_tokens"`
	OutputTokens       *int     `json:"output_tokens"`
	CacheHitTokens     *int     `json:"cache_hit_tokens"`
	LatencyMS          *int     `json:"latency_ms"`
	PrefixSHA256       string   `json:"prefix_sha256"`
	UsageStatus        string   `json:"usage_status"`
	Error              *string  `json:"error"`
	CostProxy          *float64 `json:"cost_proxy"`
	CostProxyLabel     *string  `json:"cost_proxy_label"`
}

// Validate checks the row before it is written or summarized.
func (r StepRecord) Validate() error {
	if r.Tier != routing.Cheap && r.Tier != routing.Frontier {
		return fmt.Errorf("unknown tier '%s'", r.Tier)
	}
	if r.UsageStatus != StatusMeasured && r.UsageStatus != StatusUncertain {
		return fmt.Errorf("unknown usage_status '%s'", r.UsageStatus)
	}
	if r.UsageStatus == StatusMeasured {
		if r.InputTokens == nil || r.OutputTokens == nil {
			return fmt.Errorf("measured usage needs input and output tokens")
		}
	} else if r.InputTokens != nil || r.OutputTokens != nil || r.CacheHitTokens != nil || r.CostProxy != nil {
		return fmt.Errorf("uncertain usage cannot carry token counts or a cost proxy")
	}
	if (r.SuggestionTier == nil) != (r.SuggestionAccepted == nil) {
		return fmt.Errorf("mismatched suggestion")
	}
	for _, field := range []struct {
		name, value string
	}{
		{"task_id", r.TaskID},
		{"step_id", r.StepID},
		{"job_class", r.JobClass},
		{"model_id", r.ModelID},
		{"prefix_sha256", r.PrefixSHA256},
	} {
		if field.value == "" {
			return fmt.Errorf("empty %s", field.name)
		}
	}
	return nil
}

type wireRecord struct {
	Schema             string    `json:"schema"`
	TaskID             string    `json:"task_id"`
	StepID             string    `json:"step_id"`
	JobClass           string    `json:"job_class"`
	Tier               string    `json:"tier"`
	ModelID            string    `json:"model_id"`
	Reason             string    `json:"reason"`
	SuggestionTier     *string   `json:"suggestion_tier"`
	SuggestionAccepted *bool     `json:"suggestion_accepted"`
	InputTokens        *int      `json:"input_tokens"`
	OutputTokens       *int      `json:"output_tokens"`
	CacheHitTokens     *int      `json:"cache_hit_tokens"`
	LatencyMS          *int      `json:"latency_ms"`
	PrefixSHA256       string    `json:"prefix_sha256"`
	UsageStatus        string    `json:"usage_status"`
	Error              *string   `json:"error"`
	CostProxy          *float64  `json:"cost_proxy"`
	CostProxyLabel     *string   `json:"cost_proxy_label"`
	PeakContextTokens  *int      `json:"peak_context_tokens"`
	CostPerStep        *float64  `json:"cost_per_step"`
	Accuracy           *Accuracy `json:"accuracy"`
}

// Wire converts a row to the journal object, including null accuracy fields.
func (r StepRecord) Wire() wireRecord {
	var peak *int
	if r.UsageStatus == StatusMeasured {
		peak = r.InputTokens
	}
	return wireRecord{
		Schema:             SchemaStep,
		TaskID:             r.TaskID,
		StepID:             r.StepID,
		JobClass:           r.JobClass,
		Tier:               r.Tier,
		ModelID:            r.ModelID,
		Reason:             r.Reason,
		SuggestionTier:     r.SuggestionTier,
		SuggestionAccepted: r.SuggestionAccepted,
		InputTokens:        r.InputTokens,
		OutputTokens:       r.OutputTokens,
		CacheHitTokens:     r.CacheHitTokens,
		LatencyMS:          r.LatencyMS,
		PrefixSHA256:       r.PrefixSHA256,
		UsageStatus:        r.UsageStatus,
		Error:              r.Error,
		CostProxy:          r.CostProxy,
		CostProxyLabel:     r.CostProxyLabel,
		PeakContextTokens:  peak,
		CostPerStep:        r.CostProxy,
		Accuracy:           &Accuracy{},
	}
}

// Journal is an append-only measurement log.
type Journal struct {
	path string
	mu   sync.Mutex
}

// Path is the journal file.
func (j *Journal) Path() string { return j.path }

func OpenJournal(path string) (*Journal, error) {
	info, err := os.Stat(parentDir(path))
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("journal directory does not exist: %s", parentDir(path))
	}
	return &Journal{path: path}, nil
}

func parentDir(path string) string { return filepath.Dir(path) }

// Append writes one row. The row does not contain prompts or keys.
func (j *Journal) Append(record StepRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(record.Wire())
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	file, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// ReadJournal loads every row. A bad line fails the read.
func ReadJournal(path string) ([]StepRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("journal not found: %s", path)
		}
		return nil, err
	}
	defer file.Close()
	var records []StepRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytesTrim(line)) == 0 {
			continue
		}
		record, err := parseLine(line, lineNo)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if records == nil {
		records = []StepRecord{}
	}
	return records, nil
}

func parseLine(line []byte, lineNo int) (StepRecord, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return StepRecord{}, fmt.Errorf("journal line %d is not JSON", lineNo)
	}
	schema := rawString(raw["schema"])
	if schema != SchemaStep {
		return StepRecord{}, fmt.Errorf("journal line %d has an unknown schema", lineNo)
	}
	required := []string{
		"schema", "task_id", "step_id", "job_class", "tier", "model_id", "reason",
		"suggestion_tier", "suggestion_accepted", "input_tokens", "output_tokens",
		"cache_hit_tokens", "latency_ms", "prefix_sha256", "usage_status", "error",
		"cost_proxy", "cost_proxy_label", "peak_context_tokens",
	}
	var missing []string
	for _, field := range required {
		if _, ok := raw[field]; !ok {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return StepRecord{}, fmt.Errorf("journal line %d is missing %s", lineNo, joinComma(missing))
	}
	var wire wireRecord
	if err := json.Unmarshal(line, &wire); err != nil {
		return StepRecord{}, fmt.Errorf("journal line %d has a bad field", lineNo)
	}
	record := StepRecord{
		TaskID: wire.TaskID, StepID: wire.StepID, JobClass: wire.JobClass, Tier: wire.Tier,
		ModelID: wire.ModelID, Reason: wire.Reason, SuggestionTier: wire.SuggestionTier,
		SuggestionAccepted: wire.SuggestionAccepted, InputTokens: wire.InputTokens,
		OutputTokens: wire.OutputTokens, CacheHitTokens: wire.CacheHitTokens,
		LatencyMS: wire.LatencyMS, PrefixSHA256: wire.PrefixSHA256, UsageStatus: wire.UsageStatus,
		Error: wire.Error, CostProxy: wire.CostProxy, CostProxyLabel: wire.CostProxyLabel,
	}
	if record.Tier != routing.Cheap && record.Tier != routing.Frontier {
		return StepRecord{}, fmt.Errorf("journal line %d has an unknown tier", lineNo)
	}
	if record.SuggestionTier != nil && *record.SuggestionTier != routing.Cheap && *record.SuggestionTier != routing.Frontier {
		return StepRecord{}, fmt.Errorf("journal line %d has an unknown suggestion tier", lineNo)
	}
	if (record.SuggestionTier == nil) != (record.SuggestionAccepted == nil) {
		return StepRecord{}, fmt.Errorf("journal line %d has a mismatched suggestion", lineNo)
	}
	if record.UsageStatus != StatusMeasured && record.UsageStatus != StatusUncertain {
		return StepRecord{}, fmt.Errorf("journal line %d has an unknown usage_status", lineNo)
	}
	if record.UsageStatus == StatusMeasured && (record.InputTokens == nil || record.OutputTokens == nil) {
		return StepRecord{}, fmt.Errorf("journal line %d is measured but tokens are null", lineNo)
	}
	if record.UsageStatus == StatusUncertain && (record.InputTokens != nil || record.OutputTokens != nil || record.CacheHitTokens != nil) {
		return StepRecord{}, fmt.Errorf("journal line %d is uncertain but contains token counts", lineNo)
	}
	if record.UsageStatus == StatusUncertain && record.CostProxy != nil {
		return StepRecord{}, fmt.Errorf("journal line %d is uncertain but has a cost proxy", lineNo)
	}
	if record.LatencyMS != nil && *record.LatencyMS < 0 {
		return StepRecord{}, fmt.Errorf("journal line %d has a bad latency_ms", lineNo)
	}
	for _, field := range []struct{ name, value string }{
		{"task_id", record.TaskID}, {"step_id", record.StepID}, {"job_class", record.JobClass},
		{"model_id", record.ModelID}, {"prefix_sha256", record.PrefixSHA256},
	} {
		if field.value == "" {
			return StepRecord{}, fmt.Errorf("journal line %d has an empty %s", lineNo, field.name)
		}
	}
	for _, count := range []*int{record.InputTokens, record.OutputTokens, record.CacheHitTokens} {
		if count != nil && *count < 0 {
			return StepRecord{}, fmt.Errorf("journal line %d has a bad token count", lineNo)
		}
	}
	return record, nil
}

func rawString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func joinComma(parts []string) string {
	out := parts[0]
	for _, part := range parts[1:] {
		out += ", " + part
	}
	return out
}

func bytesTrim(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}

// Block is one aggregation, either a task, a tier, or the whole journal.
type Block struct {
	Steps                  int      `json:"steps"`
	UncertainSteps         int      `json:"uncertain_steps"`
	InputTokens            *int     `json:"input_tokens"`
	OutputTokens           *int     `json:"output_tokens"`
	TokensComplete         bool     `json:"tokens_complete"`
	MeasuredInputTokens    *int     `json:"measured_input_tokens"`
	MeasuredOutputTokens   *int     `json:"measured_output_tokens"`
	PeakContextTokens      *int     `json:"peak_context_tokens"`
	CacheHitTokens         *int     `json:"cache_hit_tokens"`
	CacheHitRate           *float64 `json:"cache_hit_rate"`
	CacheReportComplete    bool     `json:"cache_report_complete"`
	MeasuredCacheHitTokens *int     `json:"measured_cache_hit_tokens"`
	PrefixReuseRate        *float64 `json:"prefix_reuse_rate"`
	LatencyMS              *int     `json:"latency_ms"`
	MeasuredLatencyMS      *int     `json:"measured_latency_ms"`
	CostProxy              *float64 `json:"cost_proxy"`
	CostProxyLabel         *string  `json:"cost_proxy_label"`
	CostPerStep            *float64 `json:"cost_per_step"`
}

// Totals is a block plus the cheap versus frontier split.
type Totals struct {
	Block
	ByTier map[string]Block `json:"by_tier"`
}

// TaskSummary is one task's totals.
type TaskSummary struct {
	TaskID string `json:"task_id"`
	Totals
}

// Summary is the efficiency scorecard.
type Summary struct {
	Schema        string        `json:"schema"`
	NumbersPolicy string        `json:"numbers_policy"`
	Tasks         []TaskSummary `json:"tasks"`
	Totals        Totals        `json:"totals"`
	Accuracy      Accuracy      `json:"accuracy"`
}

// Summarize aggregates records in first-seen task order.
func Summarize(records []StepRecord) Summary {
	order := []string{}
	grouped := map[string][]StepRecord{}
	for _, row := range records {
		if _, ok := grouped[row.TaskID]; !ok {
			order = append(order, row.TaskID)
		}
		grouped[row.TaskID] = append(grouped[row.TaskID], row)
	}
	tasks := make([]TaskSummary, 0, len(order))
	for _, id := range order {
		tasks = append(tasks, TaskSummary{TaskID: id, Totals: totals(grouped[id])})
	}
	return Summary{
		Schema:        SchemaSummary,
		NumbersPolicy: NumbersPolicy,
		Tasks:         tasks,
		Totals:        totals(records),
		Accuracy:      Accuracy{},
	}
}

func totals(rows []StepRecord) Totals {
	return Totals{
		Block: measure(rows),
		ByTier: map[string]Block{
			routing.Cheap:    measure(filterTier(rows, routing.Cheap)),
			routing.Frontier: measure(filterTier(rows, routing.Frontier)),
		},
	}
}

func filterTier(rows []StepRecord, tier string) []StepRecord {
	out := []StepRecord{}
	for _, row := range rows {
		if row.Tier == tier {
			out = append(out, row)
		}
	}
	return out
}

func measure(rows []StepRecord) Block {
	if len(rows) == 0 {
		return Block{}
	}
	uncertain := 0
	var measured []StepRecord
	for _, row := range rows {
		if row.UsageStatus != StatusMeasured {
			uncertain++
			continue
		}
		measured = append(measured, row)
	}
	tokensComplete := uncertain == 0
	measuredInput := 0
	measuredOutput := 0
	for _, row := range measured {
		measuredInput += *row.InputTokens
		measuredOutput += *row.OutputTokens
	}
	block := Block{
		Steps:                len(rows),
		UncertainSteps:       uncertain,
		TokensComplete:       tokensComplete,
		MeasuredInputTokens:  intPtr(measuredInput),
		MeasuredOutputTokens: intPtr(measuredOutput),
	}
	if tokensComplete {
		block.InputTokens = intPtr(measuredInput)
		block.OutputTokens = intPtr(measuredOutput)
		peak := 0
		seen := false
		for _, row := range measured {
			if !seen || *row.InputTokens > peak {
				peak = *row.InputTokens
				seen = true
			}
		}
		if seen {
			block.PeakContextTokens = intPtr(peak)
		}
	}
	var eligible, reported []StepRecord
	for _, row := range measured {
		if row.InputTokens != nil && *row.InputTokens > 0 {
			eligible = append(eligible, row)
			if row.CacheHitTokens != nil {
				reported = append(reported, row)
			}
		}
	}
	cacheComplete := uncertain == 0 && len(eligible) > 0 && len(reported) == len(eligible)
	cacheTokens := 0
	cacheInputs := 0
	over := false
	for _, row := range measured {
		if row.CacheHitTokens != nil && row.InputTokens != nil && *row.CacheHitTokens > *row.InputTokens {
			over = true
		}
	}
	for _, row := range reported {
		cacheTokens += *row.CacheHitTokens
		cacheInputs += *row.InputTokens
	}
	block.CacheReportComplete = cacheComplete && !over
	if len(reported) > 0 && !over {
		block.MeasuredCacheHitTokens = intPtr(cacheTokens)
	}
	if cacheComplete && !over {
		block.CacheHitTokens = intPtr(cacheTokens)
		rate := float64(cacheTokens) / float64(cacheInputs)
		block.CacheHitRate = &rate
	}
	latencySum := 0
	latencyCount := 0
	for _, row := range rows {
		if row.LatencyMS != nil {
			latencySum += *row.LatencyMS
			latencyCount++
		}
	}
	if latencyCount > 0 {
		block.MeasuredLatencyMS = intPtr(latencySum)
	}
	if latencyCount == len(rows) {
		block.LatencyMS = intPtr(latencySum)
	}
	costComplete := uncertain == 0
	costSum := 0.0
	labels := map[string]struct{}{}
	for _, row := range rows {
		if row.CostProxy == nil {
			costComplete = false
			continue
		}
		costSum += *row.CostProxy
		if row.CostProxyLabel != nil && *row.CostProxyLabel != "" {
			labels[*row.CostProxyLabel] = struct{}{}
		}
	}
	if costComplete {
		block.CostProxy = &costSum
		per := costSum / float64(len(rows))
		block.CostPerStep = &per
		if len(labels) > 0 {
			list := make([]string, 0, len(labels))
			for label := range labels {
				list = append(list, label)
			}
			sort.Strings(list)
			joined := list[0]
			for _, label := range list[1:] {
				joined += " | " + label
			}
			block.CostProxyLabel = &joined
		}
	}
	if rate := prefixReuse(rows); rate != nil {
		block.PrefixReuseRate = rate
	}
	return block
}

func prefixReuse(rows []StepRecord) *float64 {
	if len(rows) < 2 {
		return nil
	}
	counts := map[string]int{}
	for _, row := range rows {
		if row.PrefixSHA256 != "" {
			counts[row.PrefixSHA256]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	best := 0
	for _, count := range counts {
		if count > best {
			best = count
		}
	}
	rate := float64(best) / float64(len(rows))
	return &rate
}

func intPtr(v int) *int { return &v }
