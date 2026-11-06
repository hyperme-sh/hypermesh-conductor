package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
