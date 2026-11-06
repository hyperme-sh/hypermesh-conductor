package notes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	first, err := service.Retrieve("uniquealpha", "alpha", "")
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
	second, err := service.Retrieve("uniquealpha", "alpha", "")
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
	if !strings.Contains(diff, "status: drop") || !strings.Contains(diff, "[[two]]") && !strings.Contains(diff, "[[one]]") {
		t.Fatal(diff)
	}
	if strings.Contains(read(t, filepath.Join(dir, "one.md")), "status: drop") {
		t.Fatal("diff was applied to the note")
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
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
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
