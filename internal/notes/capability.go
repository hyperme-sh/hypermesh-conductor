package notes

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Capability data is a projection of linked Markdown, never execution authority.
type Capability struct {
	Schema        int                        `json:"schema"`
	ID            string                     `json:"id"`
	Kind          string                     `json:"kind"`
	Scope         string                     `json:"scope"`
	Owner         string                     `json:"owner"`
	Revision      string                     `json:"revision"`
	Class         string                     `json:"class,omitempty"`
	Uses          []CapabilityRef            `json:"uses,omitempty"`
	Requires      []CapabilityRef            `json:"requires,omitempty"`
	ObservedBy    []CapabilityRef            `json:"observed_by,omitempty"`
	Limits        []CapabilityRef            `json:"limits"`
	Inputs        map[string]CapabilityInput `json:"inputs,omitempty"`
	Preconditions []string                   `json:"preconditions,omitempty"`
	Effects       []string                   `json:"effects,omitempty"`
	Locks         []string                   `json:"locks,omitempty"`
	TimeoutMS     int                        `json:"timeout_ms,omitempty"`
	FreshnessMS   int                        `json:"freshness_ms,omitempty"`
}

type CapabilityRef struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type CapabilityRange struct {
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
}

type CapabilityInput struct {
	Type      string            `json:"type"`
	Required  bool              `json:"required"`
	Minimum   *float64          `json:"minimum,omitempty"`
	Maximum   *float64          `json:"maximum,omitempty"`
	MaxLength int               `json:"max_length,omitempty"`
	Enum      []string          `json:"enum,omitempty"`
	Axes      []CapabilityRange `json:"axes,omitempty"`
	Unit      string            `json:"unit,omitempty"`
}

type CapabilityEntry struct {
	Definition Capability `json:"definition"`
	SourceHash string     `json:"source_hash"`
	GraphHash  string     `json:"graph_hash"`
}

// Entries remain private so callers cannot mutate a validated catalog.
type CapabilityCatalog struct {
	entries    map[string]CapabilityEntry
	limits     map[string][]CapabilityRef
	locks      map[string][]string
	conditions map[string][]string
	bounds     map[string][2]int
	hashes     map[string]string
	depths     map[string]int
}

var capabilityName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)
var capabilityScope = regexp.MustCompile(`^org/[a-zA-Z0-9_-]+/site/[a-zA-Z0-9_-]+$`)
var capabilityHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validCapabilityName(s string) bool {
	return capabilityName.MatchString(s) && !strings.Contains(s, "..") && !strings.Contains(s, "//") && !strings.HasSuffix(s, "/")
}

func capabilityKey(scope, id string) string { return scope + "\x00" + id }

func capabilityReferences(c Capability) []CapabilityRef {
	refs := append([]CapabilityRef{}, c.Uses...)
	refs = append(refs, c.Requires...)
	return append(refs, c.ObservedBy...)
}

// Strict JSON rejects duplicate keys as well as unknown fields. Errors never echo
// note bodies or request values.
func capabilityJSON(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > 65536 {
		return errors.New("capability JSON size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var scan func(int) error
	scan = func(depth int) error {
		if depth > 8 {
			return errors.New("capability JSON depth")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, compound := t.(json.Delim)
		if !compound {
			return nil
		}
		seen := map[string]bool{}
		count := 0
		for d.More() {
			count++
			if count > 256 {
				return errors.New("capability JSON collection size")
			}
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				name = strings.ToLower(name)
				if !ok || seen[name] {
					return errors.New("duplicate capability key")
				}
				seen[name] = true
			}
			if err := scan(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := scan(0); err != nil {
		return errors.New("invalid capability JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing capability JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(dst); err != nil {
		return errors.New("invalid capability fields")
	}
	return nil
}

func parseCapability(n Note) (CapabilityEntry, error) {
	const fence = "```capability-json"
	lines := strings.Split(n.Body, "\n")
	start, end := -1, -1
	for i, line := range lines {
		if strings.TrimSpace(line) == fence {
			if start >= 0 {
				return CapabilityEntry{}, errors.New("multiple capability blocks")
			}
			start = i
		} else if start >= 0 && end < 0 && strings.TrimSpace(line) == "```" {
			end = i
		}
	}
	if start < 0 || end <= start {
		return CapabilityEntry{}, errors.New("missing capability block")
	}
	var c Capability
	if err := capabilityJSON([]byte(strings.Join(lines[start+1:end], "\n")), &c); err != nil {
		return CapabilityEntry{}, err
	}
	if c.ID != n.ID || n.idFromName || !capabilityHash.MatchString(n.Hash) {
		return CapabilityEntry{}, errors.New("capability requires explicit matching note identity")
	}
	if err := validateCapability(c); err != nil {
		return CapabilityEntry{}, err
	}
	links := map[string]bool{}
	for _, id := range n.Links {
		links[id] = true
	}
	for _, ref := range capabilityReferences(c) {
		if !links[ref.ID] {
			return CapabilityEntry{}, errors.New("capability relation requires a stable-ID wikilink")
		}
	}
	return CapabilityEntry{Definition: c, SourceHash: n.Hash}, nil
}

func validateCapability(c Capability) error {
	if c.Schema != 1 || !validCapabilityName(c.ID) || !capabilityScope.MatchString(c.Scope) || len(c.Scope) > 256 ||
		!validCapabilityName(c.Owner) || !validCapabilityName(c.Revision) ||
		(c.Kind != "noun" && c.Kind != "verb") || !strings.HasPrefix(c.ID, c.Kind+"/") {
		return errors.New("invalid capability identity or schema")
	}
	if len(c.Limits) == 0 || len(c.Limits) > 32 {
		return errors.New("capability requires bounded limit references")
	}
	seen := map[string]bool{}
	for _, ref := range c.Limits {
		if !validCapabilityName(ref.ID) || !(strings.HasPrefix(ref.ID, "controller/") || strings.HasPrefix(ref.ID, "policy/")) || !capabilityHash.MatchString(ref.Revision) || seen[ref.ID] {
			return errors.New("invalid immutable limit reference")
		}
		seen[ref.ID] = true
	}
	refs := capabilityReferences(c)
	if len(refs) > 128 {
		return errors.New("too many capability relations")
	}
	for _, ref := range refs {
		if !validCapabilityName(ref.ID) || !validCapabilityName(ref.Revision) {
			return errors.New("invalid capability relation")
		}
	}
	for _, ref := range append(append([]CapabilityRef{}, c.Uses...), c.ObservedBy...) {
		if !strings.HasPrefix(ref.ID, "noun/") {
			return errors.New("uses and observed_by must reference nouns")
		}
	}
	if c.Kind == "noun" {
		if !validCapabilityName(c.Class) || len(c.Inputs)+len(c.Preconditions)+len(c.Effects)+len(c.Locks) != 0 || c.TimeoutMS != 0 || c.FreshnessMS != 0 {
			return errors.New("noun contains invalid class or verb fields")
		}
		return nil
	}
	if c.Class != "" || c.TimeoutMS < 1 || c.TimeoutMS > 300000 || c.FreshnessMS < 1 || c.FreshnessMS > 300000 || len(c.Inputs) > 32 {
		return errors.New("invalid verb bounds")
	}
	for _, list := range [][]string{c.Preconditions, c.Effects, c.Locks} {
		if len(list) == 0 || len(list) > 32 {
			return errors.New("verb requires bounded preconditions, effects and locks")
		}
		seen := map[string]bool{}
		for _, s := range list {
			if !validCapabilityName(s) || seen[s] {
				return errors.New("invalid verb condition, effect or lock")
			}
			seen[s] = true
		}
	}
	for name, input := range c.Inputs {
		if !validCapabilityName(name) || strings.Contains(name, "/") {
			return errors.New("invalid input name")
		}
		if err := validateCapabilityInput(input); err != nil {
			return err
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }

func validateCapabilityInput(p CapabilityInput) error {
	if p.Unit != "" && !validCapabilityName(p.Unit) {
		return errors.New("invalid input unit")
	}
	switch p.Type {
	case "integer", "number":
		if p.Minimum == nil || p.Maximum == nil || !finite(*p.Minimum) || !finite(*p.Maximum) || *p.Minimum > *p.Maximum || p.MaxLength != 0 || len(p.Enum)+len(p.Axes) != 0 {
			return errors.New("invalid numeric input bounds")
		}
		if p.Type == "integer" && (math.Trunc(*p.Minimum) != *p.Minimum || math.Trunc(*p.Maximum) != *p.Maximum || math.Abs(*p.Minimum) > 9007199254740991 || math.Abs(*p.Maximum) > 9007199254740991) {
			return errors.New("invalid integer range")
		}
	case "boolean":
		if p.Minimum != nil || p.Maximum != nil || p.MaxLength != 0 || len(p.Enum)+len(p.Axes) != 0 || p.Unit != "" {
			return errors.New("invalid boolean input")
		}
	case "string":
		if p.MaxLength < 1 || p.MaxLength > 4096 || len(p.Enum) > 32 || p.Minimum != nil || p.Maximum != nil || len(p.Axes) != 0 || p.Unit != "" {
			return errors.New("invalid string input bounds")
		}
		seen := map[string]bool{}
		for _, value := range p.Enum {
			if len(value) > p.MaxLength || seen[value] {
				return errors.New("invalid string enum")
			}
			seen[value] = true
		}
	case "vector3":
		if len(p.Axes) != 3 || p.Unit == "" || p.Minimum != nil || p.Maximum != nil || p.MaxLength != 0 || len(p.Enum) != 0 {
			return errors.New("invalid vector input")
		}
		for _, axis := range p.Axes {
			if !finite(axis.Minimum) || !finite(axis.Maximum) || axis.Minimum > axis.Maximum {
				return errors.New("invalid vector bounds")
			}
		}
	default:
		return errors.New("unsupported input type")
	}
	return nil
}

// NewCapabilityCatalog rejects any malformed, unresolved or cross-scope graph.
func NewCapabilityCatalog(list []Note) (*CapabilityCatalog, error) {
	c := &CapabilityCatalog{entries: map[string]CapabilityEntry{}, limits: map[string][]CapabilityRef{}, locks: map[string][]string{}, conditions: map[string][]string{}, bounds: map[string][2]int{}, hashes: map[string]string{}, depths: map[string]int{}}
	ids := map[string]bool{}
	for _, n := range list {
		if n.Type != "capability" {
			continue
		}
		e, err := parseCapability(n)
		if err != nil {
			return nil, fmt.Errorf("capability note %s: %w", n.ID, err)
		}
		if ids[e.Definition.ID] {
			return nil, errors.New("duplicate capability note ID")
		}
		ids[e.Definition.ID] = true
		key := capabilityKey(e.Definition.Scope, e.Definition.ID)
		if _, ok := c.entries[key]; ok {
			return nil, errors.New("duplicate capability identity")
		}
		c.entries[key] = e
		if len(c.entries) > 256 {
			return nil, errors.New("capability catalog quota")
		}
	}
	for key := range c.entries {
		if _, _, err := c.inherited(key, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *CapabilityCatalog) inherited(key string, visiting map[string]bool) ([]CapabilityRef, []string, error) {
	if limits, ok := c.limits[key]; ok {
		return append([]CapabilityRef{}, limits...), append([]string{}, c.locks[key]...), nil
	}
	if visiting[key] || len(visiting) > 32 {
		return nil, nil, errors.New("cyclic or deep capability graph")
	}
	e, ok := c.entries[key]
	if !ok {
		return nil, nil, errors.New("missing capability reference")
	}
	visiting[key] = true
	defer delete(visiting, key)
	d := e.Definition
	limits := map[string]string{}
	locks := map[string]bool{}
	conditions := map[string]bool{}
	bounds := [2]int{d.TimeoutMS, d.FreshnessMS}
	depth := 1
	hash := sha256.New()
	hash.Write([]byte(e.SourceHash))
	for _, ref := range d.Limits {
		limits[ref.ID] = ref.Revision
	}
	for _, lock := range d.Locks {
		locks[lock] = true
	}
	for _, condition := range d.Preconditions {
		conditions[condition] = true
	}
	for _, ref := range capabilityReferences(d) {
		targetKey := capabilityKey(d.Scope, ref.ID)
		target, ok := c.entries[targetKey]
		if !ok || target.Definition.Revision != ref.Revision || target.Definition.Owner != d.Owner {
			return nil, nil, errors.New("unresolved, stale or foreign capability relation")
		}
		inherited, held, err := c.inherited(targetKey, visiting)
		if err != nil {
			return nil, nil, err
		}
		if c.depths[targetKey]+1 > depth {
			depth = c.depths[targetKey] + 1
		}
		if depth > 32 {
			return nil, nil, errors.New("capability graph depth")
		}
		hash.Write([]byte(c.hashes[targetKey]))
		for i, bound := range c.bounds[targetKey] {
			if bound > 0 && (bounds[i] == 0 || bound < bounds[i]) {
				bounds[i] = bound
			}
		}
		for _, condition := range c.conditions[targetKey] {
			conditions[condition] = true
		}
		for _, limit := range inherited {
			if revision, ok := limits[limit.ID]; ok && revision != limit.Revision {
				return nil, nil, errors.New("conflicting inherited limit revisions")
			}
			limits[limit.ID] = limit.Revision
		}
		for _, lock := range held {
			locks[lock] = true
		}
	}
	result := make([]CapabilityRef, 0, len(limits))
	lockList := make([]string, 0, len(locks))
	for id, revision := range limits {
		result = append(result, CapabilityRef{id, revision})
	}
	for lock := range locks {
		lockList = append(lockList, lock)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	sort.Strings(lockList)
	for condition := range conditions {
		c.conditions[key] = append(c.conditions[key], condition)
	}
	sort.Strings(c.conditions[key])
	c.bounds[key] = bounds
	c.hashes[key] = fmt.Sprintf("%x", hash.Sum(nil))
	c.depths[key] = depth
	c.limits[key] = append([]CapabilityRef{}, result...)
	c.locks[key] = append([]string{}, lockList...)
	return result, lockList, nil
}

// Snapshot is a deep copy scoped by the operator, not a remote discovery grant.
func (c *CapabilityCatalog) Snapshot(scope string) ([]CapabilityEntry, error) {
	if !capabilityScope.MatchString(scope) {
		return nil, errors.New("explicit organization/site scope required")
	}
	result := []CapabilityEntry{}
	for key, e := range c.entries {
		if e.Definition.Scope == scope {
			e.GraphHash = c.hashes[key]
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Definition.ID < result[j].Definition.ID })
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var copy []CapabilityEntry
	err = json.Unmarshal(raw, &copy)
	return copy, err
}

type CapabilityCall struct {
	Scope      string
	Owner      string
	ID         string
	Revision   string
	SourceHash string
	GraphHash  string
	Arguments  json.RawMessage
}

// Implement only with current trusted policy/controller state. Note text and
// request-provided booleans are not implementations of this authority.
type CapabilityAuthority interface {
	Authorize(CapabilityCall) error
	CheckPrecondition(scope, condition string) error
	CheckLimit(scope string, ref CapabilityRef) error
}

type CapabilityPlan struct {
	Arguments map[string]any
	Limits    []CapabilityRef
	Locks     []string
	Timeout   time.Duration
	Deadline  time.Time
}

// CheckCall is a preflight contract, not an executor. The adapter supplies the
// observation time from its trusted sensor state and must recheck at dispatch,
// acquire every returned lock, enforce the deadline, and use controller limits.
func (c *CapabilityCatalog) CheckCall(call CapabilityCall, observedAt, now time.Time, authority CapabilityAuthority) (CapabilityPlan, error) {
	deny := func() (CapabilityPlan, error) { return CapabilityPlan{}, errors.New("capability call refused") }
	e, ok := c.entries[capabilityKey(call.Scope, call.ID)]
	if !ok || authority == nil {
		return deny()
	}
	d := e.Definition
	key := capabilityKey(d.Scope, d.ID)
	if d.Kind != "verb" || call.Owner != d.Owner || call.Revision != d.Revision || call.SourceHash != e.SourceHash || call.GraphHash != c.hashes[key] || observedAt.IsZero() || observedAt.After(now) || now.Sub(observedAt) > time.Duration(c.bounds[key][1])*time.Millisecond {
		return deny()
	}
	var args map[string]any
	if err := capabilityJSON(call.Arguments, &args); err != nil || args == nil {
		return deny()
	}
	for name := range args {
		if _, ok := d.Inputs[name]; !ok {
			return deny()
		}
	}
	for name, p := range d.Inputs {
		value, exists := args[name]
		if !exists {
			if p.Required {
				return deny()
			}
			continue
		}
		if !capabilityValue(p, value) {
			return deny()
		}
	}
	limits, locks, err := c.inherited(capabilityKey(d.Scope, d.ID), map[string]bool{})
	if err != nil {
		return deny()
	}
	call.Arguments = append(json.RawMessage{}, call.Arguments...)
	if authority.Authorize(call) != nil {
		return deny()
	}
	for _, condition := range c.conditions[key] {
		if authority.CheckPrecondition(d.Scope, condition) != nil {
			return deny()
		}
	}
	for _, ref := range limits {
		if authority.CheckLimit(d.Scope, ref) != nil {
			return deny()
		}
	}
	timeout := time.Duration(c.bounds[key][0]) * time.Millisecond
	return CapabilityPlan{Arguments: args, Limits: limits, Locks: locks, Timeout: timeout, Deadline: now.Add(timeout)}, nil
}

func capabilityValue(p CapabilityInput, value any) bool {
	switch p.Type {
	case "integer", "number":
		n, ok := value.(json.Number)
		if !ok {
			return false
		}
		v, err := n.Float64()
		if err != nil || !finite(v) || v < *p.Minimum || v > *p.Maximum {
			return false
		}
		if p.Type == "integer" {
			_, err = n.Int64()
			return err == nil
		}
		return true
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		v, ok := value.(string)
		if !ok || len(v) > p.MaxLength {
			return false
		}
		if len(p.Enum) == 0 {
			return true
		}
		for _, choice := range p.Enum {
			if v == choice {
				return true
			}
		}
		return false
	case "vector3":
		v, ok := value.([]any)
		if !ok || len(v) != 3 {
			return false
		}
		for i, axis := range p.Axes {
			n, ok := v[i].(json.Number)
			if !ok {
				return false
			}
			f, err := n.Float64()
			if err != nil || !finite(f) || f < axis.Minimum || f > axis.Maximum {
				return false
			}
		}
		return true
	}
	return false
}

// SyncCapabilities reads the existing index after a fresh collection sync. An
// ambiguous or incomplete collection cannot provide a capability snapshot.
func (s *Store) SyncCapabilities(dir string) (*CapabilityCatalog, error) {
	stats, err := s.Sync(dir)
	if err != nil {
		return nil, err
	}
	if len(stats.Warnings) != 0 {
		return nil, errors.New("resolve note index warnings before exporting capabilities")
	}
	list, err := s.All()
	if err != nil {
		return nil, err
	}
	return NewCapabilityCatalog(list)
}

// CheckProposal accepts only unchanged or tighter existing contracts. It never
// writes notes or changes a grant. Deletion revokes availability; newly described
// capabilities still require independent owner grants before execution.
func (c *CapabilityCatalog) CheckProposal(next *CapabilityCatalog) error {
	if next == nil {
		return errors.New("missing capability proposal")
	}
	for key, oldEntry := range c.entries {
		old := oldEntry.Definition
		for nextKey, entry := range next.entries {
			if entry.Definition.ID == old.ID && nextKey != key {
				return errors.New("proposal changes capability scope")
			}
		}
		nextEntry, ok := next.entries[key]
		if !ok {
			continue
		}
		n := nextEntry.Definition
		oldJSON, _ := json.Marshal(old)
		nextJSON, _ := json.Marshal(n)
		if !bytes.Equal(oldJSON, nextJSON) && old.Revision == n.Revision {
			return errors.New("changed capability needs a new revision")
		}
		if old.Owner != n.Owner || old.Kind != n.Kind || old.Class != n.Class {
			return errors.New("proposal changes capability identity")
		}
		if !refSubset(c.limits[key], next.limits[key], true) || !stringSubset(c.conditions[key], next.conditions[key]) || !stringSubset(c.locks[key], next.locks[key]) {
			return errors.New("proposal removes inherited safety constraints")
		}
		if !refSubset(old.Uses, n.Uses, false) || !refSubset(old.Requires, n.Requires, false) || !refSubset(old.ObservedBy, n.ObservedBy, false) {
			return errors.New("proposal removes capability dependencies")
		}
		for i, bound := range c.bounds[key] {
			if bound > 0 && (next.bounds[key][i] == 0 || next.bounds[key][i] > bound) {
				return errors.New("proposal weakens time bounds")
			}
		}
		if !stringSubset(old.Effects, n.Effects) || !stringSubset(n.Effects, old.Effects) || len(old.Inputs) != len(n.Inputs) {
			return errors.New("proposal changes effects or input surface")
		}
		for name, p := range old.Inputs {
			q, ok := n.Inputs[name]
			if !ok || !narrowerInput(p, q) {
				return errors.New("proposal widens input bounds")
			}
		}
	}
	return nil
}

func stringSubset(old, next []string) bool {
	set := map[string]bool{}
	for _, s := range next {
		set[s] = true
	}
	for _, s := range old {
		if !set[s] {
			return false
		}
	}
	return true
}

func refSubset(old, next []CapabilityRef, exact bool) bool {
	set := map[string]string{}
	for _, ref := range next {
		set[ref.ID] = ref.Revision
	}
	for _, ref := range old {
		revision, ok := set[ref.ID]
		if !ok || (exact && revision != ref.Revision) {
			return false
		}
	}
	return true
}

func narrowerInput(p, q CapabilityInput) bool {
	if p.Type != q.Type || p.Unit != q.Unit || (p.Required && !q.Required) {
		return false
	}
	switch p.Type {
	case "integer", "number":
		return *q.Minimum >= *p.Minimum && *q.Maximum <= *p.Maximum
	case "string":
		return q.MaxLength <= p.MaxLength && (len(p.Enum) == 0 || (len(q.Enum) > 0 && stringSubset(q.Enum, p.Enum)))
	case "vector3":
		for i, axis := range p.Axes {
			if q.Axes[i].Minimum < axis.Minimum || q.Axes[i].Maximum > axis.Maximum {
				return false
			}
		}
	}
	return true
}
