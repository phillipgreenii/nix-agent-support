package focus

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The preview of a select (spec section 7.2 steps 3 to 7): given the final
// set, the plan and the store's links, BuildPreview resolves, for every row
// the run will newly select and every row it will remove, the EFFECT on its
// bead (mint, hold, release, none), the BECAUSE clause and the bead id, from
// the bead-state table of beadstate.go. The same resolution serves a
// preview, a dry run and an apply alike; the echo packet only renders it.
//
// Telemetry (D24): no OpenTelemetry or Prometheus signal, no logging; the
// preview reads the store and writes nothing.

// Directions of a ChangeRow.
const (
	DirectionSelect = "select"
	DirectionRemove = "remove"
)

// Kinds of an EchoRow.
const (
	EchoForced    = "forced"
	EchoHandAdd   = "handadd"
	EchoStruck    = "struck"
	EchoSkipped   = "skipped"
	EchoMerged    = "merged"
	EchoUnmerged  = "unmerged"
	EchoHydrate   = "hydrate"
	EchoCapRemove = "cap"
	// EchoSelected is a draft proposal the reply's ok selected.
	EchoSelected = "selected"
	// EchoNoChange is the row of an ok that selected and removed nothing.
	EchoNoChange = "nochange"
)

// ChangeRow is one row the run newly selects or removes. Cause is the stored
// vocabulary (operator, cap, dropped, new_period; empty for a selection) and
// CauseText its display words. State and Shown are the bead column; Effect,
// Because and Echo come from the table row of State, BeadID names the bead
// the effect lands on ("" when none exists yet).
type ChangeRow struct {
	Direction string
	Key       Key
	Title     string
	Source    string
	Cause     string
	CauseText string
	State     BeadState
	Shown     string
	Effect    string
	Because   string
	Echo      string
	BeadID    string
}

// EchoRow names what one applied token or flag resolved to, so a token that
// resolved to something unintended is visible: Kind is one of the Echo*
// words, Token the token as the echo prints it (ok, -ID, +ID, cap=N,
// --merge KEEP=ABSORBED), Other the second key of a merge, Detail the result
// clause ("struck by you, removed from the plan").
//
// Effect (mint, hold, release or none), Because and Consequence are the
// downstream consequence of the row, read from the bead-state table of
// beadstate.go: a strike reads its strike columns, a selection its select
// columns. Cause is the stored cause of a removal ("" for a selection), and
// BeadID the bead the effect lands on ("" when none exists yet).
type EchoRow struct {
	Kind        string
	Token       string
	Key         Key
	Other       *Key
	Title       string
	Detail      string
	Effect      string
	Because     string
	Consequence string
	Cause       string
	BeadID      string
}

// PreviewResult is what a select would do. Changes holds the selections
// first, then the removals; Echo one row per resolved token and merge;
// WouldHydrate the hand-added keys new to the store; NeverClosed the older
// periods the first lock would delete the rows of although they were never
// closed; ReplacesPlan whether the first lock deletes the rows of other
// periods.
type PreviewResult struct {
	Changes      []ChangeRow
	Echo         []EchoRow
	WouldHydrate []Key
	NeverClosed  []string
	FirstLock    bool
	ReplacesPlan bool
}

// lockState is the period state a preview and a preflight read outside any
// transaction.
type lockState struct {
	period      store.FocusPeriod
	found       bool
	first       bool
	replace     bool
	others      []store.FocusSelection
	neverClosed []string
}

func (d LockDeps) readLockState(in LockInput, fs FinalSet) (lockState, error) {
	var ls lockState
	p, found, err := d.Store.FocusPeriodGet(in.Period.Type, in.Period.Key)
	if err != nil {
		return ls, fmt.Errorf("focus: read period: %w", err)
	}
	ls.period, ls.found = p, found
	if found && p.ClosedAt != "" {
		return ls, ErrPeriodClosed
	}
	ls.first = true
	if found {
		rows, err := d.Store.FocusPlanRows(p.ID)
		if err != nil {
			return ls, fmt.Errorf("focus: read plan rows: %w", err)
		}
		ls.first = len(rows) == 0
	}
	all, err := d.Store.FocusRowsAllPeriods()
	if err != nil {
		return ls, fmt.Errorf("focus: read plan rows: %w", err)
	}
	periods, err := d.Store.FocusPeriods()
	if err != nil {
		return ls, fmt.Errorf("focus: read periods: %w", err)
	}
	byID := map[int64]store.FocusPeriod{}
	for _, op := range periods {
		byID[op.ID] = op
	}
	maxOther := ""
	held := map[int64]bool{}
	for _, r := range all {
		if found && r.FocusPeriodID == p.ID {
			continue
		}
		ls.others = append(ls.others, r)
		held[r.FocusPeriodID] = true
		if k := byID[r.FocusPeriodID].PeriodKey; k > maxOther {
			maxOther = k
		}
	}
	ls.replace, err = decideFirstLock(ls.first, len(fs.Insert) > 0, in.Period.Key, d.todayKey(), len(ls.others) > 0, maxOther)
	if err != nil {
		return ls, err
	}
	if ls.replace {
		for id := range held {
			if op := byID[id]; op.ClosedAt == "" {
				ls.neverClosed = append(ls.neverClosed, op.PeriodKey)
			}
		}
		sort.Strings(ls.neverClosed)
	}
	return ls, nil
}

// PreflightLock runs the checks of the lock that need no transaction: the
// addressed period is open (ErrPeriodClosed) and the first-lock rule holds (a
// usage error for an earlier or a future period). The verb calls it before
// hand-add hydration so that a doomed run never hydrates.
func PreflightLock(ctx context.Context, d LockDeps, in LockInput) error {
	_, err := d.readLockState(in, in.Final)
	return err
}

// BuildPreview resolves every row's effect, because clause and bead id for
// the final set fs of a lock over in (see the file comment). It writes
// nothing. It returns the same errors the lock would for a closed period and
// for a first lock that is earlier than the plan or in the future.
func BuildPreview(ctx context.Context, d LockDeps, in LockInput, fs FinalSet) (PreviewResult, error) {
	ls, err := d.readLockState(in, fs)
	if err != nil {
		return PreviewResult{}, err
	}
	repo := d.repo()
	inputs, err := Load(d.Store, d.Config, repo, d.now())
	if err != nil {
		return PreviewResult{}, fmt.Errorf("focus: preview: %w", err)
	}

	var keys []Key
	seen := map[Key]bool{}
	add := func(k Key) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, i := range fs.Insert {
		add(i.Key)
	}
	for _, x := range fs.Delete {
		add(x.Key)
	}
	if ls.replace {
		for _, r := range ls.others {
			add(Key{Type: r.EntityType, ID: r.EntityID})
		}
	}
	for _, s := range fs.Skipped {
		add(s.Key)
	}
	for _, m := range append(append([]Merge(nil), fs.Absorbed...), fs.Unmerge...) {
		add(m.Kept)
		add(m.Absorbed)
	}
	live := BuildLive(inputs, keys, Ranking{}, LiveOptions{Watched: LoadWatched(d.Store, d.Config)})

	pv := PreviewResult{FirstLock: ls.first, ReplacesPlan: ls.replace, NeverClosed: ls.neverClosed}
	// resolve reads the bead column of k once: the table row it falls in, the
	// effect and because clause of a select or a strike, the consequence
	// phrase the echo prints and the bead id.
	type resolved struct {
		state                          BeadState
		shown, effect, because, phrase string
		beadID                         string
	}
	resolve := func(dir string, k Key) resolved {
		cell := live.Beads[k]
		trow := RowOf(cell.State)
		r := resolved{state: cell.State, shown: cell.Shown, beadID: beadIDOf(inputs, k)}
		if dir == DirectionSelect {
			r.effect, r.because, r.phrase = trow.SelectEffect, trow.SelectBecause, trow.SelectEcho
		} else {
			r.effect, r.because, r.phrase = trow.StrikeEffect, trow.StrikeBecause, trow.StrikeEcho
		}
		r.phrase = consequencePhrase(dir, cell.State, cell.Shown, r.phrase, r.beadID)
		return r
	}
	rowSeen := map[string]bool{}
	mk := func(dir string, k Key, source, cause string) {
		id := fmt.Sprintf("%s|%s|%s", dir, k, cause)
		if rowSeen[id] {
			return
		}
		rowSeen[id] = true
		r := resolve(dir, k)
		pv.Changes = append(pv.Changes, ChangeRow{
			Direction: dir, Key: k, Title: live.Facts[k].Title, Source: source, Cause: cause,
			CauseText: causeText(cause, live.Facts[k]), State: r.state, Shown: r.shown,
			Effect: r.effect, Because: r.because, Echo: r.phrase, BeadID: r.beadID,
		})
	}
	for _, i := range fs.Insert {
		mk(DirectionSelect, i.Key, i.Source, "")
	}
	for _, x := range fs.Delete {
		mk(DirectionRemove, x.Key, "", x.Cause)
	}
	if ls.replace {
		for _, r := range ls.others {
			mk(DirectionRemove, Key{Type: r.EntityType, ID: r.EntityID}, "", CauseNewPeriod)
		}
	}

	title := func(k Key) string { return live.Facts[k].Title }
	hydrated := map[Key]bool{}
	for _, k := range in.Hydrated {
		hydrated[k] = true
	}
	echo := func(kind, token string, k Key, dir, cause, detail string) {
		r := resolve(dir, k)
		pv.Echo = append(pv.Echo, EchoRow{
			Kind: kind, Token: token, Key: k, Title: title(k), Detail: detail,
			Effect: r.effect, Because: r.because, Consequence: r.phrase, Cause: cause, BeadID: r.beadID,
		})
	}
	forced := map[Key]bool{}
	for _, k := range fs.Forced {
		forced[k] = true
	}
	handAdded := map[Key]bool{}
	for _, k := range fs.HandAdded {
		handAdded[k] = true
	}
	// The selections, in the order the lock inserts them: the draft's
	// proposals (ok), then the force-pulled and hand-added keys (+key).
	for _, i := range fs.Insert {
		switch {
		case handAdded[i.Key]:
			detail := "hand-added"
			if _, stored := inputs.Entities[i.Key]; !stored {
				pv.WouldHydrate = append(pv.WouldHydrate, i.Key)
				detail = "hydrated, new to store"
			} else if hydrated[i.Key] {
				detail = "hydrated, new to store"
			}
			echo(EchoHandAdd, "+"+i.Key.ID, i.Key, DirectionSelect, "", detail)
		case forced[i.Key]:
			echo(EchoForced, "+"+i.Key.ID, i.Key, DirectionSelect, "", "force-pulled from the candidates")
		default:
			echo(EchoSelected, "ok", i.Key, DirectionSelect, "", "selected from the draft")
		}
	}
	for _, x := range fs.Delete {
		switch x.Cause {
		case CauseOperator:
			echo(EchoStruck, "-"+x.Key.ID, x.Key, DirectionRemove, x.Cause, "struck by you, removed from the plan")
		case CauseCap:
			token := "cap"
			if in.ReplyCap != nil {
				token = fmt.Sprintf("cap=%d", *in.ReplyCap)
			}
			echo(EchoCapRemove, token, x.Key, DirectionRemove, x.Cause, "removed from the plan ("+causeText(x.Cause, live.Facts[x.Key])+")")
		default:
			echo(EchoCapRemove, "(dropped)", x.Key, DirectionRemove, x.Cause, "removed from the plan ("+causeText(x.Cause, live.Facts[x.Key])+")")
		}
	}
	if in.ReplyOk && len(fs.Insert) == 0 && len(fs.Delete) == 0 && !ls.replace {
		detail := "no change; name items with +key or use pull"
		if ls.first {
			detail = "no change"
		}
		pv.Echo = append(pv.Echo, EchoRow{Kind: EchoNoChange, Token: "ok", Detail: detail, Effect: "none"})
	}
	for _, s := range fs.Skipped {
		pv.Echo = append(pv.Echo, EchoRow{Kind: EchoSkipped, Key: s.Key, Title: title(s.Key), Detail: s.Reason, Effect: "none"})
	}
	for _, m := range fs.Absorbed {
		kept := m.Kept
		pv.Echo = append(pv.Echo, EchoRow{
			Kind: EchoMerged, Token: fmt.Sprintf("--merge %s=%s", m.Kept.ID, m.Absorbed.ID), Key: m.Absorbed, Other: &kept, Title: title(m.Absorbed),
			Detail: fmt.Sprintf("merged %s into %s (link recorded; no bead for %s)", m.Absorbed.ID, m.Kept.ID, m.Absorbed.ID), Effect: "none",
		})
	}
	for _, m := range fs.Unmerge {
		kept := m.Kept
		pv.Echo = append(pv.Echo, EchoRow{
			Kind: EchoUnmerged, Token: "+" + m.Absorbed.ID, Key: m.Absorbed, Other: &kept, Title: title(m.Absorbed),
			Detail: fmt.Sprintf("merge of %s into %s reversed (link removed)", m.Absorbed.ID, m.Kept.ID), Effect: "none",
		})
	}
	return pv, nil
}

// consequencePhrase is the words the echo and the CHANGES block print for a
// row's downstream consequence: the table's echo for the state, with the
// claimant filled in, the bead id named for an item that is a bead, and the
// reminder that pg-desk cannot stop a claimed bead on a strike. The words
// themselves live in BeadStates only.
func consequencePhrase(dir string, state BeadState, shown, phrase, beadID string) string {
	if actor, ok := strings.CutPrefix(shown, "claimed by "); ok {
		phrase = strings.ReplaceAll(phrase, "<actor>", actor)
	}
	switch {
	case state == StateNotApplicable && beadID != "":
		if dir == DirectionRemove {
			return "is bead " + beadID + "; strike does not defer it"
		}
		return "is bead " + beadID
	case state == StateClaimed && dir == DirectionRemove:
		return phrase + " (stop it with bd; pg-desk cannot)"
	}
	return phrase
}

// causeText is the display word of a stored cause.
func causeText(cause string, f Facts) string {
	switch cause {
	case CauseOperator:
		return "you"
	case CauseCap:
		return "cap"
	case CauseDropped:
		if f.Hidden {
			return "no longer a candidate: hidden"
		}
		return "no longer a candidate: inactive"
	case CauseNewPeriod:
		return "new plan"
	}
	return ""
}

// beadIDOf is the id of the bead the effect on k lands on: k itself when it
// is a bead (an epic or a bd task), else the bead minted for it (linked by
// relation source, a human escalation sibling excepted), else "".
func beadIDOf(in Inputs, k Key) string {
	if k.Type == entityTypeIssue {
		if re := in.Config.BeadIDRegexp(); re != nil && re.MatchString(k.ID) {
			return k.ID
		}
	}
	var ids []Key
	for _, l := range in.Links {
		if l.Relation == relationSource && l.FromType == entityTypeIssue && l.ToType == k.Type && l.ToID == k.ID {
			ids = append(ids, Key{Type: l.FromType, ID: l.FromID})
		}
	}
	sort.Slice(ids, func(i, j int) bool { return keyLess(ids[i], ids[j]) })
	for _, bk := range ids {
		e, ok := in.Entities[bk]
		if !ok {
			continue
		}
		if hasLabel(decodeEntityFacts(e).labels, humanLabel) {
			continue
		}
		return bk.ID
	}
	return ""
}

// HydrateHandAdds hydrates every key of keys that the store has never
// gathered (a hand-add of a new key has no entity row for the foreign key),
// through d.Hydrate, in key order. It returns the keys it hydrated. The first
// failure stops the run and is a usage error naming the key and saying that
// hydration may already have written store rows (the atomicity claim is about
// selection state only). A nil Hydrate is a failure for a key that needs it.
func HydrateHandAdds(ctx context.Context, d LockDeps, keys []Key, in Inputs) ([]Key, error) {
	var hydrated []Key
	for _, k := range keys {
		if _, stored := in.Entities[k]; stored {
			continue
		}
		if d.Hydrate == nil {
			return hydrated, usageErr("name a key the store holds", "cannot hydrate %s: no hydrator is available", k)
		}
		if err := d.Hydrate(ctx, k); err != nil {
			return hydrated, usageErr(
				"fix the key or run pg-desk "+k.Type+" refresh, then re-run",
				"cannot hydrate %s (%v); hydration may already have written store rows, but no selection state changed", k, err,
			)
		}
		hydrated = append(hydrated, k)
	}
	return hydrated, nil
}
