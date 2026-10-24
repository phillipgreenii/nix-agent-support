package backfill

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SetContract identifies the shape and version of the persisted backfill set.
const SetContract = "pg-pr-backfill.set/v1"

// The states of an enumerated PR. A PR found through a query is open (the
// queries select open PRs). A PR reached only through its anchor carries the
// state the anchor's metadata records, and `unknown` when it records none.
const (
	StateOpen    = "open"
	StateMerged  = "merged"
	StateClosed  = "closed"
	StateUnknown = "unknown"
)

// The two ways a PR comes into the set.
const (
	FromAnchor = "anchor"
	FromQuery  = "query"
)

// Set is the backfill set `enumerate` persists and `run` and `summary` read.
type Set struct {
	Contract     string `json:"contract"`
	EnumeratedAt string `json:"enumerated_at"`
	// Queries are the PR query names read from the query-name file, and
	// AnchorsQuery the tracker query that listed the anchors.
	Queries      []string `json:"queries"`
	AnchorsQuery string   `json:"anchors_query"`
	// OpenAnchorCount is the number of open anchors the tracker listed (those
	// mapped to a PR and those not), and OpenAnchorPRCount the number of distinct
	// PRs the mapped ones name. Packet P1-a's watched-versus-open-anchor check
	// reads these.
	OpenAnchorCount   int `json:"open_anchor_count"`
	OpenAnchorPRCount int `json:"open_anchor_pr_count"`
	QueryPRCount      int `json:"query_pr_count"`
	// Entries are the PRs to run, sorted by id. Unparseable are the open anchors
	// whose title maps to no PR and the query ids that are no PR id: reported,
	// never dropped.
	Entries     []Entry       `json:"entries"`
	Unparseable []Unparseable `json:"unparseable"`
}

// Entry is one PR of the set.
type Entry struct {
	PR string `json:"pr"`
	// State is open, merged, closed or unknown.
	State string `json:"state"`
	// From says how the PR came into the set: anchor, query, or both.
	From      []string `json:"from"`
	AnchorIDs []string `json:"anchor_ids,omitempty"`
	Queries   []string `json:"queries,omitempty"`
}

// Unparseable is an item the enumeration could not map to a PR.
type Unparseable struct {
	// From is anchor or query; ID the tracker issue id or the query's id; Title
	// the anchor's title.
	From   string `json:"from"`
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Reason string `json:"reason"`
}

// Has reports whether the entry came in through how (anchor or query).
func (e Entry) Has(how string) bool {
	for _, f := range e.From {
		if f == how {
			return true
		}
	}
	return false
}

// IDs lists the PR ids of the set.
func (s *Set) IDs() []string {
	out := make([]string, len(s.Entries))
	for i, e := range s.Entries {
		out[i] = e.PR
	}
	return out
}

// LoadSet reads a persisted set and refuses one that is not the contract or is empty.
func LoadSet(path string) (*Set, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the backfill set %s: %w (run enumerate first)", path, err)
	}
	var s Set
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("the backfill set %s is not JSON: %w", path, err)
	}
	if s.Contract != SetContract {
		return nil, fmt.Errorf("the backfill set %s has contract %q, want %q", path, s.Contract, SetContract)
	}
	if len(s.Entries) == 0 {
		return nil, fmt.Errorf("the backfill set %s lists no PR (run enumerate again; an empty set is never run)", path)
	}
	return &s, nil
}

// Save writes the set to path (0600, directory 0700), through a temporary file
// and a rename so a reader never sees half of it.
func (s *Set) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadQueryNames reads the query-name file the deployment renders: one PR query
// name per line; blank lines and lines starting with # are ignored. It fails
// closed: an absent file, or one that lists no name, is an error, so an
// enumeration can never silently run over no queries.
func ReadQueryNames(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("no query-name file is configured (the deployment renders one; set queries_file in the driver configuration or pass --queries-file)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the query-name file %s: %w (enumerate fails closed rather than enumerate an empty set)", path, err)
	}
	defer func() { _ = f.Close() }()
	var names []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") || seen[l] {
			continue
		}
		seen[l] = true
		names = append(names, l)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("the query-name file %s lists no query (enumerate fails closed rather than enumerate an empty set)", path)
	}
	return names, nil
}

var (
	prIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+$`)
	// anchorTitleRe is the anchor title shape `<owner>/<repo>#<n>: <title>`.
	anchorTitleRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+): `)
)

// PRFromAnchorTitle maps an anchor's title to the PR it names.
func PRFromAnchorTitle(title string) (string, bool) {
	m := anchorTitleRe.FindStringSubmatch(title)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// EnumerateOptions configure Enumerate.
type EnumerateOptions struct {
	// QueriesFile names the file of PR query names (see ReadQueryNames).
	QueriesFile string
	// AnchorsQuery is the tracker's named query that lists the anchors (the
	// merge-request issues, and any other issue the query lists; non-anchors are
	// ignored).
	AnchorsQuery string
	// Backend pins the tracker backend instance and BeadsDir the tracker the
	// connector reads (PG_CONNECTOR_ISSUE_BEADS_DIR); both optional.
	Backend  string
	BeadsDir string
	// Connector is the pg-connector command, resolved on PATH when not absolute.
	Connector string
	Env       []string
	Dir       string
	Runner    Runner
	Now       func() time.Time
}

// ErrEmptySet is returned when the enumeration found no PR at all.
var ErrEmptySet = errors.New("the enumeration found no PR: an empty backfill set is an error, not a result")

// Enumerate lists the backfill set: the tracker's open anchors plus the open
// PRs of the deployment's queries. It is read-only. It fails closed on a query-name
// file that is absent or empty, on any source that did not answer, on a truncated
// listing, and on an empty result.
func Enumerate(ctx context.Context, o EnumerateOptions) (*Set, error) {
	names, err := ReadQueryNames(o.QueriesFile)
	if err != nil {
		return nil, err
	}
	if o.AnchorsQuery == "" {
		return nil, errors.New("no anchors query is configured (the tracker's named query that lists the open anchors; set anchors_query or pass --anchors-query)")
	}
	run := o.Runner
	if run == nil {
		run = ExecRunner
	}
	conn := o.Connector
	if conn == "" {
		conn = "pg-connector"
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	call := func(env []string, args ...string) (Result, error) {
		res := run(ctx, Call{Name: conn, Args: args, Env: append(append([]string{}, o.Env...), env...), Dir: o.Dir})
		if res.StartErr != nil {
			return res, fmt.Errorf("cannot start %s: %w", conn, res.StartErr)
		}
		return res, nil
	}

	s := &Set{
		Contract:     SetContract,
		EnumeratedAt: now().UTC().Format(time.RFC3339),
		Queries:      names,
		AnchorsQuery: o.AnchorsQuery,
		Entries:      []Entry{},
		Unparseable:  []Unparseable{},
	}
	byPR := map[string]*Entry{}
	entry := func(pr string) *Entry {
		e, ok := byPR[pr]
		if !ok {
			e = &Entry{PR: pr, State: StateUnknown}
			byPR[pr] = e
		}
		return e
	}

	// 1. The tracker's open anchors.
	args := []string{"issue", "list", "--query", o.AnchorsQuery}
	var env []string
	if o.Backend != "" {
		args = append(args, "--backend", o.Backend)
	}
	if o.BeadsDir != "" {
		env = append(env, "PG_CONNECTOR_ISSUE_BEADS_DIR="+o.BeadsDir)
	}
	res, err := call(env, args...)
	if err != nil {
		return nil, err
	}
	var issues struct {
		Entities []struct {
			ID        string            `json:"id"`
			Title     string            `json:"title"`
			State     string            `json:"state"`
			IssueType string            `json:"issue_type"`
			Metadata  map[string]string `json:"metadata"`
		} `json:"entities"`
		Sources   []sourceRow `json:"sources"`
		Truncated bool        `json:"truncated"`
	}
	if err := decodeListing(res, "issue list --query "+o.AnchorsQuery, &issues); err != nil {
		return nil, err
	}
	if err := checkSources("issue list --query "+o.AnchorsQuery, issues.Sources, issues.Truncated); err != nil {
		return nil, err
	}
	anchorPRs := map[string]bool{}
	for _, it := range issues.Entities {
		pr, titled := PRFromAnchorTitle(it.Title)
		if it.IssueType != "merge-request" && !titled {
			continue // not an anchor (a child work item)
		}
		if strings.EqualFold(it.State, "closed") {
			continue
		}
		s.OpenAnchorCount++
		if !titled {
			s.Unparseable = append(s.Unparseable, Unparseable{From: FromAnchor, ID: it.ID, Title: it.Title, Reason: "the anchor's title is not <owner>/<repo>#<number>: <title>"})
			continue
		}
		anchorPRs[pr] = true
		e := entry(pr)
		e.AnchorIDs = append(e.AnchorIDs, it.ID)
		if !e.Has(FromAnchor) {
			e.From = append(e.From, FromAnchor)
		}
		if st := normalizeState(it.Metadata["state"]); st != StateUnknown && e.State == StateUnknown {
			e.State = st
		}
	}
	s.OpenAnchorPRCount = len(anchorPRs)

	// 2. The open PRs of the deployment's queries (ids only: the cheapest read).
	queryPRs := map[string]bool{}
	for _, name := range names {
		res, err := call(nil, "pr", "list", "--query", name, "--ids-only")
		if err != nil {
			return nil, err
		}
		var prs struct {
			PresentIDs []string    `json:"present_ids"`
			Sources    []sourceRow `json:"sources"`
			Truncated  bool        `json:"truncated"`
		}
		if err := decodeListing(res, "pr list --query "+name, &prs); err != nil {
			return nil, err
		}
		if err := checkSources("pr list --query "+name, prs.Sources, prs.Truncated); err != nil {
			return nil, err
		}
		for _, id := range prs.PresentIDs {
			if !prIDRe.MatchString(id) {
				s.Unparseable = append(s.Unparseable, Unparseable{From: FromQuery, ID: id, Reason: "query " + name + " listed an id that is not <owner>/<repo>#<number>"})
				continue
			}
			queryPRs[id] = true
			e := entry(id)
			if !e.Has(FromQuery) {
				e.From = append(e.From, FromQuery)
			}
			if !containsStr(e.Queries, name) {
				e.Queries = append(e.Queries, name)
			}
			e.State = StateOpen
		}
	}
	s.QueryPRCount = len(queryPRs)

	for _, pr := range sortedKeys(byPR) {
		e := byPR[pr]
		sort.Strings(e.From)
		sort.Strings(e.AnchorIDs)
		sort.Strings(e.Queries)
		s.Entries = append(s.Entries, *e)
	}
	if len(s.Entries) == 0 {
		return nil, ErrEmptySet
	}
	return s, nil
}

func normalizeState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open", "opened":
		return StateOpen
	case "merged":
		return StateMerged
	case "closed":
		return StateClosed
	}
	return StateUnknown
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

type sourceRow struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// decodeListing reads a connector listing. A listing whose stdout is not JSON, or
// whose exit code is non-zero with no usable sources, is an error carrying the
// connector's own words.
func decodeListing(res Result, what string, into any) error {
	if err := json.Unmarshal([]byte(res.Stdout), into); err != nil {
		return fmt.Errorf("pg-connector %s: exit %d and no readable answer: %s", what, res.Code, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// checkSources fails closed unless at least one source succeeded, none degraded,
// and the listing was not truncated: a partial listing is never a backfill set.
func checkSources(what string, rows []sourceRow, truncated bool) error {
	ok := 0
	for _, r := range rows {
		switch r.Status {
		case "succeeded":
			ok++
		case "disabled":
		default:
			return fmt.Errorf("pg-connector %s: source %s is %s (%s): the listing is incomplete", what, r.Source, r.Status, r.Reason)
		}
	}
	if ok == 0 {
		return fmt.Errorf("pg-connector %s: no source answered", what)
	}
	if truncated {
		return fmt.Errorf("pg-connector %s: the listing was truncated: a partial listing is never a backfill set", what)
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}
