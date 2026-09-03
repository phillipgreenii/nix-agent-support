package metrics_test

import (
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/metrics"
)

// The Emitter is the core's ingest observer too, so the ingest-time
// condition INV-DISP-3 requires in metrics can actually be wired to it
// (core.Options.Observer). This assertion used to live in metrics_test.go
// (package metrics, an INTERNAL test file) with a comment claiming "core
// never depends on metrics, and metrics never depends on core" — that
// became false at Task 4.1: internal/core/core.go now imports
// internal/metrics for the counter NAME constants statusCounters folds
// (Binding Decision 7: "never a second, divergently-counted set"). Keeping
// this assertion in an INTERNAL metrics test file would then cycle — the
// test binary for package metrics would need to import core, which imports
// metrics, which is the very package being compiled. An EXTERNAL test
// package (package metrics_test, this file) is a separate compilation unit
// that may import both metrics and core with no cycle: metrics itself
// still never imports core.
var _ core.IngestObserver = (*metrics.Emitter)(nil)

// The Emitter is discover's pull-source-failure observer too (Task 3.3,
// register gap R21 / bead pg2-00jpn). Moved here alongside the assertion
// above for the identical reason: discover.go itself imports internal/core,
// so an INTERNAL metrics test file importing discover would ALSO now cycle
// back through core -> metrics.
var _ discover.SourceFailureObserver = (*metrics.Emitter)(nil)
