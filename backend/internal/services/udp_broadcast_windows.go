//go:build windows

package services

import (
	"net"

	"golang.org/x/sys/windows"
)

func enableUDPBroadcast(conn *net.UDPConn) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := rawConn.Control(func(fd uintptr) {
		sockErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
