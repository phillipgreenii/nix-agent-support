package client_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// The client consumer of the label table: evidence to label.
func TestLabelTableEvidenceDerivesTheRowsLabel(t *testing.T) {
	var loaded []string
	for _, row := range clienttest.LabelRows(t) {
		loaded = append(loaded, row.Name)
		if got := client.EndLabel(row.Evidence.Evidence()); string(got) != row.Label {
			t.Errorf("%s: EndLabel(%+v) = %q, want %q", row.Name, row.Evidence, got, row.Label)
		}
	}
	clienttest.Covered(t, "client", loaded)
}

// oracle is the precedence table written out independently of EndLabel.
func oracle(ended, present, observed, clear, exit bool) client.Label {
	if !ended {
		return ""
	}
	if clear {
		return client.LabelCleared
	}
	if exit {
		return client.LabelExited
	}
	if !observed {
		return client.LabelUnknown
	}
	if present {
		return client.LabelCrashed
	}
	return client.LabelClosed
}

// Every combination of evidence, including the conflicting pairs: a /clear and a
// /exit together, an unobserved exit against a registry file present, and
// Conversations with no end.
func TestEndLabelTruthTableOverEveryEvidenceCombination(t *testing.T) {
	for bits := 0; bits < 32; bits++ {
		ev := client.Evidence{
			Ended: bits&1 != 0, RegistryPresentAtExit: bits&2 != 0, ExitObserved: bits&4 != 0,
			ClearEntry: bits&8 != 0, ExitEntry: bits&16 != 0,
		}
		for _, stale := range []bool{false, true} {
			ev.RegistryStaleAtDiscovery = stale
			want := oracle(ev.Ended, ev.RegistryPresentAtExit, ev.ExitObserved, ev.ClearEntry, ev.ExitEntry)
			if got := client.EndLabel(ev); got != want {
				t.Errorf("%+v: %q, want %q", ev, got, want)
			}
		}
	}
}

// The guard: a label row not loaded by every consumer fails. Each consumer calls
// clienttest.Covered with the names it loaded; this test fails when a consumer
// stopped calling it (or was deleted).
func TestEveryLabelTableConsumerChecksItsCoverage(t *testing.T) {
	for consumer, file := range map[string]string{
		"daemon": filepath.Join("..", "internal", "serve", "labels_integration_test.go"),
		"client": "labels_test.go",
		"cmd":    filepath.Join("..", "cmd", "pg-claude-code-recorder", "labels_integration_test.go"),
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("the %s consumer %s is missing: %v", consumer, file, err)
			continue
		}
		if !strings.Contains(string(b), `clienttest.Covered(t, "`+consumer+`"`) {
			t.Errorf("the %s consumer %s does not call clienttest.Covered(t, %q, ...)", consumer, file, consumer)
		}
	}
}

func TestCoveredFailsForAMissingRow(t *testing.T) {
	rows := clienttest.LabelRows(t)
	if len(rows) < 20 {
		t.Fatalf("the label table has %d rows", len(rows))
	}
	ft := &fakeT{}
	func() {
		defer func() { _ = recover() }()
		clienttest.Covered(ft, "client", []string{rows[0].Name})
	}()
	if !ft.failed {
		t.Fatal("Covered accepted a consumer that loaded one row")
	}
}

// fakeT records a failure instead of failing the test (Covered takes testing.TB).
type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Helper()                           {}
func (f *fakeT) Fatalf(format string, args ...any) { f.failed = true; panic("fatal") }
func (f *fakeT) Fatal(args ...any)                 { f.failed = true; panic("fatal") }
