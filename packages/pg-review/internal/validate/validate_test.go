package validate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

const goldenDir = "../../testdata/review-item"

// golden loads the synthetic golden run: its evidence, query log, query
// results, staged raw text and findings document.
func golden(t *testing.T) Input {
	t.Helper()
	in, err := Load(goldenDir, filepath.Join(goldenDir, "raw.txt"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(goldenDir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate("findings", b); err != nil {
		t.Fatalf("golden findings do not match the findings schema: %v", err)
	}
	if err := json.Unmarshal(b, &in.Doc); err != nil {
		t.Fatal(err)
	}
	in.Kinds = []string{"misclassified-error", "wasted-retries"}
	in.FieldMaxChars = 1200
	in.MaxFindings = 5
	return in
}

// expect validates in and requires exactly the given rule among the failures.
func expect(t *testing.T, in Input, rule string) []Failure {
	t.Helper()
	err := Validate(in)
	var ve *Error
	if !errors.As(err, &ve) {
		t.Fatalf("want a rejection for %s, got %v", rule, err)
	}
	if ve.ExitCode() != exitcode.ValidationRejected {
		t.Errorf("exit code = %d, want 4", ve.ExitCode())
	}
	for _, f := range ve.Failures {
		if f.Rule == rule {
			if f.Finding == "" || f.Field == "" {
				t.Errorf("failure lacks finding or field: %+v", f)
			}
			return ve.Failures
		}
	}
	t.Fatalf("rule %s not reported; got %v", rule, ve.Failures)
	return nil
}

func TestGoldenPasses(t *testing.T) {
	if err := Validate(golden(t)); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSection(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].WhyProblem = "  "
	expect(t, in, RuleMissingSection)
}

func TestOptionsNeedDoNothing(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Options = in.Doc.Findings[0].Options[1:]
	expect(t, in, RuleOptionsIncomplete)
	in = golden(t)
	in.Doc.Findings[0].Options[0].Name = "Wait and see"
	expect(t, in, RuleOptionsIncomplete)
}

func TestUndefinedGlossaryTerm(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Problem += " The ledger shows it."
	fs := expect(t, in, RuleUndefinedTerm)
	if !hasToken(fs, "ledger") {
		t.Errorf("want token ledger in %v", fs)
	}
	// Defining it fixes the batch.
	in.Doc.Findings[0].Glossary = append(in.Doc.Findings[0].Glossary, GlossaryTerm{Term: "ledger", Meaning: "The store of rulings."})
	if err := Validate(in); err != nil {
		t.Fatal(err)
	}
}

func TestUndefinedTermKinds(t *testing.T) {
	cases := map[string]string{
		"id":       "See ticket AB-123 for more.",
		"pg name":  "The pg-widget helper is involved.",
		"backtick": "Run `make all` first.",
		"state":    "The item stays Deferred for a while.",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			in := golden(t)
			in.Doc.Findings[0].Problem += " " + text
			expect(t, in, RuleUndefinedTerm)
		})
	}
	// A state word opening a sentence is ordinary English.
	in := golden(t)
	in.Doc.Findings[0].Problem += " Pending work grows."
	if err := Validate(in); err != nil {
		t.Fatalf("sentence-initial state word rejected: %v", err)
	}
	// The allowlist exempts plain tokens.
	in = golden(t)
	in.Doc.Findings[0].Problem += " The value is `true`."
	if err := Validate(in); err != nil {
		t.Fatalf("allowlisted token rejected: %v", err)
	}
}

func TestRawTextOverlap(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Problem = "the planner asked the sorter to order thirty small records and the sorter refused twice before it accepted a reordered argument list"
	expect(t, in, RuleRawTextOverlap)
}

func TestShortCoincidenceIsNotOverlap(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Problem += " The sorter refused twice before it accepted."
	if err := Validate(in); err != nil {
		t.Fatal(err)
	}
}

func TestSumRecomputation(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Sums[0].Total = 11
	expect(t, in, RuleSumMismatch)
}

func TestSumAddendMustBeEvidence(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Sums[0] = Sum{Label: "invented", Addends: []float64{7, 3}, Total: 10}
	expect(t, in, RuleSumAddend)
}

func TestSumAddendFromQueryResult(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Sums[0] = Sum{Label: "calls", Addends: []float64{40, 10}, Total: 50}
	if err := Validate(in); err != nil {
		t.Fatalf("addends from a cited query result rejected: %v", err)
	}
}

func TestUnknownEvidenceID(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Evidence = append(in.Doc.Findings[0].Evidence, "E-999")
	expect(t, in, RuleUnknownID)
}

func TestUnknownIDCitedInText(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Impact += " See E-777."
	expect(t, in, RuleUnknownID)
}

func TestSampleReference(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Impact += " See E-002a."
	if err := Validate(in); err != nil {
		t.Fatalf("valid sample reference rejected: %v", err)
	}
	in.Doc.Findings[0].Impact += " And E-002b."
	expect(t, in, RuleUnknownID)
}

func TestQueryIDWithoutResultFileIsUnknown(t *testing.T) {
	in := golden(t)
	delete(in.QueryResults, "Q-001")
	expect(t, in, RuleUnknownID)
}

func TestQueryIDNotInLogIsUnknown(t *testing.T) {
	in := golden(t)
	in.QueryLog = nil
	expect(t, in, RuleUnknownID)
}

func TestCountMismatch(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Situation = strings.Replace(in.Doc.Findings[0].Situation, "[E-001.errors=6]", "[E-001.errors=7]", 1)
	expect(t, in, RuleCountMismatch)
}

func TestQueryCountMismatch(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Situation = strings.Replace(in.Doc.Findings[0].Situation, "[Q-001#0.errors=10]", "[Q-001#0.errors=9]", 1)
	expect(t, in, RuleCountMismatch)
}

func TestUnknownMetric(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Situation += " [E-001.bogus=1]"
	expect(t, in, RuleUnknownMetric)
}

func TestUnknownSubject(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].SubjectKey = "tool:not-in-evidence"
	expect(t, in, RuleUnknownSubject)
}

func TestUnknownKind(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Kind = "made-up"
	expect(t, in, RuleUnknownKind)
}

func TestFieldLengthCap(t *testing.T) {
	in := golden(t)
	in.FieldMaxChars = 100
	fs := expect(t, in, RuleFieldTooLong)
	if !hasField(fs, "situation") {
		t.Errorf("want situation among %v", fs)
	}
}

func TestMissingConfirmBy(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].ConfirmBy = ""
	expect(t, in, RuleMissingConfirmBy)
}

func TestURLCopiedFromEvidence(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Problem += " Details at https://example.invalid/internal/runbook today."
	expect(t, in, RuleCopiedURL)
	// A URL that is not in the evidence is allowed.
	in = golden(t)
	in.Doc.Findings[0].Problem += " Details at https://example.invalid/other today."
	if err := Validate(in); err != nil {
		t.Fatalf("URL absent from evidence rejected: %v", err)
	}
}

func TestCommandCopiedFromEvidence(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Problem += " Try `demo-runner --fast --dry` again."
	in.Doc.Findings[0].Glossary = append(in.Doc.Findings[0].Glossary, GlossaryTerm{Term: "demo-runner --fast --dry", Meaning: "A demo command."})
	expect(t, in, RuleCopiedCommand)
}

func TestFencedCommandCopiedFromEvidence(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].Problem += "\n```\ndemo-runner --fast --dry\n```\n"
	expect(t, in, RuleCopiedCommand)
}

func TestWholeBatchRejected(t *testing.T) {
	in := golden(t)
	bad := in.Doc.Findings[0]
	bad.SubjectKey = "tool:other"
	in.Doc.Findings = append(in.Doc.Findings, bad)
	fs := expect(t, in, RuleUnknownSubject)
	for _, f := range fs {
		if f.Index == 0 {
			t.Errorf("the valid finding must not be reported: %v", f)
		}
	}
}

func TestTooManyFindings(t *testing.T) {
	in := golden(t)
	in.MaxFindings = 1
	in.Doc.Findings = append(in.Doc.Findings, in.Doc.Findings[0])
	expect(t, in, RuleTooManyFindings)
}

func TestErrorListsEveryFailure(t *testing.T) {
	in := golden(t)
	in.Doc.Findings[0].ConfirmBy = ""
	in.Doc.Findings[0].SubjectKey = "tool:nope"
	err := Validate(in)
	msg := err.Error()
	for _, want := range []string{RuleMissingConfirmBy, RuleUnknownSubject, "finding 0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

func TestVocabularyAndAllowlistEmbedded(t *testing.T) {
	if len(Vocabulary()) == 0 || len(Allowlist()) == 0 {
		t.Fatal("embedded vocabulary or allowlist is empty")
	}
}

func hasToken(fs []Failure, tok string) bool {
	for _, f := range fs {
		if f.Token == tok {
			return true
		}
	}
	return false
}

func hasField(fs []Failure, fld string) bool {
	for _, f := range fs {
		if f.Field == fld {
			return true
		}
	}
	return false
}
