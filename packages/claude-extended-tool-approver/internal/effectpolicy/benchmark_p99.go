package effectpolicy

import "time"

// BenchmarkEvaluateP99 is the CI-recorded p99 latency of BenchmarkEvaluate
// (evaluate_bench_test.go) over the golden corpus
// (internal/goldencorpus/testdata/corpus.json), feeding P11's deadline check
// (deadline.go): CheckDeadline warns when eval_deadline < 2 ×
// BenchmarkEvaluateP99.
//
// # Update procedure (manual — no CI wiring exists yet)
//
// This packet's own "Expected additional reads" note (tc-o14i5.3.14)
// recorded, as of 2026-09-29, that this repo has no ceta-specific CI
// workflow in .forgejo/ or .github/ (only the unrelated
// .github/workflows/update-flakes.yml) — this repo's own CLAUDE.md
// confirms it has no external CI at all, and (operator ruling 2026-10-01,
// bead pg2-pla9d.1) a full `nix flake check` is not a land-time gate either,
// so nothing runs the benchmark automatically. There is therefore
// no automated place to wire a "run the benchmark, commit the new constant"
// step into yet. Until that lands (tracked as a gap, not implemented by this
// packet — see this file's own history), update this constant BY HAND:
//
//	go test ./internal/effectpolicy/... -bench BenchmarkEvaluate -benchtime=10x -run '^$'
//
// and replace the value below with the reported "p99-ns" metric (rounded UP
// for stability — see the value's own recorded provenance).
//
// # Recorded value (2026-09-29, this packet's own landing)
//
// Three local runs (benchtime=10x, 10x, 20x) on the CI-equivalent dev
// sandbox reported p99-ns of 1383202, 1435845 and 1753444 respectively — the
// measurement is noisy (shared-host CPU contention, GC pauses), so the
// constant is rounded UP to 2ms, comfortably above every observed sample,
// rather than pinned to the single highest run (which would itself likely be
// exceeded by the next noisy run). A CI-hosted re-measurement should replace
// this with an actual CI-recorded value once the CI wiring above exists.
const BenchmarkEvaluateP99 = 2 * time.Millisecond
