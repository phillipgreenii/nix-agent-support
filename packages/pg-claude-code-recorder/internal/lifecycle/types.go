// Package lifecycle holds the engine that reads the Conversation and Process
// facts from the recorder's store, and the tracker that observes Processes
// (their exit instant, whether the registry file was still present, the host
// binding). The wire shapes, the five end labels and EndLabel live in the public
// wire package; the names below are aliases of them so the engine reads as it
// always did. The daemon returns evidence and never a label (ADR 0015).
package lifecycle

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"

type (
	Label               = wire.Label
	Evidence            = wire.Evidence
	Conversation        = wire.Conversation
	Holding             = wire.Holding
	ConversationShow    = wire.ConversationShow
	ConversationFilter  = wire.ConversationFilter
	ConversationList    = wire.ConversationList
	Process             = wire.Process
	Host                = wire.Host
	RegistryVersion     = wire.RegistryVersion
	ProcessShow         = wire.ProcessShow
	ProcessFilter       = wire.ProcessFilter
	ProcessList         = wire.ProcessList
	Poster              = wire.Poster
	SessionsFacts       = wire.SessionsFacts
	AwaitingInput       = wire.AwaitingInput
	SessionFacts        = wire.SessionFacts
	SessionsFactsAnswer = wire.SessionsFactsAnswer
	Ended               = wire.Ended
	EndQuery            = wire.EndQuery
	EndedResult         = wire.EndedResult
)

const (
	LabelNone    = wire.LabelNone
	LabelCleared = wire.LabelCleared
	LabelExited  = wire.LabelExited
	LabelUnknown = wire.LabelUnknown
	LabelCrashed = wire.LabelCrashed
	LabelClosed  = wire.LabelClosed

	StateOpen   = wire.StateOpen
	StateClosed = wire.StateClosed

	StageLive           = wire.StageLive
	StageQuiet          = wire.StageQuiet
	StageSettled        = wire.StageSettled
	StageHeld           = wire.StageHeld
	StageRemovalPending = wire.StageRemovalPending
	StageRemoved        = wire.StageRemoved

	MaxConversationLimit = wire.MaxConversationLimit
	SessionFactsMax      = wire.SessionFactsMax
	FactAwaitingInput    = wire.FactAwaitingInput
)

var (
	Labels = wire.Labels
	Stages = wire.Stages

	EndLabel   = wire.EndLabel
	Suspected  = wire.Suspected
	LegalPair  = wire.LegalPair
	QueryEnded = wire.QueryEnded
	ResumeLine = wire.ResumeLine
)
