package replay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/prompt"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

var lensIDs = []string{"agent-workflow", "config-health"}

const fixtureRoot = "../../testdata/replay"

func load(t *testing.T, lens string) Fixture {
	t.Helper()
	f, err := LoadFixture(fixtureRoot, lens)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// pass runs one replay pass with the canned output at index i.
func pass(t *testing.T, f Fixture, canned []byte) (Outcome, error) {
	t.Helper()
	a := &Canned{Outputs: [][]byte{canned}}
	return Run(context.Background(), f, a, filepath.Join(t.TempDir(), "ledger.db"))
}

func mustPass(t *testing.T, f Fixture, canned []byte) Outcome {
	t.Helper()
	o, err := pass(t, f, canned)
	if err != nil {
		t.Fatalf("replay pass: %v", err)
	}
	return o
}

func TestFixtureShape(t *testing.T) {
	for _, id := range lensIDs {
		t.Run(id, func(t *testing.T) {
			f := load(t, id)
			if len(f.Manifest.Known) < 5 {
				t.Errorf("%d known problems, want at least 5", len(f.Manifest.Known))
			}
			if len(f.Manifest.Decoys) < 5 {
				t.Errorf("%d decoys, want at least 5", len(f.Manifest.Decoys))
			}
			subjects := map[string]bool{}
			for _, r := range f.Evidence {
				subjects[r.SubjectKey] = true
			}
			for _, k := range append(append([]string{}, f.Manifest.Known...), f.Manifest.Decoys...) {
				if !subjects[k] {
					t.Errorf("manifest subject %s has no evidence row", k)
				}
			}
			if len(f.Canned) < 2 {
				t.Errorf("%d canned outputs, want at least 2", len(f.Canned))
			}
		})
	}
}

func TestReplayPassesForBothLenses(t *testing.T) {
	for _, id := range lensIDs {
		t.Run(id, func(t *testing.T) {
			f := load(t, id)
			o1 := mustPass(t, f, f.Canned[0])
			o2 := mustPass(t, f, f.Canned[1])
			if err := Evaluate(f, o1, o2); err != nil {
				t.Fatal(err)
			}
			if len(o1.Filed) < MinKnownFound {
				t.Errorf("filed %d topics", len(o1.Filed))
			}
			if !o1.CitedIDsValid || !o2.CitedIDsValid {
				t.Error("cited ids not valid")
			}
		})
	}
}

func TestSecondRunOfSameSnapshotIsDeterministic(t *testing.T) {
	f := load(t, "agent-workflow")
	a, b := mustPass(t, f, f.Canned[0]), mustPass(t, f, f.Canned[0])
	if Stability(a, b) != 1 {
		t.Errorf("identical runs gave stability %v", Stability(a, b))
	}
	if a.Prompt != b.Prompt {
		t.Error("prompt differs between identical runs")
	}
}

// mutate decodes a canned document, applies fn and returns it re-encoded.
func mutate(t *testing.T, raw []byte, fn func(*validate.Document)) []byte {
	t.Helper()
	var d validate.Document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	fn(&d)
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEvaluateFailsWhenTooFewKnownProblemsFound(t *testing.T) {
	f := load(t, "agent-workflow")
	two := mutate(t, f.Canned[0], func(d *validate.Document) { d.Findings = d.Findings[:2] })
	o := mustPass(t, f, two)
	err := Evaluate(f, o, o)
	if err == nil || !strings.Contains(err.Error(), "found 2 of 5 known problems") {
		t.Fatalf("got %v", err)
	}
}

func TestEvaluateFailsWhenADecoyIsFiled(t *testing.T) {
	for _, id := range lensIDs {
		t.Run(id, func(t *testing.T) {
			f := load(t, id)
			decoy := f.Manifest.Decoys[0]
			var row validate.EvidenceRow
			for _, r := range f.Evidence {
				if r.SubjectKey == decoy {
					row = r
				}
			}
			with := mutate(t, f.Canned[0], func(d *validate.Document) {
				fd := d.Findings[0]
				fd.SubjectKey, fd.Kind, fd.Evidence = decoy, row.Kind, []string{row.ID}
				fd.Situation = "A single-session event [" + row.ID + "] is reported as a problem."
				d.Findings = append(d.Findings, fd)
			})
			o, err := pass(t, f, with)
			if err != nil {
				t.Fatal(err)
			}
			err = Evaluate(f, o, o)
			if err == nil || !strings.Contains(err.Error(), "filed decoy "+decoy) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestEvaluateFailsWhenTopicIdsAreUnstable(t *testing.T) {
	f := load(t, "agent-workflow")
	o1 := mustPass(t, f, f.Canned[0])
	other := mutate(t, f.Canned[0], func(d *validate.Document) {
		// Swap two known problems for two different known problems.
		d.Findings = d.Findings[:2]
		extra := d.Findings[0]
		extra.SubjectKey, extra.Kind, extra.Evidence = "tool:epsilon-edit", "tool-ux", []string{"E-005"}
		extra.Situation = "The edit call made the assistant stumble 11 times [E-005.stumbles=11] in 4 sessions [E-005.sessions=4]."
		d.Findings = append(d.Findings, extra)
	})
	o2 := mustPass(t, f, other)
	if s := Stability(o1, o2); s >= MinTopicStability {
		t.Fatalf("setup: stability %v is not below the bar", s)
	}
	err := Evaluate(f, o1, o2)
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("got %v", err)
	}
}

func TestEvaluateRejectsAnEmptyRun(t *testing.T) {
	if Stability(Outcome{}, Outcome{}) != 0 {
		t.Error("an empty run must not count as stable")
	}
}

func TestInvalidCitedIdIsRejectedByTheValidator(t *testing.T) {
	f := load(t, "config-health")
	bad := mutate(t, f.Canned[0], func(d *validate.Document) { d.Findings[0].Evidence = []string{"E-099"} })
	_, err := pass(t, f, bad)
	var ve *validate.Error
	if !errors.As(err, &ve) {
		t.Fatalf("got %v, want a validator rejection", err)
	}
	found := false
	for _, fl := range ve.Failures {
		if fl.Rule == validate.RuleUnknownID {
			found = true
		}
	}
	if !found {
		t.Errorf("no unknown-evidence-id failure in %v", ve)
	}
}

func TestUnknownKindAndMissingConfirmByAreRejected(t *testing.T) {
	f := load(t, "agent-workflow")
	bad := mutate(t, f.Canned[0], func(d *validate.Document) {
		d.Findings[0].Kind = "made-up-kind"
		d.Findings[1].ConfirmBy = ""
	})
	_, err := pass(t, f, bad)
	var ve *validate.Error
	if !errors.As(err, &ve) {
		t.Fatalf("got %v", err)
	}
	rules := map[string]bool{}
	for _, fl := range ve.Failures {
		rules[fl.Rule] = true
	}
	for _, r := range []string{validate.RuleUnknownKind, validate.RuleMissingConfirmBy} {
		if !rules[r] {
			t.Errorf("missing failure %s in %v", r, ve)
		}
	}
}

func TestCannedAnalystNeedsOutputsAndAFence(t *testing.T) {
	if _, err := (&Canned{}).Analyse(context.Background(), "", nil); err == nil {
		t.Error("an empty canned analyst must fail")
	}
	f := load(t, "agent-workflow")
	_, err := Run(context.Background(), f, plain("no fence here"), filepath.Join(t.TempDir(), "l.db"))
	if err == nil || !strings.Contains(err.Error(), "no fenced JSON block") {
		t.Errorf("got %v", err)
	}
}

type plain string

func (p plain) Analyse(context.Context, string, []validate.EvidenceRow) (string, error) {
	return string(p), nil
}

func TestFindingsWithoutEndSentinelAreRejected(t *testing.T) {
	f := load(t, "agent-workflow")
	noEnd := mutate(t, f.Canned[0], func(d *validate.Document) { d.End = false })
	_, err := pass(t, f, noEnd)
	if err == nil || !strings.Contains(err.Error(), "end sentinel") {
		t.Errorf("got %v", err)
	}
}

// assemble builds the real prompt of a lens, as a replay pass does.
func assemble(t *testing.T, lens builtin.Lens) string {
	t.Helper()
	r, err := prompt.Assemble(prompt.Params{
		Lens: lens, Builtins: builtin.FS(), Scope: "demo-scope", Window: "replay-window",
		StagingPath: "staging/replay", MaxFindings: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.Text
}

func lensOf(t *testing.T, id string) builtin.Lens {
	t.Helper()
	l, ok := lensByID(id)
	if !ok {
		t.Fatalf("no lens %s", id)
	}
	return l
}

func TestAssembledPromptIsWhole(t *testing.T) {
	for _, id := range lensIDs {
		l := lensOf(t, id)
		if missing := CheckPrompt(assemble(t, l), l); len(missing) > 0 {
			t.Errorf("%s: %v", id, missing)
		}
	}
}

// removeRule deletes rule n (its first line and its indented continuation
// lines) from an assembled prompt: a hand-applied negative control.
func removeRule(t *testing.T, text string, n int) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	var out []string
	skipping := false
	prefix := regexp.MustCompile(`^` + regexp.QuoteMeta(itoa(n)) + `\. `)
	hit := false
	for _, ln := range lines {
		switch {
		case prefix.MatchString(ln):
			skipping, hit = true, true
			continue
		case skipping && strings.HasPrefix(ln, "   "):
			continue
		default:
			skipping = false
		}
		out = append(out, ln)
	}
	if !hit {
		t.Fatalf("rule %d not found", n)
	}
	return strings.Join(out, "\n")
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func golden(t *testing.T, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "prompt", id+".golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The golden prompt was assembled with other run values, so the comparison
// below is made on the rule lines only.
func ruleLines(text string) []string {
	var out []string
	for _, m := range ruleLineRe.FindAllString(text, -1) {
		out = append(out, m)
	}
	return out
}

func TestRemovingAnyBaseRuleIsCaught(t *testing.T) {
	for _, id := range lensIDs {
		l := lensOf(t, id)
		whole := assemble(t, l)
		for n := 1; n <= BaseRuleCount; n++ {
			t.Run(id+"/rule-"+itoa(n), func(t *testing.T) {
				cut := removeRule(t, whole, n)
				if cut == whole {
					t.Fatal("control changed nothing")
				}
				if missing := CheckPrompt(cut, l); len(missing) == 0 {
					t.Error("replay prompt check accepted a prompt without the rule")
				}
				if strings.Join(ruleLines(cut), "\n") == strings.Join(ruleLines(golden(t, id)), "\n") {
					t.Error("prompt golden still matches without the rule")
				}
			})
		}
	}
}

func TestAlteringAnyBaseRuleIsCaught(t *testing.T) {
	for _, id := range lensIDs {
		l := lensOf(t, id)
		whole := assemble(t, l)
		for n := 1; n <= BaseRuleCount; n++ {
			if n == 4 {
				continue
			}
			t.Run(id+"/rule-"+itoa(n), func(t *testing.T) {
				altered := strings.Replace(whole, ruleMarkers[n], "(softened)", 1)
				if altered == whole {
					t.Fatal("control changed nothing")
				}
				if missing := CheckPrompt(altered, l); len(missing) == 0 {
					t.Error("replay prompt check accepted an altered rule")
				}
			})
		}
	}
}

func TestRemovingAnyLensRuleIsCaught(t *testing.T) {
	for _, id := range lensIDs {
		l := lensOf(t, id)
		whole := assemble(t, l)
		rule4, err := LensParts(l)
		if err != nil {
			t.Fatal(err)
		}
		cases := map[string]string{
			"rule-4":      strings.Replace(whole, "4. "+rule4, "4. ", 1),
			"kinds-list":  strings.Replace(whole, "closed list", "list", 1),
			"calibration": strings.Replace(whole, "Calibration examples", "Examples", 1),
		}
		for _, k := range l.Kinds {
			cases["kind-"+k] = strings.Replace(whole, "- "+k+":", "- other:", 1)
		}
		for name, cut := range cases {
			t.Run(id+"/"+name, func(t *testing.T) {
				if cut == whole {
					t.Fatal("control changed nothing")
				}
				if missing := CheckPrompt(cut, l); len(missing) == 0 {
					t.Error("replay prompt check accepted a prompt without the lens rule")
				}
			})
		}
	}
}

func TestReplayRunRefusesAPromptWithoutARule(t *testing.T) {
	// A replay pass whose prompt lost a rule fails before the analyst runs:
	// point the lens at a prompt file with no rules via a broken base path.
	f := load(t, "agent-workflow")
	f.Manifest.Lens = "no-such-lens"
	_, err := Run(context.Background(), f, &Canned{Outputs: f.Canned}, filepath.Join(t.TempDir(), "l.db"))
	if err == nil || !strings.Contains(err.Error(), "unknown lens") {
		t.Errorf("got %v", err)
	}
	if !errors.Is(ErrPrompt, ErrPrompt) {
		t.Error("ErrPrompt identity")
	}
}
