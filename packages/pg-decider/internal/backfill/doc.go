// Package backfill is the PR lane's backfill driver (PR lane refactor design
// 6.5, "Adoption, backfill and the first apply"): the tool an operator runs to
// bring every followed PR through the composed command `pg-pr-change` once in
// plan mode (to review what the lane would do) and once in apply mode (the
// first apply, which nothing else re-drives for a PR that never changes
// again), and to read the results back.
//
// It is one tool with these modes (cmd/pg-pr-backfill):
//
//   - enumerate: list the backfill set from the tracker's open anchors plus the
//     open PRs of the deployment's PR queries, and persist it (Enumerate, Set).
//   - run --mode plan|apply: run the composed command over the set, paced,
//     under a floor on the GitHub GraphQL budget, resuming after an
//     interruption (Run).
//   - summary: read the run record and print the plan summary, or, in apply
//     mode, the residual check (Summarize).
//   - flip-marker: write the flip marker into the run record and as a comment
//     on the epic bead (WriteFlipMarker).
//   - writes: list every write since the flip marker by timestamp (Writes).
//
// The package execs pg-connector and the composed command as subprocesses and
// reads the run record file; it imports neither pg-desk nor the decider's
// packages, and it does not import internal/prchange (the rig of that package
// imports this one). A test in internal/prchange pins the shapes the two share.
//
// The driver is an operator-started batch that runs to completion and resumes,
// not a scheduled job: pg-router owns every clock (ADR 0077, S30). The only
// wait in it is the pace between two PRs of one pass.
package backfill
