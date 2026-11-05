//go:build ablation

package parser

// ablateNoPrepare makes pstx run each statement unprepared (database/sql
// prepares and finalizes it per call). Only the ablation build has it.
var ablateNoPrepare bool

// SetAblationNoPrepare sets the hook; call it before parsing.
func SetAblationNoPrepare(v bool) { ablateNoPrepare = v }
