package ccpool

// pg-router's session-metadata key namespace. Keys are PREFIXED (pgrouter.*) because
// they live in a KV store shared with ccpool and any other consumer; the prefix
// prevents collision with a key ccpool or another writer might use. (Design:
// docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md.)
const (
	MetaKeyBead = "pgrouter.bead" // the bead id the session is working
	MetaKeyRole = "pgrouter.role" // the pg-router role name
	MetaKeyPool = "pgrouter.pool" // owner tag; always PoolName
)

// PoolName is the owner value stamped on pgrouter.pool, identifying pg-router's sessions
// among all sessions sharing a ccpool pool DB.
const PoolName = "pg-router"

// DispatchMeta builds the session metadata pg-router stamps on a session at dispatch.
func DispatchMeta(beadID, role string) map[string]string {
	return map[string]string{
		MetaKeyBead: beadID,
		MetaKeyRole: role,
		MetaKeyPool: PoolName,
	}
}
