// Package audit is the apply hook that appends one audit comment to the work
// item behind every applied external tracker write (create, update, reopen,
// close): the rule id, the facts the rule keyed on, the seq of the triggering
// change record and a timestamp. Together with the change log's origin and
// `pg-desk <type> history`, it is the removed ledger's replacement history.
//
// The comment is an extra write after the action's own write. A failure to post
// it never rolls the action back (tracker writes are never rolled back); it is
// returned as the hook's error, which apply reports on stderr and turns into
// exit 2, because the next idempotent run will not re-derive a lost comment.
// Deduped, failed and skipped-dependency outcomes and annotate actions write no
// comment (annotations go through pg-desk, whose change log already records
// origin and actor). The comment is not a work-item write that needs a
// follow-up pg-desk refresh of its own.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
)

// Hook implements apply.Hook.
type Hook struct{}

var _ apply.Hook = Hook{}

// New returns the audit hook.
func New() Hook { return Hook{} }

// external reports whether op is a tracker write that is audited.
func external(op action.Op) bool {
	switch op {
	case action.OpCreate, action.OpUpdate, action.OpReopen, action.OpClose:
		return true
	}
	return false
}

// After posts the audit comment for an applied external action.
func (Hook) After(ctx context.Context, env apply.Env, ev apply.Event) error {
	if ev.Outcome != apply.OutcomeApplied || !external(ev.Action.Op) {
		return nil
	}
	if ev.WorkItemID == "" {
		return fmt.Errorf("audit comment for %s (rule %s): the applied action reported no work item id", ev.Action.Op, ev.Action.Rule)
	}
	clock := env.Clock
	if clock == nil {
		clock = time.Now
	}
	if err := apply.Comment(ctx, env, ev.WorkItemID, Body(ev, clock())); err != nil {
		return fmt.Errorf("audit comment on %s for %s (rule %s) was not posted (the tracker write stands): %w", ev.WorkItemID, ev.Action.Op, ev.Action.Rule, err)
	}
	return nil
}

// Finish does nothing.
func (Hook) Finish(context.Context, apply.Env, []apply.Event) error { return nil }

// Body renders the audit comment for ev at time at. The layout is stable:
//
//	pg-decider audit
//	rule: <rule id>
//	op: <op>
//	facts: <compact JSON object, keys sorted>
//	seq: <n>|none
//	at: <RFC 3339 UTC>
//
// seq is "none" when apply ran without --from-item.
func Body(ev apply.Event, at time.Time) string {
	seq := "none"
	if ev.HasSeq {
		seq = strconv.FormatInt(ev.Seq, 10)
	}
	return fmt.Sprintf("pg-decider audit\nrule: %s\nop: %s\nfacts: %s\nseq: %s\nat: %s\n",
		ev.Action.Rule, ev.Action.Op, renderFacts(ev.Action.Facts), seq, at.UTC().Format(time.RFC3339))
}

// renderFacts is compact JSON with sorted keys (encoding/json sorts map keys)
// and no HTML escaping, so the text is deterministic and readable.
func renderFacts(facts map[string]any) string {
	if len(facts) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(facts); err != nil {
		return fmt.Sprintf("{\"unrenderable\":%q}", err.Error())
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}
