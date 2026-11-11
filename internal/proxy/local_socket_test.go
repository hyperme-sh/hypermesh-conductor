//go:build linux

package proxy

import (
	"context"
	"errors"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/localtransport"
	"net"
	"net/http"
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
		if !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
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

func TestClosingFullLimiterUnblocksAccept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "conductor.sock")
	listener, err := localtransport.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	limiter := limitConns(listener, 1).(*connLimiter)
	limiter.slots <- struct{}{}
	done := make(chan error, 1)
	go func() { _, err := limiter.Accept(); done <- err }()
	limiter.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("full limiter did not stop")
	}
}
