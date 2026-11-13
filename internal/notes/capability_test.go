package notes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureScope = "org/example/site/lab"

func capabilityFixture() (Capability, Capability) {
	minimum, maximum := float64(1), float64(60)
	noun := Capability{Schema: 1, ID: "noun/network", Kind: "noun", Scope: fixtureScope, Owner: "owner", Revision: "r1", Class: "network-segment", Limits: []CapabilityRef{{"controller/targets", strings.Repeat("a", 64)}}}
	verb := Capability{Schema: 1, ID: "verb/counters", Kind: "verb", Scope: fixtureScope, Owner: "owner", Revision: "r1", Uses: []CapabilityRef{{noun.ID, noun.Revision}}, Limits: []CapabilityRef{{"policy/read-only", strings.Repeat("b", 64)}}, Inputs: map[string]CapabilityInput{"window": {Type: "integer", Required: true, Minimum: &minimum, Maximum: &maximum}}, Preconditions: []string{"read-grant", "telemetry-fresh"}, Effects: []string{"aggregate-summary"}, Locks: []string{"network/counters"}, TimeoutMS: 5000, FreshnessMS: 1000}
	return noun, verb
}

func capabilityMarkdown(c Capability) string {
	body, _ := json.MarshalIndent(c, "", "  ")
	links := ""
	for _, ref := range capabilityReferences(c) {
		links += "[[" + ref.ID + "]]\n"
	}
	return fmt.Sprintf("---\nid: %s\ntype: capability\n---\n# Fixture\n\n```capability-json\n%s\n```\n%s", c.ID, body, links)
}

func catalogFixture(t *testing.T, defs ...Capability) *CapabilityCatalog {
	t.Helper()
	list := []Note{}
	for i, c := range defs {
		list = append(list, Parse(fmt.Sprintf("%d.md", i), []byte(capabilityMarkdown(c))))
	}
	c, err := NewCapabilityCatalog(list)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func callFixture(t *testing.T, c *CapabilityCatalog, id string) CapabilityCall {
	t.Helper()
	entries, err := c.Snapshot(fixtureScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Definition.ID == id {
			return CapabilityCall{Scope: fixtureScope, Owner: e.Definition.Owner, ID: id, Revision: e.Definition.Revision, SourceHash: e.SourceHash, GraphHash: e.GraphHash, Arguments: json.RawMessage(`{"window":10}`)}
		}
	}
	t.Fatal("missing fixture capability")
	return CapabilityCall{}
}

type fixtureAuthority struct {
	revoked   bool
	condition string
	limit     string
	calls     int
	seen      CapabilityCall
}

func (a *fixtureAuthority) Authorize(c CapabilityCall) error {
	a.calls++
	a.seen = c
	if a.revoked {
		return errors.New("revoked")
	}
	return nil
}
func (a *fixtureAuthority) CheckPrecondition(scope, condition string) error {
	if scope != fixtureScope || condition == a.condition {
		return errors.New("condition unavailable")
	}
	return nil
}
func (a *fixtureAuthority) CheckLimit(scope string, ref CapabilityRef) error {
	if scope != fixtureScope || ref.ID == a.limit {
		return errors.New("limit unavailable")
	}
	return nil
}

func TestCapabilityCallUsesInheritedControllerLimits(t *testing.T) {
	noun, verb := capabilityFixture()
	c := catalogFixture(t, noun, verb)
	call := callFixture(t, c, verb.ID)
	now := time.Unix(100, 0)
	a := &fixtureAuthority{}
	p, err := c.CheckCall(call, now.Add(-time.Second), now, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Limits) != 2 || p.Limits[0].ID != "controller/targets" || p.Timeout != 5*time.Second || len(p.Locks) != 1 || a.calls != 1 || a.seen.GraphHash != call.GraphHash {
		t.Fatal("missing inherited constraints")
	}
	// Returned slices and snapshots cannot change later calls.
	p.Limits[0].Revision = "changed"
	p.Locks[0] = "changed"
	entries, _ := c.Snapshot(fixtureScope)
	for _, e := range entries {
		if e.Definition.Kind == "verb" {
			e.Definition.Inputs["window"] = CapabilityInput{Type: "boolean"}
		}
	}
	p, err = c.CheckCall(call, now, now, a)
	if err != nil || p.Limits[0].Revision != strings.Repeat("a", 64) || p.Locks[0] != "network/counters" {
		t.Fatal("mutable catalog escaped")
	}
	other, _ := c.Snapshot("org/other/site/lab")
	if len(other) != 0 {
		t.Fatal("scope leaked")
	}
}

func TestCapabilityCallRefusesStaleRevokedAndMalformedRequests(t *testing.T) {
	noun, verb := capabilityFixture()
	c := catalogFixture(t, noun, verb)
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		name      string
		mutate    func(*CapabilityCall)
		age       time.Duration
		authority *fixtureAuthority
	}{
		{"revoked", func(*CapabilityCall) {}, 0, &fixtureAuthority{revoked: true}},
		{"stale", func(*CapabilityCall) {}, time.Second + time.Nanosecond, &fixtureAuthority{}},
		{"future", func(*CapabilityCall) {}, -time.Nanosecond, &fixtureAuthority{}},
		{"limit", func(*CapabilityCall) {}, 0, &fixtureAuthority{limit: "controller/targets"}},
		{"condition", func(*CapabilityCall) {}, 0, &fixtureAuthority{condition: "telemetry-fresh"}},
		{"owner", func(c *CapabilityCall) { c.Owner = "other" }, 0, &fixtureAuthority{}},
		{"scope", func(c *CapabilityCall) { c.Scope = "org/other/site/lab" }, 0, &fixtureAuthority{}},
		{"revision", func(c *CapabilityCall) { c.Revision = "old" }, 0, &fixtureAuthority{}},
		{"hash", func(c *CapabilityCall) { c.SourceHash = strings.Repeat("f", 64) }, 0, &fixtureAuthority{}},
		{"graph", func(c *CapabilityCall) { c.GraphHash = strings.Repeat("f", 64) }, 0, &fixtureAuthority{}},
		{"missing", func(c *CapabilityCall) { c.Arguments = json.RawMessage(`{}`) }, 0, &fixtureAuthority{}},
		{"extra", func(c *CapabilityCall) { c.Arguments = json.RawMessage(`{"window":10,"extra":"private"}`) }, 0, &fixtureAuthority{}},
		{"type", func(c *CapabilityCall) { c.Arguments = json.RawMessage(`{"window":"private"}`) }, 0, &fixtureAuthority{}},
		{"range", func(c *CapabilityCall) { c.Arguments = json.RawMessage(`{"window":61}`) }, 0, &fixtureAuthority{}},
		{"duplicate", func(c *CapabilityCall) { c.Arguments = json.RawMessage(`{"window":10,"window":61}`) }, 0, &fixtureAuthority{}},
		{"null", func(c *CapabilityCall) { c.Arguments = json.RawMessage(`null`) }, 0, &fixtureAuthority{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := callFixture(t, c, verb.ID)
			tc.mutate(&call)
			_, err := c.CheckCall(call, now.Add(-tc.age), now, tc.authority)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("call was accepted or disclosed its arguments")
			}
		})
	}
	call := callFixture(t, c, verb.ID)
	if _, err := c.CheckCall(call, now, now, nil); err == nil {
		t.Fatal("absent authority accepted")
	}
}

func TestCapabilityGraphBindsDependencyContentAndConditions(t *testing.T) {
	noun, verb := capabilityFixture()
	child := verb
	child.ID = "verb/child"
	child.Requires = []CapabilityRef{{verb.ID, verb.Revision}}
	child.Preconditions = []string{"child-ready"}
	child.Locks = []string{"child/resource"}
	child.TimeoutMS = 10000
	child.FreshnessMS = 2000
	c := catalogFixture(t, noun, verb, child)
	call := callFixture(t, c, child.ID)
	now := time.Unix(100, 0)
	if _, err := c.CheckCall(call, now, now, &fixtureAuthority{condition: "telemetry-fresh"}); err == nil {
		t.Fatal("dependency condition ignored")
	}
	p, err := c.CheckCall(call, now, now, &fixtureAuthority{})
	if err != nil || len(p.Locks) != 2 || p.Timeout != 5*time.Second {
		t.Fatal("dependency constraints not inherited", err)
	}
	if _, err := c.CheckCall(call, now.Add(-1500*time.Millisecond), now, &fixtureAuthority{}); err == nil {
		t.Fatal("dependency freshness ignored")
	}
	noun.Class = "changed-without-revision"
	next := catalogFixture(t, noun, verb, child)
	if _, err := next.CheckCall(call, now, now, &fixtureAuthority{}); err == nil {
		t.Fatal("dependency byte change kept old grant binding")
	}
}

func TestCapabilityCatalogRejectsInvalidGraphs(t *testing.T) {
	for _, mode := range []string{"scope", "owner", "revision", "cycle", "limit", "missing-link", "unknown", "duplicate", "implicit-id", "schema"} {
		t.Run(mode, func(t *testing.T) {
			noun, verb := capabilityFixture()
			switch mode {
			case "scope":
				noun.Scope = "org/other/site/lab"
			case "owner":
				noun.Owner = "other"
			case "revision":
				noun.Revision = "r2"
			case "cycle":
				noun.Requires = []CapabilityRef{{verb.ID, verb.Revision}}
			case "limit":
				noun.Limits[0] = CapabilityRef{verb.Limits[0].ID, strings.Repeat("c", 64)}
			case "schema":
				verb.Schema = 2
			}
			raw := capabilityMarkdown(verb)
			if mode == "missing-link" {
				raw = strings.ReplaceAll(raw, "[[noun/network]]", "")
			}
			if mode == "unknown" {
				raw = strings.Replace(raw, `"schema": 1`, `"schema": 1, "execute": "private"`, 1)
			}
			if mode == "duplicate" {
				raw = strings.Replace(raw, `"schema": 1`, `"schema": 1, "Schema": 1`, 1)
			}
			if mode == "implicit-id" {
				raw = strings.Replace(raw, "id: verb/counters\n", "", 1)
			}
			_, err := NewCapabilityCatalog([]Note{Parse("noun.md", []byte(capabilityMarkdown(noun))), Parse("verb.md", []byte(raw))})
			if err == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
}

func TestCapabilityProposalOnlyTightensExistingSafety(t *testing.T) {
	for _, mode := range []string{"tighten", "bounds", "limit", "precondition", "timeout", "freshness", "lock", "effect", "revision", "scope", "owner"} {
		t.Run(mode, func(t *testing.T) {
			noun, verb := capabilityFixture()
			old := catalogFixture(t, noun, verb)
			verb.Revision = "r2"
			switch mode {
			case "tighten":
				max := float64(30)
				p := verb.Inputs["window"]
				p.Maximum = &max
				verb.Inputs["window"] = p
				verb.Preconditions = append(verb.Preconditions, "operator-present")
			case "bounds":
				max := float64(61)
				p := verb.Inputs["window"]
				p.Maximum = &max
				verb.Inputs["window"] = p
			case "limit":
				verb.Limits = []CapabilityRef{{"policy/other", strings.Repeat("c", 64)}}
			case "precondition":
				verb.Preconditions = []string{"telemetry-fresh"}
			case "timeout":
				verb.TimeoutMS++
			case "freshness":
				verb.FreshnessMS++
			case "lock":
				verb.Locks = []string{"other/lock"}
			case "effect":
				verb.Effects = []string{"write-network"}
			case "revision":
				verb.Revision = "r1"
				verb.TimeoutMS--
			case "scope":
				noun.Scope = "org/other/site/lab"
				verb.Scope = noun.Scope
			case "owner":
				noun.Owner = "other"
				verb.Owner = noun.Owner
			}
			next := catalogFixture(t, noun, verb)
			err := old.CheckProposal(next)
			if (err == nil) != (mode == "tighten") {
				t.Fatal("incorrect proposal result", err)
			}
		})
	}
}

func TestCapabilityIndexUsesExistingNotesAndStableIDs(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	noun, verb := capabilityFixture()
	writeNote(t, dir, "network.md", capabilityMarkdown(noun))
	writeNote(t, dir, "counters.md", capabilityMarkdown(verb))
	c, err := store.SyncCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := c.Snapshot(fixtureScope)
	if err := os.Rename(filepath.Join(dir, "network.md"), filepath.Join(dir, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	c, err = store.SyncCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := c.Snapshot(fixtureScope)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("rename changed stable identity")
	}
	neighbors, err := store.Neighbors(noun.ID)
	if err != nil || len(neighbors) != 1 || neighbors[0] != verb.ID {
		t.Fatal("existing link index not used", err)
	}
	writeNote(t, dir, "duplicate.md", capabilityMarkdown(noun))
	if _, err := store.SyncCapabilities(dir); err == nil {
		t.Fatal("ambiguous index exported")
	}
	if err := os.Remove(filepath.Join(dir, "duplicate.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "counters.md")); err != nil {
		t.Fatal(err)
	}
	c, err = store.SyncCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := c.Snapshot(fixtureScope)
	if len(entries) != 1 {
		t.Fatal("deleted capability remained")
	}
}

func TestSimulatedArmInputsAreBounded(t *testing.T) {
	noun, verb := capabilityFixture()
	noun.ID = "noun/sim-arm"
	noun.Class = "simulated-actuator"
	verb.ID = "verb/move-to"
	verb.Uses = []CapabilityRef{{noun.ID, noun.Revision}}
	verb.Inputs = map[string]CapabilityInput{"pose": {Type: "vector3", Required: true, Unit: "meter", Axes: []CapabilityRange{{-1, 1}, {-1, 1}, {0, 1}}}}
	verb.Preconditions = []string{"sim-mode", "workspace-clear", "controller-ready"}
	verb.Effects = []string{"pose-changed"}
	c := catalogFixture(t, noun, verb)
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		args string
		ok   bool
	}{{`{"pose":[0,0,0.5]}`, true}, {`{"pose":[0,0,-0.1]}`, false}, {`{"pose":[0,0]}`, false}, {`{"pose":[0,"0",0]}`, false}} {
		call := callFixture(t, c, verb.ID)
		call.Arguments = json.RawMessage(tc.args)
		_, err := c.CheckCall(call, now, now, &fixtureAuthority{})
		if (err == nil) != tc.ok {
			t.Fatal("incorrect simulated pose result", err)
		}
	}
}

func TestCapabilityDepthIsIndependentOfMemoizationOrder(t *testing.T) {
	noun, _ := capabilityFixture()
	defs := []Capability{}
	for i := 0; i < 34; i++ {
		n := noun
		n.ID = fmt.Sprintf("noun/level-%d", i)
		if i > 0 {
			n.Requires = []CapabilityRef{{fmt.Sprintf("noun/level-%d", i-1), "r1"}}
		}
		defs = append(defs, n)
	}
	for _, reverse := range []bool{false, true} {
		list := []Note{}
		for i := range defs {
			j := i
			if reverse {
				j = len(defs) - 1 - i
			}
			list = append(list, Parse(fmt.Sprintf("%d.md", j), []byte(capabilityMarkdown(defs[j]))))
		}
		if _, err := NewCapabilityCatalog(list); err == nil {
			t.Fatal("deep graph accepted")
		}
	}
}
