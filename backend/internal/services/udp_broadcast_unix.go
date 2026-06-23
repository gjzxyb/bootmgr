//go:build !windows

package services

import (
	"net"

	"golang.org/x/sys/unix"
)

func enableUDPBroadcast(conn *net.UDPConn) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := rawConn.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
