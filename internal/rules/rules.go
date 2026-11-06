// Package rules is the versioned policy the proxy reads and dream proposes.
//
// Proposals do not become live until apply. Learning cannot loosen AIuditor.
package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

const Schema = "hypermesh.conductor.rules.v0"

// File is a routing, cache-prefix, and index policy.
type File struct {
	Schema      string      `json:"schema"`
	Version     int         `json:"version"`
	Routing     Routing     `json:"routing"`
	CachePrefix CachePrefix `json:"cache_prefix"`
	IndexLinks  []IndexLink `json:"index_links"`
}

// Routing holds class tiers and size thresholds. A threshold is a written rule.
type Routing struct {
	ClassTiers                  map[string]string `json:"class_tiers,omitempty"`
	EscalateWhenPeakTokensAbove map[string]int    `json:"escalate_when_peak_tokens_above,omitempty"`
}

// CachePrefix describes what stays in the stable head.
type CachePrefix struct {
	SystemInPrefix bool   `json:"system_in_prefix"`
	ToolsInPrefix  bool   `json:"tools_in_prefix"`
	MaxTailTokens  int    `json:"max_tail_tokens,omitempty"`
	PreferOnMatch  string `json:"prefer_on_match,omitempty"`
	Note           string `json:"note,omitempty"`
}

// IndexLink is a proposed or active retrieval-graph edit.
type IndexLink struct {
	Op   string `json:"op"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Proposal is a dream output. Observations are evidence, not live policy.
type Proposal struct {
	File
	Evidence     []Evidence `json:"evidence"`
	Observations []string   `json:"observations,omitempty"`
}

// Evidence cites journal rows and a prevalence fraction.
type Evidence struct {
	Kind       string   `json:"kind"`
	JobClass   string   `json:"job_class,omitempty"`
	StepIDs    []string `json:"step_ids"`
	Count      int      `json:"count"`
	Total      int      `json:"total"`
	Prevalence float64  `json:"prevalence"`
	Excerpt    string   `json:"excerpt,omitempty"`
}

// Defaults is the built-in policy. It does not escalate and does not touch AIuditor.
func Defaults() File {
	return File{
		Schema:  Schema,
		Version: 0,
		CachePrefix: CachePrefix{
			SystemInPrefix: true,
			ToolsInPrefix:  true,
		},
		IndexLinks: []IndexLink{},
	}
}

// Load reads an active policy. A missing file is the default policy.
func Load(path string) (File, error) {
	if path == "" {
		return Defaults(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Defaults(), nil
		}
		return File{}, err
	}
	file, err := Parse(raw)
	if err != nil {
		return File{}, err
	}
	return file, nil
}

// Parse decodes a policy and rejects AIuditor loosening.
func Parse(raw []byte) (File, error) {
	if err := rejectSafetyKeys(raw); err != nil {
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return File{}, fmt.Errorf("rules file is not JSON: %w", err)
	}
	if file.Schema != "" && file.Schema != Schema {
		return File{}, fmt.Errorf("unknown rules schema %s", file.Schema)
	}
	if file.Schema == "" {
		file.Schema = Schema
	}
	if file.IndexLinks == nil {
		file.IndexLinks = []IndexLink{}
	}
	for class, tier := range file.Routing.ClassTiers {
		if tier != "cheap" && tier != "frontier" {
			return File{}, fmt.Errorf("routing class %s has tier %s", class, tier)
		}
	}
	for class, tier := range file.Routing.EscalateWhenPeakTokensAbove {
		if tier < 0 {
			return File{}, fmt.Errorf("threshold for %s is negative", class)
		}
	}
	if file.CachePrefix.PreferOnMatch != "" && file.CachePrefix.PreferOnMatch != "cheap" && file.CachePrefix.PreferOnMatch != "frontier" {
		return File{}, fmt.Errorf("prefer_on_match must be cheap or frontier")
	}
	return file, nil
}

// ParseProposal decodes a dream proposal. Safety keys are rejected.
func ParseProposal(raw []byte) (Proposal, error) {
	if err := rejectSafetyKeys(raw); err != nil {
		return Proposal{}, err
	}
	var proposal Proposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return Proposal{}, fmt.Errorf("proposal is not JSON")
	}
	file, err := Parse(raw)
	if err != nil {
		return Proposal{}, err
	}
	proposal.File = file
	return proposal, nil
}

func rejectSafetyKeys(raw []byte) error {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("rules file is not JSON")
	}
	var bad []string
	walkKeys(generic, "", &bad)
	if len(bad) > 0 {
		return fmt.Errorf("rules cannot loosen AIuditor (%s)", strings.Join(bad, ", "))
	}
	return nil
}

func walkKeys(value any, path string, bad *[]string) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, child := range obj {
		name := strings.ToLower(key)
		full := name
		if path != "" {
			full = path + "." + name
		}
		switch name {
		case "aiuditor", "fail_closed", "deny_job_classes", "bypass", "fail_open":
			*bad = append(*bad, full)
		}
		walkKeys(child, full, bad)
	}
}

// EffectiveClassTiers overlays written thresholds onto the configured tiers.
// An escalation threshold is policy. A missing threshold does not upgrade.
func EffectiveClassTiers(base map[string]string, policy File, class string, peak int) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range policy.Routing.ClassTiers {
		out[key] = value
	}
	canonical := class
	if name, err := routing.CanonicalClass(class); err == nil {
		canonical = name
	}
	if limit, ok := policy.Routing.EscalateWhenPeakTokensAbove[canonical]; ok && peak > limit {
		out[canonical] = "frontier"
	}
	return out
}

// Suggest records an in-band hint. The caller must pass it through routing,
// which rejects upgrades and downgrades that the written tier does not allow.
func Suggest(policy File, prefixMatch bool) string {
	if prefixMatch && policy.CachePrefix.PreferOnMatch == "cheap" {
		return "cheap"
	}
	return ""
}

// Marshal writes stable JSON.
func Marshal(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// SaveActive writes the live policy.
func SaveActive(path string, file File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := Marshal(file)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Diff returns sorted lines of changed policy fields.
func Diff(active, proposal File) string {
	left := flatten(active)
	right := flatten(proposal)
	keys := map[string]struct{}{}
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var buf strings.Builder
	for _, key := range ordered {
		if left[key] == right[key] {
			continue
		}
		fmt.Fprintf(&buf, "%s: %s -> %s\n", key, show(left[key]), show(right[key]))
	}
	return buf.String()
}

func show(value string) string {
	if value == "" {
		return "<absent>"
	}
	return value
}

func flatten(file File) map[string]string {
	out := map[string]string{
		"cache_prefix.system_in_prefix": strconv.FormatBool(file.CachePrefix.SystemInPrefix),
		"cache_prefix.tools_in_prefix":  strconv.FormatBool(file.CachePrefix.ToolsInPrefix),
	}
	if file.CachePrefix.MaxTailTokens != 0 {
		out["cache_prefix.max_tail_tokens"] = strconv.Itoa(file.CachePrefix.MaxTailTokens)
	}
	if file.CachePrefix.PreferOnMatch != "" {
		out["cache_prefix.prefer_on_match"] = file.CachePrefix.PreferOnMatch
	}
	if file.CachePrefix.Note != "" {
		out["cache_prefix.note"] = file.CachePrefix.Note
	}
	for class, tier := range file.Routing.ClassTiers {
		out["routing.class_tiers."+class] = tier
	}
	for class, limit := range file.Routing.EscalateWhenPeakTokensAbove {
		out["routing.escalate_when_peak_tokens_above."+class] = strconv.Itoa(limit)
	}
	for _, link := range file.IndexLinks {
		out["index_links."+link.Op+"."+link.From+"."+link.To] = "present"
	}
	return out
}
