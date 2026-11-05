//go:build !ablation

package parser

// ablateNoPrepare is the ablation hook of the opt-in insert-cost harness; in a
// normal build it is a constant false, so the compiler removes its branches.
const ablateNoPrepare = false
