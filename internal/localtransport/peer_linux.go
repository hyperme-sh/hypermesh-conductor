//go:build linux

package localtransport

import (
	"fmt"
	"os"
	"syscall"
)

func checkPeer(fd int) error {
	cred, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return err
	}
	if cred.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("Conductor socket peer belongs to another user")
	}
	return nil
}
