package supervisor

import "strings"

// The messages the daemon submits to a session on the ladder's nudge and
// interrupt rungs. Neither starts with "/" (a leading slash would run as a
// slash command) and each is one line. Both name the reason, ask the
// assistant to run `pg-assistants exit` as a command on its own (a session is
// started allowing exactly that command, so a chained form such as
// `cd x && pg-assistants exit` would stop on a permission prompt nobody
// answers) as its last action and to end its turn, and say never to wait for
// input, because nobody is attached.

const (
	sender    = "[pg-assistants]"
	exitSteps = "run `pg-assistants exit` as a command on its own (not chained with anything else) as your last action, and end your turn. " +
		"Never wait for input: nobody is attached to this session."
)

// NudgeText asks the session to wrap up: finish its current step, write its
// notes, run `pg-assistants exit` and end its turn.
func NudgeText(reason string) string {
	return sender + " Time to wrap up this session" + because(reason) +
		". Please finish your current step, write your notes, then " + exitSteps
}

// StopText follows an interrupt: stop now, write the notes, run
// `pg-assistants exit` and end the turn.
func StopText(reason string) string {
	return sender + " Stop now" + because(reason) + ". Write your notes, " + exitSteps
}

// because is the reason in parentheses, on one line; empty for no reason.
func because(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}
