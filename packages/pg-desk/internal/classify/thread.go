package classify

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

func init() { Register("thread", threadClassifier{}) }

// threadClassifier is the Strategy for the thread entity type. It reads the
// gather.ThreadFacts payload (thread_show is the connector's raw thread
// record) and emits message_added only; resolved and link_changed are not
// derivable from two snapshots and belong to the local change sources.
//
// message_added rule (the design names the kind but not the signal): a thread
// gained a message when its reply count or its last-reply time moved forward
// between the two snapshots.
//
//   - reply_count is compared as an integer, last_reply_at as a PARSED time
//     (epoch seconds with an optional fractional part, the Slack ts shape, or
//     RFC3339); never as raw strings. An unparseable or absent value is no
//     signal for that field.
//   - A reply count that shrank is treated as a partial read: it yields no
//     message_added, even when last_reply_at moved forward.
//   - A degraded snapshot (either side), an empty/null/malformed payload or a
//     missing thread_show is not read as a transition at all.
//   - A participants-only change yields no kind.
//
// The classifier is a pure function of (old, new): no clock, no I/O.
type threadClassifier struct{}

func (threadClassifier) Classify(old, new Snapshot) []Record {
	if old.Degraded || new.Degraded {
		return nil
	}
	o, ok := decodeThreadFacts(old.Payload)
	if !ok {
		return nil
	}
	n, ok := decodeThreadFacts(new.Payload)
	if !ok {
		return nil
	}

	countKnown := o.ReplyCount != nil && n.ReplyCount != nil
	if countKnown && *n.ReplyCount < *o.ReplyCount {
		return nil // appears to shrink: partial read, not a transition
	}
	if countKnown && *n.ReplyCount > *o.ReplyCount {
		return []Record{{Kind: KindMessageAdded}}
	}
	ot, oOK := parseThreadTime(o.LastReplyAt)
	nt, nOK := parseThreadTime(n.LastReplyAt)
	if oOK && nOK && nt.After(ot) {
		return []Record{{Kind: KindMessageAdded}}
	}
	return nil
}

// threadShow is the subset of the connector's thread record this classifier
// reads. ReplyCount is a pointer so an absent field is unknown, not zero.
type threadShow struct {
	ReplyCount  *int   `json:"reply_count"`
	LastReplyAt string `json:"last_reply_at"`
}

// decodeThreadFacts extracts thread_show from a marshaled gather.ThreadFacts.
// ok is false for an empty, null, malformed or thread_show-less payload.
func decodeThreadFacts(payload json.RawMessage) (threadShow, bool) {
	var facts struct {
		ThreadShow *threadShow `json:"thread_show"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &facts) != nil || facts.ThreadShow == nil {
		return threadShow{}, false
	}
	return *facts.ThreadShow, true
}

// parseThreadTime parses last_reply_at: epoch seconds with an optional
// fractional part (Slack ts, e.g. "1700000100.000200") or RFC3339.
func parseThreadTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if secs, err := strconv.ParseInt(whole, 10, 64); err == nil && allDigits(frac) {
		if len(frac) > 9 {
			frac = frac[:9]
		}
		nanos := int64(0)
		if frac != "" {
			nanos, _ = strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 64)
		}
		return time.Unix(secs, nanos).UTC(), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
