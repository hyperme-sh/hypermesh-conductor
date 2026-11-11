//go:build linux

package localtransport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "conductor.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := DialContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := Listen(path); err == nil {
		t.Fatal("replaced a live socket")
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if conn, err := DialContext(context.Background(), path); err == nil {
		conn.Close()
		t.Fatal("accepted public directory")
	}
}
func TestRejectSymlinkAndRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(link, "conductor.sock")); err == nil {
		t.Fatal("accepted symlink directory")
	}
	path := filepath.Join(target, "conductor.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("replaced existing file")
	}
	if _, err := DialContext(context.Background(), path); err == nil {
		t.Fatal("accepted regular file")
	}
}
