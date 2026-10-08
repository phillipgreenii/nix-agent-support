// Package parity holds the first half of the old-versus-new parity tool: the
// synthetic scenario fixtures and the two runners that load them into a real
// pg-desk store and report what each side planned.
//
// internal/sync is an internal package of another Go module, so this package
// cannot import it. Instead it EXECS the built binaries:
//
//   - RunOld loads a fixture through the pre-cutover code path (pg-desk run
//     with sync.mode = plan) and reads back the writes sync planned.
//   - RunNew loads the SAME fixture, migrates the store with pg-desk migrate
//     --cutover, hydrates the entities and runs pg-decider plan.
//
// Both sides are hermetic: a fresh state directory, config, HOME and PATH, with
// a fixture-driven fake pg-connector first (and only) on PATH. The package
// maps nothing and diffs nothing; that is the sibling packet's job. It imports
// neither packages/pg-desk, packages/pg-pr nor any internal/sync, so the tool
// is deleted with internal/sync as one directory.
package parity

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed testdata/*/scenario.json
var fixtureFS embed.FS

// ErrUnsupported is wrapped by a runner when a scenario cannot be loaded
// through the real binaries. The caller lists such a scenario; it never drops
// it silently.
var ErrUnsupported = errors.New("scenario cannot be loaded through the real binaries")

// FixtureRepo is the one synthetic repository every fixture lives in. pg-desk
// supports exactly one configured repository.
const FixtureRepo = "acme/api"

// fixtureAsOf is the as-of time every synthetic connector answer carries.
const fixtureAsOf = "2026-10-01T00:00:00Z"

// Scenario names one fixture directory and the entities it evaluates.
type Scenario struct {
	Name, Description string
	Entities          []string // canonical entity ids, for example "acme/api#123"
	Dir               string   // fixture directory under testdata
}

// Scenarios lists every embedded scenario, sorted by name. The fixture files
// are validated while listing, so a malformed one fails here, loudly.
func Scenarios() ([]Scenario, error) {
	files, err := fs.Glob(fixtureFS, "testdata/*/scenario.json")
	if err != nil {
		return nil, fmt.Errorf("parity: list scenarios: %w", err)
	}
	var out []Scenario
	for _, f := range files {
		dir := path.Dir(f)
		sc := Scenario{Name: path.Base(dir), Dir: dir}
		fx, err := LoadFixture(sc)
		if err != nil {
			return nil, err
		}
		sc.Description = fx.Description
		sc.Entities = append([]string(nil), fx.Entities...)
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Fixture is one decoded scenario.json: the pull requests the fake
// pg-connector serves, the work beads it lists, and the entities evaluated.
type Fixture struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Entities    []string      `json:"entities"`
	PRs         []PRFixture   `json:"prs"`
	Beads       []BeadFixture `json:"beads,omitempty"`
	// Hidden lists entities hidden through the pre-cutover `pg-desk hide`
	// before the cutover, on both sides.
	Hidden []string `json:"hidden,omitempty"`
	// WorkBeadsOpenOnly makes the fake `issue list --query work-beads` list
	// only beads whose state is "open", the way the production query
	// (`bd list --status open`) does. Unset, it lists every bead, closed
	// included: a stand-in for the ids the old sync already holds in its
	// ledger, which the harness's fresh store cannot seed. `issue show`
	// answers every bead either way.
	WorkBeadsOpenOnly bool `json:"work_beads_open_only,omitempty"`
}

// PRFixture is one synthetic pull request; unset fields take realistic
// defaults (see defaults).
type PRFixture struct {
	Number           int              `json:"number"`
	Title            string           `json:"title,omitempty"`
	State            string           `json:"state,omitempty"` // default "open"
	Branch           string           `json:"branch,omitempty"`
	Base             string           `json:"base,omitempty"` // default "main"
	Author           string           `json:"author,omitempty"`
	Draft            bool             `json:"draft,omitempty"`
	Merged           bool             `json:"merged,omitempty"`
	Labels           []string         `json:"labels,omitempty"`
	HeadSHA          string           `json:"head_sha,omitempty"`
	BaseSHA          string           `json:"base_sha,omitempty"`
	Mergeable        string           `json:"mergeable,omitempty"`          // default "MERGEABLE"
	MergeStateStatus string           `json:"merge_state_status,omitempty"` // default "CLEAN"
	ChecksRollup     string           `json:"checks_rollup,omitempty"`      // default "none"
	ReviewDecision   string           `json:"review_decision,omitempty"`
	NodeID           string           `json:"node_id,omitempty"`
	Comments         []CommentFixture `json:"comments,omitempty"`
	// CommitAuthors are the authors of the PR's commits, oldest first; empty
	// means one commit by the PR author.
	CommitAuthors []string       `json:"commit_authors,omitempty"`
	CI            []CIRunFixture `json:"ci,omitempty"`
}

// CommentFixture is one top-level PR comment.
type CommentFixture struct {
	ID       string `json:"id"`
	Author   string `json:"author,omitempty"`
	Body     string `json:"body,omitempty"`
	Resolved bool   `json:"resolved,omitempty"`
}

// CIRunFixture is one CI run on the PR's head commit.
type CIRunFixture struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"` // default "completed"
	Conclusion string `json:"conclusion,omitempty"`
	Attempt    int    `json:"attempt,omitempty"` // default 1
	HeadSHA    string `json:"head_sha,omitempty"`
}

// BeadFixture is one work bead in the tracker, in its PRE-CUTOVER shape: no
// dedup_key is ever generated for it.
type BeadFixture struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	State     string            `json:"state,omitempty"` // open, in_progress or closed; default open
	IssueType string            `json:"issue_type,omitempty"`
	Parent    string            `json:"parent,omitempty"`
	Labels    []string          `json:"labels,omitempty"`
	Assignee  string            `json:"assignee,omitempty"`
	Priority  string            `json:"priority,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	// Fbsum lists the comment ids a process-feedback bead covers; the loader
	// adds the matching fbsum:<digest> label, so the digest is never typed by
	// hand.
	Fbsum []string `json:"fbsum,omitempty"`
}

// LoadFixture reads and validates the scenario's embedded scenario.json.
func LoadFixture(sc Scenario) (*Fixture, error) {
	if sc.Dir == "" {
		return nil, fmt.Errorf("parity: scenario %q has no fixture directory", sc.Name)
	}
	b, err := fixtureFS.ReadFile(path.Join(sc.Dir, "scenario.json"))
	if err != nil {
		return nil, fmt.Errorf("parity: scenario %q: %w", sc.Name, err)
	}
	fx, err := ParseFixture(b)
	if err != nil {
		return nil, fmt.Errorf("parity: scenario %q: %w", sc.Name, err)
	}
	if want := path.Base(sc.Dir); fx.Name != want {
		return nil, fmt.Errorf("parity: scenario %q: fixture name %q does not match its directory %q", sc.Name, fx.Name, want)
	}
	return fx, nil
}

// ParseFixture decodes and validates one scenario.json. Unknown members are
// an error, so a misspelled field cannot silently change a scenario.
func ParseFixture(data []byte) (*Fixture, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var fx Fixture
	if err := dec.Decode(&fx); err != nil {
		return nil, fmt.Errorf("decode fixture: %w", err)
	}
	if err := fx.validate(); err != nil {
		return nil, err
	}
	for i := range fx.PRs {
		fx.PRs[i].applyDefaults()
	}
	for i := range fx.Beads {
		fx.Beads[i].applyDefaults()
	}
	return &fx, nil
}

func (fx *Fixture) validate() error {
	if fx.Name == "" {
		return errors.New("fixture has no name")
	}
	if fx.Description == "" {
		return fmt.Errorf("fixture %s has no description", fx.Name)
	}
	prs := map[string]bool{}
	for _, p := range fx.PRs {
		if p.Number <= 0 {
			return fmt.Errorf("fixture %s: a PR has no number", fx.Name)
		}
		id := entityID(p.Number)
		if prs[id] {
			return fmt.Errorf("fixture %s: duplicate PR %s", fx.Name, id)
		}
		prs[id] = true
		comments := map[string]bool{}
		for _, c := range p.Comments {
			if c.ID == "" || comments[c.ID] {
				return fmt.Errorf("fixture %s: %s has an empty or duplicate comment id %q", fx.Name, id, c.ID)
			}
			comments[c.ID] = true
		}
	}
	if len(fx.Entities) == 0 {
		return fmt.Errorf("fixture %s evaluates no entities", fx.Name)
	}
	for _, e := range fx.Entities {
		if !strings.HasPrefix(e, FixtureRepo+"#") {
			return fmt.Errorf("fixture %s: entity %q is not in the single synthetic repo %s", fx.Name, e, FixtureRepo)
		}
		if !prs[e] {
			return fmt.Errorf("fixture %s: entity %q has no PR", fx.Name, e)
		}
	}
	for _, h := range fx.Hidden {
		if !contains(fx.Entities, h) {
			return fmt.Errorf("fixture %s: hidden %q is not an evaluated entity", fx.Name, h)
		}
	}
	beads := map[string]bool{}
	for _, b := range fx.Beads {
		if b.ID == "" || b.Title == "" {
			return fmt.Errorf("fixture %s: a bead needs an id and a title", fx.Name)
		}
		if beads[b.ID] {
			return fmt.Errorf("fixture %s: duplicate bead %s", fx.Name, b.ID)
		}
		beads[b.ID] = true
		switch b.State {
		case "", "open", "in_progress", "closed":
		default:
			return fmt.Errorf("fixture %s: bead %s has state %q (want open, in_progress or closed)", fx.Name, b.ID, b.State)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// entityID is the canonical id of fixture PR number n.
func entityID(n int) string { return FixtureRepo + "#" + strconv.Itoa(n) }

func (p *PRFixture) applyDefaults() {
	if p.Title == "" {
		p.Title = fmt.Sprintf("Change %d", p.Number)
	}
	if p.State == "" {
		p.State = "open"
	}
	if p.Branch == "" {
		p.Branch = fmt.Sprintf("feature-%d", p.Number)
	}
	if p.Base == "" {
		p.Base = "main"
	}
	if p.Author == "" {
		p.Author = "teammate"
	}
	if p.HeadSHA == "" {
		p.HeadSHA = fmt.Sprintf("head%04d", p.Number)
	}
	if p.BaseSHA == "" {
		p.BaseSHA = fmt.Sprintf("base%04d", p.Number)
	}
	if p.Mergeable == "" {
		p.Mergeable = "MERGEABLE"
	}
	if p.MergeStateStatus == "" {
		p.MergeStateStatus = "CLEAN"
	}
	if p.ChecksRollup == "" {
		p.ChecksRollup = "none"
	}
	if len(p.CommitAuthors) == 0 {
		p.CommitAuthors = []string{p.Author}
	}
	for i := range p.Comments {
		if p.Comments[i].Author == "" {
			p.Comments[i].Author = "review-bot"
		}
	}
	for i := range p.CI {
		c := &p.CI[i]
		if c.Name == "" {
			c.Name = "build"
		}
		if c.Status == "" {
			c.Status = "completed"
		}
		if c.Attempt == 0 {
			c.Attempt = 1
		}
		if c.HeadSHA == "" {
			c.HeadSHA = p.HeadSHA
		}
	}
}

func (b *BeadFixture) applyDefaults() {
	if b.State == "" {
		b.State = "open"
	}
	if b.IssueType == "" {
		b.IssueType = "task"
		if kindAnchorTitle(b.Title) {
			b.IssueType = "merge-request"
		}
	}
	if b.UpdatedAt == "" {
		b.UpdatedAt = fixtureAsOf
	}
	if len(b.Fbsum) > 0 {
		ids := append([]string(nil), b.Fbsum...)
		sort.Strings(ids)
		b.Labels = append(append([]string(nil), b.Labels...), "fbsum:"+fbsumDigest(ids))
		b.Fbsum = nil
	}
}

// kindAnchorTitle reports the "<repo>#<n>: " anchor title shape.
func kindAnchorTitle(title string) bool {
	i := strings.Index(title, "#")
	if i <= 0 {
		return false
	}
	rest := title[i+1:]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	return j > 0 && strings.HasPrefix(rest[j:], ": ")
}

// fbsumDigest is the digest both pg-desk's sync and pg-decider's feedback rule
// compute for a SORTED set of unaddressed comment ids: sha256 over each id
// followed by a NUL byte, hex-encoded, first 12 characters; empty for no ids.
// It is reproduced here, not imported, so a fixture bead can carry the label an
// old-code feedback cycle carries.
func fbsumDigest(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ---- the fake pg-connector's wire answers -----------------------------------
//
// The shapes are those of pg-connector's pkg/schema (PR, PRCommitsResult,
// PRFilesResult, CIRun, Issue, IssueListResult), hand-written here because this
// module imports nothing from pg-connector.

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("parity: marshal fixture: %v", err))
	}
	return b
}

// wireEnvelope is the targeted-call envelope pg-desk decodes `result` from.
func wireEnvelope(result []byte) []byte {
	return mustJSON(struct {
		ProtocolVersion int             `json:"protocolVersion"`
		Result          json.RawMessage `json:"result"`
	}{1, result})
}

func prURL(n int) string { return fmt.Sprintf("https://code.example/%s/pull/%d", FixtureRepo, n) }

type commentWire struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Resolved bool   `json:"resolved"`
}

type prShowWire struct {
	ID               string        `json:"id"`
	Repo             string        `json:"repo"`
	Number           int           `json:"number"`
	Title            string        `json:"title"`
	State            string        `json:"state"`
	Branch           string        `json:"branch"`
	Base             string        `json:"base"`
	Author           string        `json:"author"`
	URL              string        `json:"url"`
	Draft            bool          `json:"draft"`
	Merged           bool          `json:"merged"`
	Labels           []string      `json:"labels,omitempty"`
	AsOf             string        `json:"as_of"`
	Stale            bool          `json:"stale"`
	Comments         []commentWire `json:"comments,omitempty"`
	HeadSHA          string        `json:"head_sha"`
	BaseSHA          string        `json:"base_sha"`
	Mergeable        string        `json:"mergeable"`
	MergeStateStatus string        `json:"merge_state_status"`
	ChecksRollup     string        `json:"checks_rollup"`
	NodeID           string        `json:"node_id,omitempty"`
	ReviewDecision   string        `json:"review_decision,omitempty"`
}

// prShowJSON is the `result` of `pr show <id>`.
func (fx *Fixture) prShowJSON(p PRFixture) []byte {
	w := prShowWire{
		ID: entityID(p.Number), Repo: FixtureRepo, Number: p.Number, Title: p.Title, State: p.State,
		Branch: p.Branch, Base: p.Base, Author: p.Author, URL: prURL(p.Number), Draft: p.Draft, Merged: p.Merged,
		Labels: p.Labels, AsOf: fixtureAsOf, HeadSHA: p.HeadSHA, BaseSHA: p.BaseSHA, Mergeable: p.Mergeable,
		MergeStateStatus: p.MergeStateStatus, ChecksRollup: p.ChecksRollup, NodeID: p.NodeID, ReviewDecision: p.ReviewDecision,
	}
	for _, c := range p.Comments {
		w.Comments = append(w.Comments, commentWire{ID: c.ID, Author: c.Author, Body: c.Body, Resolved: c.Resolved})
	}
	return mustJSON(w)
}

// prFilesJSON is the `result` of `pr files <id>`: the fixture PRs change no files.
func (fx *Fixture) prFilesJSON(p PRFixture) []byte {
	return mustJSON(map[string]any{"id": entityID(p.Number), "files": []any{}})
}

// prCommitsJSON is the `result` of `pr commits <id>`.
func (fx *Fixture) prCommitsJSON(p PRFixture) []byte {
	type commit struct {
		SHA     string `json:"sha"`
		Author  string `json:"author"`
		Message string `json:"message"`
	}
	commits := make([]commit, 0, len(p.CommitAuthors))
	for i, a := range p.CommitAuthors {
		commits = append(commits, commit{SHA: fmt.Sprintf("c%d-%04d", i+1, p.Number), Author: a, Message: fmt.Sprintf("commit %d", i+1)})
	}
	return mustJSON(map[string]any{"id": entityID(p.Number), "commits": commits})
}

// ciListJSON is the bare fan-out answer of `ci list <id>`.
func (fx *Fixture) ciListJSON(p PRFixture) []byte {
	type run struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		URL        string `json:"url"`
		Provider   string `json:"provider"`
		HeadSHA    string `json:"head_sha"`
		Repo       string `json:"repo"`
		PRID       string `json:"pr_id"`
		Attempt    int    `json:"attempt"`
		AsOf       string `json:"as_of"`
		Stale      bool   `json:"stale"`
	}
	runs := make([]run, 0, len(p.CI))
	for _, c := range p.CI {
		runs = append(runs, run{
			ID: c.ID, Name: c.Name, Status: c.Status, Conclusion: c.Conclusion,
			URL: "https://ci.example/runs/" + c.ID, Provider: "actions", HeadSHA: c.HeadSHA,
			Repo: FixtureRepo, PRID: entityID(p.Number), Attempt: c.Attempt, AsOf: fixtureAsOf,
		})
	}
	return mustJSON(map[string]any{"runs": runs, "sources": []any{}})
}

// reviewPendingJSON is the `result` of `pr review pending <id>`: the fixture
// PRs carry no pending review.
func (fx *Fixture) reviewPendingJSON(p PRFixture) []byte {
	return mustJSON(map[string]any{"pending": false, "head_sha": p.HeadSHA, "as_of": fixtureAsOf})
}

type issueWire struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	State       string            `json:"state"`
	URL         string            `json:"url"`
	Priority    string            `json:"priority,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	IssueType   string            `json:"issue_type,omitempty"`
	Assignee    string            `json:"assignee,omitempty"`
	Parent      string            `json:"parent,omitempty"`
	AsOf        string            `json:"as_of"`
	Stale       bool              `json:"stale"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Description string            `json:"description,omitempty"`
}

func (b BeadFixture) wire() issueWire {
	return issueWire{
		ID: b.ID, Title: b.Title, State: b.State, URL: "https://tracker.example/" + b.ID,
		Priority: b.Priority, Labels: b.Labels, IssueType: b.IssueType, Assignee: b.Assignee,
		Parent: b.Parent, AsOf: fixtureAsOf, UpdatedAt: b.UpdatedAt, Metadata: b.Metadata,
	}
}

// issueShowJSON is the `result` of `issue show <id>`.
func (fx *Fixture) issueShowJSON(b BeadFixture) []byte { return mustJSON(b.wire()) }

// issueDepsJSON is the `result` of `issue deps <id> --full`: no dependencies.
func (fx *Fixture) issueDepsJSON(b BeadFixture) []byte {
	return mustJSON(map[string]any{"ids": []string{}, "entities": []any{}})
}

// workBeadsJSON is the bare fan-out answer of `issue list --query work-beads`.
//
// Production's query is `list --type merge-request --status open` (the
// beads backend's config.queries.work-beads), so it never lists a closed bead.
// By default this fake lists every fixture bead anyway: the old sync resolves
// a bead it created earlier from its ledger, and the harness starts the old
// side with an empty store, so the listing stands in for those ledger ids.
// A fixture sets work_beads_open_only to model the listing literally.
func (fx *Fixture) workBeadsJSON() []byte {
	entities := make([]issueWire, 0, len(fx.Beads))
	ids := make([]string, 0, len(fx.Beads))
	for _, b := range fx.Beads {
		if fx.WorkBeadsOpenOnly && b.State != "open" {
			continue
		}
		entities = append(entities, b.wire())
		ids = append(ids, b.ID)
	}
	return mustJSON(map[string]any{"entities": entities, "present_ids": ids, "sources": []any{}})
}
