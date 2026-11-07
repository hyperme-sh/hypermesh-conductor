//go:build unix

package notes

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFIFOReadCannotBlockIndexing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipe.md")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() { _, err := readRootNote(root, dir, path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked read")
	}
	// Exercise the open flags even if an attacker swaps the FIFO after Lstat.
	go func() {
		f, err := root.OpenFile("pipe.md", noteOpenFlags, 0)
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("raced FIFO blocked open")
	}
}
