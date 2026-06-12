// Package pane holds the single source of truth for ccpool's pane-liveness
// signals — the "pane mutation = a live turn" rule shared by Unit A's cancel
// confirmation (internal/session) and Unit B's reconciled state classifier
// (internal/state). It is a leaf package (no ccpool deps) so both importers
// share ONE regex with no backwards dependency between them.
package pane

import "regexp"

// ReLiveCounter matches the live spinner's elapsed-seconds counter, e.g.
// "(5s · ↓ 13 tokens · thinking…" — present ONLY mid-turn during the THINKING
// phase (it ticks each ~1s). It is a fast positive for an in-flight thinking
// turn. It is NOT present during prose streaming (which carries no counter line
// — see internal/state's 3-frame diff) and does NOT cover a counter-less phase
// such as a long tool call — see the design's tool-call residual.
//
// In internal/session it is used only as a defense-in-depth guard on the
// confirming pane: it rejects a pathological frozen-but-byte-stable counter
// render. There pane-stability is the primary signal, not this regex.
var ReLiveCounter = regexp.MustCompile(`\(\d+s · `)
