//go:build darwin

package originguard

import "golang.org/x/sys/unix"

const ioctlGetTermios = unix.TIOCGETA
