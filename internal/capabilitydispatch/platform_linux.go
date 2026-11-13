//go:build linux

package capabilitydispatch

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func checkPeer(conn net.Conn, uid uint32) error {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return Refused
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return Refused
	}
	var cred *syscall.Ucred
	var peerErr error
	if raw.Control(func(fd uintptr) {
		cred, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}) != nil || peerErr != nil || cred == nil || cred.Uid != uid {
		return Refused
	}
	return nil
}
func privateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Refused
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		st, err := os.Lstat(dir)
		if err != nil {
			return Refused
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || (st.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && st.Mode()&os.ModeSticky != 0)) {
			return Refused
		}
		if dir == path && (stat.Uid != uint32(os.Geteuid()) || st.Mode().Perm() != 0700) {
			return Refused
		}
		if dir == "/" {
			break
		}
	}
	return nil
}
func ownedFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, Refused
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, Refused
	}
	file := os.NewFile(uintptr(fd), path)
	st, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, Refused
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || stat.Nlink != 1 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || st.Mode().Perm()&0022 != 0 {
		file.Close()
		return nil, Refused
	}
	return file, nil
}
