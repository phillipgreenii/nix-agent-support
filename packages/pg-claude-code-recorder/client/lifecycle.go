package client

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
)

// The Conversation and Process facts. The daemon returns evidence and never a
// label; EndLabel derives the label from it, and Crashed and Ended use it.
type (
	// ConversationInfo is one Conversation: its state (open or closed), stage,
	// end evidence and Process history.
	ConversationInfo = lifecycle.Conversation
	// ProcessInfo is one Process: its pid and start time, registry versions,
	// last seen alive, exit instant, host binding and control inbox.
	ProcessInfo = lifecycle.Process
	// Evidence is the end evidence EndLabel reads.
	Evidence = lifecycle.Evidence
	// Label is how a Conversation ended.
	Label = lifecycle.Label
	// ConversationFilter is the conversation.list request.
	ConversationFilter = lifecycle.ConversationFilter
	// ProcessFilter is the process.list request.
	ProcessFilter = lifecycle.ProcessFilter
	// EndedConversation is an ended Conversation with its label and resume line.
	EndedConversation = lifecycle.Ended
	// EndQuery asks for the Conversations that ended a given way.
	EndQuery = lifecycle.EndQuery
	// EndedResult is the answer of Ended.
	EndedResult = lifecycle.EndedResult
)

// The five labels.
const (
	LabelCleared = lifecycle.LabelCleared
	LabelExited  = lifecycle.LabelExited
	LabelUnknown = lifecycle.LabelUnknown
	LabelCrashed = lifecycle.LabelCrashed
	LabelClosed  = lifecycle.LabelClosed
)

// EndLabel derives how a Conversation ended from its evidence, by the precedence
// rules (first match wins): a /clear entry with a new session in the registry is
// cleared; a typed /exit entry after the Process started is exited; an exit the
// recorder did not observe is unknown; a registry file still present when the
// Process exited is crashed; otherwise closed (normally). A Conversation with no
// end has no label ("").
func EndLabel(ev Evidence) Label { return lifecycle.EndLabel(ev) }

// ResumeLine is the line that resumes a Conversation (cd <cwd> && claude
// --resume <id>, with the extract command first once the originals are gone).
func ResumeLine(c ConversationInfo) string { return lifecycle.ResumeLine(c) }

// Conversation returns one Conversation by session id (ErrNotFound when the
// recorder has never seen it).
func (c *Client) Conversation(ctx context.Context, id string) (*ConversationInfo, error) {
	var ans ConversationInfo
	if err := c.post(ctx, "conversation.show", 0, lifecycle.ConversationShow{SessionID: id}, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// ConversationList returns the Conversations whose evidence matches the filters,
// newest first. A degraded answer (the recorder is catching up) is returned with
// a nil error; check Degraded.
func (c *Client) ConversationList(ctx context.Context, f ConversationFilter) (*lifecycle.ConversationList, error) {
	var ans lifecycle.ConversationList
	if err := c.post(ctx, "conversation.list", 0, f, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Process returns one Process by pid and start time (an empty procStart is the
// newest Process with that pid).
func (c *Client) Process(ctx context.Context, pid int64, procStart string) (*ProcessInfo, error) {
	var ans ProcessInfo
	if err := c.post(ctx, "process.show", 0, lifecycle.ProcessShow{Pid: pid, ProcStart: procStart}, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// ProcessList returns the Processes that match the filter, newest first.
func (c *Client) ProcessList(ctx context.Context, f ProcessFilter) (*lifecycle.ProcessList, error) {
	var ans lifecycle.ProcessList
	if err := c.post(ctx, "process.list", 0, f, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

type poster struct{ c *Client }

func (p poster) Post(ctx context.Context, op string, body, out any) error {
	return p.c.post(ctx, op, 0, body, out)
}

// Ended returns the Conversations that ended the way q selects, newest first: it
// reads conversation.list with evidence filters and applies EndLabel.
func (c *Client) Ended(ctx context.Context, q EndQuery) (*EndedResult, error) {
	res, err := lifecycle.QueryEnded(ctx, poster{c}, q)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Crashed returns the Conversations labeled crashed that ended since the given
// time, newest first (the zero time is no bound).
func (c *Client) Crashed(ctx context.Context, since time.Time) ([]EndedConversation, error) {
	res, err := c.Ended(ctx, EndQuery{Label: LabelCrashed, Since: since})
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}
