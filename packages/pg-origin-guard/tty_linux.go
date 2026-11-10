//go:build linux

package originguard

import "golang.org/x/sys/unix"

const ioctlGetTermios = unix.TCGETS
