//go:build darwin

package copier

import "syscall"

func birthNS(_ string, st *syscall.Stat_t) int64 { return st.Birthtimespec.Nano() }
