//go:build !unix

package notes

import "os"

const noteOpenFlags = os.O_RDONLY

const indexDirOpenFlags = os.O_RDONLY

const indexFileOpenFlags = os.O_RDWR

func singleIndexLink(info os.FileInfo) bool { return true }
