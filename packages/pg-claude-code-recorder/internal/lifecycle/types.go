// Package lifecycle holds the Conversation and Process facts: the wire shapes of
// the conversation and process operations, the five end labels and the one
// function that derives a label from evidence (EndLabel), the engine that reads
// the facts from the recorder's store, and the tracker that observes Processes
// (their exit instant, whether the registry file was still present, the host
// binding). The daemon returns evidence and never a label (ADR 0015): every
// client, and the command, derives the label with EndLabel.
package lifecycle

import (
	"context"
	"time"
)

// Label is how a Conversation ended, derived from evidence.
type Label string

// The five labels (and "" for a Conversation that has no end).
const (
	LabelNone    Label = ""
	LabelCleared Label = "cleared"
	LabelExited  Label = "exited"
	LabelUnknown Label = "unknown"
	LabelCrashed Label = "crashed"
	// LabelClosed is "closed normally" (Ctrl-D, a closed terminal, SIGTERM,
	// SIGHUP; the mod's reasons resume and logout also land here).
	LabelClosed Label = "closed"
)

// Labels lists the five labels in precedence order.
var Labels = []Label{LabelCleared, LabelExited, LabelUnknown, LabelCrashed, LabelClosed}

// Evidence is the one list of end evidence that conversation.show,
// conversation.list, EndLabel and the command share.
type Evidence struct {
	// Ended is whether the Conversation has an end at all. A Conversation a
	// Process left by an in-process /resume has none (the registry stopped naming
	// it while the Process lives), and neither has an open one.
	Ended bool `json:"ended"`
	// RegistryPresentAtExit: the registry file was still present at the instant
	// the Process exited (after the grace window, when it is on).
	RegistryPresentAtExit bool `json:"registry_present_at_exit"`
	// ExitObserved is watched_across_end: the recorder watched the Process across
	// its end.
	ExitObserved bool `json:"exit_observed"`
	// ClearEntry: a /clear entry for the session made while this Process lived,
	// and the registry now names a new session id.
	ClearEntry bool `json:"clear_entry"`
	// ExitEntry is history_exit_entry: a typed /exit entry for the session
	// timestamped after this Process started.
	ExitEntry bool `json:"exit_entry"`
	// RegistryStaleAtDiscovery: at first discovery the registry file named a dead
	// pid (gone, or a start time that differs from procStart).
	RegistryStaleAtDiscovery bool       `json:"registry_stale_at_discovery"`
	Cwd                      string     `json:"cwd"`
	ExitTime                 *time.Time `json:"exit_time,omitempty"`
	LastSeenAlive            *time.Time `json:"last_seen_alive,omitempty"`
}

// EndLabel applies the precedence rules to evidence, first match wins:
//
//  1. a /clear entry (the registry now names a new session): cleared;
//  2. a typed /exit entry after the Process started: exited;
//  3. the exit was not observed: unknown;
//  4. the registry file was still present at the exit: crashed;
//  5. otherwise: closed normally.
//
// A Conversation with no end has no label.
func EndLabel(ev Evidence) Label {
	switch {
	case !ev.Ended:
		return LabelNone
	case ev.ClearEntry:
		return LabelCleared
	case ev.ExitEntry:
		return LabelExited
	case !ev.ExitObserved:
		return LabelUnknown
	case ev.RegistryPresentAtExit:
		return LabelCrashed
	}
	return LabelClosed
}

// Suspected reports whether a Conversation belongs to the --suspected-crash
// list: crashed, or unknown with the stale-at-discovery fact.
func Suspected(ev Evidence) bool {
	switch EndLabel(ev) {
	case LabelCrashed:
		return true
	case LabelUnknown:
		return ev.RegistryStaleAtDiscovery
	}
	return false
}

// Conversation states and stages. live and quiet are derived from the Processes
// (an open Conversation is live, a closed one quiet); settled and held are
// persisted by the settle stage, and removal_pending and removed belong to the
// removal stage. The legal pairs: open is only live; closed is quiet, settled,
// held, removal_pending or removed.
const (
	StateOpen   = "open"
	StateClosed = "closed"

	StageLive           = "live"
	StageQuiet          = "quiet"
	StageSettled        = "settled"
	StageHeld           = "held"
	StageRemovalPending = "removal_pending"
	StageRemoved        = "removed"
)

// Stages lists every stage of the machine.
var Stages = []string{StageLive, StageQuiet, StageHeld, StageSettled, StageRemovalPending, StageRemoved}

// LegalPair reports whether a state and stage may appear together in
// conversation.show.
func LegalPair(state, stage string) bool {
	switch state {
	case StateOpen:
		return stage == StageLive
	case StateClosed:
		for _, s := range Stages {
			if s == stage && s != StageLive {
				return true
			}
		}
	}
	return false
}

// Conversation is one row of conversation.show and conversation.list: the
// Conversation's state and stage, its end evidence and the facts around it.
type Conversation struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"` // open or closed
	Stage     string `json:"stage"`
	// OriginalsExist is whether the transcript's original files are still in the
	// Claude directory (resumable is derived from it and the archive).
	OriginalsExist bool `json:"originals_exist"`
	// Pid and ProcStart name the Process that held the Conversation last.
	Pid       int64  `json:"pid,omitempty"`
	ProcStart string `json:"proc_start,omitempty"`
	// EndTime is the Process's recorded exit time, else its last-seen-alive time
	// (EndTimeApprox), else the last write; nil while open.
	EndTime       *time.Time `json:"end_time,omitempty"`
	EndTimeApprox bool       `json:"end_time_approx,omitempty"`
	// LastWrite is the newest record time of the Conversation's transcript.
	LastWrite *time.Time `json:"last_write,omitempty"`
	// EndReason is the control mod's end reason, detail only.
	EndReason string `json:"end_reason,omitempty"`
	Evidence
	// Processes is the Conversation's Process history (conversation.show only).
	Processes []Holding `json:"processes,omitempty"`

	// regTime is the time of the last registry version of the last Process: the
	// end time of last resort when nothing else dates the end.
	regTime int64
}

// Holding is one Process that held a Conversation, and when.
type Holding struct {
	Pid       int64      `json:"pid"`
	ProcStart string     `json:"proc_start"`
	From      time.Time  `json:"from"`
	To        *time.Time `json:"to,omitempty"` // when the registry stopped naming it; nil while it does
}

// ConversationShow is the conversation.show request.
type ConversationShow struct {
	SessionID string `json:"session_id"`
}

// ConversationFilter is the conversation.list request. Each evidence field is a
// filter (a nil one does not filter), and a filter returns a superset of the
// rows its label can come from, so a client's prefilter never loses a row.
type ConversationFilter struct {
	Since                    string `json:"since,omitempty"` // RFC 3339; on the end time (or last write when open)
	Cwd                      string `json:"cwd,omitempty"`   // the path and its subpaths
	State                    string `json:"state,omitempty"`
	Ended                    *bool  `json:"ended,omitempty"`
	RegistryPresentAtExit    *bool  `json:"registry_present_at_exit,omitempty"`
	ExitObserved             *bool  `json:"exit_observed,omitempty"`
	ClearEntry               *bool  `json:"clear_entry,omitempty"`
	ExitEntry                *bool  `json:"exit_entry,omitempty"`
	RegistryStaleAtDiscovery *bool  `json:"registry_stale_at_discovery,omitempty"`
	Limit                    int    `json:"limit,omitempty"` // default 1000, at most 100000
}

// ConversationList is the conversation.list answer, newest first.
type ConversationList struct {
	Conversations []Conversation `json:"conversations"`
	// Degraded is true while the recorder is behind or backfilling: the list is
	// partial.
	Degraded bool `json:"degraded,omitempty"`
}

// Process is one row of process.show and process.list.
type Process struct {
	Pid       int64  `json:"pid"`
	ProcStart string `json:"proc_start"`
	// SessionID is the Conversation the registry names now (or named last).
	SessionID string     `json:"session_id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Status    string     `json:"status,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	Cwd       string     `json:"cwd,omitempty"`
	// Alive is true while no exit has been recorded for the Process.
	Alive                    bool       `json:"alive"`
	FirstSeen                time.Time  `json:"first_seen"`
	LastSeenAlive            *time.Time `json:"last_seen_alive,omitempty"`
	ExitTime                 *time.Time `json:"exit_time,omitempty"`
	ExitObserved             bool       `json:"exit_observed"`
	RegistryPresentAtExit    bool       `json:"registry_present_at_exit"`
	RegistryDeletedAt        *time.Time `json:"registry_deleted_at,omitempty"`
	RegistryStaleAtDiscovery bool       `json:"registry_stale_at_discovery"`
	Host                     *Host      `json:"host,omitempty"`
	// ControlInbox is whether <pid>.fifo is present, so a client can tell a
	// Claude without the mod from one with it.
	ControlInbox bool `json:"control_inbox"`
	// Versions and Conversations are filled by process.show: every registry
	// version, and the Conversations the Process held, in order.
	Versions      []RegistryVersion `json:"versions,omitempty"`
	Conversations []Holding         `json:"conversations,omitempty"`
}

// Host is the host binding (see proc.Host).
type Host struct {
	Kind          string `json:"kind"`
	CmuxSurface   string `json:"cmux_surface_id,omitempty"`
	CmuxWorkspace string `json:"cmux_workspace_id,omitempty"`
	TmuxSocket    string `json:"tmux_socket,omitempty"`
	TmuxPane      string `json:"tmux_pane,omitempty"`
	TermProgram   string `json:"term_program,omitempty"`
}

// RegistryVersion is one stored version of a Process's registry file.
type RegistryVersion struct {
	At         time.Time `json:"at"`
	SessionID  string    `json:"session_id"`
	Name       string    `json:"name,omitempty"`
	Status     string    `json:"status,omitempty"`
	WaitingFor string    `json:"waiting_for,omitempty"`
}

// ProcessShow is the process.show request.
type ProcessShow struct {
	Pid       int64  `json:"pid"`
	ProcStart string `json:"proc_start,omitempty"` // empty: the newest Process with that pid
}

// ProcessFilter is the process.list request.
type ProcessFilter struct {
	Alive     *bool  `json:"alive,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// ProcessList is the process.list answer, newest first.
type ProcessList struct {
	Processes []Process `json:"processes"`
	Degraded  bool      `json:"degraded,omitempty"`
}

// Poster is the part of a recorder client the Ended helper needs.
type Poster interface {
	Post(ctx context.Context, op string, body, out any) error
}

// SessionsFacts is the sessions.facts request: per-session facts for a bounded
// list of session ids, each field requested by name so a caller pays only for
// what it names. The only field today is "awaiting_input".
type SessionsFacts struct {
	SessionIDs []string `json:"session_ids"`
	Fields     []string `json:"fields"`
}

// SessionFactsMax is the most session ids one sessions.facts request may carry.
const SessionFactsMax = 500

// FactAwaitingInput is the sessions.facts field name of the awaiting-input fact.
const FactAwaitingInput = "awaiting_input"

// AwaitingInput is the awaiting-input fact of one session: a question among the
// newest 50 AskUserQuestion tool_uses of the session's own transcript that no
// tool_result answers. ToolUseID and AskedAt describe the newest such question.
type AwaitingInput struct {
	Open      bool      `json:"open"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	AskedAt   time.Time `json:"asked_at,omitempty"`
}

// SessionFacts is the facts of one session. Found is false for a session the
// recorder has never seen.
type SessionFacts struct {
	Found         bool           `json:"found"`
	AwaitingInput *AwaitingInput `json:"awaiting_input,omitempty"`
}

// SessionsFactsAnswer is the sessions.facts answer. Unavailable lists requested
// fields the recorder cannot serve yet (a backfill is running); a caller falls
// back to its own query for those and never reads a negative from them.
type SessionsFactsAnswer struct {
	Sessions    map[string]SessionFacts `json:"sessions"`
	Unavailable []string                `json:"unavailable,omitempty"`
}
