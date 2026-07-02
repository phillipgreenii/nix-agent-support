package gate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

// one repo applied at "tip", .beads resolves to a single DB at /ws.
const checkInfoJSON = `{"wsid":"home","root":"/ws","terminal":"m",
	"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":false}]}`

func stubDiscover(dirs ...string) func([]string, string) ([]discover.DB, error) {
	return func(_ []string, _ string) ([]discover.DB, error) {
		out := make([]discover.DB, len(dirs))
		for i, d := range dirs {
			out[i] = discover.DB{Dir: d, Identity: "id-" + d}
		}
		return out, nil
	}
}

func checkDeps(f run.Runner, disc func([]string, string) ([]discover.DB, error)) CheckDeps {
	return CheckDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, Discover: disc}
}

func TestCheck_resolvesWhenPatchIDInHistory(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "resolve", "g-1"}, run.Result{}, nil)

	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved = %v", out.Resolved)
	}
}

func TestCheck_dryRunMutatesNothing(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, DryRun: true, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.WouldResolve) != 1 || len(out.Resolved) != 0 {
		t.Fatalf("dry-run: resolved=%v would=%v", out.Resolved, out.WouldResolve)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 4 && c.Args[2] == "gate" && c.Args[3] == "resolve" {
			t.Fatal("dry-run issued a gate resolve")
		}
	}
}

func TestCheck_unknownRepoSkipsAndReports(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-2","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:ghost:zzz","created_at":"2026-06-26T00:00:00Z"}]}`}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Repo != "ghost" {
		t.Fatalf("skipped = %+v", out.Skipped)
	}
}

// Stale boundary: a never-applied repo (applied_ref="") so no scan; one young
// gate (left alone) + one old gate (acted on) in the same run.
func TestCheck_staleBoundaryYoungerVsOlder(t *testing.T) {
	f := run.NewFakeRunner()
	info := `{"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"","dirty":false}]}`
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[
			{"id":"g-young","issue_type":"gate","await_type":"pn:applied","await_id":"home:repo-a:y","created_at":"2026-06-25T23:30:00Z"},
			{"id":"g-old","issue_type":"gate","await_type":"pn:applied","await_id":"home:repo-a:o","created_at":"2026-06-24T00:00:00Z"}
		]}`}, nil)
	// only the old gate is acted on (convert-to-human → AddLabel)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-old", "--add-label", "human"}, run.Result{}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 24 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.StaleActions) != 1 || out.StaleActions[0].GateID != "g-old" {
		t.Fatalf("stale = %+v (only g-old, ~2d old, should be acted on; g-young ~30m)", out.StaleActions)
	}
}

// Multi-DB: the gate lives in the second DB and must be resolved in THAT DB.
func TestCheck_multiDBResolvesInOwnDB(t *testing.T) {
	f := run.NewFakeRunner()
	info := `{"wsid":"home","root":"/ws","terminal":"m","repos":[
		{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tipa","dirty":false},
		{"name":"repo-b","path":"/ws/repo-b","applied_ref":"tipb","dirty":false}]}`
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
	// DB /ws has no pn:applied gates for us.
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	// DB /ws/repo-b holds the gate.
	f.AddResponse("bd", []string{"-C", "/ws/repo-b", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-b","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-b:pidb","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"baseb"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-b", "merge-base", "--is-ancestor", "baseb", "tipb"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-b", "log", "-p", "--no-merges", "baseb..tipb"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-b", "patch-id", "--stable"}, run.Result{Stdout: "pidb sha\n"}, nil)
	// resolve MUST target /ws/repo-b (the gate's own DB).
	f.AddResponse("bd", []string{"-C", "/ws/repo-b", "gate", "resolve", "g-b"}, run.Result{}, nil)

	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws", "/ws/repo-b")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-b" {
		t.Fatalf("resolved = %v", out.Resolved)
	}
	// verify the resolve call's -C dir was /ws/repo-b
	found := false
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 5 && c.Args[2] == "gate" && c.Args[3] == "resolve" && c.Args[4] == "g-b" {
			if c.Args[1] != "/ws/repo-b" {
				t.Fatalf("resolve -C dir = %q, want /ws/repo-b", c.Args[1])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no resolve call recorded")
	}
}

// Baseline set but NOT an ancestor of applied_ref → fall back to -n N scan.
func TestCheck_baselineNotAncestorFallsBackToLastN(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"stale-base"}}]}`}, nil)
	// merge-base --is-ancestor fails (non-zero) → not an ancestor
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "stale-base", "tip"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))
	// MUST scan the last-N form, not stale-base..tip
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "-n", "100", "tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "resolve", "g-1"}, run.Result{}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved = %v (should fall back to -n N scan, not false-miss)", out.Resolved)
	}
}

func TestCheck_strictSkipsDirty(t *testing.T) {
	f := run.NewFakeRunner()
	info := `{"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":true}]}`
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z"}]}`}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, Strict: true, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0].Reason, "dirty") {
		t.Fatalf("skipped = %+v (strict should skip dirty repo)", out.Skipped)
	}
}

// >50 gates: proves --limit 0 returns all and the loop processes every one
// (here all reference an unknown repo → all are skipped).
func TestCheck_over50Gates(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := range 60 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"g-%d","issue_type":"gate","await_type":"pn:applied","await_id":"home:ghost%d:p","created_at":"2026-06-26T00:00:00Z"}`, i, i)
	}
	b.WriteString(`]}`)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: b.String()}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 60 {
		t.Fatalf("processed %d gates, want 60 (proves --limit 0, no 50-cap truncation)", len(out.Skipped))
	}
}
