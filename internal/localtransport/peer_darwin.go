//go:build darwin

package localtransport

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func checkPeer(fd int) error {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return err
	}
	if cred.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("Conductor socket peer belongs to another user")
	}
	return nil
}
