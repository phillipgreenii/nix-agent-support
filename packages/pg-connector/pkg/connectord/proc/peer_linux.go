//go:build linux

package proc

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// sunPathCapacity is sizeof(sockaddr_un.sun_path) on Linux.
const sunPathCapacity = 108

// MaxSocketPathBytes is the size of sockaddr_un.sun_path on this platform: 108
// bytes on Linux. The path and its terminating NUL MUST fit, so the longest
// path the shell accepts is one byte shorter (see CheckSocketPath).
func MaxSocketPathBytes() int { return sunPathCapacity }

// PeerUID returns the effective uid of the process on the other end of a unix
// socket connection: SO_PEERCRED on Linux.
func PeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peer credentials need a unix socket connection, got %T", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	var (
		ucred   *unix.Ucred
		sockErr error
	)
	if err := raw.Control(func(fd uintptr) {
		ucred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	if sockErr != nil {
		return 0, fmt.Errorf("peer credentials (SO_PEERCRED): %w", sockErr)
	}
	if ucred == nil {
		return 0, errors.New("peer credentials (SO_PEERCRED): empty result")
	}
	return ucred.Uid, nil
}
