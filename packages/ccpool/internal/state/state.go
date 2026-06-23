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

	ct "github.com/phillipgreenii/claude-transcript"
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
	// LastText is the best-available last assistant message, populated by Gather
	// ONLY when State is Idle (the last REPLY) or Error (the last ERROR text).
	// Empty for every other state — Gather does not read the transcript for them.
	// Classify never sets this (it is pure); see Gather's lastText resolver.
	LastText string
	// Question is the AskUserQuestion text, populated ONLY when State is
	// WaitingForHuman. Unlike LastText it comes straight from the row
	// (Row.PendingQuestion, set by the `ask` hook) — no resolver/IO — so Classify
	// sets it directly and it stays pure (pg2-7a5b).
	Question string
	// RegistryFound / Registry mirror the gathered registry signal onto the
	// result so downstream consumers (the pg2-yukh ingestion guard) can reuse
	// the same verdict ccpool classified with, without re-reading the registry.
	// RegistryFound is false when no live row was matched (Registry is zero).
	RegistryFound bool
	Registry      ct.ActivityVerdict
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
	// RegistryFound is true when Gather located a LIVE (pid-gated) Claude
	// session-registry row for this session (matched by ClaudeSessionID). When
	// false, Registry is the zero verdict and Classify ignores it — the
	// classifier falls back to the pane+row precedence unchanged.
	RegistryFound bool
	// Registry is the shared claude-transcript activity verdict for this
	// session's registry row (Active/WaitingForHuman/Idle), already pid-gated
	// and freshness-cross-checked by Gather. Consulted by Classify only when
	// RegistryFound. It NEVER supplies the thinking/streaming substate — that
	// remains pane-derived (the registry has no such field). Mapping:
	// Active->Working, WaitingForHuman->WaitingForHuman, Idle->Idle.
	Registry ct.ActivityVerdict
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
// Precedence (first match wins). The registry verdict (in.Registry, valid only
// when in.RegistryFound) is an additive cross-check: it NEVER supplies substate
// (thinking/streaming stay pane-derived) and NEVER overrides the hook-set
// NeedsInput row. Mapping: Active->working, WaitingForHuman->waiting-for-human,
// Idle->idle.
//  1. !Live                                  -> not-live (carry LastKnown = row state)
//  2. in-flight (pane)                        -> working + sub (thinking|streaming)
//  3. settled + row NeedsInput                -> waiting-for-human (hook-set; PRIMARY)
//  4. settled + registry WaitingForHuman      -> waiting-for-human (registry cross-check)
//  5. settled + Awaiting (transcript)         -> waiting-for-human (transcript FALLBACK)
//  6. settled + registry Active               -> working (substate none; busy TRUSTED)
//  7. settled + row Failed                    -> error
//  8. settled + row Starting                  -> working/thinking (launching)
//  9. settled + registry Idle                 -> idle (positive idle confirmation)
// 10. else                                    -> idle
func Classify(in Inputs) Result {
	res := Result{Name: in.Name, Live: in.Live, LastKnown: in.Row.State}
	res.RegistryFound = in.RegistryFound
	res.Registry = in.Registry
	if !in.Live {
		res.State = NotLive
		return res
	}
	v := ClassifyFrame(in.Frame1, in.Frame2, in.Frame3, in.NumFrames)
	if v.InFlight {
		// The thinking COUNTER is a reliable turn signal (a freshly-launched session
		// never renders `(Ns · `), so trust it unconditionally. Counter-less pane
		// animation (streaming detected via frame-diff) is AMBIGUOUS with a session
		// still DRAWING its TUI at launch, so only treat it as a turn when the store
		// row corroborates one is underway (Working/Starting). Otherwise fall through
		// to the settled branches — a freshly-ready session reads idle, not streaming.
		if v.Sub == SubThinking || in.Row.State == store.Working || in.Row.State == store.Starting {
			res.State = Working
			res.SubState = v.Sub
			return res
		}
	}
	// Settled. The hook-set row signal is PRIMARY: the `ask` PreToolUse hook flips
	// the row to NeedsInput the instant AskUserQuestion is invoked, so a settled
	// NeedsInput row is a deterministic waiting-for-human. Surface the row's
	// pending question directly (no IO — it was persisted by the hook, pg2-7a5b).
	if in.Row.State == store.NeedsInput {
		res.State = WaitingForHuman
		res.Question = in.Row.PendingQuestion
		return res
	}
	// Registry cross-check (precedence 4): a pid-gated registry verdict of
	// WaitingForHuman classifies waiting-for-human even when the hook never
	// fired. Surfaces the row's pending question (may be empty). Placed after the
	// hook-set NeedsInput row so that PRIMARY signal still wins.
	if in.RegistryFound && in.Registry.Activity == ct.WaitingForHuman {
		res.State = WaitingForHuman
		res.Question = in.Row.PendingQuestion
		return res
	}
	// FALLBACK (defense-in-depth): the transcript Awaiting signal still classifies
	// waiting-for-human when the row is NOT NeedsInput (e.g. the hook never fired).
	if in.Awaiting {
		res.State = WaitingForHuman
		res.Question = in.Row.PendingQuestion
		return res
	}
	// Registry cross-check (precedence 6): the pane settled this sample but a
	// pid-gated registry verdict of Active says a turn is underway (busy is
	// TRUSTED — never demoted on transcript/pane staleness; mirrors pa-monitor).
	// No substate: the pane was settled, so thinking/streaming is unknown.
	if in.RegistryFound && in.Registry.Activity == ct.Active {
		res.State = Working
		res.SubState = SubNone
		return res
	}
	if in.Row.State == store.Errored {
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
//   - lastText wraps claude-transcript's LastAssistantText over the row's
//     transcript path; it is resolved ONLY after Classify decides the reconciled
//     state, and ONLY when that state is Idle (the last REPLY) or Error (the last
//     ERROR text), so no transcript read happens for working/not-live/
//     waiting-for-human. Its error is tolerated exactly like awaiting (on error,
//     LastText is left empty) so a missing/half-written transcript never crashes
//     a status query.
//   - registry wraps the cmd layer's registry lookup: it sweeps the Claude
//     session registry (~/.claude/sessions), matches the row by
//     ClaudeSessionID, PID-gates the match (PidAlive), and returns
//     ClassifyActivity's verdict plus a "found a live row" bool. A missing dir,
//     no match, or a dead pid yields (zero verdict, false) — Classify then
//     ignores it and uses the pane+row precedence. Resolved whenever the
//     session is live (cheap relative to the pane captures).
//
// The fast path: capture Frame1; if it carries the live counter, return
// immediately (NumFrames=1, no sleep, no extra captures). Otherwise sample two
// more frames PaneDiffInterval apart.
func Gather(p Paner, sleep func(time.Duration), awaiting func() (bool, error), lastText func() (string, error), registry func() (ct.ActivityVerdict, bool), tmuxName, name string, row store.Session) (Result, error) {
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

	// Resolve the transcript awaiting signal whenever the session is live. We do
	// NOT gate this on ClassifyFrame().InFlight: the startup-draw gate in Classify
	// can DEMOTE an in-flight streaming verdict (counter-less frame-diff on a
	// non-Working/Starting row) to a settled branch that consults Awaiting — so a
	// ClassifyFrame-only gate would wrongly skip the read on that path. The read is
	// cheap relative to the pane captures and error-tolerant (a missing/half-written
	// transcript leaves Awaiting=false), and Classify ignores Awaiting on the
	// branches where it genuinely is in-flight, so always reading it is correct.
	if a, aerr := awaiting(); aerr == nil {
		in.Awaiting = a
	}
	// Resolve the registry verdict whenever the session is live. The resolver
	// swallows its own errors into found=false (a missing dir / no match / dead
	// pid all read as "no live row"), so a registry hiccup never crashes a status
	// query — Classify then ignores it and falls back to pane+row.
	in.Registry, in.RegistryFound = registry()
	res := Classify(in)
	// Populate LastText only for the two states that surface it: Idle (the last
	// REPLY) and Error (the last ERROR text). Other states never read the
	// transcript here. The resolver error is tolerated like awaiting's: on error
	// LastText is left empty, so a missing/half-written transcript never crashes.
	if res.State == Idle || res.State == Error {
		if txt, terr := lastText(); terr == nil {
			res.LastText = txt
		}
	}
	return res, nil
}
