// Package history stores original tails that compaction replaced.
//
// This file is the recovery store. It is not the metrics journal, and the
// dream command does not read it unless prompt bodies are explicitly opted in.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const Schema = "hypermesh.conductor.history.v0"

// maxHistoryBytes is the total size of history.jsonl. maxTaskHistoryBytes
// is the portion one task id may occupy. Both are quotas, not per-append caps.
var (
	maxHistoryBytes     = 32 << 20
	maxTaskHistoryBytes = 8 << 20
	// Stores are opened per request. Serialize the complete history update
	// across instances, including quota eviction and atomic replacement.
	historyMu sync.Mutex
)

// Record is one original tail.
type Record struct {
	Schema       string          `json:"schema"`
	TaskID       string          `json:"task_id"`
	StepID       string          `json:"step_id"`
	PrefixSHA256 string          `json:"prefix_sha256"`
	Summary      string          `json:"summary"`
	OriginalTail json.RawMessage `json:"original_tail"`
}

// Store is an append-only history file next to the metrics journal.
type Store struct {
	path string
}

// PathFor returns the history file beside a metrics journal.
func PathFor(journalPath string) string {
	return filepath.Join(filepath.Dir(journalPath), "history.jsonl")
}

// Open creates a store. The parent directory must already exist.
func Open(path string) (*Store, error) {
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() {
		return nil, err
	}
	return &Store{path: path}, nil
}

// Append writes one original tail. The file stays within the global quota
// by dropping the oldest rows, and within the per-task quota the same way.
// A single row larger than either quota is refused and nothing is written.
func (s *Store) Append(record Record) error {
	record.Schema = Schema
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	need := len(raw) + 1
	if need > maxHistoryBytes || need > maxTaskHistoryBytes {
		return fmt.Errorf("history record exceeds the quota")
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	if err := s.makeRoom(record.TaskID, need); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := ensureOwnerOnly(s.path); err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) makeRoom(task string, need int) error {
	existing, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var lines [][]byte
	start := 0
	for i := 0; i <= len(existing); i++ {
		if i == len(existing) || existing[i] == '\n' {
			if i > start {
				lines = append(lines, append([]byte(nil), existing[start:i]...))
			}
			start = i + 1
		}
	}
	total := 0
	taskTotal := 0
	tasks := make([]string, len(lines))
	sizes := make([]int, len(lines))
	for i, line := range lines {
		sizes[i] = len(line) + 1
		tasks[i] = taskIDOf(line)
		total += sizes[i]
		if tasks[i] == task {
			taskTotal += sizes[i]
		}
	}
	drop := 0
	for drop < len(lines) && (total+need > maxHistoryBytes || taskTotal+need > maxTaskHistoryBytes) {
		total -= sizes[drop]
		if tasks[drop] == task {
			taskTotal -= sizes[drop]
		}
		drop++
	}
	if total+need > maxHistoryBytes || taskTotal+need > maxTaskHistoryBytes {
		return fmt.Errorf("history record exceeds the quota")
	}
	if drop == 0 {
		return nil
	}
	return s.rewrite(lines[drop:])
}

func taskIDOf(line []byte) string {
	var row struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return ""
	}
	return row.TaskID
}

func (s *Store) rewrite(lines [][]byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".history-*.jsonl")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	for _, line := range lines {
		if _, err := tmp.Write(append(line, '\n')); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	ok = true
	return ensureOwnerOnly(s.path)
}

// ensureOwnerOnly keeps prompt tails unreadable by other accounts.
// An existing group- or world-readable file is tightened before the write.
func ensureOwnerOnly(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("history file is readable by other users")
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("history file is readable by other users")
	}
	return nil
}

// Read loads every history row.
func Read(path string) ([]Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			line := raw[start:i]
			start = i + 1
			if len(line) == 0 {
				continue
			}
			var record Record
			if err := json.Unmarshal(line, &record); err != nil {
				return nil, err
			}
			out = append(out, record)
		}
	}
	return out, nil
}
