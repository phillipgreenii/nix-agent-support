package internal

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// worstCasePoll is the longest one list poll can run for a given turn cap: each
// turn takes at most slowTurnLatency, the backend's own deadline cuts the run
// off at its configured value (never more than scriptout.MaxConfiguredTimeout),
// and the umbrella kills the child scriptout.BackendDeadlineMargin later.
func worstCasePoll(turnCap int) time.Duration {
	run := time.Duration(turnCap) * slowTurnLatency
	if run > scriptout.MaxConfiguredTimeout {
		run = scriptout.MaxConfiguredTimeout
	}
	return run + scriptout.BackendDeadlineMargin
}

// TestListPollIsBoundedByTheSourcePeriod is the sizing regression guard of bead
// pg2-lacaev. The scheduler polls its sources serially once per sourcePeriod, so
// one source's worst poll MUST leave at least half of the period to the others.
// The bound is derived from the code's own limits (the turn cap range and the
// longest deadline a config may arm), never from a retyped number: raising
// maxMaxTurns or scriptout.MaxConfiguredTimeout, or lengthening the slowest
// observed turn, fails here before it can stall a tick.
func TestListPollIsBoundedByTheSourcePeriod(t *testing.T) {
	budget := sourcePeriod / 2
	for turns := 1; turns <= maxMaxTurns; turns++ {
		if got := worstCasePoll(turns); got > budget {
			t.Fatalf("max_turns %d: worst-case poll %v exceeds half the %v source period (%v)",
				turns, got, sourcePeriod, budget)
		}
	}
	// The default cap and the largest cap both stay inside it by construction;
	// say so for the largest explicitly so a failure names the culprit.
	if got := worstCasePoll(maxMaxTurns); got > budget {
		t.Fatalf("largest accepted max_turns %d: worst-case poll %v exceeds %v", maxMaxTurns, got, budget)
	}
}

// TestListExpectedTurnsFitTheTurnCapAndDeadline: the run listPrompt asks for (one
// search, its result, the final reply) must fit under the default turn cap and
// inside the longest deadline a config may arm, with the kill margin to spare,
// so the prompt's own design is never the thing that trips either limit.
func TestListExpectedTurnsFitTheTurnCapAndDeadline(t *testing.T) {
	if listExpectedTurns > defaultMaxTurns {
		t.Fatalf("listExpectedTurns %d exceeds defaultMaxTurns %d", listExpectedTurns, defaultMaxTurns)
	}
	expected := time.Duration(listExpectedTurns)*slowTurnLatency + scriptout.BackendDeadlineMargin
	if expected > scriptout.MaxConfiguredTimeout {
		t.Fatalf("an expected run (%d turns at %v plus the %v margin = %v) exceeds the longest deadline %v",
			listExpectedTurns, slowTurnLatency, scriptout.BackendDeadlineMargin, expected, scriptout.MaxConfiguredTimeout)
	}
	if expected >= worstCasePoll(maxMaxTurns) {
		t.Fatalf("an expected run (%v) is not shorter than the worst case (%v): the turn cap bounds nothing",
			expected, worstCasePoll(maxMaxTurns))
	}
}

// TestListPrompt_AsksForOneSearchAndNoPerThreadReads pins the per-call work
// (bead pg2-lacaev: turn count drives a poll's duration, 3-turn runs took 11-43 s
// and 5-turn runs about 100 s). The prompt MUST name the one search tool, demand
// exactly one call and forbid every read tool and further paging; the read-only
// allow-list's other tools are named only inside the prohibition.
func TestListPrompt_AsksForOneSearchAndNoPerThreadReads(t *testing.T) {
	p := listPrompt("in:#general")
	for _, want := range []string{
		"exactly ONE call to the slack_search_public tool",
		"Do NOT call any other tool",
		"do not request further pages",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("listPrompt does not contain %q:\n%s", want, p)
		}
	}
	// Every read tool the runner allows, other than the search, appears only
	// as a forbidden one: after the prohibition and before the reply shape.
	prohibition := strings.Index(p, "Do NOT call any other tool")
	shape := strings.Index(p, "Respond with ONLY one JSON object")
	if prohibition < 0 || shape < 0 || prohibition > shape {
		t.Fatalf("prohibition (%d) and reply shape (%d) not in the expected order", prohibition, shape)
	}
	for _, tool := range readOnlyTools {
		name := strings.TrimPrefix(tool, "mcp__slack__")
		if name == "slack_search_public" {
			continue
		}
		if i := strings.Index(p, name); i >= 0 && (i < prohibition || i > shape) {
			t.Errorf("listPrompt mentions %q outside the prohibition (offset %d)", name, i)
		}
	}
}
