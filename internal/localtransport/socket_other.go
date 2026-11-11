//go:build !linux

package localtransport

import (
	"context"
	"fmt"
	"net"
)

func Listen(path string) (net.Listener, error) {
	return nil, fmt.Errorf("protected Conductor sockets currently require Linux; use verified HTTPS on this platform")
}
func DialContext(ctx context.Context, path string) (net.Conn, error) {
	return nil, fmt.Errorf("protected Conductor sockets currently require Linux; use verified HTTPS on this platform")
}
