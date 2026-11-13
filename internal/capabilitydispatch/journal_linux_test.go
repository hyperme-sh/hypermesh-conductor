//go:build linux

package capabilitydispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalSurvivesRestartAndRefusesSymlinks(t *testing.T) {
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	path := filepath.Join(directory, "dispatch.db")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Reserve("call", "hash"); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal, err = OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if journal.Reserve("call", "changed") == nil {
		t.Fatal("call replayed after restart")
	}
	alias := filepath.Join(directory, "alias.db")
	os.Symlink(path, alias)
	if other, err := OpenJournal(alias); err == nil {
		other.Close()
		t.Fatal("symlink journal accepted")
	}
}
