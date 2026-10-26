package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// An unanswered tool call counts as activity (bead pg2-r4ig2, INV-CCH-28).
//
// A transcript is written when a message or tool call/result completes, so a
// subagent inside ONE long Bash call (a git commit running the pre-commit
// hooks, a pre-land hook run) writes nothing until the call returns and looks
// quiet once the quiet window elapses. For the END test of a close-or-release
// (drain) session that is not a bounded mistake: closing the pane kills the
// call, releases the claim, parks the bead human and counts a handler-error.
// So the end test additionally asks whether a Bash tool_use is still waiting
// for its tool_result inside the call's declared timeout plus a grace. The
// post-completion callers of waitSessionQuiet (worktree removal, settled-
// session close) do NOT ask: there a wrong "quiet" is bounded (INV-CCH-23) and
// the plain window keeps the pool slot's hold short.
//
// Limits: a Bash call started with run_in_background returns at once, so its
// tool_use is answered and this test does not see it; and only Bash declares a
// timeout, so any other unanswered tool call is still read as quiet.
const (
	// inflightGrace is added to a call's declared timeout before an unanswered
	// call is read as a dead session: the tool's own timeout kill, the result
	// write and the poll interval all land after the declared timeout.
	inflightGrace = 30 * time.Second
	// bashDefaultTimeout is the Bash tool's timeout when the call declares none.
	bashDefaultTimeout = 120 * time.Second
	// bashMaxTimeout caps a declared timeout (the Bash tool's maximum), so a
	// nonsense value cannot hold a dead session open indefinitely.
	bashMaxTimeout = 600 * time.Second
	// inflightTailBytes is how much of a transcript's end is parsed. A pending
	// call and its result are both at the end; a line longer than the tail is
	// not decodable and reads as "no unanswered call" (the pre-pg2-r4ig2 test).
	inflightTailBytes = 512 << 10
)

// InFlightToolCall reports whether the session transcript at transcriptPath, or
// a sibling <transcript-without-.jsonl>/subagents/*.jsonl, ends with a Bash
// tool_use that has no tool_result yet while now is still inside that call's
// declared timeout (default 120 s, capped at 600 s) plus inflightGrace, counted
// from the file's last write. A missing or undecodable file is false.
func InFlightToolCall(transcriptPath string, now time.Time) bool {
	files := []string{transcriptPath}
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(transcriptPath, ".jsonl"), "subagents", "*.jsonl"))
	files = append(files, subs...)
	for _, p := range files {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		timeout, pending := pendingBashTimeout(p)
		if pending && now.Sub(fi.ModTime()) < timeout+inflightGrace {
			return true
		}
	}
	return false
}

type inflightBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Input     struct {
		Timeout any `json:"timeout"`
	} `json:"input"`
}

type inflightRecord struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// pendingBashTimeout returns the longest declared timeout among the Bash
// tool_use blocks in the tail of path that no later tool_result answers, and
// whether there is any such block.
func pendingBashTimeout(path string) (time.Duration, bool) {
	tail, err := readTail(path, inflightTailBytes)
	if err != nil {
		return 0, false
	}
	pending := map[string]time.Duration{}
	for _, line := range bytes.Split(tail, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec inflightRecord
		if json.Unmarshal(line, &rec) != nil || len(rec.Message.Content) == 0 {
			continue
		}
		var blocks []inflightBlock
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			continue // string content or an undecodable shape
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				if b.Name == "Bash" && b.ID != "" {
					pending[b.ID] = declaredBashTimeout(b.Input.Timeout)
				}
			case "tool_result":
				delete(pending, b.ToolUseID)
			}
		}
	}
	var longest time.Duration
	for _, d := range pending {
		longest = max(longest, d)
	}
	return longest, len(pending) > 0
}

// declaredBashTimeout converts a Bash tool_use "timeout" (milliseconds; 0 or
// absent means the default) to the duration the call may run.
func declaredBashTimeout(v any) time.Duration {
	ms, _ := v.(float64) // a non-numeric value reads as "no timeout declared"
	if ms <= 0 {
		return bashDefaultTimeout
	}
	return min(time.Duration(ms*float64(time.Millisecond)), bashMaxTimeout)
}
