//go:build !linux

package capabilitydispatch

import (
	"net"
	"os"
)

func checkPeer(net.Conn, uint32) error   { return Refused }
func privateDirectory(string) error      { return Refused }
func ownedFile(string) (*os.File, error) { return nil, Refused }
