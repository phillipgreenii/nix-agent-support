package pipeline

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// InstallIssueGate installs the delivery gate (INV-ICD-16) on st with its
// final scope: EVERY issue entity, whatever its backend, appends no
// change_log row until it is watched. The configuration is no longer
// consulted (the owning-instance scope was the step 2 rollout), so the gate is
// installed even when the configuration cannot be loaded; the parameter stays
// so every store-open path keeps one call shape. Every production store-open
// path calls it, so a row cannot reach change_log around the gate.
func InstallIssueGate(st *store.Store, _ *config.Config) {
	st.SetGateScope(store.AllIssueGateScope())
}
