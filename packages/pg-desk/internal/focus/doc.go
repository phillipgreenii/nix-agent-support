// Package focus is the seam package of pg-desk's daily-focus feature.
//
// Every later focus package (the candidate set, the rank, the draft, the
// verbs) imports it. This packet adds only the Annotator seam: the one
// function through which a focus verb writes the focus_selected annotation of
// an entity, so a test can substitute an Annotator that fails for one entity
// and prove the partial-failure path (a table write that committed without
// its annotation, mended by select --repair).
//
// The focus tables are read and written through internal/store (focus.go
// there); this package holds no SQL and no policy.
//
// Telemetry: this package emits no OpenTelemetry or Prometheus signal and
// logs nothing. The annotation's change_log record is the only trace; its
// sequence is returned to the caller, which records it in the run record.
package focus
