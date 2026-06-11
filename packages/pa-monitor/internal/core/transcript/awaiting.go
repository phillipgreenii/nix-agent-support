package transcript

import ct "github.com/phillipgreenii/claude-transcript"

// IsAwaitingInput is re-exported from claude-transcript. It returns true if the
// last assistant turn in the transcript contains an AskUserQuestion tool_use
// with no matching tool_result yet.
func IsAwaitingInput(path string) (bool, error) { return ct.IsAwaitingInput(path) }
