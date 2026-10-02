package classify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The pr classifier: a Strategy comparing two observed pr snapshots. The
// payload is a marshaled gather.Facts; this file decodes only the fields it
// compares and imports neither gather nor pg-connector's schema package.
//
// Signal-to-kind mapping (pinned by one row per kind in pr_test.go):
//
//	opened               old pr_show carried no usable state (absent, null or
//	                     empty) and the new state is open
//	reopened             old state closed or merged, new state open
//	closed               new state closed (closed, not merged), old state differed
//	merged               new state merged (merged flag or state "merged"), old state differed
//	draft_changed        pr_show.draft differs (both sides must carry it)
//	head_changed         pr_show.head_sha differs, falling back to the
//	                     top-level head_sha (both sides must carry one)
//	base_changed         pr_show.base (the base branch name) differs
//	ci_changed           the CI run set differs (run id, attempt, name, status,
//	                     conclusion), or pr_show.checks_rollup differs; the run
//	                     comparison needs a usable CI read on BOTH sides
//	mergeability_changed pr_show.mergeable differs, carried verbatim with
//	                     UNKNOWN as a value (both sides must carry it)
//	review_changed       review_decision, review_count or any review's
//	                     (id, state) differs
//	feedback_changed     comment_count or any comment's (id, resolved), at the
//	                     PR level or inside a review thread, differs
//
// Deliberately not signals: the embedded as_of and stale, updated_at,
// merge_state_status and base_sha (they move with events outside the PR),
// titles, bodies and comment text. work_changed and link_changed belong to
// the local-change-sources packet and are never emitted here.
//
// Missing data is never read as a change: a new snapshot without a pr_show
// yields nothing, an old one without it yields at most opened, and a field
// absent on either side is skipped. closed is additionally withheld whenever
// the new snapshot is degraded, by the Snapshot flag or by gather's own
// Facts.degraded marker, mirroring the core's rule.
func init() {
	Register("pr", prClassifier{})
}

type prClassifier struct{}

// prFacts is the decoded subset of gather.Facts this classifier compares.
type prFacts struct {
	Show     json.RawMessage `json:"pr_show"`
	CI       json.RawMessage `json:"ci"`
	HeadSHA  string          `json:"head_sha"`
	Degraded string          `json:"degraded"`
}

// prShowFields is the decoded subset of schema.PR this classifier compares.
type prShowFields struct {
	State          string      `json:"state"`
	Base           string      `json:"base"`
	Draft          *bool       `json:"draft"`
	Merged         bool        `json:"merged"`
	HeadSHA        string      `json:"head_sha"`
	Mergeable      string      `json:"mergeable"`
	ChecksRollup   string      `json:"checks_rollup"`
	ReviewDecision string      `json:"review_decision"`
	CommentCount   int         `json:"comment_count"`
	ReviewCount    int         `json:"review_count"`
	Comments       []prComment `json:"comments"`
	Reviews        []prReview  `json:"reviews"`
}

type prComment struct {
	ID       string `json:"id"`
	Resolved bool   `json:"resolved"`
}

type prReview struct {
	ID       string      `json:"id"`
	State    string      `json:"state"`
	Comments []prComment `json:"comments"`
}

// prCIResult is the ci list fan-out result ({"runs":[...],"sources":[...]}).
type prCIResult struct {
	Runs    []prCIRun    `json:"runs"`
	Sources []prCISource `json:"sources"`
}

type prCIRun struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Attempt    int    `json:"attempt"`
	Stale      bool   `json:"stale"`
}

type prCISource struct {
	Status string `json:"status"`
}

// prObserved is one side of the comparison after decoding.
type prObserved struct {
	facts   prFacts
	show    prShowFields
	hasShow bool
	state   string // "", "open", "closed" or "merged"
}

func prDecode(s Snapshot) (prObserved, bool) {
	var o prObserved
	if len(bytes.TrimSpace(s.Payload)) == 0 {
		return o, false
	}
	if err := json.Unmarshal(s.Payload, &o.facts); err != nil {
		return o, false
	}
	// An absent, null or empty-object pr_show is missing data.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(o.facts.Show, &probe); err != nil || len(probe) == 0 {
		return o, true
	}
	if err := json.Unmarshal(o.facts.Show, &o.show); err != nil {
		return o, true
	}
	o.hasShow = true
	o.state = prStateOf(o.show)
	return o, true
}

// prStateOf folds state and the merged flag into open, closed or merged, the
// same way gather's removed re-read does. An unrecognised state with no
// merged flag is unknown ("").
func prStateOf(s prShowFields) string {
	st := strings.ToLower(strings.TrimSpace(s.State))
	switch {
	case s.Merged || st == "merged":
		return "merged"
	case st == "open":
		return "open"
	case st == "closed":
		return "closed"
	default:
		return ""
	}
}

func (o prObserved) head() string {
	if o.show.HeadSHA != "" {
		return o.show.HeadSHA
	}
	return o.facts.HeadSHA
}

// prCIFingerprint returns a canonical rendering of the CI run set and whether
// the read is usable: decodable, from at least one succeeded source, with no
// degraded source and no run served stale from a cache. An unusable read is
// missing data, never a CI change.
func prCIFingerprint(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var ci prCIResult
	if err := json.Unmarshal(raw, &ci); err != nil {
		return "", false
	}
	succeeded := false
	for _, s := range ci.Sources {
		switch s.Status {
		case "succeeded":
			succeeded = true
		case "disabled":
		default:
			return "", false
		}
	}
	if !succeeded {
		return "", false
	}
	rows := make([]string, 0, len(ci.Runs))
	for _, r := range ci.Runs {
		if r.Stale {
			return "", false
		}
		rows = append(rows, fmt.Sprintf("%s|%d|%s|%s|%s", r.ID, r.Attempt, r.Name, r.Status, r.Conclusion))
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n"), true
}

func prReviewFingerprint(s prShowFields) string {
	rows := make([]string, 0, len(s.Reviews))
	for _, r := range s.Reviews {
		rows = append(rows, r.ID+"|"+r.State)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

func prFeedbackFingerprint(s prShowFields) string {
	var rows []string
	add := func(scope string, cs []prComment) {
		for _, c := range cs {
			rows = append(rows, fmt.Sprintf("%s|%s|%t", scope, c.ID, c.Resolved))
		}
	}
	add("pr", s.Comments)
	for _, r := range s.Reviews {
		add("review:"+r.ID, r.Comments)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// Classify implements Classifier.
func (prClassifier) Classify(old, new Snapshot) []Record {
	n, ok := prDecode(new)
	if !ok || !n.hasShow {
		return nil
	}
	o, ok := prDecode(old)
	if !ok {
		return nil
	}
	degraded := new.Degraded || n.facts.Degraded != ""

	var out []Record
	emit := func(k Kind) { out = append(out, Record{Kind: k}) }

	if !o.hasShow || o.state == "" {
		// Nothing on the old side to compare against: the only thing a
		// healthy open observation can say is that the PR became visible.
		if n.state == "open" {
			emit(KindOpened)
		}
		return out
	}

	if n.state != "" && n.state != o.state {
		switch n.state {
		case "open":
			emit(KindReopened)
		case "merged":
			emit(KindMerged)
		case "closed":
			if !degraded {
				emit(KindClosed)
			}
		}
	}
	if o.show.Draft != nil && n.show.Draft != nil && *o.show.Draft != *n.show.Draft {
		emit(KindDraftChanged)
	}
	if oh, nh := o.head(), n.head(); oh != "" && nh != "" && oh != nh {
		emit(KindHeadChanged)
	}
	if o.show.Base != "" && n.show.Base != "" && o.show.Base != n.show.Base {
		emit(KindBaseChanged)
	}
	if prCIChanged(o, n) {
		emit(KindCiChanged)
	}
	if o.show.Mergeable != "" && n.show.Mergeable != "" && o.show.Mergeable != n.show.Mergeable {
		emit(KindMergeabilityChanged)
	}
	if o.show.ReviewDecision != n.show.ReviewDecision ||
		o.show.ReviewCount != n.show.ReviewCount ||
		prReviewFingerprint(o.show) != prReviewFingerprint(n.show) {
		emit(KindReviewChanged)
	}
	if o.show.CommentCount != n.show.CommentCount ||
		prFeedbackFingerprint(o.show) != prFeedbackFingerprint(n.show) {
		emit(KindFeedbackChanged)
	}
	return out
}

func prCIChanged(o, n prObserved) bool {
	if o.show.ChecksRollup != "" && n.show.ChecksRollup != "" && o.show.ChecksRollup != n.show.ChecksRollup {
		return true
	}
	of, ook := prCIFingerprint(o.facts.CI)
	nf, nok := prCIFingerprint(n.facts.CI)
	return ook && nok && of != nf
}
