// Package replay is the harness a lens must pass before it gets a schedule.
//
// A fixture holds synthetic evidence rows with known problems and decoys, a
// manifest naming both by subject key, and canned analyst outputs. One replay
// pass assembles the lens prompt, checks that every prompt rule is present,
// hands the prompt to an Analyst, validates the final message with the real
// validator and files it into a real ledger. Evaluate then applies the pass
// criteria to the outcome of two passes over the same snapshot.
//
// The default analyst is canned and deterministic. A live analyst is a
// separate, on-demand check behind the "live" build tag.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/prompt"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// Pass criteria (design 5.9).
const (
	// MinKnownFound is the number of known problems a lens must find.
	MinKnownFound = 3
	// MinTopicStability is the least fraction of topic ids of one run that
	// must recur in a second run of the same snapshot.
	MinTopicStability = 0.8
)

// Manifest names the known problems and the decoys of a fixture by subject
// key.
type Manifest struct {
	Lens   string   `json:"lens"`
	Scope  string   `json:"scope"`
	Known  []string `json:"known"`
	Decoys []string `json:"decoys"`
}

// Fixture is the synthetic snapshot of one lens.
type Fixture struct {
	Dir      string
	Manifest Manifest
	Evidence []validate.EvidenceRow
	// Canned holds the canned analyst outputs (canned-<n>.json), in order.
	Canned [][]byte
}

// LoadFixture reads testdata/replay/<lens> under root.
func LoadFixture(root, lens string) (Fixture, error) {
	dir := filepath.Join(root, lens)
	f := Fixture{Dir: dir}
	if err := readJSON(filepath.Join(dir, "manifest.json"), &f.Manifest); err != nil {
		return f, err
	}
	if err := readJSON(filepath.Join(dir, "evidence.json"), &f.Evidence); err != nil {
		return f, err
	}
	for n := 1; ; n++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("canned-%d.json", n)))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return f, err
		}
		f.Canned = append(f.Canned, b)
	}
	if len(f.Canned) == 0 {
		return f, fmt.Errorf("replay: %s holds no canned-<n>.json", dir)
	}
	return f, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("replay: %s: %w", path, err)
	}
	return nil
}

// Analyst turns an assembled prompt and the run's evidence into the analyst's
// final message (a fenced JSON block).
type Analyst interface {
	Analyse(ctx context.Context, promptText string, evidence []validate.EvidenceRow) (string, error)
}

// Canned is the deterministic analyst: it returns the next canned output,
// fenced as a real final message, and wraps around after the last.
type Canned struct {
	Outputs [][]byte
	n       int
}

// Analyse implements Analyst.
func (c *Canned) Analyse(_ context.Context, _ string, _ []validate.EvidenceRow) (string, error) {
	if len(c.Outputs) == 0 {
		return "", errors.New("replay: no canned output")
	}
	out := c.Outputs[c.n%len(c.Outputs)]
	c.n++
	return "Findings follow.\n\n```json\n" + strings.TrimSpace(string(out)) + "\n```\n", nil
}

// Filed is one topic a pass filed.
type Filed struct {
	TopicID    string
	SubjectKey string
	Kind       string
}

// Outcome is the result of one replay pass.
type Outcome struct {
	Prompt string
	Doc    validate.Document
	Filed  []Filed
	// CitedIDsValid is true when every evidence id a finding lists names a
	// row of the snapshot.
	CitedIDsValid bool
}

// ErrPrompt means the assembled prompt lacks a rule.
var ErrPrompt = errors.New("replay: prompt check failed")

var fenceRe = regexp.MustCompile("(?s)```[A-Za-z]*[ \t]*\r?\n(.*?)\r?\n?```")

// Run performs one replay pass of fixture f with analyst a against a fresh
// ledger at ledgerPath.
func Run(ctx context.Context, f Fixture, a Analyst, ledgerPath string) (Outcome, error) {
	var out Outcome
	lens, ok := lensByID(f.Manifest.Lens)
	if !ok {
		return out, fmt.Errorf("replay: unknown lens %q", f.Manifest.Lens)
	}
	lim := builtin.DefaultLimits()
	maxFindings := min(lens.MaxFindings, lim.ItemsMaxPerRun)
	window := "replay-window"
	res, err := prompt.Assemble(prompt.Params{
		Lens: lens, Builtins: builtin.FS(), Scope: f.Manifest.Scope, Window: window,
		StagingPath: "staging/replay", MaxFindings: maxFindings,
	})
	if err != nil {
		return out, err
	}
	out.Prompt = res.Text
	if missing := CheckPrompt(res.Text, lens); len(missing) > 0 {
		return out, fmt.Errorf("%w: %s", ErrPrompt, strings.Join(missing, "; "))
	}

	final, err := a.Analyse(ctx, res.Text, f.Evidence)
	if err != nil {
		return out, err
	}
	m := fenceRe.FindAllStringSubmatch(final, -1)
	if len(m) == 0 {
		return out, errors.New("replay: the final message has no fenced JSON block")
	}
	if err := json.Unmarshal([]byte(m[len(m)-1][1]), &out.Doc); err != nil {
		return out, fmt.Errorf("replay: findings do not parse: %w", err)
	}
	if !out.Doc.End {
		return out, errors.New("replay: findings lack the end sentinel")
	}
	out.CitedIDsValid = citedIDsValid(out.Doc, f.Evidence)
	if err := validate.Validate(validate.Input{
		Doc: out.Doc, Evidence: f.Evidence, Kinds: lens.Kinds,
		FieldMaxChars: lim.FieldMaxChars, MaxFindings: maxFindings,
	}); err != nil {
		return out, err
	}

	l, err := ledger.OpenPath(ctx, ledgerPath)
	if err != nil {
		return out, err
	}
	defer l.Close()
	var run ledger.Run
	err = l.WithTx(ctx, func(tx *ledger.Tx) error {
		var e error
		run, e = tx.InsertRun(ctx, ledger.Run{Lens: lens.ID, Scope: f.Manifest.Scope, PID: os.Getpid(), StartedAt: l.Now()})
		return e
	})
	if err != nil {
		return out, err
	}
	if _, err := l.FileBatch(ctx, ledger.Batch{
		RunID: run.ID, Doc: out.Doc, Evidence: f.Evidence, MaxPending: lim.ItemsMaxPending,
	}); err != nil {
		return out, err
	}
	items, err := l.PendingItems(ctx)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		out.Filed = append(out.Filed, Filed{TopicID: it.TopicID, SubjectKey: it.Writeup.SubjectKey, Kind: it.Kind})
	}
	sort.Slice(out.Filed, func(i, j int) bool { return out.Filed[i].TopicID < out.Filed[j].TopicID })
	return out, nil
}

func lensByID(id string) (builtin.Lens, bool) {
	for _, l := range builtin.Lenses() {
		if l.ID == id {
			return l, true
		}
	}
	return builtin.Lens{}, false
}

var idRe = regexp.MustCompile(`^E-\d{3,}`)

func citedIDsValid(doc validate.Document, rows []validate.EvidenceRow) bool {
	have := map[string]bool{}
	for _, r := range rows {
		have[r.ID] = true
	}
	for _, f := range doc.Findings {
		for _, e := range f.Evidence {
			if idRe.MatchString(e) && !have[idRe.FindString(e)] {
				return false
			}
		}
	}
	return true
}

// Evaluate applies the pass criteria to two passes over the same snapshot:
// at least MinKnownFound known problems found, no decoy filed, every cited id
// valid, and topic ids at least MinTopicStability identical. It returns one
// error naming every criterion that failed.
func Evaluate(f Fixture, first, second Outcome) error {
	var fails []string
	for i, o := range []Outcome{first, second} {
		subjects := map[string]bool{}
		for _, fl := range o.Filed {
			subjects[fl.SubjectKey] = true
		}
		found := 0
		for _, k := range f.Manifest.Known {
			if subjects[k] {
				found++
			}
		}
		if found < MinKnownFound {
			fails = append(fails, fmt.Sprintf("run %d found %d of %d known problems, need %d", i+1, found, len(f.Manifest.Known), MinKnownFound))
		}
		for _, d := range f.Manifest.Decoys {
			if subjects[d] {
				fails = append(fails, fmt.Sprintf("run %d filed decoy %s", i+1, d))
			}
		}
		if !o.CitedIDsValid {
			fails = append(fails, fmt.Sprintf("run %d cites an unknown evidence id", i+1))
		}
	}
	if s := Stability(first, second); s < MinTopicStability {
		fails = append(fails, fmt.Sprintf("topic ids are %.0f%% identical, need %.0f%%", s*100, MinTopicStability*100))
	}
	if len(fails) > 0 {
		return errors.New("replay failed: " + strings.Join(fails, "; "))
	}
	return nil
}

// Stability is the fraction of the topic ids of first that also occur in
// second. Two empty runs are not stable (a lens that files nothing proves
// nothing).
func Stability(first, second Outcome) float64 {
	if len(first.Filed) == 0 {
		return 0
	}
	in2 := map[string]bool{}
	for _, f := range second.Filed {
		in2[f.TopicID] = true
	}
	same := 0
	for _, f := range first.Filed {
		if in2[f.TopicID] {
			same++
		}
	}
	return float64(same) / float64(len(first.Filed))
}
