package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/unstick"
)

// Shape tests for the prose contract between `pb unstick` and the
// /pb:unstick-beads orchestrator and pb:unstick-batch-worker documents
// (tc-86kj9). They read the committed marketplace files, so a rewrite that
// drops a safety rule, or drifts from the pb constants, fails here.
//
// The 5-epic batch bound and its sharing of the 4-worker pool are enforced in
// Go by TestPackEpics (internal/unstick) and
// TestPrepareEpics_workerPoolSharedWithReviewBatches; the 4-agent cap itself
// exists only as orchestrator prose, so it is asserted here from the document.

const marketplacePB = "../../../../claude-marketplace/pb/"

// readMarketplace reads a file of the pb marketplace plugin. The nix go-tests
// gate builds from ./packages/pb alone, where the marketplace tree is absent;
// the in-tree run (the commit-time run-unit-tests hook) still enforces these.
func readMarketplace(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(marketplacePB + rel)
	if os.IsNotExist(err) {
		t.Skip("claude-marketplace/pb is not in this source tree (nix sandbox)")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// squash collapses whitespace so assertions survive prettier line wrapping.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func requireAll(t *testing.T, doc, name string, wants ...string) {
	t.Helper()
	d := squash(doc)
	for _, w := range wants {
		if !strings.Contains(d, squash(w)) {
			t.Errorf("%s lacks %q", name, w)
		}
	}
}

func TestWorkerAgentEpicCandidateMode(t *testing.T) {
	doc := readMarketplace(t, "agents/unstick-batch-worker.md")
	requireAll(
		t, doc, "unstick-batch-worker.md",
		// The explicit prohibition (bead tc-86kj9, desired behavior 3).
		"MUST NOT close on all-children-closed alone",
		// Mode selection.
		"## Epic-candidate mode",
		"`BATCH` starts with `E`",
		"`EPIC=1`",
		// Per-bead steps: 3 to 5 skipped, 6 to 9 run.
		"SKIP steps 3 to 5",
		"run steps 6 to 9",
		// D-9: never defer an epic or block it by a child.
		"MUST NOT undefer or defer an epic",
		"`--blocked-by` one of its children (D-9)",
		// Outcomes.
		"`close-epic`",
		"`unchanged`",
		"`needs-children`",
		"`closed <id>: <reason>`",
		"`--outcome needs-children`",
		"`--recheck-when on-change`",
		// Closing requirements.
		"Read the `close_reason` of EACH child",
		"won't-do, superseded",
		"merge-base --is-ancestor",
		"OPEN `phase-trigger` bead",
		"`discovered-from`",
		"recheck-when: <that-bead-id> closes",
		// human-labelled epics: recorded evidence only.
		"`human`-labelled epics are still verified",
		"close reasons and comments ONLY: no live probes",
	)
	// The outcome is in the marker-outcome list and the hand-back word list.
	requireAll(t, doc, "unstick-batch-worker.md", "`released`, `needs-children`)", "closed|needs-children|skipped")
	if !strings.Contains(doc, unstick.OutcomeNeedsChildren) {
		t.Errorf("worker does not mention pb's %q outcome", unstick.OutcomeNeedsChildren)
	}

	// The frontmatter description advertises the E batches too.
	fm := doc[:strings.Index(doc, "\n---\n\n")]
	requireAll(t, fm, "unstick-batch-worker.md frontmatter", "finished-epic candidates", "close-epic")
}

func TestUnstickBeadsDescribesEpicStage(t *testing.T) {
	doc := readMarketplace(t, "commands/unstick-beads.md")
	requireAll(
		t, doc, "unstick-beads.md",
		// Frontmatter and intro (the sweep is no longer only non-ready beads).
		"plus its finished-epic candidates",
		"**finished-epic candidates**",
		"Workers MUST NOT close an epic on all-children-closed alone",
		// Stage 1 reference.
		"`triage-epic-candidates.txt`",
		"`WORKDIR/batches/E<NN>`",
		"`epic_candidates` and `epic_marker_skip`",
		"`E<NN>` of kind `epic`",
		"batches of 3 to 5 epics",
		// Stage 6: one list, one pool, E batches counted in the cap.
		"combined Stage 1 `batches` list",
		"E batches hold at most **5** epics each and count toward the 4-agent cap",
		"at most **4** agents running at once (Stage 2 included)",
		"`EPIC=1`",
		// Stage 8: the epic line and needs-you.
		"epic candidates N = closed a + unchanged b + needs-children c + marker-skipped d",
		"`needs-children` epic belongs here",
		// The dispatch prompt still carries the attribution instruction.
		"append the line `closed <bead-id>: <reason>`",
	)

	// The prose bounds equal the pb constants.
	requireAll(
		t, doc, "unstick-beads.md",
		fmt.Sprintf("batches of %d to %d epics", unstick.EpicMinBatch, unstick.EpicMaxBatch),
		fmt.Sprintf("at most **%d** epics each", unstick.EpicMaxBatch),
	)
	if unstick.EpicMaxBatch > 5 {
		t.Errorf("EpicMaxBatch = %d, the epic batch bound is 5", unstick.EpicMaxBatch)
	}

	// The report line the document quotes is the one pb prints.
	re := regexp.MustCompile(`epic candidates N = closed a \+ unchanged b \+ needs-children c \+ marker-skipped d`)
	if !re.MatchString(squash(doc)) {
		t.Error("unstick-beads.md does not quote the report's epic arithmetic line")
	}
	if !strings.Contains(squash(doc), "worker pool") {
		t.Error("unstick-beads.md no longer describes the worker pool")
	}
}
