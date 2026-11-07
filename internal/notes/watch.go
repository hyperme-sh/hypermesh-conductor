package notes

import (
	"context"
	"time"
)

// Watch polls the notes directory and reindexes files whose content hash changed.
func Watch(ctx context.Context, dir string, store *Store, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = store.Sync(dir)
		}
	}
}
