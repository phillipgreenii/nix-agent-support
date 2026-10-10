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
// Correlation groups and the epic slot rule (groups.go, slot.go): Groups
// joins candidates by DERIVED work links (a source link never joins) into
// groups that take one slot and expose their members and raw due and
// priority values; ApplySlotRule removes an epic from the ranked slots while
// it has an open direct child, before the cap line is counted, and returns
// the trailing "epics with children in play" block (sorted by kind and key,
// not counted against the cap, with an in-plan descendant count) and the
// number of children an epic names that the store does not hold yet. Both are
// pure functions of Inputs and the candidates.
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
