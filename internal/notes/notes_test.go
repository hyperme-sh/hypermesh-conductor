package notes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/compact"
)

func TestIndexRebuildSearchAndGoalRule(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "ship.md", `---
id: ship
type: Goal
status: done
tags: [release]
---
# Ship the router
The goal links to [[classify]].
`)
	writeNote(t, dir, "classify.md", `---
id: classify
type: Steps
status: open
order: 1
---
# Classify
Parse each request.
`)
	store := openTestStore(t, dir)
	stats, err := store.Rebuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 2 || stats.Links != 1 || len(stats.Warnings) != 1 {
		t.Fatalf("%+v", stats)
	}
	if !strings.Contains(stats.Warnings[0], "goal ship cannot close while step classify is open") {
		t.Fatal(stats.Warnings)
	}
	ids, err := store.Search("parse request", 5)
	if err != nil || len(ids) != 1 || ids[0] != "classify" {
		t.Fatal(err, ids)
	}
	neighbors, err := store.Neighbors("classify")
	if err != nil || len(neighbors) != 1 || neighbors[0] != "ship" {
		t.Fatal(err, neighbors)
	}
	primer, err := Primer(store, 400)
	if err != nil || !strings.Contains(primer, "Ship the router") || !strings.Contains(primer, "Classify") {
		t.Fatal(err, primer)
	}
	status, err := store.Status()
	if err != nil || status.Notes != 2 || status.Embedded != 0 || status.Generation == "" {
		t.Fatal(err, status)
	}

	path := filepath.Join(dir, "classify.md")
	updated := strings.Replace(read(t, path), "Parse each request.", "Parse each request again.", 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := store.Sync(dir)
	if err != nil || again.Updated != 1 {
		t.Fatal(err, again)
	}
	next, err := store.Generation()
	if err != nil || next == status.Generation {
		t.Fatal("edit did not change the index generation")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Sync(dir)
	if err != nil || removed.Removed != 1 || removed.Notes != 1 {
		t.Fatal(err, removed)
	}
}

func TestRetrievalCacheAndPrefixInsert(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "alpha.md", `---
id: alpha
type: fact
status: open
---
# Alpha
Alpha fact UNIQUEALPHA.
[[beta]]
`)
	writeNote(t, dir, "beta.md", `---
id: beta
type: note
status: open
---
# Beta
Beta detail UNIQUEBETA.
`)
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.Hops = 1
	service.Budget = 400
	first, err := service.Retrieve("uniquealpha", "alpha", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Block, "UNIQUEALPHA") || !strings.Contains(first.Block, "UNIQUEBETA") {
		t.Fatal(first.Block)
	}
	if !strings.Contains(first.Block, "untrusted") {
		t.Fatal(first.Block)
	}
	if first.Hits != 0 || first.Lookups == 0 {
		t.Fatal(first)
	}
	second, err := service.Retrieve("uniquealpha", "alpha", "", true)
	if err != nil || second.Hits == 0 || second.Block != first.Block {
		t.Fatal(err, second)
	}
	system := `{"role":"system","name":"keep","content":"Stay stable."}`
	body := []byte(`{"messages":[` + system + `,{"role":"user","content":"uniquealpha"}]}`)
	out, err := InsertAfterPrefix(body, first.Block)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), system) || !strings.Contains(string(out), "UNIQUEBETA") {
		t.Fatal(string(out))
	}
	if strings.Index(string(out), system) > strings.Index(string(out), "UNIQUEBETA") {
		t.Fatal("neighborhood was inserted before the prefix")
	}
}

func TestMarkdownDiffIsNotApplied(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "one.md", `---
id: one
type: fact
status: open
---
# Shared
Mentions the widget catalog.
`)
	writeNote(t, dir, "two.md", `---
id: two
type: fact
status: open
---
# Shared
The widget catalog is local.
`)
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	list, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	diff := MarkdownDiffs(list)
	if !strings.Contains(diff, "status: dropped") || !strings.Contains(diff, "[[two]]") && !strings.Contains(diff, "[[one]]") {
		t.Fatal(diff)
	}
	if strings.Contains(read(t, filepath.Join(dir, "one.md")), "status: drop") {
		t.Fatal("diff was applied to the note")
	}
}

func TestPrimerTrimFinishesInsideBudget(t *testing.T) {
	dir := t.TempDir()
	title := strings.Repeat("alpha", 80)
	for i := 0; i < 12; i++ {
		writeNote(t, dir, fmt.Sprintf("fact-%02d.md", i), fmt.Sprintf(`---
id: fact-%02d
type: fact
status: open
---
# %s %02d
`, i, title, i))
	}
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		text, err := Primer(store, 24)
		done <- outcome{text, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if tokens := compact.EstimateTokens(got.text); tokens > 24 {
			t.Fatalf("tokens %d\n%s", tokens, got.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("primer trim did not finish")
	}
}

func TestQuotedFrontmatterDecodesScalars(t *testing.T) {
	note := Parse("alpha.md", []byte("---\nid: \"alpha\"\ntype: \"goal\"\nstatus: \"done\"\norder: \"3\"\n---\n# Alpha\n"))
	if note.ID != "alpha" || note.Type != "goal" || note.Status != "done" || note.Order != 3 || !Closed(note.Status) {
		t.Fatalf("%+v", note)
	}
	single := Parse("beta.md", []byte("---\nid: 'beta'\ntype: 'fact'\nstatus: 'closed'\n---\n# Beta\n"))
	if single.ID != "beta" || single.Type != "fact" || !Closed(single.Status) || !Closed("drop") {
		t.Fatalf("%+v", single)
	}
}

func TestBasenameIDsStayDistinct(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a/index.md", "# Alpha\nALPHAONLY stays.\n")
	writeNote(t, dir, "b/index.md", "# Beta\nBETAONLY stays.\n")
	store := openTestStore(t, dir)
	stats, err := store.Rebuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 2 {
		t.Fatalf("%+v", stats)
	}
	alpha, err := store.Search("alphaonly", 5)
	if err != nil || len(alpha) != 1 || alpha[0] == "index" {
		t.Fatal(err, alpha)
	}
	beta, err := store.Search("betaonly", 5)
	if err != nil || len(beta) != 1 || beta[0] == alpha[0] {
		t.Fatal(err, beta, alpha)
	}
	if _, ok, err := store.Get(alpha[0]); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, ok, err := store.Get(beta[0]); err != nil || !ok {
		t.Fatal(err, ok)
	}
}

func TestExplicitIDCollisionDoesNotReplace(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a/one.md", "---\nid: same\ntype: note\n---\n# One\nALPHAONLY\n")
	writeNote(t, dir, "b/two.md", "---\nid: same\ntype: note\n---\n# Two\nBETAONLY\n")
	store := openTestStore(t, dir)
	stats, err := store.Rebuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 1 || len(stats.Warnings) == 0 {
		t.Fatalf("%+v", stats)
	}
	note, ok, err := store.Get("same")
	if err != nil || !ok || !strings.Contains(note.Body, "ALPHAONLY") || strings.Contains(note.Body, "BETAONLY") {
		t.Fatal(err, ok, note.Body)
	}
	found, err := store.Search("betaonly", 5)
	if err != nil || len(found) != 0 {
		t.Fatal(err, found)
	}
}

func TestDreamDiffSeesStoredLinks(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "alpha.md", `---
id: alpha
type: note
status: open
---
# Alpha
Mentions beta and gamma.
[[beta]]
`)
	writeNote(t, dir, "beta.md", `---
id: beta
type: note
status: open
---
# Beta
Beta detail.
`)
	writeNote(t, dir, "gamma.md", `---
id: gamma
type: note
status: open
---
# Gamma
Gamma detail.
`)
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	list, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	var alpha Note
	for _, note := range list {
		if note.ID == "alpha" {
			alpha = note
		}
	}
	if len(alpha.Links) != 1 || alpha.Links[0] != "beta" {
		t.Fatalf("%+v", alpha.Links)
	}
	diff := MarkdownDiffs(list)
	if strings.Contains(diff, "[[beta]]") || !strings.Contains(diff, "[[gamma]]") {
		t.Fatal(diff)
	}
	// The dream path used to hand MarkdownDiffs notes whose Links field was
	// empty. The wikilink in the body is still an existing link, and it must
	// not crowd out the missing one.
	stripped := append([]Note(nil), list...)
	for i := range stripped {
		stripped[i].Links = nil
	}
	again := MarkdownDiffs(stripped)
	if strings.Contains(again, "[[beta]]") || !strings.Contains(again, "[[gamma]]") {
		t.Fatal(again)
	}
}

func TestRetrieveStaysWithinBudget(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "wide.md", "---\nid: wide\ntype: note\nstatus: open\n---\n# "+strings.Repeat("A", 900)+"\nWIDEMARKER\n")
	writeNote(t, dir, "tiny.md", "---\nid: tiny\ntype: note\nstatus: open\n---\n# Tiny\nTINYMARKER\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.Primer = false
	service.Hops = 0
	service.Budget = 80
	got, err := service.Retrieve("", "wide", "tiny", true)
	if err != nil {
		t.Fatal(err)
	}
	if compact.EstimateTokens(got.Block) > service.Budget {
		t.Fatalf("tokens %d\n%s", compact.EstimateTokens(got.Block), got.Block)
	}
	if strings.Contains(got.Block, "WIDEMARKER") || !strings.Contains(got.Block, "TINYMARKER") || !strings.Contains(got.Block, "untrusted") {
		t.Fatal(got.Block)
	}
	service.Budget = 5
	tight, err := service.Retrieve("", "tiny", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if tight.Block != "" || compact.EstimateTokens(tight.Block) > 5 {
		t.Fatal(tight.Block)
	}
}

func TestRetrieveCountsItsOwnLookups(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "alpha.md", "---\nid: alpha\ntype: fact\nstatus: open\n---\n# Alpha\nUNIQUEALPHA\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]Result, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = service.Retrieve("uniquealpha", "alpha", "", true)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil || results[i].Lookups != 1 {
			t.Fatalf("%d %+v %v", i, results[i], errs[i])
		}
	}
}

func TestDotRootIsIndexed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".notes")
	writeNote(t, dir, "fact.md", "---\nid: fact-1\ntype: fact\nstatus: open\n---\n# Fact\nVISIBLEFACT\n")
	writeNote(t, dir, ".conductor/hidden.md", "---\nid: hidden\ntype: note\n---\n# Hidden\nHIDDENFACT\n")
	store := openTestStore(t, dir)
	stats, err := store.Rebuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 1 {
		t.Fatalf("%+v", stats)
	}
	found, err := store.Search("visiblefact", 5)
	if err != nil || len(found) != 1 || found[0] != "fact-1" {
		t.Fatal(err, found)
	}
	if ids, err := store.Search("hiddenfact", 5); err != nil || len(ids) != 0 {
		t.Fatal(err, ids)
	}
}

func TestUntrustedCallerCannotNameANote(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "secret.md", "---\nid: secret-note\ntype: note\nstatus: open\n---\n# Secret\nSECRETNOTEBODY\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.Primer = false
	got, err := service.Retrieve("hello", "secret-note", "secret-note", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Block, "SECRETNOTEBODY") {
		t.Fatal(got.Block)
	}
	trusted, err := service.Retrieve("hello", "secret-note", "", true)
	if err != nil || !strings.Contains(trusted.Block, "SECRETNOTEBODY") {
		t.Fatal(err, trusted.Block)
	}
}

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func writeNote(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
