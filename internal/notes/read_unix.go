//go:build unix

package notes

import (
	"errors"
	"io"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// readNoteBytes opens rel beneath dir without following a symlink. The file
// description is checked after open, so a rename onto the path between a
// stat and a later read cannot redirect the read outside the notes directory.
func readNoteBytes(dir *os.File, rel string) ([]byte, error) {
	fd, err := openNote(int(dir.Fd()), rel)
	if err != nil {
		if rejectedNoteOpen(err) {
			return nil, errSkipNote
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errSkipNote
	}
	return io.ReadAll(f)
}

func openNote(dirfd int, rel string) (int, error) {
	if runtime.GOOS == "linux" {
		fd, err := unix.Openat2(dirfd, rel, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
		})
		if err == nil || !fallbackOpenat2(err) {
			return fd, err
		}
	}
	return unix.Openat(dirfd, rel, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

func fallbackOpenat2(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP)
}

func rejectedNoteOpen(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EPERM)
}
