package cli

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedParentExitCancelsWork(t *testing.T) {
	var parent atomic.Int64
	parent.Store(42)
	ticks := make(chan time.Time, 2)
	ctx, stop, err := watchParent(context.Background(), 42, func() int { return int(parent.Load()) }, ticks)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ticks <- time.Now()
	select {
	case <-ctx.Done():
		t.Fatal("live parent cancelled")
	default:
	}
	parent.Store(1)
	ticks <- time.Now()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent exit did not cancel")
	}
}
func TestManagedParentRefusesStaleOrInitParent(t *testing.T) {
	for _, id := range []int{-1, 0, 1, 43} {
		if _, _, err := watchParent(context.Background(), id, func() int { return 42 }, nil); err == nil {
			t.Fatalf("accepted parent %d", id)
		}
	}
	var out bytes.Buffer
	if code := Main([]string{"serve", "--parent-pid", "42"}, &out, &out); code != 2 {
		t.Fatal("accepted parent watch on network server")
	}
}
func TestManagedParentWatchStopsOnNormalShutdown(t *testing.T) {
	outer, stopOuter := context.WithCancel(context.Background())
	ctx, stop, err := watchParent(outer, 42, func() int { return 42 }, make(chan time.Time))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stopOuter()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel")
	}
}
