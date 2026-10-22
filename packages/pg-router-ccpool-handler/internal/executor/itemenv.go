package executor

import "github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"

// Item-context environment variable names (INV-CCH-32). Both adapters set
// exactly these three for every payload; the ccpool launch (ccpool.go) and the
// command executor share itemContextEnv so the names never drift.
const (
	envItemRole = "PG_ROUTER_ROLE"
	envItemID   = "PG_ROUTER_ITEM_ID"
	envItemType = "PG_ROUTER_ITEM_TYPE"
)

// itemContextEnv returns the item context a payload is launched with: the role
// name and the item's ID and Type, and nothing else. The item's Title is
// deliberately left out: it is untrusted free text, and a command role can
// template it into its argv.
func itemContextEnv(roleName string, it item.Item) map[string]string {
	return map[string]string{
		envItemRole: roleName,
		envItemID:   it.ID,
		envItemType: it.Type,
	}
}
