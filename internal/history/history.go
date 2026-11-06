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
	mu   sync.Mutex
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

// Append writes one original tail.
func (s *Store) Append(record Record) error {
	record.Schema = Schema
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
