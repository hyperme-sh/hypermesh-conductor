//go:build !unix

package notes

import "os"

const noteOpenFlags = os.O_RDONLY
