package classify

import (
	"encoding/json"
	"time"
)

// SourceTerminal reports whether an entity's stored facts show it terminal in
// its source system, which is spec 6.1 condition (a) for being active:
//
//   - pr: pr_show.state is closed or merged;
//   - issue: the issue is terminal by the same rule the issue classifier maps
//     to closed: issue_show.status_category is "done" when the tracker sent
//     one, otherwise issue_show.state is one of the terminal state names.
//     Rollout: a stored done-category Jira issue whose name is not in that
//     set (for example "Complete") becomes terminal here the moment its
//     facts carry the category, so the next hydration deactivates it;
//   - thread: thread_show.last_reply_at is older than window at now (window
//     <= 0 means the 7 day default).
//
// It never fabricates: missing, empty or undecodable facts, an unknown state
// and an unknown or unparseable last-reply time are all NOT terminal, and an
// unregistered entity type is never terminal. It is a pure function of its
// arguments (no clock, no I/O).
func SourceTerminal(entityType string, facts json.RawMessage, now time.Time, window time.Duration) bool {
	switch entityType {
	case "pr":
		o, ok := prDecode(Snapshot{Payload: facts})
		return ok && o.hasShow && (o.state == "closed" || o.state == "merged")
	case "issue":
		v := decodeIssueFacts(facts)
		return v.show != nil && v.terminal()
	case "thread":
		if window <= 0 {
			window = localDefaultThreadWindow
		}
		last, ok := localLastReply(facts)
		return ok && now.Sub(last) > window
	}
	return false
}
