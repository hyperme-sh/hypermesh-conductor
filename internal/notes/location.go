package notes

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenCollection keeps the derived DB out of the collaborator-writable tree.
// SQLite opens pages and sidecars by pathname, so the location must remain
// owner-controlled throughout the lifetime of the Store.
func OpenCollection(dir, configured string) (*Store, error) {
	collection, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	realCollection, err := filepath.EvalSymlinks(collection)
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	if configured == "" {
		configured = filepath.Join(cache, "hypermesh-conductor", "notes", fmt.Sprintf("%x", sha256.Sum256([]byte(realCollection))), "index.db")
	}
	path, err := filepath.Abs(configured)
	if err != nil {
		return nil, err
	}
	if within(collection, path) || within(realCollection, path) {
		return nil, fmt.Errorf("SQLite index must be outside the notes directory")
	}
	parent, err := canonicalIndexParent(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if within(realCollection, parent) {
		return nil, fmt.Errorf("SQLite index must be outside the notes directory")
	}
	if runtime.GOOS == "windows" {
		privateCache, err := canonicalIndexParent(cache)
		if err != nil {
			return nil, err
		}
		if !within(privateCache, parent) {
			return nil, fmt.Errorf("SQLite index must be under the user's local cache directory")
		}
	}
	if err := ensureIndexParent(parent, true); err != nil {
		return nil, err
	}
	store, err := Open(filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return nil, err
	}
	store.collection = realCollection
	return store, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Resolve existing aliases before checking ancestry and creating missing dirs.
func canonicalIndexParent(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	real, err = canonicalIndexParent(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, filepath.Base(path)), nil
}

func ensureIndexParent(path string, leaf bool) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := ensureIndexParent(parent, false); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err = os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || !trustedIndexDirectory(info, leaf) {
		return fmt.Errorf("index directory is not owner-controlled: %s", path)
	}
	return nil
}
