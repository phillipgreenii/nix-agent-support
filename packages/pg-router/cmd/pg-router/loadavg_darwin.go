//go:build darwin

package main

import "golang.org/x/sys/unix"

// sysctlLoad1 reads vm.loadavg in-process (no cgo, no process launch).
func sysctlLoad1() (float64, bool) {
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, false
	}
	return parseKernLoadavg(b)
}
