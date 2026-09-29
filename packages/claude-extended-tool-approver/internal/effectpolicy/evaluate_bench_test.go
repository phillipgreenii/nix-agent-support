package effectpolicy

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// This is docket tc-o14i5.3's Phase 2 packet "l" (tc-o14i5.3.14):
// BenchmarkEvaluate + CI p99 recording, feeding the P11 deadline formula
// (see deadline.go). The benchmark's own job is narrow: measure Evaluate's
// (K1's fold entry point) real end-to-end latency over the golden corpus, so
// a p99 can be recorded into benchmark_p99.go and checked by CheckDeadline.
//
// Scope: Bash rows only. internal/goldencorpus's corpus carries non-Bash
// tool rows too (Write, Skill, Agent, ...), but this package's own
// TestNoDirectHookioImport guard (imports_guard_test.go) forbids this
// package from importing internal/hookio — and building a Claude Code
// Write/Edit HookInput (the only other path Evaluate's sibling,
// EvaluateGraph, is reached through — see claudecodeadapter.Evaluate) needs
// exactly that import. The packet's own Contract names "K1's fold entry
// point (Evaluate/Fold)" as the thing benchmarked, not EvaluateGraph, and
// Bash rows are both the majority of the corpus (41/70 as of this packet)
// and the expensive path (shell parse + multi-node graph build), so scoping
// the benchmark to them is a deliberate, in-bounds choice, not a shortfall:
// the one-node file-tool path EvaluateGraph handles directly is cheap by
// construction (a single node, no cmdparse) and not the latency driver P11's
// deadline math cares about.
const corpusPath = "../goldencorpus/testdata/corpus.json"

// loadBashCorpusCommands reads the golden corpus and returns every Bash
// row's (command, cwd, projectRoot) triple, in corpus file order. Declared
// locally (not reusing goldencorpus's own unexported loadCorpus helper,
// which lives in a different package's _test.go and is not importable) —
// this is the smallest reader this file needs, not a general-purpose corpus
// loader.
func loadBashCorpusCommands(tb testing.TB) []evalcontract.Request {
	tb.Helper()
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		tb.Fatalf("reading %s: %v", corpusPath, err)
	}
	var rows []goldencorpus.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		tb.Fatalf("unmarshalling %s: %v", corpusPath, err)
	}

	var reqs []evalcontract.Request
	for _, r := range rows {
		if r.ToolInput.ToolName != "Bash" {
			continue
		}
		cmd, _ := r.ToolInput.ToolInput["command"].(string)
		if cmd == "" {
			tb.Fatalf("case %q: Bash row has empty/missing tool_input.command", r.Case)
		}
		reqs = append(reqs, evalcontract.Request{
			Command:     cmd,
			CWD:         r.CWDPathState.CWD,
			ProjectRoot: r.CWDPathState.ProjectRoot,
		})
	}
	if len(reqs) == 0 {
		tb.Fatal("golden corpus has zero Bash rows")
	}
	return reqs
}

// percentile returns the p-th percentile (0 < p <= 1) of durations, using
// the nearest-rank method (index = ceil(p*n)-1). durations is sorted
// in place. Called only with a non-empty slice.
func percentile(durations []time.Duration, p float64) time.Duration {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	idx := int(p*float64(len(durations))+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(durations) {
		idx = len(durations) - 1
	}
	return durations[idx]
}

// BenchmarkEvaluate measures Evaluate (K1's fold entry point) over every
// Bash row in the golden corpus, once per row per b.N iteration, and reports
// the p50/p99 of the per-row latency distribution as custom metrics (Go's
// built-in ns/op is a mean over the whole loop, not a percentile — P11 needs
// the tail, so this benchmark computes it itself rather than relying on
// ns/op).
//
// Run per this packet's own Validation section:
//
//	go test ./internal/effectpolicy/... -bench BenchmarkEvaluate -benchtime=10x -run '^$'
//
// See benchmark_p99.go for the CI-recorded constant this benchmark's p99
// output feeds, and its own doc comment for the (currently manual) update
// procedure.
func BenchmarkEvaluate(b *testing.B) {
	reqs := loadBashCorpusCommands(b)
	reg := cmddesc.DefaultRegistry()
	policies := DefaultPolicies()
	graphPolicies := DefaultGraphPolicies()

	durations := make([]time.Duration, 0, b.N*len(reqs))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, req := range reqs {
			start := time.Now()
			Evaluate(req, reg, policies, graphPolicies)
			durations = append(durations, time.Since(start))
		}
	}
	b.StopTimer()

	p50 := percentile(durations, 0.50)
	p99 := percentile(durations, 0.99)
	b.ReportMetric(float64(p50.Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns")
}
