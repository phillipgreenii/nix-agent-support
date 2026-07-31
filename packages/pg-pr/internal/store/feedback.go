package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Feedback is one feedback item (any kind). The type-specific tail is nullable.
// Note: the `file` field for code-comment-thread is enforced at the Go layer
// (see UpsertFeedback); other kind-specific fields are advisory and not enforced.
type Feedback struct {
	ID          int64
	PRID        int64
	Kind        string
	ExternalID  string
	Fingerprint string
	Status      string
	Title       string
	Body        string

	SubjectSHA       string
	FirstSeenHeadSHA string
	IsOutdated       bool
	IsMinimized      bool
	MinimizedReason  string

	AuthorLogin string
	AuthorKind  string
	AgentName   string
	IsOurs      bool
	AuthorRole  string

	DispositionAction string
	DispositionNote   string
	ReplyBody         string
	ResponseID        string
	Severity          string
	ManagedUpstream   bool

	File           string
	Line           int
	ThreadResolved bool
	CommentNodeID  string
	RunID          string
	CheckName      string
	Conclusion     string
	Related        bool
	RetryCount     int
	Link           string
}

// Message is one comment within a code-comment-thread feedback item.
type Message struct {
	ID          int64
	FeedbackID  int64
	ExternalID  string
	AuthorLogin string
	AuthorKind  string
	AgentName   string
	IsOurs      bool
	AuthorRole  string
	Body        string
	PostedAt    string
}

// ListFilter narrows ListFeedback.
type ListFilter struct {
	// ActiveOnly drops outdated/minimized/superseded items.
	ActiveOnly bool
	// Kind, when non-empty, restricts to one kind.
	Kind string
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullStr returns nil when s is empty so that optional constrained TEXT columns
// receive SQL NULL instead of an empty string (which may violate CHECK constraints).
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UpsertFeedback inserts or updates a feedback row by (pr_id, fingerprint),
// returning its id. On update it overwrites only upstream-sourced fields, never
// the agent-owned disposition/reply columns.
func (db *DB) UpsertFeedback(ctx context.Context, f Feedback) (int64, error) {
	var id int64
	err := db.InTx(ctx, func(tx *Tx) error {
		var e error
		id, e = tx.UpsertFeedback(f)
		return e
	})
	return id, err
}

// UpsertFeedback inserts or updates a feedback row by (pr_id, fingerprint)
// inside the transaction, returning its id. Preserves the nullStr author
// handling and the code-comment-thread file guard.
func (t *Tx) UpsertFeedback(f Feedback) (int64, error) {
	if f.PRID == 0 || f.Kind == "" || f.Fingerprint == "" {
		return 0, errors.New("store: UpsertFeedback requires pr_id, kind, fingerprint")
	}
	// Only `file` (for code-comment-thread) is hard-required at the Go layer;
	// other type-specific fields (run_id, check_name, etc.) are advisory and
	// not enforced in this phase.
	if f.Kind == "code-comment-thread" && strings.TrimSpace(f.File) == "" {
		return 0, errors.New("store: code-comment-thread requires file")
	}
	if f.Status == "" {
		f.Status = "new"
	}
	now := nowRFC3339()
	_, err := t.Exec(
		`
INSERT INTO feedback
  (pr_id, kind, external_id, fingerprint, status, title, body,
   subject_sha, first_seen_head_sha, is_outdated, is_minimized, minimized_reason,
   author_login, author_kind, agent_name, is_ours, author_role,
   severity, managed_upstream,
   file, line, thread_resolved, comment_node_id, run_id, check_name, conclusion, related, retry_count, link,
   created_at, updated_at)
VALUES (?,?,?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?, ?,?,?,?,?,?,?,?,?,?, ?,?)
ON CONFLICT(pr_id, fingerprint) DO UPDATE SET
  external_id=excluded.external_id, title=excluded.title, body=excluded.body,
  subject_sha=excluded.subject_sha, is_outdated=excluded.is_outdated,
  is_minimized=excluded.is_minimized, minimized_reason=excluded.minimized_reason,
  author_login=excluded.author_login, author_kind=excluded.author_kind,
  agent_name=excluded.agent_name, is_ours=excluded.is_ours, author_role=excluded.author_role,
  severity=excluded.severity, managed_upstream=excluded.managed_upstream,
  file=excluded.file, line=excluded.line, thread_resolved=excluded.thread_resolved,
  comment_node_id=excluded.comment_node_id, run_id=excluded.run_id, check_name=excluded.check_name,
  conclusion=excluded.conclusion, related=excluded.related, retry_count=excluded.retry_count,
  link=excluded.link, updated_at=excluded.updated_at`,
		f.PRID, f.Kind, f.ExternalID, f.Fingerprint, f.Status, f.Title, f.Body,
		f.SubjectSHA, f.FirstSeenHeadSHA, b2i(f.IsOutdated), b2i(f.IsMinimized), f.MinimizedReason,
		f.AuthorLogin, nullStr(f.AuthorKind), f.AgentName, b2i(f.IsOurs), f.AuthorRole,
		f.Severity, b2i(f.ManagedUpstream),
		f.File, f.Line, b2i(f.ThreadResolved), f.CommentNodeID, f.RunID, f.CheckName, f.Conclusion, b2i(f.Related), f.RetryCount, f.Link,
		now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("store: upsert feedback (pr=%d fp=%s): %w", f.PRID, f.Fingerprint, err)
	}
	var id int64
	err = t.QueryRow("SELECT id FROM feedback WHERE pr_id=? AND fingerprint=?", f.PRID, f.Fingerprint).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: read back feedback id: %w", err)
	}
	return id, nil
}

const feedbackCols = `id, pr_id, kind, external_id, fingerprint, status, title, body,
  subject_sha, first_seen_head_sha, is_outdated, is_minimized, minimized_reason,
  author_login, author_kind, agent_name, is_ours, author_role,
  disposition_action, disposition_note, reply_body, response_id, severity, managed_upstream,
  file, line, thread_resolved, comment_node_id, run_id, check_name, conclusion, related, retry_count, link`

func scanFeedback(s interface{ Scan(...any) error }) (*Feedback, error) {
	var f Feedback
	var (
		isOutdated, isMin, isOurs, threadResolved, related int
		managed                                            int
		dispAction, dispNote, replyBody, responseID        sql.NullString
		minReason, subjectSHA, firstSeen                   sql.NullString
		file, commentNode, runID, checkName, concl, link   sql.NullString
		severity                                           sql.NullString
		authorLogin, authorKind, agentName, authorRole     sql.NullString
		line, retry                                        sql.NullInt64
	)
	err := s.Scan(&f.ID, &f.PRID, &f.Kind, &f.ExternalID, &f.Fingerprint, &f.Status, &f.Title, &f.Body,
		&subjectSHA, &firstSeen, &isOutdated, &isMin, &minReason,
		&authorLogin, &authorKind, &agentName, &isOurs, &authorRole,
		&dispAction, &dispNote, &replyBody, &responseID, &severity, &managed,
		&file, &line, &threadResolved, &commentNode, &runID, &checkName, &concl, &related, &retry, &link)
	if err != nil {
		return nil, err
	}
	f.SubjectSHA, f.FirstSeenHeadSHA, f.MinimizedReason = subjectSHA.String, firstSeen.String, minReason.String
	f.IsOutdated, f.IsMinimized, f.IsOurs = isOutdated == 1, isMin == 1, isOurs == 1
	f.AuthorLogin, f.AuthorKind, f.AgentName, f.AuthorRole = authorLogin.String, authorKind.String, agentName.String, authorRole.String
	f.DispositionAction, f.DispositionNote = dispAction.String, dispNote.String
	f.ReplyBody, f.ResponseID, f.Severity = replyBody.String, responseID.String, severity.String
	f.ManagedUpstream, f.ThreadResolved, f.Related = managed == 1, threadResolved == 1, related == 1
	f.File, f.CommentNodeID, f.RunID = file.String, commentNode.String, runID.String
	f.CheckName, f.Conclusion, f.Link = checkName.String, concl.String, link.String
	f.Line, f.RetryCount = int(line.Int64), int(retry.Int64)
	return &f, nil
}

// GetFeedback returns one item by id, or nil.
func (db *DB) GetFeedback(ctx context.Context, id int64) (*Feedback, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT "+feedbackCols+" FROM feedback WHERE id=?", id)
	f, err := scanFeedback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get feedback %d: %w", id, err)
	}
	return f, nil
}

// ListFeedback returns a PR's feedback, oldest first.
func (db *DB) ListFeedback(ctx context.Context, prID int64, filter ListFilter) ([]Feedback, error) {
	q := "SELECT " + feedbackCols + " FROM feedback WHERE pr_id=?"
	args := []any{prID}
	if filter.Kind != "" {
		q += " AND kind=?"
		args = append(args, filter.Kind)
	}
	if filter.ActiveOnly {
		q += " AND is_outdated=0 AND is_minimized=0 AND status NOT IN ('superseded','resolved')"
	}
	q += " ORDER BY id"
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list feedback pr=%d: %w", prID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// blockingFeedbackKinds are the feedback kinds that gate auto-merge while
// unresolved. ci-failure has always gated the merge loop; self-review
// (pg2-4c5i.34, Q1 — always block) joins it so agent self-review findings block
// until dispositioned. Both clear via `pg-pr feedback disposition` (status →
// dispositioned) or supersession/resolution.
var blockingFeedbackKinds = []string{"ci-failure", "self-review"}

// HasBlockingFeedback reports whether the PR has any unresolved blocking
// feedback — a row of a blocking kind whose status is NOT dispositioned /
// resolved / superseded. This is the canonical merge-gate predicate: while it
// is true the PR MUST NOT auto-merge. self-review rows gate exactly like
// ci-failure rows (they are ingested at status='new' by the my-PR sink).
//
// Merge-gate wiring note: there is currently no Go merge-decision point where
// this predicate can be enforced automatically. The merge loop is driven by the
// bd/skill layer (pr-merge / pr-automerge are human-invoked skills); no Go code
// executes at the moment a merge decision is made. Enforcement therefore lives
// at the process-feedback skill layer, which MUST call HasBlockingFeedback
// before approving a merge and surface any blocking items to the operator.
// When/if a Go merge-decision point is introduced (e.g. a daemon automerge
// worker), wire this predicate in at that callsite.
func (db *DB) HasBlockingFeedback(ctx context.Context, prID int64) (bool, error) {
	// Build the IN-list placeholders for the blocking kinds.
	placeholders := make([]string, len(blockingFeedbackKinds))
	args := make([]any, 0, len(blockingFeedbackKinds)+1)
	args = append(args, prID)
	for i, k := range blockingFeedbackKinds {
		placeholders[i] = "?"
		args = append(args, k)
	}
	q := "SELECT EXISTS(SELECT 1 FROM feedback WHERE pr_id=? AND kind IN (" +
		strings.Join(placeholders, ",") +
		") AND status NOT IN ('dispositioned','resolved','superseded'))"
	var exists int
	if err := db.sql.QueryRowContext(ctx, q, args...).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: has blocking feedback pr=%d: %w", prID, err)
	}
	return exists == 1, nil
}

// processableFeedbackKinds are the feedback kinds that represent WORK TO
// PROCESS on a PR — the ones a process-feedback bead exists to drive. Reviewer
// comments (top-level and inline threads), CI failures, and the agent's own
// self-review findings all qualify. 'review-request' and 'jira-link' do NOT:
// they are routing/metadata rows, not findings, and no producer writes them
// today (they exist only as fingerprint kinds).
var processableFeedbackKinds = []string{"code-comment-thread", "pr-comments", "ci-failure", "self-review"}

// unaddressedFeedbackStatuses are the statuses that still need processing. The
// complement ('dispositioned','replied','resolved','superseded') is work the
// agent has already handled, and mirrors HasBlockingFeedback's exclusion set
// plus 'replied'.
var unaddressedFeedbackStatuses = []string{"new", "presented"}

// UnaddressedFeedback summarises the feedback on prID that still needs
// processing (pg2-onq1e). It is the single decision point for "did this sync
// surface anything to process?", and its result is what the beadsbridge
// projection writes into the process-feedback bead's description.
//
// prAuthorLogin is the PR's own author. Rows they authored are EXCLUDED, which
// is the fix for the self-feeding loop: pg-pr posts replies under the user's
// OWN login (there is no bot account), so on my own PR an agent reply is
// indistinguishable from a comment I typed — and re-counting it as feedback
// produced a bead asking the agent to process its own replies. The comparison
// is case-insensitive because GitHub logins are.
//
// A second, independent guard covers agent replies on a TEAMMATE's PR, where
// the author login is not mine: is_ours=1 (marker-detected — see
// internal/marker) rows are excluded too. 'self-review' is the deliberate
// exception, because those rows are ours BY CONSTRUCTION and exist precisely to
// be processed (see internal/reviewsink).
//
// Returns a non-nil summary always; Unaddressed == 0 means "nothing to
// process".
func (db *DB) UnaddressedFeedback(ctx context.Context, prID int64, prAuthorLogin string) (*FeedbackSummary, error) {
	kindPH := make([]string, len(processableFeedbackKinds))
	statusPH := make([]string, len(unaddressedFeedbackStatuses))
	args := make([]any, 0, len(processableFeedbackKinds)+len(unaddressedFeedbackStatuses)+2)
	args = append(args, prID)
	for i, k := range processableFeedbackKinds {
		kindPH[i] = "?"
		args = append(args, k)
	}
	for i, s := range unaddressedFeedbackStatuses {
		statusPH[i] = "?"
		args = append(args, s)
	}
	args = append(args, prAuthorLogin)
	q := `
SELECT kind, COALESCE(author_login,''), fingerprint
FROM feedback
WHERE pr_id=?
  AND kind IN (` + strings.Join(kindPH, ",") + `)
  AND status IN (` + strings.Join(statusPH, ",") + `)
  AND is_outdated=0 AND is_minimized=0
  AND NOT (is_ours=1 AND kind <> 'self-review')
  AND (author_login IS NULL OR author_login='' OR LOWER(author_login) <> LOWER(?))
ORDER BY fingerprint`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: unaddressed feedback pr=%d: %w", prID, err)
	}
	defer func() { _ = rows.Close() }()

	sum := &FeedbackSummary{ByKind: map[string]int{}}
	seenReviewer := map[string]struct{}{}
	h := sha256.New()
	for rows.Next() {
		var kind, login, fp string
		if err := rows.Scan(&kind, &login, &fp); err != nil {
			return nil, fmt.Errorf("store: scan unaddressed feedback pr=%d: %w", prID, err)
		}
		sum.Unaddressed++
		sum.ByKind[kind]++
		if login != "" {
			if _, dup := seenReviewer[login]; !dup {
				seenReviewer[login] = struct{}{}
				sum.Reviewers = append(sum.Reviewers, login)
			}
		}
		// Fingerprints arrive sorted (ORDER BY fingerprint), so the digest is
		// stable regardless of insertion order.
		_, _ = h.Write([]byte(fp))
		_, _ = h.Write([]byte{0})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: unaddressed feedback pr=%d: %w", prID, err)
	}
	if sum.Unaddressed == 0 {
		// Keep the zero value unambiguous: no counts, no digest.
		return &FeedbackSummary{}, nil
	}
	sort.Strings(sum.Reviewers)
	sum.Digest = hex.EncodeToString(h.Sum(nil))[:12]
	return sum, nil
}

// SetDisposition records the agent's decision and (optionally) a queued reply.
// Moves status to "dispositioned".
func (db *DB) SetDisposition(ctx context.Context, id int64, action, note, reply string) error {
	return db.InTx(ctx, func(tx *Tx) error {
		return tx.SetDisposition(id, action, note, reply)
	})
}

// SetDisposition records the agent's decision and (optionally) a queued reply
// inside the transaction. Moves status to "dispositioned".
func (t *Tx) SetDisposition(id int64, action, note, reply string) error {
	switch action {
	case "will-fix", "wont-fix", "no-action":
		// valid
	default:
		return fmt.Errorf("store: invalid disposition action %q", action)
	}
	now := nowRFC3339()
	res, err := t.Exec(`
UPDATE feedback SET disposition_action=?, disposition_note=?, reply_body=?, status='dispositioned', updated_at=?
WHERE id=?`, action, note, reply, now, id)
	if err != nil {
		return fmt.Errorf("store: set disposition %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: feedback %d not found", id)
	}
	return nil
}

// MarkReplied records the upstream response id and moves status to "replied".
// No event emitted; direct write is intentional (reply delivery is fire-and-forget).
func (db *DB) MarkReplied(ctx context.Context, id int64, responseID string) error {
	now := nowRFC3339()
	_, err := db.sql.ExecContext(ctx,
		"UPDATE feedback SET response_id=?, status='replied', updated_at=? WHERE id=?",
		responseID, now, id)
	if err != nil {
		return fmt.Errorf("store: mark replied %d: %w", id, err)
	}
	return nil
}

// ListPendingReplies returns items with a queued reply_body but no response_id
// yet — the durable reply-delivery work list (re-scanned each reconcile).
func (db *DB) ListPendingReplies(ctx context.Context) ([]Feedback, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT "+feedbackCols+
		" FROM feedback WHERE reply_body IS NOT NULL AND reply_body <> '' AND (response_id IS NULL OR response_id='') ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: list pending replies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ReplaceMessages upserts a set of messages for a code-comment-thread feedback
// row. Each message is keyed by (feedback_id, external_id); on conflict the
// row is updated so re-ingestion is idempotent. The method is on *Tx so it
// runs in the same transaction as the parent UpsertFeedback call.
func (t *Tx) ReplaceMessages(feedbackID int64, msgs []Message) error {
	for _, m := range msgs {
		if m.ExternalID == "" {
			return fmt.Errorf("store: ReplaceMessages: message missing external_id (feedback=%d)", feedbackID)
		}
		_, err := t.Exec(
			`
INSERT INTO code_comment_message
  (feedback_id, external_id, author_login, author_kind, agent_name, is_ours, author_role, body, posted_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(feedback_id, external_id) DO UPDATE SET
  author_login=excluded.author_login, author_kind=excluded.author_kind,
  agent_name=excluded.agent_name, is_ours=excluded.is_ours,
  author_role=excluded.author_role, body=excluded.body, posted_at=excluded.posted_at`,
			feedbackID,
			m.ExternalID,
			nullStr(m.AuthorLogin),
			nullStr(m.AuthorKind),
			nullStr(m.AgentName),
			b2i(m.IsOurs),
			nullStr(m.AuthorRole),
			m.Body,
			nullStr(m.PostedAt),
		)
		if err != nil {
			return fmt.Errorf("store: upsert message %s (feedback=%d): %w", m.ExternalID, feedbackID, err)
		}
	}
	return nil
}

// ListMessages returns the thread messages for a single feedback item, oldest
// first.
func (db *DB) ListMessages(ctx context.Context, feedbackID int64) ([]Message, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, feedback_id, external_id, author_login, author_kind, agent_name, is_ours, author_role, body, posted_at
FROM code_comment_message WHERE feedback_id=? ORDER BY posted_at, id`, feedbackID)
	if err != nil {
		return nil, fmt.Errorf("store: list messages feedback=%d: %w", feedbackID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		var m Message
		var (
			authorLogin, authorKind, agentName, authorRole sql.NullString
			isOurs                                         int
			postedAt                                       sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.FeedbackID, &m.ExternalID,
			&authorLogin, &authorKind, &agentName, &isOurs, &authorRole,
			&m.Body, &postedAt); err != nil {
			return nil, fmt.Errorf("store: scan message: %w", err)
		}
		m.AuthorLogin, m.AuthorKind, m.AgentName, m.AuthorRole = authorLogin.String, authorKind.String, agentName.String, authorRole.String
		m.IsOurs = isOurs == 1
		m.PostedAt = postedAt.String
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReconcileStaleness marks ci-failure rows whose subject_sha != the PR head as
// superseded. Code-thread is_outdated comes from the provider (set on upsert),
// so it is NOT touched here.
// No event emitted; direct write is intentional (reconciliation is a bulk sweep, not a tracked mutation).
func (db *DB) ReconcileStaleness(ctx context.Context, prID int64, headSHA string) error {
	_, err := db.sql.ExecContext(ctx, `
UPDATE feedback SET status='superseded', updated_at=?
WHERE pr_id=? AND kind='ci-failure' AND subject_sha IS NOT NULL AND subject_sha <> ?
  AND status NOT IN ('superseded','resolved')`,
		nowRFC3339(), prID, headSHA)
	if err != nil {
		return fmt.Errorf("store: reconcile staleness pr=%d: %w", prID, err)
	}
	return nil
}
