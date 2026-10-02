package beadhandler

import "syscall"

func sysProcGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
