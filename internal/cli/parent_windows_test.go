//go:build windows

package cli

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestWindowsRejectsUnixParentWatchBeforeConfig(t *testing.T) {
	var out bytes.Buffer
	code := Main([]string{"serve", "--socket", "--parent-pid", strconv.Itoa(os.Getppid())}, &out, &out)
	if code != 2 || !strings.Contains(out.String(), "parent-pid requires Linux or macOS") {
		t.Fatalf("code %d: %s", code, out.String())
	}
}
