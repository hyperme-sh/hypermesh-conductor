package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHistoryFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Record{TaskID: "t", StepID: "s", OriginalTail: json.RawMessage(`"secret-tail"`)}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Record{TaskID: "t", StepID: "s2", OriginalTail: json.RawMessage(`"again"`)}); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func TestHistoryStaysInsideTheQuota(t *testing.T) {
	previous, previousTask := maxHistoryBytes, maxTaskHistoryBytes
	maxHistoryBytes = 1200
	maxTaskHistoryBytes = 700
	t.Cleanup(func() {
		maxHistoryBytes = previous
		maxTaskHistoryBytes = previousTask
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tail := strings.Repeat("x", 400)
	for i, task := range []string{"a", "a", "b", "a", "b"} {
		err := store.Append(Record{
			TaskID: task, StepID: string(rune('0' + i)),
			OriginalTail: json.RawMessage(`"` + tail + `"`),
		})
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > int64(maxHistoryBytes) || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("size %d mode %o", info.Size(), info.Mode().Perm())
		}
	}
	rows, err := Read(path)
	if err != nil || len(rows) == 0 || rows[len(rows)-1].TaskID != "b" {
		t.Fatal(err, rows)
	}
	var taskA int
	for _, row := range rows {
		if row.TaskID == "a" {
			taskA += len(row.OriginalTail)
		}
	}
	if taskA > maxTaskHistoryBytes {
		t.Fatalf("task quota %d", taskA)
	}
	huge := json.RawMessage(`"` + strings.Repeat("z", maxHistoryBytes) + `"`)
	if err := store.Append(Record{TaskID: "c", StepID: "big", OriginalTail: huge}); err == nil {
		t.Fatal("oversized history row was stored")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > int64(maxHistoryBytes) {
		t.Fatal(err, info.Size())
	}
}

func TestHistoryUpdatesAreSerializedAcrossStores(t *testing.T) {
	previous, previousTask := maxHistoryBytes, maxTaskHistoryBytes
	maxHistoryBytes = 8000
	maxTaskHistoryBytes = 8000
	t.Cleanup(func() {
		maxHistoryBytes = previous
		maxTaskHistoryBytes = previousTask
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("o", 180)
	for i := 0; i < 30; i++ {
		if err := store.Append(Record{TaskID: "old", StepID: fmt.Sprintf("o%d", i), OriginalTail: json.RawMessage(`"` + old + `"`)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- next.Append(Record{TaskID: "new", StepID: fmt.Sprintf("n%d", i), OriginalTail: json.RawMessage(`"row"`)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, row := range rows {
		if row.TaskID == "new" {
			found[row.StepID] = true
		}
	}
	if len(found) != 20 {
		t.Fatalf("kept %d of 20 new rows", len(found))
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > int64(maxHistoryBytes) {
		t.Fatal(err, info.Size())
	}
}

func TestConcurrentStoresPreserveAppendsDuringEviction(t *testing.T) {
	oldTotal, oldTask := maxHistoryBytes, maxTaskHistoryBytes
	maxHistoryBytes, maxTaskHistoryBytes = 10000, 10000
	defer func() { maxHistoryBytes, maxTaskHistoryBytes = oldTotal, oldTask }()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Record{TaskID: "old", Summary: strings.Repeat("x", 9500)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				t.Error(err)
				return
			}
			if err := s.Append(Record{TaskID: fmt.Sprint(i), Summary: strings.Repeat("y", 100)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		seen[r.TaskID] = true
	}
	for i := 0; i < 20; i++ {
		if !seen[fmt.Sprint(i)] {
			t.Fatalf("lost appended record %d", i)
		}
	}
}
