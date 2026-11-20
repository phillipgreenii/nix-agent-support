// Package tui implements pg-router's operator-facing terminal UI. This file
// names the TUI's view of the status reply: every type is the published
// status contract (packages/pg-router/api/status), aliased so the screens keep
// their short names. The Poller that fills it in lives in poller.go.
package tui

import "github.com/phillipgreenii/pg-router/api/status"

// StatusReply is the decode target for the cli.status-reply wire shape.
type StatusReply = status.Reply

// The nested reply types, under the names the screens use.
type (
	CoreInfo       = status.CoreInfo
	LevelRow       = status.Level
	LevelReading   = status.LevelReading
	LevelOverride  = status.LevelOverride
	PausedRole     = status.PausedRole
	FailingClause  = status.FailingClause
	Queue          = status.Queue
	Delivery       = status.Delivery
	Dispatch       = status.Dispatch
	QueueLog       = status.QueueLog
	LastCompaction = status.LastCompaction
	Registration   = status.Registration
	ActivityEntry  = status.ActivityEntry
	Backoff        = status.Backoff
	Failure        = status.Failure
	Listener       = status.Listener
	Source         = status.Source
)
