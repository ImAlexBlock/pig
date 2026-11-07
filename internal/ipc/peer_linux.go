//go:build linux

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

func remoteUID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1
	}
	uid := -1
	_ = raw.Control(func(fd uintptr) {
		cred, cerr := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if cerr == nil {
			uid = int(cred.Uid)
		}
	})
	return uid
}
