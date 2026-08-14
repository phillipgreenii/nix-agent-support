package classify

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
)

// PromptVersion is bumped whenever the rubric below changes MEANING.
//
// It is versioned for the same reason the canned queries are: a classification run
// is only comparable with the next one if the instruction that produced it is
// identified. Two runs at different prompt versions are two different
// measurements, and a precision figure carried across a silent rubric change is
// worse than no figure at all.
// 1 — pg2-oisvb's rubric.
// 2 — pg2-v150u adds the hook-refusal-body glossary entry. The rubric states that a
//
//	reader NEEDS the per-signal glossary to interpret the evidence, so shipping a
//	detector that contributes 160 candidates without one would leave the model
//	guessing what it was looking at — a meaning change, hence the bump.
const PromptVersion = 2

// rubric is the classification instruction.
//
// Three properties are load-bearing and MUST survive edits:
//
//  1. NOT-A-MISTAKE IS THE DEFAULT. Tier 1 is a recall filter, so most candidates
//     are ordinary work. A rubric that nudges toward finding fault produces a
//     census of the classifier's eagerness.
//  2. THE PREVENTION FIELD IS MANDATORY. A finding with no stated prevention
//     cannot be routed to an artifact, and an unroutable finding is what Tier 3
//     exists to eliminate.
//  3. THE MODEL NEVER DECIDES THE ROUTE. It may hint; Tier 3's routing table is
//     the authority. Letting the classifier route would make the taxonomy drift
//     per call, which is exactly what happened when the routing was done by hand.
const rubric = `You are classifying candidate agent mistakes from Claude Code transcripts.

Each candidate below was found by a STRUCTURAL detector tuned for recall, so MOST
CANDIDATES ARE NOT MISTAKES. Ordinary work that looks like a candidate includes:
the user's next instruction arriving after a tool call; a legitimate ` + "`git reset`" + `
during a rebase or cleanup; a file edited several times because the change
genuinely had several parts; a retry that fixed a real quoting bug.

Return "not-a-mistake" unless the evidence shows something actually went wrong.

WHAT EACH SIGNAL DETECTED — you need this to read the evidence, because several
signals fire on things that are not visible in the excerpt alone. It tells you what
was observed and where each detector is known to over-fire; the CLASS is still yours
to decide.

  typed-turn        a turn a person TYPED, paired with the agent action immediately
                    before it. Fires on every human turn, so it over-fires massively:
                    most are simply the next instruction, an answer, or a status
                    report from the user.
  interruption      the harness sentinel written when a person INTERRUPTED the agent
                    part-way through an action. detail.prev_tool_names says what was
                    running. The excerpt is only the sentinel; the fact being reported
                    is that a person spent attention stopping the agent.
  denied-tool-call  a tool call that was refused. kind=user-rejected on AskUserQuestion
                    is what the harness records whenever the user types their own answer
                    instead of picking an offered option — 51 of 61 such rows in this
                    corpus. kind=permission-denied and kind=classifier-denied are the
                    permission layer refusing, not a person.
  hook-refusal-body a HOOK or GUARD refused the call BEFORE it ran, and said so in the
                    result body. detail.kind is the refusal verb bucket (blocked,
                    refusing, prohibited, deny-listed, must-include, hook-error), NOT a
                    verdict. THE GUARD ALREADY EXISTS AND ALREADY FIRED, so the question
                    is never "should there be a hook" — it is whether the refusal was
                    RIGHT. Both happen in this corpus. A guard firing on the wrong
                    command is permission-friction (the .git guard has blocked an
                    ordinary 'find … -not -path', and once blocked the approver's own
                    CLI). A guard firing correctly on something an instruction should
                    have prevented is specification-miss (the sleep guard fired 80 times
                    across 80 distinct sessions — once per session, so the reflex is
                    re-learned from scratch every time). Read the opening to tell which.
  undo              work taken back although every call SUCCEEDED. kind=edit-reversal
                    means a later Edit restored an earlier Edit's original text;
                    kind=write-then-delete means a file was written and later removed,
                    which is also what a deliberately temporary probe looks like;
                    kind=git-undo is a checkout/reset/revert/restore, which is also
                    what an ordinary rebase or staging step looks like.
  churn             one file edited N or more times inside one session. Drafting a
                    document in many passes looks identical to rewriting code that was
                    wrong the first four times.
  escaping-retry    the same command re-issued, byte-different but IDENTICAL after
                    removing whitespace, quotes and backslashes. Nothing the shell
                    cares about changed between the two.
  ack               the agent's own acknowledgment text. detail.provenance says whether
                    the nearest preceding turn was typed by a person (user-caught) or
                    not (self-caught). Read the text: it may be an admission of error,
                    or merely a status update that happened to match a phrase.

CLASSES (choose exactly one per candidate):
  not-a-mistake        normal work, or not enough evidence to say otherwise
  user-correction      a person told the agent it was wrong, or stopped it
  self-caught-mistake  the agent noticed and repaired its own error
  specification-miss   the agent had the instruction and did not follow it
  verification-miss    work asserted complete that was not (claim before checking)
  guidance-defect      an instruction/memory/skill CAUSED the error
  permission-friction  the action was correct; the approver or permission layer refused it
  tooling-defect       infrastructure: flaky service, unavailable model, broken tool

CONFIDENCE: high | medium | low. Use low when the excerpt is too short to judge.

For every candidate also give:
  what        one clause: what the agent did wrong (empty for not-a-mistake)
  prevention  one clause: what would have prevented it (empty for not-a-mistake)
  route       a HINT only, one of: global-rule, workspace-rule, skill, slash-command,
              subagent-prompt-template, hook, permission-config, not-actionable

Reply with ONLY a JSON array, one object per candidate, no prose and no code fence:
[{"id":"<id>","class":"<class>","confidence":"<confidence>","what":"","prevention":"","route":""}]

CANDIDATES:
`

// promptCandidate is the candidate shape the classifier sees. It is deliberately
// narrower than candidate.Candidate: transcript paths, session ids and timestamps
// are provenance for a human reader and would only invite the model to reason about
// identity instead of evidence.
type promptCandidate struct {
	ID          string            `json:"id"`
	Signal      string            `json:"signal"`
	Kind        string            `json:"kind,omitempty"`
	IsSidechain bool              `json:"is_sidechain"`
	Evidence    string            `json:"evidence"`
	Detail      map[string]string `json:"detail,omitempty"`
}

func renderPrompt(cands []candidate.Candidate) (string, error) {
	items := make([]promptCandidate, 0, len(cands))
	for _, c := range cands {
		items = append(items, promptCandidate{
			ID:          CandidateID(c),
			Signal:      string(c.Signal),
			Kind:        c.Kind,
			IsSidechain: c.IsSidechain,
			Evidence:    c.Excerpt,
			Detail:      c.Detail,
		})
	}
	body, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render classifier prompt: %w", err)
	}
	var sb strings.Builder
	sb.WriteString(rubric)
	sb.Write(body)
	sb.WriteString("\n")
	return sb.String(), nil
}
