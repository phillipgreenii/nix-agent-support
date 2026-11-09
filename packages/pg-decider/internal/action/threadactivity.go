package action

import "encoding/json"

// The thread-activity annotation kind: one annotation per (pr, thread id,
// message id), written under the decider's own namespace
// (decider.pr-decider.thread-activity.<thread id>.<message id>), so applying
// the same action twice writes once (pg-desk writes nothing for a value the
// key already holds).
const (
	// ThreadActivityRuleID is the id of the thread-activity decider rule.
	ThreadActivityRuleID = "thread-activity"
	// ThreadActivityDecider is the decider name in the annotation namespace.
	ThreadActivityDecider = "pr-decider"
)

// ThreadActivityViewKey is the key of the annotation as the view prints it
// under annotations.decider["pr-decider"].
func ThreadActivityViewKey(threadID, messageID string) string {
	return ThreadActivityRuleID + "." + threadID + "." + messageID
}

// ThreadActivityKey is the full annotation key the action targets and
// `pg-desk pr annotate --key` takes.
func ThreadActivityKey(threadID, messageID string) string {
	return "decider." + ThreadActivityDecider + "." + ThreadActivityViewKey(threadID, messageID)
}

// ThreadActivityValue is the annotation value: thread id, message id, author,
// timestamp and a short excerpt, as JSON with a fixed member order.
type ThreadActivityValue struct {
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
	Author    string `json:"author"`
	Timestamp string `json:"timestamp"`
	Excerpt   string `json:"excerpt"`
}

// String renders the value as the annotation's string.
func (v ThreadActivityValue) String() string {
	b, _ := json.Marshal(v) // a struct of strings cannot fail
	return string(b)
}
