package cli

import (
	"context"
	"errors"
	"os"
	"time"
)

func managedParentContext(ctx context.Context, expected int) (context.Context, context.CancelFunc, error) {
	if expected == 0 {
		child, cancel := context.WithCancel(ctx)
		return child, cancel, nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	child, cancel, err := watchParent(ctx, expected, os.Getppid, ticker.C)
	if err != nil {
		ticker.Stop()
		return nil, nil, err
	}
	return child, func() { cancel(); ticker.Stop() }, nil
}

// The OS parent relation cannot be replaced by an unrelated process reusing a PID.
func watchParent(ctx context.Context, expected int, current func() int, ticks <-chan time.Time) (context.Context, context.CancelFunc, error) {
	if expected < 2 || current() != expected {
		return nil, nil, errors.New("managed parent is unavailable")
	}
	child, cancel := context.WithCancel(ctx)
	go func() {
		for {
			select {
			case <-child.Done():
				return
			case _, ok := <-ticks:
				if !ok || current() != expected {
					cancel()
					return
				}
			}
		}
	}()
	return child, cancel, nil
}
