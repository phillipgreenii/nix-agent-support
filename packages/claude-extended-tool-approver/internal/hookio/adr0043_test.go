package hookio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoOpinionStillSerializesAsAbstain is ADR 0043 trap 5, pinned.
//
// The rename is an IDENTIFIER rename. The SERIALIZED value is a data contract with
// tens of thousands of already-logged rows (asklog's hook_decision and
// decision_trace_entries.decision, `evaluate`'s replay_result) and with the replay
// differential this change is verified by. Renaming the string would break every
// historical join silently — nothing else in the tree compares it to a constant.
func TestNoOpinionStillSerializesAsAbstain(t *testing.T) {
	if got := NoOpinion.String(); got != "abstain" {
		t.Fatalf("NoOpinion.String() = %q, want %q — the serialized value is a data contract (ADR 0043)", got, "abstain")
	}
	// The other three are pinned in the same place so a future reorder of the iota
	// cannot quietly renumber one of them into another's string.
	for _, tc := range []struct {
		d    Decision
		want string
	}{
		{Approve, "approve"},
		{NoOpinion, "abstain"},
		{Ask, "ask"},
		{Reject, "reject"},
	} {
		if got := tc.d.String(); got != tc.want {
			t.Errorf("Decision(%d).String() = %q, want %q", int(tc.d), got, tc.want)
		}
	}
}

// TestRestrictivenessOrderIsUnchanged pins the property ADR 0043 promises it does not
// touch: NoOpinion keeps Abstain's position, so MostRestrictive and every
// Decision-ordering comparison behave identically. A reorder here would silently
// change what a compound expression folds to.
func TestRestrictivenessOrderIsUnchanged(t *testing.T) {
	if !(Approve < NoOpinion && NoOpinion < Ask && Ask < Reject) {
		t.Fatalf("restrictiveness order broken: Approve=%d NoOpinion=%d Ask=%d Reject=%d",
			Approve, NoOpinion, Ask, Reject)
	}
	if Approve != 0 {
		t.Fatalf("Approve = %d, want 0 — RuleResult{} is documented as the Approve zero value", Approve)
	}
}

// TestErrNotApplicableIsBareAndUnwrappable pins ADR 0043's decision 5 behaviourally:
// errors.Is must find it, and a genuine failure must NOT match it.
func TestErrNotApplicableIsBareAndUnwrappable(t *testing.T) {
	res, err := NotApplicable()
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("NotApplicable() error %v does not satisfy errors.Is(ErrNotApplicable)", err)
	}
	if res.Decision != Approve || res.Reason != "" || res.Module != "" || res.Trace != nil {
		t.Fatalf("NotApplicable() result = %+v, want the zero RuleResult (ADR 0043 decision 2)", res)
	}
	if errors.Is(fmt.Errorf("resolver timed out: %w", errors.New("boom")), ErrNotApplicable) {
		t.Fatal("a genuine failure matched ErrNotApplicable")
	}
}

// TestFromRecursionTranslatesInnerNoOpinionToNotApplicable pins the recursion-boundary
// rule ADR 0043's Consequences require to be stated: an inner NoOpinion is the inner
// chain's exhaustion verdict, so a rule forwarding it has no opinion of its own and
// the OUTER chain must continue. Getting this backwards makes nix/docker/kubectl/
// envvars stop the chain where they used to continue it — a decision change.
func TestFromRecursionTranslatesInnerNoOpinionToNotApplicable(t *testing.T) {
	_, err := FromRecursion(RuleResult{Decision: NoOpinion, Reason: "inner chain exhausted"})
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("inner NoOpinion translated to %v, want ErrNotApplicable", err)
	}
	for _, d := range []Decision{Approve, Ask, Reject} {
		got, err := FromRecursion(RuleResult{Decision: d, Module: "inner"})
		if err != nil {
			t.Errorf("inner %s translated to error %v, want it forwarded verbatim", d, err)
		}
		if got.Decision != d || got.Module != "inner" {
			t.Errorf("inner %s came back as %+v, want it forwarded verbatim", d, got)
		}
	}
}

// TestErrNotApplicableIsNeverWrappedInSource is the mechanical half of decision 5.
//
// A wrap is not a compile error and no behavioural test can catch it — a buried
// ErrNotApplicable stops matching errors.Is, the engine treats the rule as FAILED
// rather than absent, and (worse, in the other direction) a genuine failure made to
// match it would be silently treated as "not applicable". So the only available guard
// is a source scan, and it is cheap.
func TestErrNotApplicableIsNeverWrappedInSource(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	walkGoFiles(t, root, func(path string, src string) {
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, "ErrNotApplicable") {
				continue
			}
			// Prose is allowed to NAME the prohibition (and does, at every converted
			// site), and a CONSUMER is allowed to test for it — errors.Is is exactly
			// the comparison decision 5 exists to protect. Neither produces a wrap.
			if strings.HasPrefix(trimmed, "//") || strings.Contains(trimmed, "errors.Is(") {
				continue
			}
			// What remains is a producer, so the only legal spellings are a bare
			// `return ... ErrNotApplicable` and the declaration itself. Any
			// error-composing call on the same line is a wrap.
			if strings.Contains(trimmed, "%w") || strings.Contains(trimmed, "fmt.Errorf") ||
				strings.Contains(trimmed, "errors.Join") {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel(root, path), i+1, trimmed))
			}
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("ErrNotApplicable MUST be returned bare (ADR 0043 decision 5); wrapped at:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestVerdictHasNoProductionCallers keeps hookio.Verdict a TEST/adapter shim. It
// discards the error, so a production decision path using it would lose the
// per-rule failure record and the ability to honour a fail-closed carve-out —
// exactly what ADR 0043 introduced the error channel for.
func TestVerdictHasNoProductionCallers(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	walkGoFiles(t, root, func(path string, src string) {
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "verdict.go") {
			return
		}
		for i, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "hookio.Verdict(") || strings.Contains(line, "= Verdict(") {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel(root, path), i+1, strings.TrimSpace(line)))
			}
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("hookio.Verdict is a test/adapter shim and MUST NOT appear in production code; found:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestSerializedAbstainEmittersAgree pins the FOUR non-test emitters ADR 0043 names.
// Only one of them lives in this package; the others are asserted where they live
// (asklog, cmd). This scan proves none of them was changed to a different literal,
// which is the failure that would break historical joins without breaking any test.
func TestSerializedAbstainEmittersAgree(t *testing.T) {
	root := moduleRoot(t)
	want := map[string]string{
		"internal/hookio/types.go":                                      `return "abstain"`,
		"internal/asklog/recorder.go":                                   `return "abstain"`,
		"cmd/claude-extended-tool-approver/cmd_evaluate.go":             `return "abstain"`,
		"cmd/claude-extended-tool-approver/cmd_set_correct_decision.go": `"abstain": true`,
	}
	for file, needle := range want {
		b, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(b), needle) {
			t.Errorf("%s no longer contains %q — the serialized value MUST stay \"abstain\" (ADR 0043 decision 1)", file, needle)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test's working directory")
		}
		dir = parent
	}
}

func walkGoFiles(t *testing.T, root string, fn func(path, src string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fn(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}
