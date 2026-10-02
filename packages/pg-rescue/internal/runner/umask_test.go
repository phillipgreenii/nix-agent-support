package runner_test

import "syscall"

func setUmask(m int) int { return syscall.Umask(m) }
