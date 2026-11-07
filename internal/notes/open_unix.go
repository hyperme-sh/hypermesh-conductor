//go:build unix

package notes

import (
	"os"
	"syscall"
)

// A regular file swapped for a FIFO must not block before f.Stat can reject it.
const noteOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK

const indexDirOpenFlags = os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

const indexFileOpenFlags = os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func singleIndexLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func trustedIndexDirectory(info os.FileInfo, leaf bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != os.Geteuid() && (leaf || stat.Uid != 0)) {
		return false
	}
	return info.Mode().Perm()&0o022 == 0 || (!leaf && info.Mode()&os.ModeSticky != 0)
}
