package serve

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
)

// LoopDriver is the part of the settle stage the daemon's settle loop needs: the
// mode, the timings, the passes it runs and the signal that asks for the next.
type LoopDriver interface {
	EventMode() bool
	Tick() (settle.TickStats, error)
	Wake(now time.Time) (settle.TickStats, error)
	NextDue() (time.Time, bool)
	MinWake() time.Duration
	ReconcileEvery() time.Duration
	DirtyWake() <-chan struct{}
}

// DirtySink is the part of the settle stage the daemon's ingest path and the
// lifecycle tracker's change callback need: telling it what moved.
type DirtySink interface {
	Dirty(file int64, copyOffset int64)
	LiveChanged()
}

// OpsPort is the part of the settle stage the daemon's removal operations need:
// the held list, where a Session stands, and the Remove and Extract actions.
type OpsPort interface {
	HeldCtx(ctx context.Context) ([]settle.HeldRow, error)
	RecheckHeld() ([]settle.Outcome, error)
	WhereCtx(ctx context.Context, session string) (settle.WhereInfo, error)
	Remove(opt settle.RemoveOptions) (settle.RemoveReport, error)
	Extract(session, to string) (settle.ExtractResult, error)
}

// DoctorPort is the part of the settle stage the daemon's doctor loop, status
// and backfill need: the doctor passes, and the space and tick figures the status
// and the backfill read.
type DoctorPort interface {
	Doctor() (settle.DoctorResult, error)
	DoctorRun() (settle.DoctorAnswer, error)
	DoctorTick() error
	DoctorProjection() (int64, time.Duration, error)
	LowSpace() bool
	LastPhases() settle.TickPhases
	LastCounts() (counts map[string]int, at time.Time, ok bool)
}
