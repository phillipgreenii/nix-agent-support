package stranded

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// Statuses are the stored statuses a claim can be in; a bead is a candidate
// only while its stored status is one of these and it has an assignee. The
// order is the label order of the exported metric.
func Statuses() []string { return []string{"open", "in_progress", "hooked"} }

// uuidRE matches a lower-case UUID anywhere in an assignee, which covers
// <uuid>-drain, drain-<uuid> and a plain <uuid>. Upper-case is deliberately not
// matched: session ids are written in lower case.
var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// Config configures a Classifier.
type Config struct {
	// ClaudeDir is the Claude data directory; transcripts live under its
	// projects subdirectory.
	ClaudeDir string
	// Window is how recent a transcript write must be to count as activity.
	Window time.Duration
	// OperatorNames are assignees whose name appears in nearly every
	// transcript; their claims are judged by claim age instead.
	OperatorNames []string
}

// Claim is one claim that has no live owner. Its fields are the bead's own;
// nothing here is transcript text.
type Claim struct {
	BeadID   string
	Assignee string
	Status   string
	// ClaimTime is started_at, else updated_at; zero when the bead has neither.
	ClaimTime time.Time
}

// Result is the outcome of one classification: the claims with no live owner,
// ordered by bead id. It is the stranded pass's metrics.Emitter.
type Result struct {
	Claims []Claim
}

// Samples implements metrics.Emitter.
func (r Result) Samples(db string) []metrics.Sample {
	counts := map[string]int{}
	var oldest time.Time
	for _, c := range r.Claims {
		counts[c.Status]++
		if !c.ClaimTime.IsZero() && (oldest.IsZero() || c.ClaimTime.Before(oldest)) {
			oldest = c.ClaimTime
		}
	}
	var out []metrics.Sample
	for _, st := range Statuses() {
		out = append(out, metrics.Sample{
			Family: metrics.FamStrandedClaims,
			Labels: metrics.SeriesLabel("db", db, "status", st),
			Value:  float64(counts[st]),
		})
	}
	if !oldest.IsZero() {
		out = append(out, metrics.Sample{
			Family: metrics.FamOldestStranded,
			Labels: metrics.SeriesLabel("db", db),
			Value:  float64(oldest.Unix()),
		})
	}
	return out
}

// Classifier decides which claims have no live owner. It holds the incremental
// transcript cache, so one Classifier serves exactly one database's candidate
// stream; it is not safe for concurrent use.
type Classifier struct {
	cfg       Config
	operators map[string]struct{}
	scanner   *Scanner
	stat      func(string) (fs.FileInfo, error)
}

// NewClassifier builds a Classifier. open is the transcript opener; nil means
// os.Open.
func NewClassifier(cfg Config, open Opener) *Classifier {
	ops := make(map[string]struct{}, len(cfg.OperatorNames))
	for _, n := range cfg.OperatorNames {
		ops[n] = struct{}{}
	}
	return &Classifier{cfg: cfg, operators: ops, scanner: NewScanner(open), stat: os.Stat}
}

func (c *Classifier) isCandidate(b bd.Bead) bool {
	if b.Assignee == "" {
		return false
	}
	for _, st := range Statuses() {
		if b.Status == st {
			return true
		}
	}
	return false
}

func claimTime(b bd.Bead) time.Time {
	switch {
	case b.StartedAt != nil:
		return *b.StartedAt
	case b.UpdatedAt != nil:
		return *b.UpdatedAt
	}
	return time.Time{}
}

// Classify returns the claims among beads that have no live owner at now. A
// claim is live when any of these holds:
//
//  1. a lower-case UUID inside the assignee names a transcript
//     <claudeDir>/projects/<slug>/<uuid>.jsonl written within the window;
//  2. a transcript (a session's own or a subagent's, never a statusline
//     sidecar) written within the window uses the assignee as a claim value
//     on a line the session wrote (output a tool printed does not count);
//  3. the assignee is an operator name and the claim itself is younger than the
//     window (rule 2 is skipped for these, since the name is everywhere).
//
// A transcript that cannot be listed or read fails the whole classification
// with a failure.TranscriptError: guessing would raise a false alarm or hide a
// real one.
func (c *Classifier) Classify(ctx context.Context, beads []bd.Bead, now time.Time) (Result, error) {
	cutoff := now.Add(-c.cfg.Window)
	var cands []bd.Bead
	needTranscripts := false
	for _, b := range beads {
		if !c.isCandidate(b) {
			continue
		}
		cands = append(cands, b)
		if _, op := c.operators[b.Assignee]; !op {
			needTranscripts = true
		}
	}

	var (
		liveSessions = map[string]struct{}{}
		claimValues  map[string]struct{}
	)
	if needTranscripts {
		var err error
		liveSessions, claimValues, err = c.transcriptActivity(ctx, cutoff)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{}, failure.New(failure.TranscriptError, "stranded", err)
		}
	}

	var res Result
	for _, b := range cands {
		since := claimTime(b)
		if _, op := c.operators[b.Assignee]; op {
			if !since.IsZero() && !since.Before(cutoff) {
				continue
			}
		} else if c.live(b.Assignee, liveSessions, claimValues) {
			continue
		}
		res.Claims = append(res.Claims, Claim{BeadID: b.ID, Assignee: b.Assignee, Status: b.Status, ClaimTime: since})
	}
	sort.Slice(res.Claims, func(i, j int) bool { return res.Claims[i].BeadID < res.Claims[j].BeadID })
	return res, nil
}

func (c *Classifier) live(assignee string, sessions, claimValues map[string]struct{}) bool {
	for _, id := range uuidRE.FindAllString(assignee, -1) {
		if _, ok := sessions[id]; ok {
			return true
		}
	}
	_, ok := claimValues[assignee]
	return ok
}

// transcriptActivity lists the transcripts, drops those written before cutoff
// without opening them, and returns the ids of the sessions whose own
// transcript is in the window plus every claim value written in any in-window
// transcript.
func (c *Classifier) transcriptActivity(ctx context.Context, cutoff time.Time) (map[string]struct{}, map[string]struct{}, error) {
	files, err := ct.DiscoverTranscripts(filepath.Join(c.cfg.ClaudeDir, "projects"))
	if err != nil {
		return nil, nil, err
	}
	sessions := map[string]struct{}{}
	var sources []Source
	for _, f := range files {
		info, err := c.stat(f.Path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, nil, err
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		if !f.Subagent {
			sessions[strings.TrimSuffix(filepath.Base(f.Path), ".jsonl")] = struct{}{}
		}
		sources = append(sources, Source{Path: f.Path, Info: info})
	}
	values, err := c.scanner.Claims(ctx, sources)
	if err != nil {
		return nil, nil, err
	}
	return sessions, values, nil
}
