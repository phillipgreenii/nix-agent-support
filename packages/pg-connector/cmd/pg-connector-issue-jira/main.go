// pg-connector-issue-jira is the issue capability's second Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity —
// mirroring cmd/pg-connector-issue-beads/main.go's identical structure
// [carry-over basis: cmd/pg-connector-issue-beads/main.go]. It implements
// pkg/provider/issue.Provider against a real external Jira integration by
// shelling out to phillipg-nix-repo-base's generic pjira CLI (internal).
//
// Unlike pg-connector-issue-beads, this binary's dispatch table DOES gain an
// auth_status entry: internal.Backend implements pkg/provider.AuthChecker
// via pjira's own auth-status op (see internal/backend.go's CheckAuth doc
// comment) — capabilities.ops (via scriptout.AddCapabilities) reflects that
// automatically, with no separate hand-typed ops list.
package main

import (
	"os"
	"time"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring every existing Tier-2 backend's own convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (a pjira-CLI-backed Backend) and its
// op-dispatch table, then hands the table to the Tier-1 core's generic
// serve loop.
func run() int {
	backend := internal.New(internal.NewCLIRunner())
	return scriptout.ServeLoop(instrument(newDispatchTable(backend), os.Getenv))
}

// instrument wraps table so every call appends one event to this backend's
// own rotating event log (bead pg2-ltddq; package eventlog explains the
// ownership contract). It lives apart from newDispatchTable so the wiring
// tests that drive newDispatchTable never write to the real state home. With
// the log disabled or unresolvable it returns table unchanged.
func instrument(table scriptout.DispatchTable, getenv func(string) string) scriptout.DispatchTable {
	return eventlog.Instrument(table, eventlog.SinkFromEnv(getenv), Version, time.Now)
}

// newDispatchTable builds the issue capability's table (show/create/
// comment/transition, plus auth_status since internal.Backend implements
// AuthChecker) via issue.NewDispatchTable, then merges in the search
// capability's table (search, plus its own auth_status entry —
// functionally identical to issue's, since both type-assert the same
// backend; harmless to overwrite) built by
// pkg/provider/search.NewDispatchTable (bead pg2-8hcnx: this backend's
// `pjira search --jql` call already existed for List but was never wired
// to the cross-capability "search" op), then adds this backend's own
// capabilities entry via scriptout.AddCapabilities — AddCapabilities
// computes capabilities.ops straight from this table's own registered op
// names, so this backend never hand-types a second, separately maintained
// ops list [carry-over basis: cmd/pg-connector-issue-beads/main.go's
// identical newDispatchTable, bead pg2-fh2vh].
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := issue.NewDispatchTable(backend)
	for op, handler := range search.NewDispatchTable(backend) {
		table[op] = handler
	}
	// activity (list_activity, plus its own auth_status entry, identical in
	// effect to the others) built by pkg/provider/activity.NewDispatchTable:
	// this backend's own ListActivity, scoped to the operator's own identity
	// (see internal/activity.go).
	for op, handler := range activity.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.IssueSchemaVersion, capabilitiesBase())
}

// capabilitiesBase declares this backend's schemaVersions and its
// non-empty state and priority vocabularies (Jira's own classic default
// workflow/priority names — see internal.Vocabulary/internal.
// PriorityVocabulary's doc comments for why those are real, not invented).
// Version carries this binary's own ldflags-stamped build var.
func capabilitiesBase() scriptout.CapabilitiesResponse {
	return scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"issue":    schema.IssueSchemaVersion,
			"search":   schema.SearchSchemaVersion,
			"activity": schema.ActivitySchemaVersion,
		},
		Vocabulary: map[string]any{
			"state":    internal.Vocabulary,
			"priority": internal.PriorityVocabulary,
			// The activity kinds this backend emits; capabilities.ops stays
			// derived from the table, never a hand-typed list.
			"activity_kinds": internal.ActivityKinds,
		},
		Version: Version,
	}
}
