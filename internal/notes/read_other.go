//go:build !unix

package notes

import (
	"os"
	"path/filepath"
)

func readNoteBytes(dir *os.File, rel string) ([]byte, error) {
	path := filepath.Join(dir.Name(), rel)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errSkipNote
	}
	return os.ReadFile(path)
}
