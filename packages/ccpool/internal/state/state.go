// Package state computes a reconciled, multi-signal view of a session's CURRENT
// state, overriding the store's cached last-turn outcome. Read-only: it never
// mutates a session (contrast internal/session). It is a pure core (Classify,
// ClassifyFrame) wrapped by a thin gatherer (Gather) so the capture-diff timing
// is injectable in tests.
//
// It imports only internal/store (for the row + the State enum) and
// internal/pane (the shared ReLiveCounter). It deliberately does NOT depend on
// internal/session — it defines its own narrow Paner port — to avoid an import
// cycle and keep the package fake-driven testable.
package state

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/ccpool/internal/pane"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// State is the reconciled vocabulary (distinct from store.State, which is the
// cached last-turn outcome). A string type so JSON/human rendering is trivial.
type State string

const (
	Idle            State = "idle"
	Working         State = "working"
	WaitingForHuman State = "waiting-for-human"
	Error           State = "error"
	NotLive         State = "not-live"
)

// SubState qualifies Working only; empty for every other State.
type SubState string

const (
	SubNone      SubState = ""
	SubThinking  SubState = "thinking"
	SubStreaming SubState = "streaming"
)

// Result is the reconciled answer.
type Result struct {
	Name      string
	State     State
	SubState  SubState
	Live      bool
	LastKnown store.State // the cached store state (always populated; the headline for not-live)
}

// Paner is the minimal pane port (a subset of session.Tmux). The cmd layer
// satisfies it with the real *tmux.Client; this narrow interface keeps the
// package dependency-light and the fakes tiny.
type Paner interface {
	HasSession(name string) bool
	CapturePane(name string) (string, error)
}

// Inputs are the gathered signals fed to the pure Classify.
type Inputs struct {
	Name string
	Live bool
	Row  store.Session // for LastKnown + the Failed -> error signal + the Starting launch edge
	// Awaiting is the transcript signal (a dangling AskUserQuestion). The cmd
	// layer computes it via claude-transcript's IsAwaitingInput over the row's
	// TranscriptPath; an unreadable/empty transcript yields false (tolerated).
	Awaiting bool
	// Frame1..Frame3 are up to three pane captures, PaneDiffInterval apart.
	// NumFrames records how many were actually captured: 1 means the fast path
	// short-circuited (Frame1 carried the live counter, so Frame2/Frame3 are
	// unread); 3 means the counter-less path sampled all three.
	Frame1    string
	Frame2    string
	Frame3    string
	NumFrames int
}

// PaneDiffInterval is the gap between adjacent captures used to detect a
// counter-less (streaming) animation. Three frames span 2*PaneDiffInterval
// (~350ms total). Chosen > a streaming frame interval and a thinking tick so a
// live turn always mutates within the window, yet short enough that a settled
// query returns sub-half-second. Read-only status, so a transient streaming
// stall mis-reading as idle is acceptable (Risks) — unlike Unit A's
// confirmStable budget, which must not false-confirm.
const PaneDiffInterval = 175 * time.Millisecond

// PaneVerdict is the pure pane classification.
type PaneVerdict struct {
	InFlight bool
	Sub      SubState
}

// ClassifyFrame is the pure pane analysis over up to three frames (R2 of the
// design). Frame2/Frame3 are ignored when nFrames < 3 (the fast path: a counter
// was already present in f1). Note that prose STREAMING carries NO live counter
// (the counter is a thinking-phase artifact only — confirmed by Unit A's
// real-claude evidence), so streaming is detected purely by frame mutation.
//
//   - counter in any captured frame -> in-flight, thinking
//   - any adjacent counter-less pair differs (f1!=f2 OR f2!=f3) -> in-flight, streaming
//   - all captured frames identical, no counter -> settled
//
// `streaming` detection is best-effort: a pane-quiet pause longer than the
// sampling window (e.g. a long tool call emitting nothing) reads as settled.
// That self-corrects on the next query and is harmless for a read-only status.
func ClassifyFrame(f1, f2, f3 string, nFrames int) PaneVerdict {
	if pane.ReLiveCounter.MatchString(f1) {
		return PaneVerdict{InFlight: true, Sub: SubThinking}
	}
	if nFrames < 3 {
		// Fast path short-circuited but f1 had no counter (only happens if the
		// caller set NumFrames=1 without a counter — defensive: treat as settled).
		return PaneVerdict{}
	}
	if pane.ReLiveCounter.MatchString(f2) || pane.ReLiveCounter.MatchString(f3) {
		return PaneVerdict{InFlight: true, Sub: SubThinking}
	}
	if f1 != f2 || f2 != f3 {
		return PaneVerdict{InFlight: true, Sub: SubStreaming}
	}
	return PaneVerdict{}
}

// Classify applies the reconciliation precedence to gathered Inputs. Pure: no
// I/O, no clock. This is the unit under test.
//
// Precedence (first match wins):
//  1. !Live                         -> not-live (carry LastKnown = row state)
//  2. in-flight (pane)              -> working + sub (thinking|streaming)
//  3. settled + Awaiting            -> waiting-for-human
//  4. settled + row Failed          -> error
//  5. settled + row Starting        -> working/thinking (launching; direct row check)
//  6. else                          -> idle
func Classify(in Inputs) Result {
	res := Result{Name: in.Name, Live: in.Live, LastKnown: in.Row.State}
	if !in.Live {
		res.State = NotLive
		return res
	}
	v := ClassifyFrame(in.Frame1, in.Frame2, in.Frame3, in.NumFrames)
	if v.InFlight {
		res.State = Working
		res.SubState = v.Sub
		return res
	}
	// Settled.
	if in.Awaiting {
		res.State = WaitingForHuman
		return res
	}
	if in.Row.State == store.Failed {
		res.State = Error
		return res
	}
	if in.Row.State == store.Starting {
		// Brand-new / launching: the row says a turn is starting but the pane is
		// not yet animating. Trust the row over a presumed-idle static pane.
		res.State = Working
		res.SubState = SubThinking
		return res
	}
	res.State = Idle
	return res
}

// Gather performs the live signal collection and returns the reconciled Result.
// It is the impure shell around the pure Classify: it gates on liveness
// (HasSession over tmuxName), samples the pane (with the fast-path
// short-circuit), resolves Awaiting, then calls Classify.
//
//   - sleep is injected (real = time.Sleep; tests pass a recording no-op).
//   - awaiting wraps claude-transcript's IsAwaitingInput over the row's
//     transcript path; its error is tolerated (treated as not-awaiting) so a
//     missing/half-written transcript never crashes a status query.
//
// The fast path: capture Frame1; if it carries the live counter, return
// immediately (NumFrames=1, no sleep, no extra captures). Otherwise sample two
// more frames PaneDiffInterval apart.
func Gather(p Paner, sleep func(time.Duration), awaiting func() (bool, error), tmuxName, name string, row store.Session) (Result, error) {
	in := Inputs{Name: name, Row: row}
	in.Live = p.HasSession(tmuxName)
	if !in.Live {
		// No pane/transcript reads — meaningless without a session.
		return Classify(in), nil
	}

	f1, err := p.CapturePane(tmuxName)
	if err != nil {
		return Result{}, fmt.Errorf("capture pane: %w", err)
	}
	in.Frame1 = f1
	in.NumFrames = 1
	if !pane.ReLiveCounter.MatchString(f1) {
		// Counter-less: could be settled or streaming. Sample two more frames.
		sleep(PaneDiffInterval)
		f2, err := p.CapturePane(tmuxName)
		if err != nil {
			return Result{}, fmt.Errorf("capture pane: %w", err)
		}
		sleep(PaneDiffInterval)
		f3, err := p.CapturePane(tmuxName)
		if err != nil {
			return Result{}, fmt.Errorf("capture pane: %w", err)
		}
		in.Frame2, in.Frame3, in.NumFrames = f2, f3, 3
	}

	// Resolve the transcript signal only when we might land in a settled branch;
	// an in-flight verdict ignores it. Computing it unconditionally here is fine
	// (Classify ignores Awaiting for the in-flight/not-live branches) and keeps
	// Gather simple, but skip it on the fast path where we already know we are
	// in-flight to avoid a needless transcript read.
	if !ClassifyFrame(in.Frame1, in.Frame2, in.Frame3, in.NumFrames).InFlight {
		a, aerr := awaiting()
		if aerr == nil {
			in.Awaiting = a
		}
		// aerr is tolerated: leave Awaiting=false and fall through.
	}
	return Classify(in), nil
}
