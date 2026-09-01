package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/cache"
	"github.com/phillipgreenii/pg-ccaudit/internal/classify"
	"github.com/phillipgreenii/pg-ccaudit/internal/ledger"
)

// fakeCLIRunner answers with a fixed class for every candidate id present in
// the prompt, in the same envelope shape `claude -p --output-format json`
// produces — the same technique internal/classify's own test suite uses, so
// nothing here reaches a model, a network or a credential.
//
// It decodes the real CANDIDATES: section of the prompt (rather than
// scanning for bracket characters) because the rubric text ABOVE that
// section already contains an example JSON array of its own — a naive
// first-'['/last-']' scan would span both.
func fakeCLIRunner(t *testing.T, class string, usdPerCall float64) classify.Runner {
	t.Helper()
	const marker = "CANDIDATES:\n"
	return func(_ context.Context, _ []string, stdin string) ([]byte, error) {
		idx := strings.LastIndex(stdin, marker)
		if idx < 0 {
			t.Fatalf("fake runner: prompt has no %q section:\n%s", marker, stdin)
		}
		tail := strings.TrimSpace(stdin[idx+len(marker):])
		var items []map[string]any
		if err := json.Unmarshal([]byte(tail), &items); err != nil {
			t.Fatalf("fake runner: decode candidates JSON: %v\n%s", err, tail)
		}
		var verdicts []map[string]string
		for _, it := range items {
			id, _ := it["id"].(string)
			if id == "" {
				continue
			}
			verdicts = append(verdicts, map[string]string{
				"id": id, "class": class, "confidence": "high",
				"what": "w", "prevention": "p", "route": "global-rule",
			})
		}
		body, err := json.Marshal(verdicts)
		if err != nil {
			t.Fatalf("marshal fake verdicts: %v", err)
		}
		env := map[string]any{
			"result":         string(body),
			"total_cost_usd": usdPerCall,
			"usage": map[string]any{
				"input_tokens": 5, "output_tokens": 5,
				"cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
			},
		}
		out, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal fake envelope: %v", err)
		}
		return out, nil
	}
}

// TestReportSurvivesAMidRunKill is pg2-ohvpk's testable claim, exercised at
// the level cmdReport itself calls: start a `report --classifier cli` run,
// stop it partway through (the SIGTERM-equivalent — see below), and assert
// (a) the findings streamed before the stop are on the captured writer,
// (b) the cost ledger shows that run's calls and $ afterward, and (c) a
// subsequent `classify status` over the same window reports those
// candidates as cached rather than pending.
//
// The kill is simulated by cancelling the context passed to
// runClassifierStreaming rather than sending a real SIGTERM to an OS
// process: main.go builds that exact context with
// signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM) and threads it
// down unchanged to this same call, so cancelling it here is the faithful
// in-process equivalent of that signal landing — chosen because compiling
// and signalling a separate OS process from `go test` in this sandbox is
// impractical, exactly the fallback pg2-ohvpk's own testable claim allows
// when a literal SIGTERM test is not.
func TestReportSurvivesAMidRunKill(t *testing.T) {
	mistakeIndex(t)
	// classify.NewCLI() (built by newClassifier, used below by the "classify
	// status" half of this test) resolves its command from this override, so
	// its Name() matches the fake classifier this test drives directly:
	// both must answer to the same cache/ledger classifier key.
	t.Setenv(classify.EnvCommand, "fake")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	cf := addCensusFlags(fs)
	if err := fs.Parse([]string{"--classifier", "cli", "--batch", "2"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	db, _, err := openIndex(*cf.db)
	if err != nil {
		t.Fatalf("openIndex: %v", err)
	}
	defer func() { _ = db.Close() }()
	set, err := extract(context.Background(), db, cf)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(set.Candidates) < 6 {
		t.Fatalf("fixture has only %d candidates; need enough for several batches of 2", len(set.Candidates))
	}
	total := len(set.Candidates)

	base := fakeCLIRunner(t, "self-caught-mistake", 0.02)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callsMade := 0
	cl := &classify.CLI{Command: []string{"fake"}, Batch: 2}
	cl.Run = func(rc context.Context, argv []string, stdin string) ([]byte, error) {
		out, rerr := base(rc, argv, stdin)
		callsMade++
		if callsMade == 2 {
			// The SIGTERM-equivalent: arrives right after this batch's
			// result is already in hand, exactly as a real SIGTERM could
			// land between one `claude -p` call finishing and the next
			// starting.
			cancel()
		}
		return out, rerr
	}

	var streamed bytes.Buffer
	res, rerr := runClassifierStreaming(ctx, cl, set, 0, "report", &streamed)
	if rerr == nil {
		t.Fatal("a run cancelled mid-pass must return an error, not finish silently")
	}
	if res.Cost.Calls != 2 {
		t.Fatalf("cost.Calls=%d, want exactly 2 (batch 3 must never have been attempted)", res.Cost.Calls)
	}
	if len(res.Classifications) != 4 {
		t.Fatalf("got %d classifications, want 4 (2 batches of 2 candidates)", len(res.Classifications))
	}

	// (a) — findings emitted before the kill are present in captured output.
	for _, c := range res.Classifications {
		id := classify.CandidateID(c.Candidate)
		if !strings.Contains(streamed.String(), id) {
			t.Errorf("streamed output is missing candidate %s, classified before the kill", id)
		}
	}
	if strings.Count(streamed.String(), "self-caught-mistake") != 4 {
		t.Errorf("streamed output does not carry all 4 pre-kill classifications:\n%s", streamed.String())
	}

	// (b) — the cost ledger shows this run's calls and $ afterward.
	ledgerPath, err := ledger.DefaultPath()
	if err != nil {
		t.Fatalf("ledger.DefaultPath: %v", err)
	}
	entries, err := ledger.Load(ledgerPath)
	if err != nil {
		t.Fatalf("ledger.Load: %v", err)
	}
	latest := ledger.Latest(entries)
	if len(latest) != 1 {
		t.Fatalf("ledger has %d run(s) recorded, want 1", len(latest))
	}
	run := latest[0]
	if run.Calls != 2 {
		t.Errorf("ledger run.Calls=%d, want 2", run.Calls)
	}
	if diff := run.USD - 0.04; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ledger run.USD=%v, want 0.04 (2 calls at $0.02)", run.USD)
	}
	if run.Done {
		t.Error("a killed run's ledger entry must record done=false — it never reached its own end")
	}
	if run.Command != "report" {
		t.Errorf("ledger run.Command=%q, want %q", run.Command, "report")
	}

	// (c) — a subsequent `classify status` over the same window shows the
	// killed run's classifications as cached.
	statusPath := filepath.Join(t.TempDir(), "stdout")
	statusOut, err := os.Create(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdClassifyStatus(context.Background(), cf, statusOut, os.Stderr); err != nil {
		t.Fatalf("cmdClassifyStatus: %v", err)
	}
	if err := statusOut.Close(); err != nil {
		t.Fatal(err)
	}
	statusBody, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCached := fmt.Sprintf("%d cached", 4)
	wantPending := fmt.Sprintf("%d pending", total-4)
	if !strings.Contains(string(statusBody), wantCached) {
		t.Errorf("classify status does not report the killed run's 4 candidates as cached:\n%s", statusBody)
	}
	if !strings.Contains(string(statusBody), wantPending) {
		t.Errorf("classify status does not report the remaining %d candidates as pending:\n%s", total-4, statusBody)
	}

	// The cache itself, checked directly: every classified candidate is
	// keyed by (id, classifier, prompt version), exactly as cmdClassifyStatus
	// looks it up.
	cachePath, err := cache.DefaultPath()
	if err != nil {
		t.Fatalf("cache.DefaultPath: %v", err)
	}
	cached, err := cache.Load(cachePath)
	if err != nil {
		t.Fatalf("cache.Load: %v", err)
	}
	for _, c := range res.Classifications {
		key := cache.Key{ID: classify.CandidateID(c.Candidate), Classifier: cl.Name(), PromptVersion: classify.PromptVersion}
		if _, ok := cached[key]; !ok {
			t.Errorf("cache is missing an entry for %s", key.ID)
		}
	}
}
