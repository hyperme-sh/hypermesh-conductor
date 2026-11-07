package notes

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestSyncUpdatesDerivedIDsWhenABasenameCollides(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a/index.md", "# Alpha\nALPHAONLY stays.\n")
	store := openTestStore(t, dir)
	if _, err := store.Sync(dir); err != nil {
		t.Fatal(err)
	}
	alone, err := store.Search("alphaonly", 5)
	if err != nil || len(alone) != 1 || alone[0] != "index" {
		t.Fatal(err, alone)
	}
	writeNote(t, dir, "b/index.md", "# Beta\nBETAONLY stays.\n")
	if _, err := store.Sync(dir); err != nil {
		t.Fatal(err)
	}
	alpha, err := store.Search("alphaonly", 5)
	if err != nil || len(alpha) != 1 || alpha[0] != "a/index" {
		t.Fatal(err, alpha)
	}
	beta, err := store.Search("betaonly", 5)
	if err != nil || len(beta) != 1 || beta[0] != "b/index" {
		t.Fatal(err, beta)
	}
	if _, ok, err := store.Get("index"); err != nil || ok {
		t.Fatal(err, ok)
	}
}

func TestLinkResolutionPrefersExactID(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "alpha.md", "---\nid: alpha\ntype: note\nstatus: open\n---\n# Beta\nTitle matches the other id.\n")
	writeNote(t, dir, "beta.md", "---\nid: beta\ntype: note\nstatus: open\n---\n# Plain\nThe id is beta.\n")
	writeNote(t, dir, "gamma.md", "---\nid: gamma\ntype: note\nstatus: open\n---\n# Other\nTitle-only target.\n")
	writeNote(t, dir, "source.md", "---\nid: source\ntype: note\nstatus: open\n---\n# Source\n[[beta]]\n[[Other]]\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	neighbors, err := store.Neighbors("source")
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 2 || neighbors[0] != "beta" || neighbors[1] != "gamma" {
		t.Fatalf("%v", neighbors)
	}
}

func TestSearchSplitsPunctuationLikeTheIndex(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "secret.md", "---\nid: secret\ntype: note\n---\n# Secret\nUse api_key and foo-bar here.\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"api_key", "foo-bar"} {
		ids, err := store.Search(query, 5)
		if err != nil || len(ids) != 1 || ids[0] != "secret" {
			t.Fatal(query, err, ids)
		}
	}
}

func TestSyncFollowsSwappedNoteIDs(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a.md", "---\nid: alpha\ntype: note\n---\n# A\nALPHABODY\n")
	writeNote(t, dir, "b.md", "---\nid: beta\ntype: note\n---\n# B\nBETABODY\n")
	store := openTestStore(t, dir)
	if _, err := store.Sync(dir); err != nil {
		t.Fatal(err)
	}
	writeNote(t, dir, "a.md", "---\nid: beta\ntype: note\n---\n# B\nBETABODY\n")
	writeNote(t, dir, "b.md", "---\nid: alpha\ntype: note\n---\n# A\nALPHABODY\n")
	stats, err := store.Sync(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 2 || len(stats.Warnings) != 0 {
		t.Fatalf("%+v", stats)
	}
	alpha, ok, err := store.Get("alpha")
	if err != nil || !ok || !strings.HasSuffix(alpha.Path, "b.md") || !strings.Contains(alpha.Body, "ALPHABODY") {
		t.Fatal(err, ok, alpha)
	}
	beta, ok, err := store.Get("beta")
	if err != nil || !ok || !strings.HasSuffix(beta.Path, "a.md") || !strings.Contains(beta.Body, "BETABODY") {
		t.Fatal(err, ok, beta)
	}
}

func TestReadNoteBytesDoesNotFollowASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "environ.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDESECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "secret.md")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, err := readRootNote(root, dir, filepath.Join(dir, "secret.md"))
	if err == nil || bytes.Contains(data, []byte("OUTSIDESECRET")) {
		t.Fatal(err, data)
	}
}

func TestSymlinkNotesAreNotIndexed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "environ.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDESECRET from another file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "secret.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, dir, "local.md", "---\nid: local\ntype: note\n---\n# Local\nLOCALBODY stays.\n")
	store := openTestStore(t, dir)
	stats, err := store.Rebuild(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 1 {
		t.Fatalf("%+v", stats)
	}
	if ids, err := store.Search("outsidesecret", 5); err != nil || len(ids) != 0 {
		t.Fatal(err, ids)
	}
	if _, ok, err := store.Get("secret"); err != nil || ok {
		t.Fatal(err, ok)
	}
	note, ok, err := store.Get("local")
	if err != nil || !ok || !strings.Contains(note.Body, "LOCALBODY") {
		t.Fatal(err, ok, note.Body)
	}
}

func TestSyncFollowsAMovedNote(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a/alpha.md", "---\nid: alpha\ntype: note\nstatus: open\n---\n# Alpha\nMOVEDBODY\n")
	store := openTestStore(t, dir)
	if _, err := store.Sync(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "a", "alpha.md"), filepath.Join(dir, "b", "alpha.md")); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Sync(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Notes != 1 {
		t.Fatalf("%+v", stats)
	}
	note, ok, err := store.Get("alpha")
	if err != nil || !ok || !strings.Contains(note.Body, "MOVEDBODY") || !strings.HasSuffix(note.Path, filepath.Join("b", "alpha.md")) {
		t.Fatal(err, ok, note)
	}
}

func TestIndexIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".conductor", "index.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	check := func() {
		t.Helper()
		for _, candidate := range []string{path, filepath.Dir(path)} {
			info, err := os.Stat(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s mode %o", candidate, info.Mode().Perm())
			}
		}
	}
	check()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	check()
}

func TestOpenLeavesAnExistingDatabaseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "index.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o077 == 0 {
		t.Fatalf("existing directory mode %o", dirInfo.Mode().Perm())
	}
	dbInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if dbInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("database mode %o", dbInfo.Mode().Perm())
	}
}

func TestDatabasePathWithURICharactersStaysInPlace(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "notes?archive")
	path := filepath.Join(dir, ".conductor", "index.db")
	writeNote(t, dir, "secret.md", "---\nid: secret\ntype: note\n---\n# Secret\nURIKEEP stays here.\n")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "notes")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	ids, err := store.Search("urikeep", 5)
	if err != nil || len(ids) != 1 || ids[0] != "secret" {
		t.Fatal(err, ids)
	}
}

func TestSearchKeepsUnicodeWords(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "secret.md", "---\nid: secret\ntype: note\n---\n# Secret\nThe contraseña is local. Приветствие stays.\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	spanish, err := store.Search("contraseña", 5)
	if err != nil || len(spanish) != 1 || spanish[0] != "secret" {
		t.Fatal(err, spanish)
	}
	cyrillic, err := store.Search("Приветствие", 5)
	if err != nil || len(cyrillic) != 1 || cyrillic[0] != "secret" {
		t.Fatal(err, cyrillic)
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

func TestSyncUpdatesDerivedIDsWhenCollisionAppears(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a/index.md", "# Alpha\nALPHAONLY\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	writeNote(t, dir, "b/index.md", "# Beta\n[[a/index]]\n")
	if _, err := store.Sync(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("a/index"); err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, ok, err := store.Get("index"); err != nil || ok {
		t.Fatal(err, ok)
	}
	neighbors, err := store.Neighbors("b/index")
	if err != nil || len(neighbors) != 1 || neighbors[0] != "a/index" {
		t.Fatal(err, neighbors)
	}
}

func TestExactIDWinsOverTitleWhenResolvingLinks(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a.md", "---\nid: alpha\ntype: note\n---\n# beta\nWrong target\n")
	writeNote(t, dir, "b.md", "---\nid: beta\ntype: steps\nstatus: open\n---\n# Actual step\n")
	writeNote(t, dir, "goal.md", "---\nid: goal\ntype: goal\nstatus: done\n---\n# Goal\n[[beta]]\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	neighbors, err := store.Neighbors("goal")
	if err != nil || len(neighbors) != 1 || neighbors[0] != "beta" {
		t.Fatal(err, neighbors)
	}
	violations, err := store.Violations()
	if err != nil || len(violations) != 1 {
		t.Fatal(err, violations)
	}
}

func TestIndexPreservesExistingParentPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("changed parent to %o", info.Mode().Perm())
	}
	db, err := os.Stat(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	if db.Mode().Perm() != 0o600 {
		t.Fatalf("database mode %o", db.Mode().Perm())
	}
}

func TestIndexPathMetacharactersAndUnicodeSearch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes?archive#100%")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(t.TempDir(), "notes#100%")
	}
	writeNote(t, dir, "words.md", "# Words\ncontraseña привет 世界\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"contraseña", "привет", "世界"} {
		found, err := store.Search(term, 5)
		if err != nil || len(found) != 1 {
			t.Fatalf("search %q: %v %v", term, found, err)
		}
	}
}

func TestSearchSplitsPunctuationLikeFTS(t *testing.T) {
	for _, text := range []string{"api_key", "foo-bar", "owner.service"} {
		t.Run(text, func(t *testing.T) {
			dir := t.TempDir()
			writeNote(t, dir, "identifier.md", "# Identifier\n"+text+"\n")
			store := openTestStore(t, dir)
			if _, err := store.Rebuild(dir); err != nil {
				t.Fatal(err)
			}
			got, err := store.Search(text, 5)
			if err != nil || len(got) != 1 {
				t.Fatalf("search %q: %v %v", text, got, err)
			}
		})
	}
}

func TestSyncSwappedExplicitIDs(t *testing.T) {
	dir := t.TempDir()
	alpha := "---\nid: alpha\n---\n# Alpha\nALPHABODY [[beta]]\n"
	beta := "---\nid: beta\n---\n# Beta\nBETABODY [[alpha]]\n"
	writeNote(t, dir, "a.md", alpha)
	writeNote(t, dir, "b.md", beta)
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	writeNote(t, dir, "a.md", beta)
	writeNote(t, dir, "b.md", alpha)
	for i := 0; i < 2; i++ {
		stats, err := store.Sync(dir)
		if err != nil || len(stats.Warnings) > 0 {
			t.Fatalf("sync: %+v %v", stats, err)
		}
		for id, path := range map[string]string{"alpha": "b.md", "beta": "a.md"} {
			note, ok, err := store.Get(id)
			if err != nil || !ok || note.Path != filepath.Join(dir, path) {
				t.Fatalf("%s: %+v %v", id, note, err)
			}
		}
		if stats.Links != 2 {
			t.Fatalf("swapped link graph: %+v", stats)
		}
	}
}

func TestRootedNoteReadRejectsSwappedSymlinksAndParentEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"direct.md", "parent"} {
		target := outside
		if name == "direct.md" {
			target = filepath.Join(outside, "secret.md")
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if name == "parent" {
			path = filepath.Join(path, "secret.md")
		}
		if raw, err := readRootNote(root, dir, path); err == nil {
			t.Fatalf("escaped root: %q", raw)
		}
	}
}

func TestRenameInvalidatesCachedResolvedNeighborhood(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "old.md", "---\nid: target\ntype: note\n---\nTARGETBODY\n")
	writeNote(t, dir, "link.md", "---\nid: linker\ntype: note\n---\n# Link\n[[new]]\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.Primer = false
	before, err := service.Retrieve("", "linker", "", true)
	if err != nil || strings.Contains(before.Block, "TARGETBODY") {
		t.Fatal(err, before)
	}
	if err := os.Rename(filepath.Join(dir, "old.md"), filepath.Join(dir, "new.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(dir); err != nil {
		t.Fatal(err)
	}
	after, err := service.Retrieve("", "linker", "", true)
	if err != nil || after.Hits != 0 || !strings.Contains(after.Block, "TARGETBODY") {
		t.Fatal(err, after)
	}
}

func TestPrimerRetainsBlockedGoalWithSpacesInID(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "Ship release.md", "---\ntype: goal\nstatus: done\n---\n# Shipping\n[[openstep]]\n")
	writeNote(t, dir, "openstep.md", "---\ntype: steps\nstatus: open\n---\n# Unfinished work\n")
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	primer, err := Primer(store, 200)
	if err != nil || !strings.Contains(primer, "Shipping") || !strings.Contains(primer, "Unfinished work") {
		t.Fatal(err, primer)
	}
}

func TestNoteReadRejectsSparseFileBeforeAllocation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.md")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	file.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if body, err := readRootNote(root, dir, path); err == nil || len(body) != 0 {
		t.Fatal(err, len(body))
	}
}
func TestSyncRejectsAggregateSizeBeforeReplacingExistingIndex(t *testing.T) {
	old := maxSyncBytes
	maxSyncBytes = 200
	t.Cleanup(func() { maxSyncBytes = old })
	dir := t.TempDir()
	writeNote(t, dir, "one.md", strings.Repeat("a", 120))
	store := openTestStore(t, dir)
	if _, err := store.Rebuild(dir); err != nil {
		t.Fatal(err)
	}
	writeNote(t, dir, "two.md", strings.Repeat("b", 120))
	if _, err := store.Sync(dir); err == nil {
		t.Fatal("aggregate quota not enforced")
	}
	all, err := store.All()
	if err != nil || len(all) != 1 {
		t.Fatal(err, all)
	}
}
