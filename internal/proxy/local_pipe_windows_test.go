//go:build windows

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/localtransport"
)

func TestPrivatePipeHTTPAndShutdown(t *testing.T) {
	t.Setenv("HYPERMESH_CONDUCTOR_SOCKET", "")
	t.Setenv("HYPERMESH_CONDUCTOR_INSTANCE", fmt.Sprintf("proxy-%d", time.Now().UnixNano()))
	path, err := localtransport.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := localtransport.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- New(config.Config{}, nil, nil).ServeLocalListenerContext(ctx, listener) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return localtransport.DialContext(ctx, path) }}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("http://local/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("models status: %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipe shutdown stalled")
	}
	next, err := localtransport.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
