package ccpool

// pr-pool's session-metadata key namespace. Keys are PREFIXED (prpool.*) because
// they live in a KV store shared with ccpool and any other consumer; the prefix
// prevents collision with a key ccpool or another writer might use. (Design:
// docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md.)
const (
	MetaKeyBead = "prpool.bead" // the bead id the session is working
	MetaKeyRole = "prpool.role" // the pr-pool role name
	MetaKeyPool = "prpool.pool" // owner tag; always PoolName
)

// PoolName is the owner value stamped on prpool.pool, identifying pr-pool's sessions
// among all sessions sharing a ccpool pool DB.
const PoolName = "pr-pool"

// DispatchMeta builds the session metadata pr-pool stamps on a session at dispatch.
func DispatchMeta(beadID, role string) map[string]string {
	return map[string]string{
		MetaKeyBead: beadID,
		MetaKeyRole: role,
		MetaKeyPool: PoolName,
	}
}
