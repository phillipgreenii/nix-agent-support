package ledger

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/render"
)

// Outbox constants.
const (
	// BeadLease is how long a filing claim holds before another filer may take it.
	BeadLease = 10 * time.Minute
	// MaxBeadAttempts is the number of filing attempts after which a
	// decision's bead state is failed.
	MaxBeadAttempts = 5
	// UntrustedHeader heads every work bead body.
	UntrustedHeader = "Untrusted origin: written by an automated analyst"
)

// BeadRequest is one work bead to file.
type BeadRequest struct {
	// Key is the idempotency key, kept in the bead's external reference.
	Key string
	// Tracker is the tracker root directory ("" for the filer's own cwd).
	Tracker string
	Label   string
	Title   string
	Body    string
}

// BeadFiler is the tracker seam. Find looks for a bead carrying key in its
// external reference; Create files one.
type BeadFiler interface {
	Find(ctx context.Context, tracker, key string) (id string, found bool, err error)
	Create(ctx context.Context, req BeadRequest) (id string, err error)
}

// claimDecision takes the filing claim of one decision by compare-and-set:
// pending, or filing with an expired lease, becomes filing with a fresh lease
// and one more attempt. ok is false when the decision is not claimable (it is
// held, finished or failed). A decision that has used its attempts becomes
// failed instead of being claimed.
func (l *Ledger) claimDecision(ctx context.Context, id int64) (d Decision, ok bool, err error) {
	err = l.WithTx(ctx, func(t *Tx) error {
		d, err = t.getDecision(ctx, id)
		if err != nil {
			return err
		}
		now := t.now()
		expired := d.BeadState == BeadFiling && !now.Before(parseTime(d.BeadLeaseExpiresAt))
		if d.BeadState != BeadPending && !expired {
			return nil
		}
		if d.BeadAttempts >= MaxBeadAttempts {
			_, err := t.exec(ctx, `UPDATE decision SET bead_state='failed', bead_lease_expires_at='' WHERE id=?`, id)
			d.BeadState = BeadFailed
			return err
		}
		res, err := t.exec(ctx, `UPDATE decision SET bead_state='filing', bead_attempts=bead_attempts+1, bead_lease_expires_at=?
			WHERE id=? AND (bead_state='pending' OR (bead_state='filing' AND bead_lease_expires_at<=?))`,
			fmtTime(now.Add(BeadLease)), id, fmtTime(now))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		ok = true
		d.BeadState = BeadFiling
		d.BeadAttempts++
		d.BeadLeaseExpiresAt = fmtTime(now.Add(BeadLease))
		return nil
	})
	return d, ok, err
}

// finishDecision sets filed and records the bead id, only while the claim is
// still held.
func (l *Ledger) finishDecision(ctx context.Context, id int64, bead string) error {
	return l.WithTx(ctx, func(t *Tx) error {
		_, err := t.exec(ctx, `UPDATE decision SET bead_state='filed', work_bead=?, bead_lease_expires_at='' WHERE id=? AND bead_state='filing'`, bead, id)
		return err
	})
}

// releaseDecision ends a failed attempt: back to pending, or failed once the
// attempts are used up.
func (l *Ledger) releaseDecision(ctx context.Context, id int64) (string, error) {
	var state string
	err := l.WithTx(ctx, func(t *Tx) error {
		if _, err := t.exec(ctx, `UPDATE decision SET bead_lease_expires_at='',
			bead_state=CASE WHEN bead_attempts>=? THEN 'failed' ELSE 'pending' END
			WHERE id=? AND bead_state='filing'`, MaxBeadAttempts, id); err != nil {
			return err
		}
		return t.queryRow(ctx, `SELECT bead_state FROM decision WHERE id=?`, id).Scan(&state)
	})
	return state, err
}

// DueBeadDecisions lists the Adopted decisions whose bead is pending or whose
// filing lease has expired, oldest first.
func (l *Ledger) DueBeadDecisions(ctx context.Context) ([]int64, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id,bead_state,bead_lease_expires_at FROM decision
		WHERE bead_state IN ('pending','filing') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	now := l.now()
	var ids []int64
	for rows.Next() {
		var id int64
		var st, lease string
		if err := rows.Scan(&id, &st, &lease); err != nil {
			return nil, err
		}
		if st == BeadFiling && now.Before(parseTime(lease)) {
			continue
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OutboxFailed counts the decisions whose bead filing is terminally failed.
func (l *Ledger) OutboxFailed(ctx context.Context) (int, error) {
	var n int
	err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision WHERE bead_state='failed'`).Scan(&n)
	return n, err
}

// GetDecision returns one decision by id.
func (l *Ledger) GetDecision(ctx context.Context, id int64) (Decision, error) {
	return scanDecision(l.db.QueryRowContext(ctx, decisionSelect+` WHERE id=?`, id))
}

// OutboxResult counts what one FileAdoptedBeads pass did.
type OutboxResult struct {
	Filed   int `json:"filed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// BeadBody is the work bead body: the untrusted-origin header, then the
// operator-seen rendered item, stripped of control characters.
func BeadBody(item Item, d Decision) string {
	var b strings.Builder
	b.WriteString(UntrustedHeader + "\n\n")
	b.WriteString(render.Strip(render.Renumber(item.Rendered, int(item.Number), item.State, 0)))
	if n := strings.TrimSpace(render.Strip(d.Rationale)); n != "" {
		b.WriteString("\nRuling note: " + n + "\n")
	}
	return b.String()
}

// FileAdoptedBeads files the work bead of every Adopted decision whose
// bead_state is pending or whose filing lease expired. Each decision is
// claimed by compare-and-set first, so concurrent filers never both act on
// it. The bead is looked up by idempotency key before it is created, so a
// crash between the tracker call and the row update never files a second
// bead. A tracker failure leaves the ruling standing: the decision goes back
// to pending (failed after five attempts).
func FileAdoptedBeads(ctx context.Context, l *Ledger, f BeadFiler) (OutboxResult, error) {
	var res OutboxResult
	ids, err := l.DueBeadDecisions(ctx)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		d, ok, err := l.claimDecision(ctx, id)
		if err != nil {
			return res, err
		}
		if !ok {
			if d.BeadState == BeadFailed {
				res.Failed++
			} else {
				res.Skipped++
			}
			continue
		}
		bead, ferr := fileOne(ctx, l, f, d)
		if ferr == nil {
			if err := l.finishDecision(ctx, id, bead); err != nil {
				return res, err
			}
			res.Filed++
			continue
		}
		if _, err := l.releaseDecision(ctx, id); err != nil {
			return res, err
		}
		res.Failed++
	}
	return res, nil
}

func fileOne(ctx context.Context, l *Ledger, f BeadFiler, d Decision) (string, error) {
	if strings.TrimSpace(d.RepoLabel) == "" {
		return "", errors.New("the repo label of the change cannot be determined (see docs/outbox.md)")
	}
	if d.BeadIdempotencyKey == "" {
		return "", errors.New("decision has no idempotency key")
	}
	if id, found, err := f.Find(ctx, d.TargetTracker, d.BeadIdempotencyKey); err != nil {
		return "", err
	} else if found {
		return id, nil
	}
	it, err := l.itemOfTopic(ctx, d.TopicID)
	if err != nil {
		return "", err
	}
	return f.Create(ctx, BeadRequest{
		Key: d.BeadIdempotencyKey, Tracker: d.TargetTracker, Label: d.RepoLabel,
		Title: render.Strip(it.Writeup.Title), Body: BeadBody(it, d),
	})
}

func (l *Ledger) itemOfTopic(ctx context.Context, topicID string) (Item, error) {
	return scanItem(l.db.QueryRowContext(ctx, itemSelect+` WHERE t.topic_id=?`, topicID))
}

// BdFiler files beads with the bd command line.
type BdFiler struct {
	// Command is the tracker command; "" means "bd".
	Command string
}

func (b BdFiler) cmd() string {
	if b.Command == "" {
		return "bd"
	}
	return b.Command
}

func (b BdFiler) args(tracker string, rest ...string) []string {
	var a []string
	if tracker != "" {
		a = append(a, "-C", tracker)
	}
	return append(a, rest...)
}

func (b BdFiler) run(ctx context.Context, stdin string, args []string) (string, error) {
	c := exec.CommandContext(ctx, b.cmd(), args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", b.cmd(), args[len(args)-1], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Find looks for a bead (any status) whose external reference is key.
func (b BdFiler) Find(ctx context.Context, tracker, key string) (string, bool, error) {
	out, err := b.run(ctx, "", b.args(tracker, "list", "--all", "--external-ref", key, "--json"))
	if err != nil {
		return "", false, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", false, nil
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		var wrapped struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err2 := json.Unmarshal([]byte(out), &wrapped); err2 != nil {
			return "", false, fmt.Errorf("unreadable bead list: %v", err)
		}
		rows = wrapped.Data
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].ID, true, nil
}

// Create files one bead and returns its id.
func (b BdFiler) Create(ctx context.Context, r BeadRequest) (string, error) {
	out, err := b.run(ctx, r.Body, b.args(r.Tracker, "create", "--silent", "--external-ref", r.Key,
		"-l", r.Label, "--body-file", "-", "--", r.Title))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("the tracker printed no bead id")
	}
	return id, nil
}

var _ = sql.ErrNoRows
