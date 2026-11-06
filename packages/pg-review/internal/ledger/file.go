package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/redact"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/render"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

var (
	// ErrBatchConflict means the run already filed a different batch.
	ErrBatchConflict = errors.New("ledger: run already filed a different batch")
	// ErrRunState means the run is not in a state that accepts a batch.
	ErrRunState = errors.New("ledger: run does not accept findings in its state")
)

// RejectedReason is the skip_reason of a run whose findings were rejected.
// Such a Failed run may file again (the one repair attempt).
const RejectedReason = "findings rejected"

// Batch is a validated findings document ready to be stored.
type Batch struct {
	RunID    int64
	Doc      validate.Document
	Evidence []validate.EvidenceRow
	// MaxPending is items_max_pending.
	MaxPending int
	// ScopeRoot is the path prefix the redaction package strips.
	ScopeRoot string
}

// FileResult reports what a batch did.
type FileResult struct {
	Filed          int  `json:"filed"`
	Deduplicated   int  `json:"deduplicated"`
	Skipped        int  `json:"skipped"`
	Waiting        int  `json:"waiting"`
	Reconsidered   int  `json:"reconsidered"`
	Replayed       bool `json:"replayed,omitempty"`
	DryRun         bool `json:"dry_run,omitempty"`
	PendingAfter   int  `json:"pending"`
	PromotedBefore int  `json:"promoted,omitempty"`
}

func batchHash(doc validate.Document) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// FileBatch stores every finding of a batch in ONE BEGIN IMMEDIATE
// transaction: a crash or an error leaves nothing, and two concurrent callers
// serialise on the write lock. Per finding: a new topic is created Pending
// (Waiting when the pending cap is reached); a Pending or Waiting topic gets
// an occurrence; a Deferred or Rejected topic returns to Pending only when a
// reconsideration trigger fired, otherwise only an occurrence is recorded; an
// Adopted topic gets an occurrence. Filing the identical batch for a run again
// succeeds without change; a different batch fails with ErrBatchConflict.
// Structured findings are redacted before storage.
func (l *Ledger) FileBatch(ctx context.Context, b Batch) (FileResult, error) {
	var res FileResult
	hash, err := batchHash(b.Doc)
	if err != nil {
		return res, err
	}
	err = l.WithTx(ctx, func(t *Tx) error {
		res = FileResult{}
		run, err := t.GetRun(ctx, b.RunID)
		if err != nil {
			return err
		}
		if run.Lens != b.Doc.Lens {
			return fmt.Errorf("%w: document lens %q is not the run's lens %q", ErrInvalid, b.Doc.Lens, run.Lens)
		}
		switch {
		case run.State == RunFiled:
			var prev string
			err := t.queryRow(ctx, `SELECT value FROM meta WHERE key=?`, batchKey(run.ID)).Scan(&prev)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && prev != hash) {
				return ErrBatchConflict
			}
			if err != nil {
				return err
			}
			res.Replayed = true
			return nil
		case run.State == RunRunning, run.State == RunAwaitingAnalyst,
			run.State == RunFailed && run.SkipReason == RejectedReason:
		default:
			return fmt.Errorf("%w: run %d is %s", ErrRunState, run.ID, run.State)
		}

		promoted, err := t.PromoteWaitingLogged(ctx, b.MaxPending)
		if err != nil {
			return err
		}
		res.PromotedBefore = len(promoted)

		rows := map[string]validate.EvidenceRow{}
		for _, r := range b.Evidence {
			rows[r.ID] = r
		}
		seen := map[string]bool{}
		for i := range b.Doc.Findings {
			f, err := redactFinding(b.Doc.Findings[i], b.ScopeRoot)
			if err != nil {
				return err
			}
			if err := t.fileOne(ctx, b, run, f, rows, seen, &res); err != nil {
				return err
			}
		}
		run.State = RunFiled
		run.SkipReason = ""
		run.EndedAt = t.now()
		run.ItemsFiled, run.ItemsDeduplicated, run.ItemsSkipped = res.Filed, res.Deduplicated, res.Skipped
		if err := t.UpdateRun(ctx, run); err != nil {
			return err
		}
		if _, err := t.exec(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, batchKey(run.ID), hash); err != nil {
			return err
		}
		return t.queryRow(ctx, `SELECT COUNT(*) FROM topic WHERE state='Pending'`).Scan(&res.PendingAfter)
	})
	return res, err
}

func batchKey(runID int64) string { return "batch:" + strconv.FormatInt(runID, 10) }

func redactFinding(f validate.Finding, scopeRoot string) (validate.Finding, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return f, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return f, err
	}
	v, _ = redact.Value(v, scopeRoot)
	raw, err = json.Marshal(v)
	if err != nil {
		return f, err
	}
	var out validate.Finding
	return out, json.Unmarshal(raw, &out)
}

func (t *Tx) pendingCount(ctx context.Context) (int, error) {
	var n int
	err := t.queryRow(ctx, `SELECT COUNT(*) FROM topic WHERE state='Pending'`).Scan(&n)
	return n, err
}

// observe derives what the run saw of a finding from the evidence it cites.
func (t *Tx) observe(ctx context.Context, topicID string, f validate.Finding, rows map[string]validate.EvidenceRow) (Observation, map[string]float64, error) {
	o := Observation{Now: t.now()}
	metrics := map[string]float64{}
	var version float64
	hasVersion := false
	for _, id := range f.Evidence {
		r, ok := rows[id]
		if !ok {
			continue
		}
		if v, ok := r.Metrics["rate"]; ok && (!o.HasRate || v > o.Rate) {
			o.Rate, o.HasRate = v, true
		}
		if v, ok := r.Metrics["cost"]; ok && (!o.HasCost || v > o.Cost) {
			o.Cost, o.HasCost = v, true
		}
		if v, ok := r.Metrics["version"]; ok {
			version, hasVersion = v, true
		}
		if ts, err := time.Parse(time.RFC3339, r.LastSeen); err == nil && ts.After(o.TargetSeenAt) {
			o.TargetSeenAt = ts
		}
	}
	if o.HasRate {
		metrics["rate"] = o.Rate
	}
	if o.HasCost {
		metrics["cost"] = o.Cost
	}
	if hasVersion {
		metrics["version"] = version
		var prev string
		err := t.queryRow(ctx, `SELECT metrics FROM occurrence WHERE topic_id=? ORDER BY id DESC LIMIT 1`, topicID).Scan(&prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return o, nil, err
		}
		if err == nil {
			var pm map[string]float64
			if json.Unmarshal([]byte(prev), &pm) == nil {
				if pv, ok := pm["version"]; ok && pv != version {
					o.VersionChanged = true
				}
			}
		}
	}
	return o, metrics, nil
}

func (t *Tx) fileOne(ctx context.Context, b Batch, run Run, f validate.Finding, rows map[string]validate.EvidenceRow, seen map[string]bool, res *FileResult) error {
	var defined, subjType string
	for _, r := range b.Evidence {
		if r.SubjectKey == f.SubjectKey {
			defined, subjType = r.DefinedIn, r.Type
			break
		}
	}
	if defined == "" {
		return fmt.Errorf("%w: finding %q has no evidence row for its subject", ErrInvalid, f.SubjectKey)
	}
	id := TopicID(defined, f.SubjectKey, f.Kind)
	if seen[id] {
		res.Deduplicated++
		return nil
	}
	seen[id] = true

	wj, err := json.Marshal(f)
	if err != nil {
		return err
	}
	rendered := render.Render(render.Item{
		State: StatePending, Lens: b.Doc.Lens, Scope: b.Doc.Scope,
		RunDate: t.now().Format("2006-01-02"), Missing: b.Doc.Unverified, Finding: f,
	})
	refs, _ := json.Marshal(f.Evidence)

	obs, metrics, err := t.observe(ctx, id, f, rows)
	if err != nil {
		return err
	}
	_, versionSensitive := metrics["version"]

	tp, err := t.GetTopic(ctx, id)
	if errors.Is(err, ErrNotFound) {
		pending, err := t.pendingCount(ctx)
		if err != nil {
			return err
		}
		waiting := pending >= b.MaxPending
		tp, err = t.CreateTopic(ctx, NewTopic{
			DefinedIn: defined, SubjectKey: f.SubjectKey, SubjectType: subjType,
			Kind: f.Kind, Lens: b.Doc.Lens, Waiting: waiting, VersionSensitive: versionSensitive,
		})
		if err != nil {
			return err
		}
		if _, err := t.AddRevision(ctx, id, run.ID, string(wj), rendered); err != nil {
			return err
		}
		mj, _ := json.Marshal(metrics)
		if err := t.AddOccurrence(ctx, id, run.ID, b.Doc.Scope, b.Doc.Lens, string(refs), string(mj)); err != nil {
			return err
		}
		res.Filed++
		if waiting {
			res.Waiting++
			return t.addEvent(ctx, id, EventWaiting, "pending cap reached")
		}
		return nil
	}
	if err != nil {
		return err
	}

	mj, _ := json.Marshal(metrics)
	record := func() error {
		return t.AddOccurrence(ctx, id, run.ID, b.Doc.Scope, b.Doc.Lens, string(refs), string(mj))
	}
	// refresh stores the write-up when its content changed (which bumps the revision).
	refresh := func() error {
		var last string
		err := t.queryRow(ctx, `SELECT writeup_json FROM revision WHERE topic_id=? ORDER BY id DESC LIMIT 1`, id).Scan(&last)
		if err == nil && last == string(wj) {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = t.AddRevision(ctx, id, run.ID, string(wj), rendered)
		return err
	}

	switch tp.State {
	case StatePending, StateWaiting, StateAdopted:
		// Before storing the occurrence so the version trigger sees the prior one.
		if tp.State != StateAdopted {
			if err := refresh(); err != nil {
				return err
			}
		}
		if err := record(); err != nil {
			return err
		}
		res.Deduplicated++
	case StateDeferred, StateRejected:
		reason, fired, err := t.Reconsider(ctx, tp, obs)
		if err != nil {
			return err
		}
		if err := record(); err != nil {
			return err
		}
		if !fired {
			res.Skipped++
			return nil
		}
		pending, err := t.pendingCount(ctx)
		if err != nil {
			return err
		}
		to, kindNote := StatePending, "reconsidered: "+reason
		if pending >= b.MaxPending {
			to = StateWaiting
			kindNote += " (pending cap reached: Waiting)"
		}
		if err := t.Transition(ctx, id, to); err != nil {
			return err
		}
		if err := t.addEvent(ctx, id, EventReconsidered, kindNote); err != nil {
			return err
		}
		if err := refresh(); err != nil {
			return err
		}
		res.Filed++
		res.Reconsidered++
		if to == StateWaiting {
			res.Waiting++
		}
	}
	return nil
}

// FailRun marks a run Failed because its findings were rejected, in its own
// transaction, so the run stays retryable (one repair attempt).
func (l *Ledger) FailRun(ctx context.Context, runID int64) error {
	return l.WithTx(ctx, func(t *Tx) error {
		run, err := t.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.State != RunRunning && run.State != RunAwaitingAnalyst && run.State != RunFailed {
			return nil
		}
		run.State = RunFailed
		run.SkipReason = RejectedReason
		run.RepairCount++
		run.EndedAt = t.now()
		return t.UpdateRun(ctx, run)
	})
}

// FileFindings is the whole of `agent file` for one run: refuse on a failed
// integrity check, decode and validate the document against the run's
// evidence (a rejection marks the run Failed unless dryRun), then FileBatch.
// A *validate.Error return maps to exit 4.
func FileFindings(ctx context.Context, l *Ledger, cfg *config.Effective, runID int64, raw []byte, dryRun bool) (FileResult, error) {
	if err := l.IntegrityCheck(ctx); err != nil {
		return FileResult{}, err
	}
	run, err := l.GetRun(ctx, runID)
	if err != nil {
		return FileResult{}, err
	}
	reject := func(rule, tok string) error {
		if !dryRun {
			if err := l.FailRun(ctx, runID); err != nil {
				return err
			}
		}
		return &validate.Error{Failures: []validate.Failure{{Index: -1, Finding: "#document", Field: "document", Rule: rule, Token: tok}}}
	}
	if err := schema.Validate("findings", raw); err != nil {
		return FileResult{}, reject("schema", err.Error())
	}
	var doc validate.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return FileResult{}, reject("schema", err.Error())
	}
	if doc.Lens != run.Lens {
		return FileResult{}, reject("run-mismatch", "lens "+doc.Lens)
	}
	lens, ok := cfg.Lens(run.Lens)
	if !ok {
		return FileResult{}, fmt.Errorf("%w: lens %q is not configured", ErrInvalid, run.Lens)
	}
	var scopeRoot string
	if sc, ok := cfg.Scope(run.Scope); ok && len(sc.Prefixes) > 0 {
		scopeRoot = sc.Prefixes[0]
	}
	b := Batch{RunID: runID, Doc: doc, MaxPending: cfg.Limits.ItemsMaxPending, ScopeRoot: scopeRoot}
	if run.State == RunFiled {
		return l.FileBatch(ctx, b) // replay check needs no evidence
	}
	in, err := validate.Load(run.EvidenceDir, run.StagingPath)
	if err != nil {
		return FileResult{}, err
	}
	in.Doc = doc
	in.Kinds = lens.Kinds
	in.FieldMaxChars = cfg.Limits.FieldMaxChars
	in.MaxFindings = cfg.MaxFindings(lens)
	if err := validate.Validate(in); err != nil {
		var ve *validate.Error
		if errors.As(err, &ve) && !dryRun {
			if ferr := l.FailRun(ctx, runID); ferr != nil {
				return FileResult{}, ferr
			}
		}
		return FileResult{}, err
	}
	if dryRun {
		return FileResult{DryRun: true}, nil
	}
	b.Evidence = in.Evidence
	return l.FileBatch(ctx, b)
}
