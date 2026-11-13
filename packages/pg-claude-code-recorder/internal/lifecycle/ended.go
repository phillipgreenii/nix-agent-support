package lifecycle

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Ended is a Conversation that has ended, with the label a client derived from
// its evidence and the line to resume it.
type Ended struct {
	Conversation
	Label  Label  `json:"label"`
	Resume string `json:"resume"`
}

// EndQuery asks for the Conversations that ended a given way.
type EndQuery struct {
	// Label selects one label; Suspected instead selects crashed plus unknown
	// rows that carry registry_stale_at_discovery. Both zero selects every ended
	// Conversation.
	Label     Label
	Suspected bool
	Since     time.Time
	Cwd       string
	// Limit bounds the rows returned (0: no limit here; the command defaults to 50).
	Limit int
}

// EndedResult is the answer of QueryEnded.
type EndedResult struct {
	Rows []Ended
	// Degraded is true while the recorder is behind or backfilling: the rows are
	// partial.
	Degraded bool
	// UnknownStale counts, for an empty crashed query, the unknown rows with a
	// stale registry file (what --suspected-crash would list).
	UnknownStale int
}

func boolp(b bool) *bool { return &b }

// prefilter returns the evidence filters a label's rows must satisfy. A filter
// returns a superset of the rows the label can come from, so the precedence
// rules applied afterwards never miss a row.
func prefilter(q EndQuery) ConversationFilter {
	f := ConversationFilter{Ended: boolp(true), Cwd: q.Cwd, Limit: MaxLimit}
	if !q.Since.IsZero() {
		f.Since = q.Since.UTC().Format(time.RFC3339Nano)
	}
	if q.Suspected {
		f.Suspected = true
		return f
	}
	switch q.Label {
	case LabelCrashed:
		f.ExitObserved, f.RegistryPresentAtExit = boolp(true), boolp(true)
	case LabelExited:
		f.ExitEntry = boolp(true)
	case LabelCleared:
		f.ClearEntry = boolp(true)
	case LabelClosed:
		f.ExitObserved, f.RegistryPresentAtExit = boolp(true), boolp(false)
	case LabelUnknown:
		f.ExitObserved = boolp(false)
	}
	return f
}

// QueryEnded reads conversation.list with evidence filters and applies EndLabel
// to each row: the daemon returns evidence, the client derives the label.
func QueryEnded(ctx context.Context, p Poster, q EndQuery) (EndedResult, error) {
	var ans ConversationList
	if err := p.Post(ctx, "conversation.list", prefilter(q), &ans); err != nil {
		return EndedResult{}, err
	}
	res := EndedResult{Degraded: ans.Degraded}
	for _, c := range ans.Conversations {
		l := EndLabel(c.Evidence)
		keep := l != LabelNone
		switch {
		case q.Suspected:
			keep = Suspected(c.Evidence)
		case q.Label != LabelNone:
			keep = l == q.Label
		}
		if !keep {
			continue
		}
		res.Rows = append(res.Rows, Ended{Conversation: c, Label: l, Resume: ResumeLine(c)})
		if q.Limit > 0 && len(res.Rows) >= q.Limit {
			break
		}
	}
	if q.Label == LabelCrashed && !q.Suspected && len(res.Rows) == 0 {
		var stale ConversationList
		f := ConversationFilter{Ended: boolp(true), RegistryStaleAtDiscovery: boolp(true), ExitObserved: boolp(false), Cwd: q.Cwd, Limit: MaxLimit}
		if !q.Since.IsZero() {
			f.Since = q.Since.UTC().Format(time.RFC3339Nano)
		}
		if err := p.Post(ctx, "conversation.list", f, &stale); err == nil {
			for _, c := range stale.Conversations {
				if Suspected(c.Evidence) {
					res.UnknownStale++
				}
			}
		}
	}
	return res, nil
}

var plainPath = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if plainPath.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ResumeLine is the line that resumes a Conversation. Resume is scoped to the
// project directory, so it starts with cd <cwd>; once the Conversation is removed
// the extract command comes first.
func ResumeLine(c Conversation) string {
	if c.Cwd == "" {
		return "claude --resume " + c.SessionID + "  # the project directory is unknown: run it there"
	}
	line := "cd " + shellQuote(c.Cwd) + " && claude --resume " + c.SessionID
	if !c.OriginalsExist {
		line = "pg-claude-code-recorder extract " + c.SessionID + " && " + line
	}
	return line
}
