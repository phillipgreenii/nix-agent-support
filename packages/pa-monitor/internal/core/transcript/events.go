package transcript

// The Claude transcript event model is shared with ccpool and lives in the
// github.com/phillipgreenii/claude-transcript module. These type aliases
// re-export the canonical definitions so pa-monitor's existing
// internal/core/transcript importers keep working unchanged.

import ct "github.com/phillipgreenii/claude-transcript"

type (
	Event       = ct.Event
	Message     = ct.Message
	Usage       = ct.Usage
	ContentList = ct.ContentList
	Block       = ct.Block
)
