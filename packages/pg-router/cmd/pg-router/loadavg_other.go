//go:build !darwin

package main

// sysctlLoad1 has no source off darwin: Linux is served by /proc/loadavg.
func sysctlLoad1() (float64, bool) { return 0, false }
