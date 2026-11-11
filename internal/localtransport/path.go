package localtransport

import (
	"os"
	"path/filepath"
)

// DefaultPath is private to the logged-in OS user, not a system-wide port.
func DefaultPath() (string, error) {
	if path := os.Getenv("HYPERMESH_CONDUCTOR_SOCKET"); path != "" {
		return filepath.Abs(path)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "hypermesh", "conductor", "run", "conductor.sock"), nil
}
