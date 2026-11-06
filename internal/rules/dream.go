package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/hyperme-sh/hypermesh-conductor/internal/history"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
)

// Outcome is a structured result attached to a journal step.
type Outcome struct {
	TaskID string `json:"task_id"`
	StepID string `json:"step_id"`
	Kind   string `json:"kind"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// DreamInput is the offline planner's view. Bodies are optional.
type DreamInput struct {
	Records  []metrics.StepRecord
	Outcomes []Outcome
	History  []history.Record
	Bodies   bool
}

// Dream writes one inactive proposal. It does not modify the active policy.
func Dream(in DreamInput, active File, proposalsDir string) (Proposal, string, error) {
	proposal := Proposal{File: active}
	proposal.Schema = Schema
	proposal.Evidence = []Evidence{}
	proposal.Observations = nil
	if proposal.IndexLinks == nil {
		proposal.IndexLinks = []IndexLink{}
	}

	byClass := map[string][]metrics.StepRecord{}
	for _, row := range in.Records {
		byClass[row.JobClass] = append(byClass[row.JobClass], row)
	}
	outcomesByStep := map[string][]Outcome{}
	for _, outcome := range in.Outcomes {
		outcomesByStep[outcomeKey(outcome.TaskID, outcome.StepID)] = append(outcomesByStep[outcomeKey(outcome.TaskID, outcome.StepID)], outcome)
	}

	classes := make([]string, 0, len(byClass))
	for class := range byClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		rows := byClass[class]
		var misses []string
		var missKeys []string
		for _, row := range rows {
			if row.Tier != "cheap" {
				continue
			}
			for _, outcome := range outcomesByStep[outcomeKey(row.TaskID, row.StepID)] {
				if outcome.Kind == "escalation" || outcome.Kind == "test_failed" {
					misses = append(misses, row.StepID)
					missKeys = append(missKeys, outcomeKey(row.TaskID, row.StepID))
					break
				}
			}
		}
		if len(misses) >= 2 && len(rows) > 0 && float64(len(misses))/float64(len(rows)) >= 0.5 {
			limit := medianPeak(rows, missKeys)
			if proposal.Routing.EscalateWhenPeakTokensAbove == nil {
				proposal.Routing.EscalateWhenPeakTokensAbove = map[string]int{}
			}
			proposal.Routing.EscalateWhenPeakTokensAbove[class] = limit
			proposal.Evidence = append(proposal.Evidence, Evidence{
				Kind: "routing_threshold", JobClass: class, StepIDs: misses,
				Count: len(misses), Total: len(rows), Prevalence: float64(len(misses)) / float64(len(rows)),
			})
		}
	}

	cacheReported := 0
	cacheHits := 0
	var cacheSteps []string
	for _, row := range in.Records {
		if row.CacheHitTokens == nil || row.InputTokens == nil || *row.InputTokens == 0 {
			continue
		}
		cacheReported++
		cacheSteps = append(cacheSteps, row.StepID)
		if *row.CacheHitTokens > 0 {
			cacheHits++
		}
	}
	if cacheReported >= 2 && cacheHits == 0 {
		proposal.CachePrefix.SystemInPrefix = true
		proposal.CachePrefix.ToolsInPrefix = true
		proposal.CachePrefix.Note = "keep the system prompt and tool definitions in the prefix; compact only the tail"
		proposal.Evidence = append(proposal.Evidence, Evidence{
			Kind: "cache_prefix", StepIDs: cacheSteps, Count: cacheReported, Total: cacheReported, Prevalence: 1,
		})
	}

	journalSteps := map[string]bool{}
	for _, row := range in.Records {
		journalSteps[outcomeKey(row.TaskID, row.StepID)] = true
	}
	linkAdds := map[string][]string{}
	linkDrops := map[string][]string{}
	for _, outcome := range in.Outcomes {
		if !journalSteps[outcomeKey(outcome.TaskID, outcome.StepID)] {
			continue
		}
		key := outcome.From + "\t" + outcome.To
		switch outcome.Kind {
		case "link_used":
			linkAdds[key] = append(linkAdds[key], outcome.StepID)
		case "link_unused":
			linkDrops[key] = append(linkDrops[key], outcome.StepID)
		case "aiuditor_deny":
			proposal.Observations = append(proposal.Observations, "aiuditor denied "+outcome.StepID+"; routing may change upstream of the hop")
		}
	}
	for key, steps := range linkAdds {
		if len(steps) < 2 {
			continue
		}
		from, to, _ := stringsCut(key)
		proposal.IndexLinks = append(proposal.IndexLinks, IndexLink{Op: "add", From: from, To: to})
		proposal.Evidence = append(proposal.Evidence, Evidence{
			Kind: "index_link", StepIDs: steps, Count: len(steps), Total: len(in.Outcomes), Prevalence: fraction(len(steps), len(in.Outcomes)),
		})
	}
	for key, steps := range linkDrops {
		if len(steps) < 2 {
			continue
		}
		from, to, _ := stringsCut(key)
		proposal.IndexLinks = append(proposal.IndexLinks, IndexLink{Op: "drop", From: from, To: to})
		proposal.Evidence = append(proposal.Evidence, Evidence{
			Kind: "index_link", StepIDs: steps, Count: len(steps), Total: len(in.Outcomes), Prevalence: fraction(len(steps), len(in.Outcomes)),
		})
	}
	sort.Slice(proposal.IndexLinks, func(i, j int) bool {
		if proposal.IndexLinks[i].Op != proposal.IndexLinks[j].Op {
			return proposal.IndexLinks[i].Op < proposal.IndexLinks[j].Op
		}
		if proposal.IndexLinks[i].From != proposal.IndexLinks[j].From {
			return proposal.IndexLinks[i].From < proposal.IndexLinks[j].From
		}
		return proposal.IndexLinks[i].To < proposal.IndexLinks[j].To
	})

	if in.Bodies {
		for i := range proposal.Evidence {
			excerpt := excerptFor(proposal.Evidence[i].StepIDs, in.History)
			proposal.Evidence[i].Excerpt = excerpt
		}
	}

	raw, err := Marshal(proposal)
	if err != nil {
		return Proposal{}, "", err
	}
	if err := rejectSafetyKeys(raw); err != nil {
		return Proposal{}, "", err
	}
	if err := os.MkdirAll(proposalsDir, 0o755); err != nil {
		return Proposal{}, "", err
	}
	version, err := nextVersion(proposalsDir)
	if err != nil {
		return Proposal{}, "", err
	}
	proposal.Version = version
	raw, err = Marshal(proposal)
	if err != nil {
		return Proposal{}, "", err
	}
	path := filepath.Join(proposalsDir, fmt.Sprintf("%04d.json", version))
	mode := os.FileMode(0o644)
	if in.Bodies {
		mode = 0o600
	}
	if err := os.WriteFile(path, raw, mode); err != nil {
		return Proposal{}, "", err
	}
	if in.Bodies {
		if err := ownerOnly(path); err != nil {
			return Proposal{}, "", err
		}
	}
	return proposal, path, nil
}

// ownerOnly keeps a body-bearing proposal unreadable by other accounts.
// An existing group- or world-readable file is tightened, then checked.
func ownerOnly(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("proposal is readable by other users")
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("proposal is readable by other users")
	}
	return nil
}

func excerptFor(stepIDs []string, rows []history.Record) string {
	wanted := map[string]struct{}{}
	for _, id := range stepIDs {
		wanted[id] = struct{}{}
	}
	for _, row := range rows {
		if _, ok := wanted[row.StepID]; !ok {
			continue
		}
		text := row.Summary
		if text == "" {
			text = string(row.OriginalTail)
		}
		if len(text) > 120 {
			text = text[:120]
		}
		return text
	}
	return ""
}

func medianPeak(rows []metrics.StepRecord, keys []string) int {
	wanted := map[string]struct{}{}
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	var peaks []int
	for _, row := range rows {
		if _, ok := wanted[outcomeKey(row.TaskID, row.StepID)]; !ok || row.InputTokens == nil {
			continue
		}
		peaks = append(peaks, *row.InputTokens)
	}
	if len(peaks) == 0 {
		return 0
	}
	sort.Ints(peaks)
	return peaks[len(peaks)/2]
}

func fraction(count, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
}

func stringsCut(key string) (string, string, bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '\t' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

func nextVersion(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, entry := range entries {
		var n int
		if _, err := fmt.Sscanf(entry.Name(), "%d.json", &n); err == nil && n > max {
			max = n
		}
	}
	return max + 1, nil
}

// Apply copies a proposal onto the active policy and archives the previous one.
func Apply(activePath, proposalPath string) error {
	raw, err := os.ReadFile(proposalPath)
	if err != nil {
		return err
	}
	proposal, err := ParseProposal(raw)
	if err != nil {
		return err
	}
	current, err := Load(activePath)
	if err != nil {
		return err
	}
	revDir := filepath.Join(filepath.Dir(activePath), "revisions")
	if err := os.MkdirAll(revDir, 0o755); err != nil {
		return err
	}
	blob, err := Marshal(current)
	if err != nil {
		return err
	}
	rev := nextRevision(revDir)
	if err := os.WriteFile(filepath.Join(revDir, fmt.Sprintf("%04d.json", rev)), blob, 0o644); err != nil {
		return err
	}
	next := proposal.File
	if next.Version <= current.Version {
		next.Version = current.Version + 1
	}
	return SaveActive(activePath, next)
}

// Rollback restores the newest archived policy.
func Rollback(activePath string) error {
	revDir := filepath.Join(filepath.Dir(activePath), "revisions")
	entries, err := os.ReadDir(revDir)
	if err != nil {
		return fmt.Errorf("no revision to restore")
	}
	best := ""
	bestN := -1
	for _, entry := range entries {
		var n int
		if _, err := fmt.Sscanf(entry.Name(), "%d.json", &n); err != nil {
			continue
		}
		if n > bestN {
			bestN = n
			best = entry.Name()
		}
	}
	if best == "" {
		return fmt.Errorf("no revision to restore")
	}
	raw, err := os.ReadFile(filepath.Join(revDir, best))
	if err != nil {
		return err
	}
	file, err := Parse(raw)
	if err != nil {
		return err
	}
	if err := SaveActive(activePath, file); err != nil {
		return err
	}
	return os.Remove(filepath.Join(revDir, best))
}

func outcomeKey(taskID, stepID string) string {
	return taskID + "\x00" + stepID
}

func nextRevision(dir string) int {
	entries, _ := os.ReadDir(dir)
	max := 0
	for _, entry := range entries {
		var n int
		if _, err := fmt.Sscanf(entry.Name(), "%d.json", &n); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}
