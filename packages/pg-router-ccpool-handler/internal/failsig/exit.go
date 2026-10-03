package failsig

import (
	"strconv"
	"strings"
)

// ExitFacts are the ccpool session facts last observed for a dispatched
// session before it was judged to have exited. They are OBSERVED facts, read
// from the `ccpool list` row the poll loop already holds (ADR 0015); ccpool
// does not expose a process exit status or signal, so none is recorded here.
// Naming a cause from them is the handler's judgment, made by [ClassifyExit].
type ExitFacts struct {
	// Observed is false when no list row, present or absent, was ever read
	// for the session. Every other field is then meaningless.
	Observed bool
	// Present is false when the session was absent from the list.
	Present bool
	// State is ccpool's state string as last seen ("" when never seen).
	State string
	// Live is ccpool's liveness (tmux has-session) as last seen.
	Live bool
	// CloseReason is ccpool's recorded close reason ("" when still open).
	CloseReason string
}

// exitLinePrefix starts the one line [ExitFacts] renders to. The session-exit
// rows in table.go match on it, so the format below is part of their contract.
const exitLinePrefix = "ccpool-session:"

// line renders f as one line, always starting with [exitLinePrefix].
func (f ExitFacts) line() string {
	if !f.Observed {
		return exitLinePrefix + " not-observed"
	}
	state, reason := f.State, f.CloseReason
	if state == "" {
		state = "none"
	}
	if reason == "" {
		reason = "none"
	}
	return exitLinePrefix + " state=" + state +
		" live=" + strconv.FormatBool(f.Live) +
		" present=" + strconv.FormatBool(f.Present) +
		" close_reason=" + reason
}

// tailLines is how many trailing non-empty transcript lines [ClassifyExit]
// keeps as evidence when no transcript row explains the exit.
const tailLines = 3

// ClassifyExit names the failure signature of a session that exited before
// completing its work, from the transcript text and the session facts
// observed at exit.
//
// The facts line is appended AFTER the transcript, and the session-exit rows
// sit last in the table, so any row that matches transcript text still wins
// by precedence (an API error in the transcript explains the exit better than
// "state=errored" does). The facts only decide when nothing else matched.
//
// Evidence differs from [Classify] in one way. When the signature is
// [Unknown] or a session-exit signature, no matched line explains the exit,
// so the evidence is the redacted facts line followed by the redacted last
// [tailLines] non-empty transcript lines, cut to [MaxEvidenceLen] bytes. It is
// therefore never empty. For any other signature the evidence is the matched
// line, exactly as [Classify] returns it.
func ClassifyExit(transcript string, f ExitFacts) Result {
	facts := f.line()
	text := transcript
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	res := Classify(text + facts)
	switch res.Signature {
	case Unknown, SessionErrored, SessionIdle, SessionGone:
		res.Evidence = exitEvidence(transcript, facts)
	}
	return res
}

// exitEvidence joins the redacted facts line and the redacted tail of the
// transcript. The whole transcript is redacted BEFORE any line is picked or
// cut, for the same reason [Classify] does: cutting first could leave a
// credential fragment that no pattern recognizes.
func exitEvidence(transcript, facts string) string {
	ev := Redact(facts)
	var tail []string
	lines := strings.Split(Redact(transcript), "\n")
	for i := len(lines) - 1; i >= 0 && len(tail) < tailLines; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			tail = append([]string{l}, tail...)
		}
	}
	if len(tail) > 0 {
		ev += " | tail: " + strings.Join(tail, " / ")
	}
	return strings.TrimSpace(cut(ev, 0, MaxEvidenceLen))
}
