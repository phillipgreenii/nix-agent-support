package roles

import (
	"testing"
)

func TestExternalID_andDisplayName(t *testing.T) {
	r := Role{Name: "worker"}
	if got := r.ExternalID("pg-router-", "zr-w.2", "STAMP"); got != "pg-router-worker-zr-w.2-STAMP" {
		t.Fatalf("ExternalID = %q", got)
	}
	if got := r.DisplayName("pg-router-", "zr-w.2"); got != "pg-router-worker-zr-w.2" {
		t.Fatalf("DisplayName = %q", got)
	}
}

// The built-in role/query pairing (BuiltinRoleSet/BuiltinQuerySet) and its
// ccpool-specific prompt bodies (feedbackPromptBody/workerPromptBody/
// reviewPromptBody) moved out of this package entirely (docket pg2-oju6w's
// Task 5.8, ADR 0065's "Source-side boundary" section): the beads-shaped
// role/query pairing now lives as a registered kind:"source" participant in
// packages/pg-router-ccpool-handler (see that module's own query config
// tests); an unconfigured pg-router core runs with zero roles and zero
// queries (internal/config.Load, TestLoad_noFile_zeroRolesAndQueries). The
// prompt bodies were already unwired reference text as of Task 5.4 (their
// one consumer, roles.CCPoolConfig.Prompt/PromptBody, was deleted then) and
// are not ported anywhere — they carried no remaining behavior to preserve.
