package escalate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Labels every escalation bead carries (pending-review policy 5): `human` so it
// is a human's to resolve, `human-focus-required` so the operator sees it
// quickly instead of finding it on a passive dashboard.
const (
	LabelHuman              = "human"
	LabelHumanFocusRequired = "human-focus-required"
)

// DefaultEscalationLabel is the label the tracker's named list query uses to
// find the open escalation beads (see Config.EscalationLabel).
const DefaultEscalationLabel = "pending-review-escalation"

// Metadata keys written on an escalation bead. The bead is the only state this
// tool keeps; nothing is cached in a file.
const (
	// KeyKey is the stable dedupe key: "pr:<pr id>" for a per-PR bead and
	// "rollup:<reason>" for a systemic roll-up bead.
	KeyKey = "review_escalation_key"
	// KeyKind is KindPR or KindRollup.
	KeyKind = "review_escalation_kind"
	// KeyReason is the blocked_human_pending reason last seen.
	KeyReason = "review_escalation_reason"
	// KeyPR (per-PR bead) names the PR.
	KeyPR = "review_escalation_pr"
	// KeyHead is the PR head the blocked submit targeted.
	KeyHead = "review_escalation_head"
	// KeyReviewURL is the web URL of the pending review that could not be removed.
	KeyReviewURL = "review_escalation_review_url"
	// KeyLastNotified is when the operator was last notified (RFC 3339, UTC).
	// It is written only after a notification was delivered.
	KeyLastNotified = "review_escalation_last_notified_at"
	// KeyPRs (roll-up bead) is the sorted, ";"-joined set of PRs it covers.
	KeyPRs = "review_escalation_prs"
)

// Values of KeyKind.
const (
	KindPR     = "pr"
	KindRollup = "rollup"
)

// Defaults for Config. They are configuration, not constants of the design:
// the operator MAY overrule them from the command line.
const (
	// DefaultRenotifyInterval is how long a repeat blocked outcome for the same
	// PR (or roll-up) stays silent after the last push notification. The first
	// notification is immediate; later ones are reminders.
	DefaultRenotifyInterval = 12 * time.Hour
	// DefaultRollupThreshold: when MORE than this many PRs are blocked for the
	// same systemic reason, one roll-up bead replaces per-PR beads.
	DefaultRollupThreshold = 3
)

// DefaultSystemicReasons are the non-human reasons: they mean the tool or its
// credentials are failing, so a burst of them is one problem, not many PRs'
// problems. human_edited is never systemic: it is genuinely per-PR.
var DefaultSystemicReasons = []string{ReasonDetectionFailed, ReasonDeleteRefused, ReasonArchiveFailed}

// Issue is the part of a tracker issue that escalation reads back.
type Issue struct {
	ID       string
	Title    string
	Labels   []string
	Metadata map[string]string
}

// NewIssue is a bead to create.
type NewIssue struct {
	Title       string
	Description string
	Priority    string
	Labels      []string
	Metadata    map[string]string
}

// Tracker is the port to the issue tracker. Every error a method returns is a
// delivery failure that Handle surfaces.
type Tracker interface {
	// ListOpen returns every OPEN escalation bead (per-PR and roll-up), across
	// all non-closed states. An incomplete answer MUST be an error: a partial
	// list would let a duplicate be created.
	ListOpen(ctx context.Context) ([]Issue, error)
	Create(ctx context.Context, in NewIssue) (Issue, error)
	Comment(ctx context.Context, id, body string) error
	SetMetadata(ctx context.Context, id string, md map[string]string) error
	Close(ctx context.Context, id, reason string) error
}

// Notification is one push notification. The text is plain and untrusted
// (it carries tool output); a Notifier MUST NOT interpolate it into a shell.
type Notification struct {
	// Key is the escalation's dedupe key, for a notifier that wants to collapse
	// repeats on its side as well.
	Key   string
	Title string
	Body  string
	// URL is the pending review's web URL when known.
	URL string
}

// Notifier is the port to the operator's push channel.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Config is the escalation policy. The zero value of each field selects its
// default.
type Config struct {
	RenotifyInterval time.Duration
	// RollupThreshold: more than this many PRs blocked for one systemic reason
	// are rolled up into a single bead. Zero selects DefaultRollupThreshold; a
	// negative value disables roll-up.
	RollupThreshold int
	SystemicReasons []string
	// EscalationLabel is the lookup label every escalation bead carries.
	EscalationLabel string
	// Labels are extra labels for every created bead (for example a repo label
	// the tracker requires). They are supplied by the deployment, never
	// defaulted here.
	Labels   []string
	Priority string
}

func (c Config) withDefaults() Config {
	if c.RenotifyInterval <= 0 {
		c.RenotifyInterval = DefaultRenotifyInterval
	}
	if c.RollupThreshold == 0 {
		c.RollupThreshold = DefaultRollupThreshold
	}
	if len(c.SystemicReasons) == 0 {
		c.SystemicReasons = DefaultSystemicReasons
	}
	if c.EscalationLabel == "" {
		c.EscalationLabel = DefaultEscalationLabel
	}
	return c
}

// Report lists what one Handle call did, one line per action, for the log.
type Report struct {
	Actions []string
}

func (r *Report) add(format string, a ...any) {
	r.Actions = append(r.Actions, fmt.Sprintf(format, a...))
}

// Escalator applies the escalation policy.
type Escalator struct {
	tracker  Tracker
	notifier Notifier
	now      func() time.Time
	cfg      Config
}

// New builds an Escalator. now is the clock (time.Now in production, a fake in
// tests).
func New(t Tracker, n Notifier, now func() time.Time, cfg Config) *Escalator {
	return &Escalator{tracker: t, notifier: n, now: now, cfg: cfg.withDefaults()}
}

// Handle applies one review-submit outcome.
//
//   - blocked_human_pending raises (or bumps) exactly one escalation for the PR
//     and sends a push notification, no more often than the re-notify interval;
//   - posted, skipped and replaced close the PR's escalation.
//
// Handle never swallows a failure. The bead path and the notification path are
// independent: a failure on one does not skip the other, and every failure is
// returned (joined), each prefixed "tracker:" or "notify:".
func (e *Escalator) Handle(ctx context.Context, o Outcome) (Report, error) {
	var rep Report
	issues, listErr := e.tracker.ListOpen(ctx)
	switch o.Status {
	case StatusPosted, StatusSkipped, StatusReplaced:
		if listErr != nil {
			return rep, fmt.Errorf("tracker: list open escalations: %w", listErr)
		}
		return rep, e.resolve(ctx, &rep, o, index(issues))
	case StatusBlocked:
		if listErr != nil {
			// Dedupe is impossible, but the operator MUST still hear about a
			// stuck review: notify, then report both problems.
			errs := []error{fmt.Errorf("tracker: list open escalations: %w", listErr)}
			if err := e.send(ctx, &rep, e.notification(o, "")); err != nil {
				errs = append(errs, err)
			}
			return rep, errors.Join(errs...)
		}
		return rep, e.escalate(ctx, &rep, o, index(issues))
	default:
		return rep, fmt.Errorf("unknown status %q", o.Status)
	}
}

// view is the open escalations indexed for lookup.
type view struct {
	byPR     map[string]Issue // per-PR beads keyed by PR id
	rollups  map[string]Issue // roll-up beads keyed by reason
	prBeads  []Issue          // per-PR beads, in list order
	rollList []Issue
}

func index(issues []Issue) view {
	v := view{byPR: map[string]Issue{}, rollups: map[string]Issue{}}
	for _, is := range issues {
		switch is.Metadata[KeyKind] {
		case KindPR:
			if pr := is.Metadata[KeyPR]; pr != "" {
				v.byPR[pr] = is
				v.prBeads = append(v.prBeads, is)
			}
		case KindRollup:
			if r := is.Metadata[KeyReason]; r != "" {
				v.rollups[r] = is
				v.rollList = append(v.rollList, is)
			}
		}
	}
	return v
}

func prKey(pr string) string         { return "pr:" + pr }
func rollupKey(reason string) string { return "rollup:" + reason }

func splitPRs(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, ";")
}

func joinPRs(prs []string) string {
	sort.Strings(prs)
	return strings.Join(prs, ";")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func without(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// resolve closes the PR's escalation and removes it from any roll-up.
func (e *Escalator) resolve(ctx context.Context, rep *Report, o Outcome, v view) error {
	var errs []error
	why := fmt.Sprintf("resolved: the review submit for %s answered %s", o.PR, o.Status)
	if o.HeadSHA != "" {
		why += " at head " + o.HeadSHA
	}
	// Every per-PR bead for the PR is closed, so a duplicate left by a race
	// does not outlive the block it was raised for.
	for _, own := range v.prBeads {
		if own.Metadata[KeyPR] != o.PR {
			continue
		}
		if err := e.tracker.Close(ctx, own.ID, why); err != nil {
			errs = append(errs, fmt.Errorf("tracker: close %s: %w", own.ID, err))
		} else {
			rep.add("closed %s (%s)", own.ID, why)
		}
	}
	for _, r := range v.rollList {
		if contains(splitPRs(r.Metadata[KeyPRs]), o.PR) {
			if err := e.leaveRollup(ctx, rep, r, o.PR, why); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// leaveRollup removes pr from a roll-up bead, closing the bead when it was the
// last one.
func (e *Escalator) leaveRollup(ctx context.Context, rep *Report, r Issue, pr, why string) error {
	rest := without(splitPRs(r.Metadata[KeyPRs]), pr)
	if len(rest) == 0 {
		if err := e.tracker.Close(ctx, r.ID, why+"; no PR is left in this roll-up"); err != nil {
			return fmt.Errorf("tracker: close roll-up %s: %w", r.ID, err)
		}
		rep.add("closed roll-up %s (last PR %s left)", r.ID, pr)
		return nil
	}
	if err := e.tracker.SetMetadata(ctx, r.ID, map[string]string{KeyPRs: joinPRs(rest)}); err != nil {
		return fmt.Errorf("tracker: update roll-up %s: %w", r.ID, err)
	}
	if err := e.tracker.Comment(ctx, r.ID, fmt.Sprintf("%s left this roll-up: %s. %d PR(s) remain: %s", pr, why, len(rest), strings.Join(rest, ", "))); err != nil {
		return fmt.Errorf("tracker: comment on roll-up %s: %w", r.ID, err)
	}
	rep.add("removed %s from roll-up %s", pr, r.ID)
	return nil
}

// escalate raises or bumps the escalation for a blocked outcome.
func (e *Escalator) escalate(ctx context.Context, rep *Report, o Outcome, v view) error {
	var errs []error

	// A PR that is now blocked for a different reason leaves the roll-up of its
	// old reason.
	for _, r := range v.rollList {
		if r.Metadata[KeyReason] != o.Reason && contains(splitPRs(r.Metadata[KeyPRs]), o.PR) {
			if err := e.leaveRollup(ctx, rep, r, o.PR, "it is now blocked for "+o.Reason); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if own, ok := v.byPR[o.PR]; ok {
		errs = append(errs, e.bump(ctx, rep, own, o)...)
		return errors.Join(errs...)
	}
	if r, ok := v.rollups[o.Reason]; ok {
		errs = append(errs, e.joinRollup(ctx, rep, r, o)...)
		return errors.Join(errs...)
	}
	if e.cfg.RollupThreshold > 0 && contains(e.cfg.SystemicReasons, o.Reason) {
		var others []string
		for _, b := range v.prBeads {
			if b.Metadata[KeyReason] == o.Reason && b.Metadata[KeyPR] != o.PR {
				others = append(others, b.Metadata[KeyPR])
			}
		}
		if len(others)+1 > e.cfg.RollupThreshold {
			errs = append(errs, e.createRollup(ctx, rep, o, others)...)
			return errors.Join(errs...)
		}
	}
	errs = append(errs, e.createPR(ctx, rep, o)...)
	return errors.Join(errs...)
}

// due reports whether a notification is owed given the last one's time.
func (e *Escalator) due(last string) bool {
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return e.now().Sub(t) >= e.cfg.RenotifyInterval
}

func (e *Escalator) stamp() string { return e.now().UTC().Format(time.RFC3339) }

// bump records a repeat blocked outcome on the PR's existing bead. It never
// creates a second bead, and notifies only when the re-notify interval passed.
func (e *Escalator) bump(ctx context.Context, rep *Report, own Issue, o Outcome) []error {
	var errs []error
	md := map[string]string{KeyReason: o.Reason}
	if o.HeadSHA != "" {
		md[KeyHead] = o.HeadSHA
	}
	if o.ReviewURL != "" {
		md[KeyReviewURL] = o.ReviewURL
	}
	if err := e.tracker.SetMetadata(ctx, own.ID, md); err != nil {
		errs = append(errs, fmt.Errorf("tracker: update %s: %w", own.ID, err))
	}
	if err := e.tracker.Comment(ctx, own.ID, fmt.Sprintf("Blocked again at %s: %s. %s", e.stamp(), o.Reason, detail(o))); err != nil {
		errs = append(errs, fmt.Errorf("tracker: comment on %s: %w", own.ID, err))
	} else {
		rep.add("bumped %s (no second bead)", own.ID)
	}
	e.notifyIfDue(ctx, rep, own, e.notification(o, own.ID), &errs)
	return errs
}

// notifyIfDue sends n when the bead's last notification is older than the
// re-notify interval, and records the time only after a delivered notification.
func (e *Escalator) notifyIfDue(ctx context.Context, rep *Report, bead Issue, n Notification, errs *[]error) {
	last := bead.Metadata[KeyLastNotified]
	if !e.due(last) {
		rep.add("notification suppressed for %s (last sent %s, re-notify interval %s)", bead.ID, last, e.cfg.RenotifyInterval)
		return
	}
	if err := e.send(ctx, rep, n); err != nil {
		*errs = append(*errs, err)
		return
	}
	if err := e.tracker.SetMetadata(ctx, bead.ID, map[string]string{KeyLastNotified: e.stamp()}); err != nil {
		*errs = append(*errs, fmt.Errorf("tracker: record notification time on %s: %w", bead.ID, err))
	}
}

func (e *Escalator) send(ctx context.Context, rep *Report, n Notification) error {
	if err := e.notifier.Notify(ctx, n); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	rep.add("notified: %s", n.Title)
	return nil
}

func (e *Escalator) labels() []string {
	out := []string{LabelHuman, LabelHumanFocusRequired, e.cfg.EscalationLabel}
	for _, l := range e.cfg.Labels {
		if !contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func (e *Escalator) createPR(ctx context.Context, rep *Report, o Outcome) []error {
	var errs []error
	md := map[string]string{
		KeyKey:    prKey(o.PR),
		KeyKind:   KindPR,
		KeyPR:     o.PR,
		KeyReason: o.Reason,
	}
	if o.HeadSHA != "" {
		md[KeyHead] = o.HeadSHA
	}
	if o.ReviewURL != "" {
		md[KeyReviewURL] = o.ReviewURL
	}
	created, err := e.tracker.Create(ctx, NewIssue{
		Title:       "Pending review blocked: " + o.PR,
		Description: describePR(o),
		Priority:    e.cfg.Priority,
		Labels:      e.labels(),
		Metadata:    md,
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("tracker: create escalation for %s: %w", o.PR, err))
		// The operator still hears about it.
		if nerr := e.send(ctx, rep, e.notification(o, "")); nerr != nil {
			errs = append(errs, nerr)
		}
		return errs
	}
	rep.add("created %s for %s (%s)", created.ID, o.PR, o.Reason)
	created.Metadata = md
	e.notifyIfDue(ctx, rep, created, e.notification(o, created.ID), &errs)
	return errs
}

func (e *Escalator) createRollup(ctx context.Context, rep *Report, o Outcome, others []string) []error {
	var errs []error
	prs := append(append([]string{}, others...), o.PR)
	md := map[string]string{
		KeyKey:    rollupKey(o.Reason),
		KeyKind:   KindRollup,
		KeyReason: o.Reason,
		KeyPRs:    joinPRs(prs),
	}
	created, err := e.tracker.Create(ctx, NewIssue{
		Title:       "Pending review blocked on many PRs: " + o.Reason,
		Description: describeRollup(o.Reason, prs, e.cfg.RollupThreshold),
		Priority:    e.cfg.Priority,
		Labels:      e.labels(),
		Metadata:    md,
	})
	n := e.rollupNotification(o, len(prs), "")
	if err != nil {
		errs = append(errs, fmt.Errorf("tracker: create roll-up for %s: %w", o.Reason, err))
		if nerr := e.send(ctx, rep, n); nerr != nil {
			errs = append(errs, nerr)
		}
		return errs
	}
	rep.add("created roll-up %s for %s (%d PRs)", created.ID, o.Reason, len(prs))
	created.Metadata = md
	n = e.rollupNotification(o, len(prs), created.ID)
	e.notifyIfDue(ctx, rep, created, n, &errs)
	return errs
}

// joinRollup adds a PR to the open roll-up of its reason.
func (e *Escalator) joinRollup(ctx context.Context, rep *Report, r Issue, o Outcome) []error {
	var errs []error
	prs := splitPRs(r.Metadata[KeyPRs])
	member := contains(prs, o.PR)
	if !member {
		prs = append(prs, o.PR)
		if err := e.tracker.SetMetadata(ctx, r.ID, map[string]string{KeyPRs: joinPRs(prs)}); err != nil {
			errs = append(errs, fmt.Errorf("tracker: update roll-up %s: %w", r.ID, err))
		}
	}
	verb := "Blocked again"
	if !member {
		verb = "Joined"
	}
	if err := e.tracker.Comment(ctx, r.ID, fmt.Sprintf("%s at %s: %s %s. %s", verb, e.stamp(), o.PR, o.Reason, detail(o))); err != nil {
		errs = append(errs, fmt.Errorf("tracker: comment on roll-up %s: %w", r.ID, err))
	} else {
		rep.add("added %s to roll-up %s (no per-PR bead)", o.PR, r.ID)
	}
	e.notifyIfDue(ctx, rep, r, e.rollupNotification(o, len(prs), r.ID), &errs)
	return errs
}

func detail(o Outcome) string {
	var parts []string
	if o.HeadSHA != "" {
		parts = append(parts, "head "+o.HeadSHA)
	}
	if o.ReviewURL != "" {
		parts = append(parts, "pending review "+o.ReviewURL)
	}
	if o.Message != "" {
		parts = append(parts, o.Message)
	}
	return strings.Join(parts, "; ")
}

func (e *Escalator) notification(o Outcome, beadID string) Notification {
	body := fmt.Sprintf("%s. %s", o.Reason, detail(o))
	if beadID != "" {
		body += " Bead: " + beadID + "."
	}
	return Notification{
		Key:   prKey(o.PR),
		Title: "Pending review stuck: " + o.PR,
		Body:  body,
		URL:   o.ReviewURL,
	}
}

func (e *Escalator) rollupNotification(o Outcome, n int, beadID string) Notification {
	body := fmt.Sprintf("%d PRs are blocked for %s: this looks systemic (credentials, permissions or the host), not a per-PR problem.", n, o.Reason)
	if beadID != "" {
		body += " Bead: " + beadID + "."
	}
	return Notification{
		Key:   rollupKey(o.Reason),
		Title: "Pending reviews stuck on many PRs: " + o.Reason,
		Body:  body,
	}
}

// reasonExplanation says, in plain words, what each reason means and what a
// human can do about it.
func reasonExplanation(reason string) string {
	switch reason {
	case ReasonHumanEdited:
		return "The stale pending review has content the tool cannot prove it posted unedited (a removed marker, an added or edited comment, or no content digest), so it was left untouched."
	case ReasonDeleteRefused:
		return "The host refused to delete the stale pending review (for example it was submitted meanwhile, or the credential may not delete reviews), so it was left in place and nothing new was posted."
	case ReasonDetectionFailed:
		return "The pending review could not be looked up, so nothing was posted, deleted or submitted. This is usually a credential, permission or availability problem, not a problem with this PR."
	case ReasonArchiveFailed:
		return "The stale pending review could not be archived before deletion, so nothing was deleted and nothing new was posted. Check the archive location is writable."
	default:
		return "The stale pending review could not be removed, so nothing new was posted."
	}
}

func describePR(o Outcome) string {
	url := o.ReviewURL
	if url == "" {
		url = "(unknown: the lookup failed)"
	}
	return fmt.Sprintf(`A stale pending review on %s could not be removed automatically, so a human has to resolve it.

PR: %s
Reason: %s
Head: %s
Pending review: %s
Detail: %s

%s

To resolve: open the pending review and delete it, or submit it. This bead closes automatically the next time a review submit for this PR answers posted, skipped or replaced. A repeat of the same block adds a comment here and does not open a second bead.
`, o.PR, o.PR, o.Reason, o.HeadSHA, url, o.Message, reasonExplanation(o.Reason))
}

func describeRollup(reason string, prs []string, threshold int) string {
	sorted := append([]string{}, prs...)
	sort.Strings(sorted)
	return fmt.Sprintf(`More than %d PRs have a stale pending review that could not be removed for the same non-human reason, %s. That points at one shared cause, so this single bead replaces per-PR beads for this reason.

%s

PRs: %s

This bead tracks PRs by its %s metadata. A PR leaves it automatically when its next review submit answers posted, skipped or replaced, and the bead closes when no PR is left. PRs that already had their own bead before the threshold was crossed keep it.
`, threshold, reason, reasonExplanation(reason), strings.Join(sorted, ", "), KeyPRs)
}
