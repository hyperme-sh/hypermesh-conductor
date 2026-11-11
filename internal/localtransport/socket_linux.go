//go:build linux

package localtransport

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func validate(path string, socket bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("Conductor socket path must be owned by this user and private")
	}
	if socket {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("Conductor path is not a socket")
		}
	} else if !info.IsDir() {
		return fmt.Errorf("Conductor socket directory is not a directory")
	}
	return nil
}

func Listen(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("Conductor socket path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := validate(dir, false); err != nil {
		return nil, err
	}
	// Never remove an existing path: it may belong to another live instance.
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("Conductor socket path must be absolute")
	}
	if err := validate(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	if err := validate(path, true); err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		conn.Close()
		return nil, err
	}
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			peerErr = e
		} else if cred.Uid != uint32(os.Geteuid()) {
			peerErr = fmt.Errorf("Conductor socket peer belongs to another user")
		}
	})
	if err != nil || peerErr != nil {
		conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, peerErr
	}
	return conn, nil
}
