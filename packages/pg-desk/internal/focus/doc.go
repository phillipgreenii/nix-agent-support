// Package focus is the seam package of pg-desk's daily-focus feature.
//
// Every later focus package (the rank, the draft, the verbs) imports it.
//
// The candidate set (inputs.go, load.go, candidates.go, exclusions.go): Load
// reads the store into the pure Inputs value, and Candidates computes the set
// of items that may ENTER a plan: the seeds (an open PR that is mine,
// co-owned or lists the operator as a requested reviewer; a Jira issue
// assigned to a configured operator identity; a bead labelled
// pg-focus-planable) and every issue or pr entity one link away from a seed,
// less the exclusions (an inactive or hidden entity, a minted focus bead, a
// work-item bead, a deferred bead, a terminal entity, an absorbed key).
// ExplainCandidate answers why one key is, or is not, in the set from the
// same predicates. Nothing in the computation reads a clock (the caller
// passes the reading), calls a tracker or writes the store.
//
// The Annotator seam (annotator.go): the one function through which a focus
// verb writes the focus_selected annotation of an entity, so a test can
// substitute an Annotator that fails for one entity and prove the
// partial-failure path (a table write that committed without its annotation,
// mended by select --repair).
//
// The focus tables are read and written through internal/store (focus.go
// there); this package holds no SQL and no policy about the tables.
//
// Telemetry: this package emits no OpenTelemetry or Prometheus signal and
// logs nothing. The annotation's change_log record is the only trace of a
// write; its sequence is returned to the caller, which records it in the run
// record. The candidate set is returned to the caller, which prints it.
package focus
