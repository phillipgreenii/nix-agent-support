package client

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// The Conversation and Process facts. The daemon returns evidence and never a
// label; EndLabel derives the label from it, and Crashed and Ended use it.
type (
	// ConversationInfo is one Conversation: its state (open or closed), stage,
	// end evidence and Process history.
	ConversationInfo = wire.Conversation
	// ProcessInfo is one Process: its pid and start time, registry versions,
	// last seen alive, exit instant, host binding and control inbox.
	ProcessInfo = wire.Process
	// Evidence is the end evidence EndLabel reads.
	Evidence = wire.Evidence
	// Label is how a Conversation ended.
	Label = wire.Label
	// ConversationFilter is the conversation.list request.
	ConversationFilter = wire.ConversationFilter
	// ProcessFilter is the process.list request.
	ProcessFilter = wire.ProcessFilter
	// EndedConversation is an ended Conversation with its label and resume line.
	EndedConversation = wire.Ended
	// EndQuery asks for the Conversations that ended a given way.
	EndQuery = wire.EndQuery
	// EndedResult is the answer of Ended.
	EndedResult = wire.EndedResult
)

// The five labels.
const (
	LabelCleared = wire.LabelCleared
	LabelExited  = wire.LabelExited
	LabelUnknown = wire.LabelUnknown
	LabelCrashed = wire.LabelCrashed
	LabelClosed  = wire.LabelClosed
)

// EndLabel derives how a Conversation ended from its evidence, by the precedence
// rules (first match wins): a /clear entry with a new session in the registry is
// cleared; a typed /exit entry after the Process started is exited; an exit the
// recorder did not observe is unknown; a registry file still present when the
// Process exited is crashed; otherwise closed (normally). A Conversation with no
// end has no label ("").
func EndLabel(ev Evidence) Label { return wire.EndLabel(ev) }

// ResumeLine is the line that resumes a Conversation (cd <cwd> && claude
// --resume <id>, with the extract command first once the originals are gone).
func ResumeLine(c ConversationInfo) string { return wire.ResumeLine(c) }

// Conversation returns one Conversation by session id (ErrNotFound when the
// recorder has never seen it).
func (c *Client) Conversation(ctx context.Context, id string) (*ConversationInfo, error) {
	var ans ConversationInfo
	if err := c.post(ctx, "conversation.show", 0, wire.ConversationShow{SessionID: id}, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// ConversationList returns the Conversations whose evidence matches the filters,
// newest first. A degraded answer (the recorder is catching up) is returned with
// a nil error; check Degraded.
func (c *Client) ConversationList(ctx context.Context, f ConversationFilter) (*wire.ConversationList, error) {
	var ans wire.ConversationList
	if err := c.post(ctx, "conversation.list", 0, f, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// Process returns one Process by pid and start time (an empty procStart is the
// newest Process with that pid).
func (c *Client) Process(ctx context.Context, pid int64, procStart string) (*ProcessInfo, error) {
	var ans ProcessInfo
	if err := c.post(ctx, "process.show", 0, wire.ProcessShow{Pid: pid, ProcStart: procStart}, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// ProcessList returns the Processes that match the filter, newest first.
func (c *Client) ProcessList(ctx context.Context, f ProcessFilter) (*wire.ProcessList, error) {
	var ans wire.ProcessList
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
	res, err := wire.QueryEnded(ctx, poster{c}, q)
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

// The sessions.facts types.
type (
	// SessionsFactsAnswer is the sessions.facts answer.
	SessionsFactsAnswer = wire.SessionsFactsAnswer
	// SessionFacts is the facts of one session.
	SessionFacts = wire.SessionFacts
	// AwaitingInput is the awaiting-input fact of one session.
	AwaitingInput = wire.AwaitingInput
)

// FactAwaitingInput names the awaiting-input field of SessionsFacts.
const FactAwaitingInput = wire.FactAwaitingInput

// HasCapability reports whether the daemon's last hello listed the operation.
func (c *Client) HasCapability(op string) bool {
	for _, x := range c.hello.Capabilities {
		if x == op {
			return true
		}
	}
	return false
}

// SessionsFacts returns per-session facts for at most 500 session ids, each
// field named in fields. A field listed in Unavailable is not served yet and the
// caller must read it another way.
func (c *Client) SessionsFacts(ctx context.Context, ids, fields []string) (*SessionsFactsAnswer, error) {
	var ans SessionsFactsAnswer
	if err := c.post(ctx, "sessions.facts", 0, wire.SessionsFacts{SessionIDs: ids, Fields: fields}, &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}
