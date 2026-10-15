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
//     assertion.json;
//   - the status snapshot: BuildSnapshot turns a tick's sweep, gate and
//     reconcile results into status.json and WriteSnapshot writes it
//     atomically (mode 0600), once per tick before the heartbeat is touched
//     (snapshot.go); ExternalProbe lists the non-keeper `pmset -g assertions`
//     holders, failing soft (snapshot_external.go).
//
// The tick loop, the heartbeat, the singleton lock and config loading are
// filled in by later packets; they call
// Reconciler.Reconcile with the tick's ReconcileInputs and read the
// ReconcileResult.
package keeper
