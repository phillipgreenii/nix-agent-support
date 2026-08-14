// Package gold owns the hand-labelled evaluation set for Tier 2 (bead pg2-oisvb,
// criterion 4).
//
// # Why the gold set is not committed to this repository
//
// Every entry quotes a real transcript or the operator's own critique of real work.
// The default location is therefore OUTSIDE the repo, beside the index it evaluates
// ($XDG_DATA_HOME/pg-ccaudit/goldset.jsonl), and the only gold data in git is the
// small SYNTHETIC fixture the unit tests use. A gold set that had to be committed
// would either be scrubbed until it stopped resembling the corpus, or it would
// publish the corpus.
//
// # The two kinds of entry, and why they are counted separately
//
// A LABELLED entry references a Tier 1 candidate by (signal, path, seq) and carries
// a class. Those are the entries precision and recall are computed over.
//
// A FILE-CHANNEL entry references no candidate at all, because it came from a file
// rather than a session: the `feedback_*.md` memories under
// ~/.claude/projects/*/memory/ and the workspace's own FEEDBACK.md. These are the
// most valuable corrections on the machine — human-authored, already distilled,
// each with an explicit reason — and they are STRUCTURALLY INVISIBLE to a
// transcript census. That is not a gap to paper over: it means any correction rate
// derived from transcripts alone UNDERCOUNTS by an unknown margin, and the honest
// move is to count the channel and say so. Reporting a transcript-only rate as
// "the correction rate" is wrong, so FileChannel is carried into the report.
package gold

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnvPath overrides the gold set location.
const EnvPath = "PG_CCAUDIT_GOLD"

// Source says where an entry's ground truth came from.
type Source string

const (
	// SourceHandLabelled is a Tier 1 candidate read and labelled by a person or an
	// agent working from the transcript text.
	SourceHandLabelled Source = "hand-labelled"
	// SourceFileChannel is a correction the operator wrote into a FILE instead of a
	// session, so no transcript turn exists to detect.
	SourceFileChannel Source = "file-channel"
)

// Entry is one ground-truth label.
type Entry struct {
	// ID is classify.CandidateID for a labelled entry, or `file:<path>` for a
	// file-channel entry.
	ID     string `json:"id"`
	Source Source `json:"source"`
	Signal string `json:"signal,omitempty"`
	Path   string `json:"path,omitempty"`
	Seq    int64  `json:"seq,omitempty"`
	// Class is the hand label. Empty means UNLABELLED — `gold sample` emits
	// unlabelled rows precisely so the labelling step is visible and reviewable
	// rather than implied.
	Class string `json:"class"`
	// Labeller records who assigned the class. Recorded rather than assumed
	// because it bounds what the evaluation proves: an agent-labelled gold set
	// measures agreement between two models, and only an operator-labelled one
	// measures agreement with the person whose attention the corrections cost.
	Labeller string `json:"labeller,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Labelled reports whether the entry can take part in scoring.
func (e Entry) Labelled() bool { return strings.TrimSpace(e.Class) != "" }

// Set is a gold file.
type Set struct {
	Entries []Entry
}

// Labelled returns the entries that reference a candidate AND carry a class.
func (s Set) Labelled() []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if e.Source == SourceFileChannel || !e.Labelled() {
			continue
		}
		out = append(out, e)
	}
	return out
}

// FileChannel returns the corrections that never appeared in a transcript.
func (s Set) FileChannel() []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if e.Source == SourceFileChannel {
			out = append(out, e)
		}
	}
	return out
}

// Unlabelled returns candidate-referencing entries still awaiting a class.
func (s Set) Unlabelled() []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if e.Source != SourceFileChannel && !e.Labelled() {
			out = append(out, e)
		}
	}
	return out
}

// DefaultPath resolves the gold set location, beside the index by default.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "pg-ccaudit", "goldset.jsonl"), nil
}

// Load reads a gold set. A malformed line fails the load rather than being
// skipped: unlike the transcript corpus, where per-line tolerance keeps coverage
// provable, a gold set is small and hand-maintained, so a line that does not parse
// is a mistake to fix, and silently dropping it would inflate every score computed
// against the entries that remained.
func Load(path string) (Set, error) {
	f, err := os.Open(path)
	if err != nil {
		return Set{}, fmt.Errorf("open gold set %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var set Set
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	n := 0
	seen := make(map[string]bool)
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return Set{}, fmt.Errorf("gold set %s line %d: %w", path, n, err)
		}
		if e.ID == "" {
			return Set{}, fmt.Errorf("gold set %s line %d: entry has no id", path, n)
		}
		if seen[e.ID] {
			return Set{}, fmt.Errorf("gold set %s line %d: duplicate id %q", path, n, e.ID)
		}
		seen[e.ID] = true
		set.Entries = append(set.Entries, e)
	}
	if err := sc.Err(); err != nil {
		return Set{}, fmt.Errorf("read gold set %s: %w", path, err)
	}
	return set, nil
}

// Save writes a gold set, creating the parent directory.
func Save(path string, set Set) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create gold set directory %s: %w", dir, err)
		}
	}
	var sb strings.Builder
	for _, e := range set.Entries {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode gold entry %s: %w", e.ID, err)
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write gold set %s: %w", path, err)
	}
	return nil
}

// Merge folds new entries into an existing set, PRESERVING every existing label.
//
// Preservation is the whole contract: re-seeding or re-sampling must never discard
// hand labelling, or the gold set silently resets to unlabelled and the evaluation
// it feeds reports on nothing while still printing numbers.
func Merge(existing, incoming Set) Set {
	byID := make(map[string]int, len(existing.Entries))
	out := Set{Entries: append([]Entry(nil), existing.Entries...)}
	for i, e := range out.Entries {
		byID[e.ID] = i
	}
	for _, e := range incoming.Entries {
		if i, ok := byID[e.ID]; ok {
			if !out.Entries[i].Labelled() && e.Labelled() {
				out.Entries[i].Class = e.Class
				out.Entries[i].Labeller = e.Labeller
			}
			if out.Entries[i].Excerpt == "" {
				out.Entries[i].Excerpt = e.Excerpt
			}
			continue
		}
		byID[e.ID] = len(out.Entries)
		out.Entries = append(out.Entries, e)
	}
	return out
}

// FeedbackFile is one discovered file-channel correction source.
type FeedbackFile struct {
	Path  string
	Title string
	Lines int
}

// DiscoverFeedback finds the file-channel correction sources.
//
// Two shapes, both real and both verified present on this machine: the
// `feedback_*.md` memories under each project's memory directory (13 across all
// project directories), and a whole-workspace FEEDBACK.md. Nothing is inferred from
// a filename beyond the `feedback` prefix — the CONTENT is the operator's, and
// this function's only job is to find it and count it.
func DiscoverFeedback(memoryRoot string, extra []string) ([]FeedbackFile, error) {
	var out []FeedbackFile
	matches, err := filepath.Glob(filepath.Join(memoryRoot, "*", "memory", "feedback_*.md"))
	if err != nil {
		return nil, fmt.Errorf("scan memory root %s: %w", memoryRoot, err)
	}
	sort.Strings(matches)
	for _, m := range matches {
		ff, err := describe(m)
		if err != nil {
			return nil, err
		}
		out = append(out, ff)
	}
	for _, p := range extra {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		ff, err := describe(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ff)
	}
	return out, nil
}

func describe(path string) (FeedbackFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return FeedbackFile{}, fmt.Errorf("read feedback file %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	title := ""
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			title = strings.TrimSpace(strings.TrimLeft(t, "# "))
			break
		}
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	return FeedbackFile{Path: path, Title: title, Lines: len(lines)}, nil
}

// FromFeedback turns discovered feedback files into file-channel gold entries.
//
// They are labelled user-correction on arrival and NOT left for a labeller: the
// operator wrote each one specifically to correct agent behaviour, so the ground
// truth is the file's existence. Their value is as the count of a channel a
// transcript census cannot see, and as the seed vocabulary for what a real
// correction looks like.
func FromFeedback(files []FeedbackFile) Set {
	var set Set
	for _, f := range files {
		set.Entries = append(set.Entries, Entry{
			ID:       "file:" + f.Path,
			Source:   SourceFileChannel,
			Class:    "user-correction",
			Labeller: "operator (authored the file)",
			Excerpt:  f.Title,
			Note:     fmt.Sprintf("%d line(s); correction written to a FILE, not into a session", f.Lines),
		})
	}
	return set
}
