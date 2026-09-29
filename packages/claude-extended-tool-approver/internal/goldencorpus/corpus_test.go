package goldencorpus

import (
	"encoding/json"
	"os"
	"testing"
)

// This packet's own Validation section (docket tc-o14i5.1.2): "No consuming
// loader/engine is wired to this format yet in this packet (that is
// later-phase work), so validation is structural: every seeded row is
// well-formed against the field list above (a mechanical check ... or a go
// test over the corpus directory if the implementer's chosen layout is
// Go-parseable); report the total seed-row count." This file is that go
// test.

const corpusPath = "testdata/corpus.json"

func loadCorpus(t *testing.T) []Row {
	t.Helper()
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("reading %s: %v", corpusPath, err)
	}
	var rows []Row
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("unmarshalling %s: %v", corpusPath, err)
	}
	return rows
}

// TestCorpusWellFormed structurally validates every seeded row against the
// P2 corpus contract's field list, and reports the total seed-row count
// this packet's Validation section requires.
func TestCorpusWellFormed(t *testing.T) {
	rows := loadCorpus(t)
	if len(rows) == 0 {
		t.Fatal("corpus.json contains no rows")
	}

	seenCase := make(map[string]bool, len(rows))
	for i, r := range rows {
		if r.Case == "" {
			t.Errorf("row %d: empty case name", i)
		} else if seenCase[r.Case] {
			t.Errorf("row %d: duplicate case name %q", i, r.Case)
		}
		seenCase[r.Case] = true

		if r.ToolInput.ToolName == "" {
			t.Errorf("case %q: tool_input.tool_name is empty", r.Case)
		}
		if r.ToolInput.ToolInput == nil {
			t.Errorf("case %q: tool_input.tool_input is nil (must be present, even if {})", r.Case)
		}

		if r.CWDPathState.CWD == "" {
			t.Errorf("case %q: cwd_path_state.cwd is empty", r.Case)
		}

		if r.Mode == "" {
			t.Errorf("case %q: mode is empty", r.Case)
		} else if !r.Mode.Valid() {
			t.Errorf("case %q: mode %q is not one of the recognised Mode values", r.Case, r.Mode)
		}

		if r.ExpectedVerdict == "" {
			t.Errorf("case %q: expected_verdict is empty", r.Case)
		} else if !r.ExpectedVerdict.Valid() {
			t.Errorf("case %q: expected_verdict %q is not one of {approve, reject, not-approve}", r.Case, r.ExpectedVerdict)
		}

		if len(r.Tags) == 0 {
			t.Errorf("case %q: no tags -- every row must record which required seed-row category (or categories) it counts toward", r.Case)
		}
	}

	t.Logf("goldencorpus: %d seeded rows, all structurally well-formed", len(rows))
}

// tagCounts is a small helper: how many rows carry each tag.
func tagCounts(rows []Row) map[string]int {
	counts := make(map[string]int)
	for _, r := range rows {
		for _, tag := range r.Tags {
			counts[tag]++
		}
	}
	return counts
}

// TestCorpusRequiredCategoriesSeeded mechanically checks the packet's own
// acceptance criteria: every required seed-row category (docket design,
// Phase 0 item 2 / this packet's own Contract) is represented by at least
// one row, and the confirmed-hole / gitdir-write / non-shell-tool /
// policy-carveout ADR sub-categories are each represented individually so
// none of them is silently short (mirroring the packet's own "0048/0060
// exhaustion names TWO ADRs — do not stop one short" caution).
func TestCorpusRequiredCategoriesSeeded(t *testing.T) {
	rows := loadCorpus(t)
	counts := tagCounts(rows)

	requireAtLeast := func(tag string, min int) {
		t.Helper()
		if counts[tag] < min {
			t.Errorf("tag %q: got %d rows, want at least %d", tag, counts[tag], min)
		}
	}

	// Confirmed holes (Ground truth, verified 2026-09-25): argv0-by-
	// path.Base, env-default-Permitted, treefmt --config-file /tmp/...,
	// self-permission writes, plus both named .git/** write cases.
	requireAtLeast("confirmed-hole", 6)
	requireAtLeast("gitdir-write", 2)

	// Push flag rows / R6 rows: every named flag/spelling plus the
	// delete-ref ladder and the pn workspace approvable pair.
	requireAtLeast("push-flag", 16)
	requireAtLeast("r6", 16)

	// R7 rows: the 4 repo-level REJECT leaves + 6 repo-level ABSTAIN
	// leaves + 3 home-equivalent REJECT leaves + 5 home-equivalent
	// ABSTAIN leaves named by R7/D12 ruling #4.
	requireAtLeast("r7", 18)

	// The 8 named non-shell tools seen in the decision DB.
	requireAtLeast("non-shell-tool", 8)

	// The 9 policy-carve-out bullets / 10 distinct ADR numbers (the
	// "0048/0060 exhaustion" bullet names two ADRs -- do not stop one
	// short by treating it as a single ADR).
	requireAtLeast("policy-carveout", 15)
	for _, adr := range []string{
		"adr-0044", "adr-0047", "adr-0048", "adr-0049", "adr-0050",
		"adr-0059", "adr-0060", "adr-0066", "adr-0073", "adr-0074",
	} {
		requireAtLeast(adr, 1)
	}

	// At least one common-read-only-workflow must-approve row.
	requireAtLeast("read-only-workflow", 1)
}

// TestCorpusReadOnlyWorkflowRowsApprove pins the "must-approve" half of the
// read-only-workflow category's own name: every row tagged
// read-only-workflow carries expected_verdict "approve".
func TestCorpusReadOnlyWorkflowRowsApprove(t *testing.T) {
	rows := loadCorpus(t)
	for _, r := range rows {
		for _, tag := range r.Tags {
			if tag == "read-only-workflow" && r.ExpectedVerdict != Approve {
				t.Errorf("case %q: tagged read-only-workflow (must-approve) but expected_verdict is %q", r.Case, r.ExpectedVerdict)
			}
		}
	}
}

// TestCorpusSettingsAllowDoesNotMaskAMustRejectRow is a data-integrity check
// for the P2 corpus contract's own invariant ("a must-reject or not-approve
// row MUST NOT produce an EFFECTIVE allow ... where a settings
// permissions.allow rule matches"): the one row seeded specifically to
// exercise that invariant (r7_repo_settings_json_reject, whose settings
// fixture carries a matching permissions_allow entry) still records
// expected_verdict "reject" in the corpus data itself. This only checks the
// SEED DATA is self-consistent — no engine is wired to this format yet to
// prove it actually resists the masking at evaluation time (that is
// later-phase work).
func TestCorpusSettingsAllowDoesNotMaskAMustRejectRow(t *testing.T) {
	rows := loadCorpus(t)
	const wantCase = "r7_repo_settings_json_reject"
	for _, r := range rows {
		if r.Case != wantCase {
			continue
		}
		if len(r.Settings.PermissionsAllow) == 0 {
			t.Fatalf("case %q: expected a non-empty settings.permissions_allow fixture (the row exists to exercise the effective-allow invariant)", wantCase)
		}
		if r.ExpectedVerdict != Reject {
			t.Errorf("case %q: expected_verdict = %q, want %q despite the matching settings.permissions_allow entry", wantCase, r.ExpectedVerdict, Reject)
		}
		return
	}
	t.Fatalf("corpus does not contain the expected row %q", wantCase)
}
