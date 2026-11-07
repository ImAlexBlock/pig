//go:build !darwin && !linux

package ipc

import "net"

// remoteUID is unavailable on this platform; file permissions are the only
// guard.
func remoteUID(net.Conn) int { return -1 }
