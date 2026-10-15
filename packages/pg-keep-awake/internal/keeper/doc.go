// Package keeper holds the single process that owns the keep-awake assertion
// (one caffeinate). Its parts so far:
//
//   - lease maintenance: the Sweeper reaps dead, expired and orphaned leases
//     under leases/.lock (sweep.go) and runs the stuck-client backstop
//     (backstop.go);
//   - assertion management: the Reconciler (assertion.go, assertion_adopt.go,
//     assertion_proc.go) converges the anchor and caffeinate processes on the
//     desired state computed each tick from the live leases, the pause, the
//     battery gate and the linger window. It starts the anchor and then
//     /usr/bin/caffeinate <union flags> -w <anchor pid>, replaces the pair make
//     before break, applies the anchor churn guard, adopts an existing pair at
//     start (assertion.json, then a state_dir-scoped process scan) and writes
//     assertion.json.
//
// The tick loop, the heartbeat, the singleton lock, config loading and the
// status snapshot are filled in by later packets; they call
// Reconciler.Reconcile with the tick's ReconcileInputs and read the
// ReconcileResult.
package keeper
