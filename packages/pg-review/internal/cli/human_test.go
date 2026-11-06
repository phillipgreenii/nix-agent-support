package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// seedFindings files synthetic findings through the real ledger and returns
// the item numbers in the order given.
func seedFindings(t *testing.T, l *ledger.Ledger, fs ...validate.Finding) []int64 {
	t.Helper()
	var run ledger.Run
	err := l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		var err error
		run, err = tx.InsertRun(context.Background(), ledger.Run{Lens: "agent-workflow", Scope: "demo-scope", PID: 1})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []validate.EvidenceRow
	for _, f := range fs {
		rows = append(rows, validate.EvidenceRow{ID: "E-" + f.SubjectKey, SubjectKey: f.SubjectKey, DefinedIn: "a.md", Type: "file"})
	}
	if _, err := l.FileBatch(context.Background(), ledger.Batch{
		RunID: run.ID, Doc: validate.Document{Lens: "agent-workflow", Scope: "demo-scope", Findings: fs},
		Evidence: rows, MaxPending: 12,
	}); err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, f := range fs {
		var n int64
		if err := l.DB().QueryRow(`SELECT seq FROM topic WHERE subject_key=?`, f.SubjectKey).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func syn(subject, title string, evidence ...string) validate.Finding {
	return validate.Finding{
		SubjectKey: subject, Kind: "stale", Title: title, Category: "workflow",
		Situation: "s", Impact: "about 5 minutes per week", Evidence: evidence,
		Targets: []string{"pkg-a/file.md"},
	}
}

type fakeFiler struct {
	reqs []ledger.BeadRequest
	fail bool
}

func (f *fakeFiler) Find(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (f *fakeFiler) Create(_ context.Context, r ledger.BeadRequest) (string, error) {
	if f.fail {
		return "", fmt.Errorf("tracker down")
	}
	f.reqs = append(f.reqs, r)
	return fmt.Sprintf("fake-%d", len(f.reqs)), nil
}

func useFiler(t *testing.T, f ledger.BeadFiler) {
	t.Helper()
	saved := beadFiler
	beadFiler = func(*config.Effective) ledger.BeadFiler { return f }
	t.Cleanup(func() { beadFiler = saved })
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	env := config.EnvFromOS()
	if err := mkdirWrite(env.Path(), body); err != nil {
		t.Fatal(err)
	}
}

func TestPendingOrderingAndBannerGolden(t *testing.T) {
	_, l := hermetic(t)
	ids := seedFindings(
		t, l,
		syn("older-weak", "Should weak go first?", "E-older-weak"),
		syn("strong", "Should strong go first?", "E-strong", "E-x", "E-y"),
		syn("newer-weak", "Should weak come last?", "E-newer-weak"),
	)
	code, out, stderr := run(t, "human", "pending")
	if code != exitcode.OK {
		t.Fatalf("exit %d %s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines: %q", lines)
	}
	if !strings.HasPrefix(lines[0], "Pending: 3 | oldest: ") || !strings.Contains(lines[0], "| held: none | stale: none | last runs: agent-workflow Filed ") ||
		!strings.HasSuffix(lines[0], "| maintain: never ran") {
		t.Errorf("banner: %q", lines[0])
	}
	want := []string{
		fmt.Sprintf("%d  [agent-workflow]  Should strong go first?  impact: about 5 minutes per week", ids[1]),
		fmt.Sprintf("%d  [agent-workflow]  Should weak go first?  impact: about 5 minutes per week", ids[0]),
		fmt.Sprintf("%d  [agent-workflow]  Should weak come last?  impact: about 5 minutes per week", ids[2]),
	}
	for i, w := range want {
		if lines[i+1] != w {
			t.Errorf("line %d:\n got %q\nwant %q", i+1, lines[i+1], w)
		}
	}
}

func TestPendingBannerShowsStaleHeartbeat(t *testing.T) {
	_, l := hermetic(t)
	old := l.Now().Add(-11 * 60 * 1e9) // 11 minutes, more than twice the period
	if err := l.WithTx(context.Background(), func(tx *ledger.Tx) error { return tx.SetHeartbeat(context.Background(), old) }); err != nil {
		t.Fatal(err)
	}
	_, out, _ := run(t, "human", "pending")
	if !strings.Contains(out, "maintain: stale, last ran 11m ago") {
		t.Errorf("banner: %q", out)
	}
	fresh := l.Now().Add(-2 * 60 * 1e9)
	_ = l.WithTx(context.Background(), func(tx *ledger.Tx) error { return tx.SetHeartbeat(context.Background(), fresh) })
	_, out, _ = run(t, "human", "pending")
	if strings.Contains(out, "maintain:") {
		t.Errorf("fresh heartbeat shown: %q", out)
	}
}

func TestPendingAutoDefersFirst(t *testing.T) {
	_, l := hermetic(t)
	writeConfig(t, "schema_version = 1\n[limits]\nitems_auto_defer_days = 1\n")
	seedFindings(t, l, syn("a", "Should a?", "E-a"))
	if _, err := l.DB().Exec(`UPDATE topic SET updated_at='2020-01-01T00:00:00.000000000Z'`); err != nil {
		t.Fatal(err)
	}
	_, out, _ := run(t, "human", "pending")
	if !strings.HasPrefix(out, "Pending: 0 |") || strings.Contains(out, "Should a?") {
		t.Errorf("auto-defer not applied: %q", out)
	}
	if n := count(t, l, "decision"); n != 0 {
		t.Errorf("auto-defer wrote %d decision rows", n)
	}
}

func TestShowRecordsViewAndStrips(t *testing.T) {
	_, l := hermetic(t)
	f := syn("a", "Should \x1b[31ma\x1b[0m run?", "E-a")
	ids := seedFindings(t, l, f)
	code, out, _ := run(t, "human", "show", fmt.Sprint(ids[0]))
	if code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out, "\x1b") || !strings.Contains(out, fmt.Sprintf("Item %d ", ids[0])) || !strings.Contains(out, "State: Pending") {
		t.Errorf("show output: %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("pg-review human show %d --evidence", ids[0])) {
		t.Errorf("evidence hint not renumbered: %q", out)
	}
	if n := count(t, l, "view"); n != 1 {
		t.Errorf("views %d", n)
	}
	if code, _, _ := run(t, "human", "show", "9999"); code != exitcode.ValidationRejected {
		t.Errorf("unknown id exit %d", code)
	}
	if code, _, _ := run(t, "human", "show", "zero"); code != exitcode.Usage {
		t.Errorf("bad id exit %d", code)
	}
}

func TestRuleWithoutShowExitsSix(t *testing.T) {
	_, l := hermetic(t)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	code, _, stderr := run(t, "human", "rule", fmt.Sprint(ids[0]), "rejected")
	if code != exitcode.Ledger || !strings.Contains(stderr, "show it first") {
		t.Fatalf("exit %d %q", code, stderr)
	}
	if n := count(t, l, "decision"); n != 0 {
		t.Errorf("decision stored")
	}
}

func TestRuleAfterItemChangedExitsSix(t *testing.T) {
	_, l := hermetic(t)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	id := fmt.Sprint(ids[0])
	run(t, "human", "show", id)
	if _, err := l.DB().Exec(`UPDATE topic SET revision=revision+1`); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run(t, "human", "rule", id, "rejected")
	if code != exitcode.Ledger || !strings.Contains(stderr, "show it again") {
		t.Fatalf("exit %d %q", code, stderr)
	}
}

func TestRuleSeveralIdsEchoesTitleAndState(t *testing.T) {
	_, l := hermetic(t)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"), syn("b", "Should b?", "E-b"))
	a, b := fmt.Sprint(ids[0]), fmt.Sprint(ids[1])
	if code, _, _ := run(t, "human", "show", a, b); code != exitcode.OK {
		t.Fatal("show")
	}
	code, out, stderr := run(t, "human", "rule", a, b, "deferred", "--note", "need data", "--reconsider-when", "date:2027-01-01;target-changed", "--useful", "yes")
	if code != exitcode.OK {
		t.Fatalf("exit %d %s", code, stderr)
	}
	for _, w := range []string{a + "  Should a?  -> Deferred", b + "  Should b?  -> Deferred"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in %q", w, out)
		}
	}
	d, _ := l.LatestDecision(context.Background(), mustTopic(t, l, ids[0]))
	if d.Rationale != "need data" || d.ReconsiderWhen != "date:2027-01-01;target-changed" || d.Useful != "yes" {
		t.Errorf("decision %+v", d)
	}
}

func mustTopic(t *testing.T, l *ledger.Ledger, n int64) string {
	t.Helper()
	var id string
	if err := l.DB().QueryRow(`SELECT topic_id FROM topic WHERE seq=?`, n).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRuleUsageErrors(t *testing.T) {
	hermetic(t)
	for _, c := range [][]string{
		{"human", "rule", "1", "maybe"},
		{"human", "rule", "x", "rejected"},
		{"human", "rule", "1", "deferred", "--reconsider-when", "when the moon is full"},
		{"human", "rule", "1", "rejected", "--useful", "maybe"},
	} {
		if code, _, _ := run(t, c...); code != exitcode.Usage {
			t.Errorf("%v: exit %d", c, code)
		}
	}
}

func TestRuleAdoptedFilesBeadWithConfiguredLabel(t *testing.T) {
	_, l := hermetic(t)
	writeConfig(t, "schema_version = 1\n[tracker]\ndir = \"/tmp/tracker-root\"\ndefault_label = \"fallback\"\n[[tracker.repo]]\nprefix = \"pkg-a/\"\nlabel = \"label-a\"\n")
	f := &fakeFiler{}
	useFiler(t, f)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	id := fmt.Sprint(ids[0])
	run(t, "human", "show", id)
	code, out, stderr := run(t, "human", "rule", id, "adopted")
	if code != exitcode.OK {
		t.Fatalf("exit %d %s", code, stderr)
	}
	if len(f.reqs) != 1 || f.reqs[0].Label != "label-a" || f.reqs[0].Tracker != "/tmp/tracker-root" ||
		!strings.HasPrefix(f.reqs[0].Body, "Untrusted origin: written by an automated analyst") {
		t.Fatalf("requests: %+v", f.reqs)
	}
	if !strings.Contains(out, "-> Adopted") || !strings.Contains(out, "bead: filed fake-1") {
		t.Errorf("echo: %q", out)
	}
	// Ruling again is idempotent: no second bead.
	run(t, "human", "rule", id, "adopted")
	if len(f.reqs) != 1 {
		t.Errorf("second ruling filed %d beads", len(f.reqs))
	}
}

func TestRuleAdoptedBdFailureKeepsRuling(t *testing.T) {
	_, l := hermetic(t)
	writeConfig(t, "schema_version = 1\n[tracker]\ndefault_label = \"x\"\n")
	useFiler(t, &fakeFiler{fail: true})
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	id := fmt.Sprint(ids[0])
	run(t, "human", "show", id)
	code, out, _ := run(t, "human", "rule", id, "adopted")
	if code != exitcode.OK || !strings.Contains(out, "-> Adopted") || !strings.Contains(out, "bead: pending") {
		t.Fatalf("exit %d %q", code, out)
	}
	d, _ := l.LatestDecision(context.Background(), mustTopic(t, l, ids[0]))
	if d.BeadState != ledger.BeadPending || d.Ruling != ledger.RulingAdopted {
		t.Errorf("decision %+v", d)
	}
}

func TestPendingDecidedListsRulings(t *testing.T) {
	_, l := hermetic(t)
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"), syn("b", "Should b?", "E-b"))
	a, b := fmt.Sprint(ids[0]), fmt.Sprint(ids[1])
	run(t, "human", "show", a, b)
	run(t, "human", "rule", a, "rejected")
	run(t, "human", "rule", b, "deferred")
	_, out, _ := run(t, "human", "pending", "--decided")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "Deferred") || !strings.Contains(lines[1], "Rejected") ||
		!strings.Contains(lines[1], "Should a?") {
		t.Errorf("decided: %q", lines)
	}
}

func mkdirWrite(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
