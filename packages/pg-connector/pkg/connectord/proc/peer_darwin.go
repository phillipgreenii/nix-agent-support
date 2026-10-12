//go:build darwin

package proc

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// sunPathCapacity is sizeof(sockaddr_un.sun_path) on darwin.
const sunPathCapacity = 104

// MaxSocketPathBytes is the size of sockaddr_un.sun_path on this platform: 104
// bytes on darwin. The path and its terminating NUL MUST fit, so the longest
// usable path is one byte shorter (see CheckSocketPath).
func MaxSocketPathBytes() int { return sunPathCapacity }

// PeerUID returns the effective uid of the process on the other end of a unix
// socket connection: LOCAL_PEERCRED (the getpeereid(3) mechanism) on darwin.
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
		xucred  *unix.Xucred
		sockErr error
	)
	if err := raw.Control(func(fd uintptr) {
		xucred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	if sockErr != nil {
		return 0, fmt.Errorf("peer credentials (LOCAL_PEERCRED): %w", sockErr)
	}
	if xucred == nil {
		return 0, errors.New("peer credentials (LOCAL_PEERCRED): empty result")
	}
	return xucred.Uid, nil
}
