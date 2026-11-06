package aiuditor

import (
	"io"
	"os"
)

func osOpen(path string) (io.ReadCloser, error) {
	return os.Open(path)
}
