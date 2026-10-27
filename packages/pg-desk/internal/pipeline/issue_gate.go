package pipeline

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// InstallIssueGate installs the first rollout scope of the delivery gate
// (INV-ICD-16) on st: an issue entity whose backend column names one of
// cfg's owning instances, or whose backend is NULL and whose id resolves to
// the single owning instance, appends no change_log row until it is watched.
// An issue entity of any other backend (beads) is out of scope and still
// emits through the old tiers. With no owning instance configured nothing is
// in scope. Every production store-open path calls it, so a row cannot reach
// change_log around the gate.
func InstallIssueGate(st *store.Store, cfg *config.Config) {
	owning := map[string]bool{}
	for _, inst := range cfg.OwningInstances("issue") {
		owning[inst] = true
	}
	st.SetGateScope(store.OwningGateScope(owning, func(id string) (string, bool) {
		return cfg.OwningInstanceForID("issue", id)
	}))
}
