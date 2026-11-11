//go:build linux

package proxy

import (
	"context"
	"errors"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/localtransport"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalShutdownRemovesSocketAndAllowsRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "conductor.sock")
	listener, err := localtransport.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- New(config.Config{}, nil, nil).ServeLocalListenerContext(ctx, listener) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stalled")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
	next, err := localtransport.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
