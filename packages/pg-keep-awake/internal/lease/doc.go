// Package lease is the private on-disk state layer of pg-keep-awake. The CLI,
// the caffeinate shim, the keeper and the anchor all share it, so none of them
// re-implements the state-directory checks, the lease write and read protocol,
// the atomic file writer, or the process-liveness test.
//
// The state directory is ${XDG_STATE_HOME:-~/.local/state}/pg-keep-awake/. It
// MUST be owned by the current user with no group or other permission bits;
// an existing directory that is not is refused with a *StateDirError (callers
// map it to exit 3) and is never chmod-ed. Files are written 0600.
//
// Files and who touches them:
//
//	leases/<id>.json       one lease per file (CLI, shim)         Lease
//	leases/.lock           flock guarding create, remove, reap
//	keeper.lock            keeper singleton lock (keeper)
//	keeper.heartbeat       empty file, mtime is the contract (keeper)
//	assertion.json         anchor and caffeinate the keeper started
//	status.json            snapshot read by `status`
//	anchor-exit.json       why the anchor last exited
//	pause.json             an active pause
//	config.last-good.toml  last configuration that parsed
//
// Every JSON file carries schema_version. Readers ignore unknown fields; a
// file whose schema_version is greater than SchemaVersion is reported as
// *UnreadableError (never deleted) so `status` can show it as unreadable.
package lease
