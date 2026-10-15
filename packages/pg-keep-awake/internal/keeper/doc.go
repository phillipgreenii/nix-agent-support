// Package keeper holds the single process that owns the keep-awake assertion
// (one caffeinate). This part is its lease maintenance: the Sweeper reaps
// dead, expired and orphaned leases under leases/.lock (see sweep.go) and runs
// the stuck-client backstop (see backstop.go). The tick loop, the heartbeat
// and spawning the assertion are filled in by later packets.
package keeper
