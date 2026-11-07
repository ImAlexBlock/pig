//go:build darwin

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
		xu, xerr := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if xerr == nil {
			uid = int(xu.Uid)
		}
	})
	return uid
}
