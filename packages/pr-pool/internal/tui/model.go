// Package tui implements pr-pool's operator-facing terminal UI: the
// Model/Update/View skeleton (the tea.Model interface) and Run, the
// exported entry point cmd/pr-pool/tui_cmd.go hands off to (Task 4.5). This
// file carries the Model/Options types and the screen transitions driven
// purely by the poll cycle -- loading/no-core/main/quiescing. Everything
// keyboard-driven beyond the bare screen enum -- drill-down, modals, the
// gate toggle, flash, the error log -- is out of scope here; it lands in
// the sibling packets covering Tasks 4.6-4.8.
package tui

import (
	"errors"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// coreStateStarted is the wire value of StatusReply.Core.State
// (conformance.Started.String(), internal/core/core.go's composeStatusReply)
// that keeps the TUI on screenMain. Any other value -- draining, stopping,
// crashing, ... -- means the core is not fully serving, and the TUI shows
// screenQuiescing instead: a normal lifecycle phase (INV-LIFE-2), never a
// poll error [design: Task 4.5 Step 1].
const coreStateStarted = "started"

// ModalKind selects which full-screen modal is currently open while
// m.screen == screenModal (Task 4.5's screen enum names the coarse
// "a modal is open" state; ModalKind, Task 4.8's own addition, says WHICH
// one). Mirrors pa-monitor's own ModalKind (packages/pa-monitor/internal/
// tui/model.go), with ModalGates added for this package's gates modal.
type ModalKind int

const (
	ModalNone ModalKind = iota
	ModalHelp
	ModalLegend
	ModalGates
)

// Options configures Run, the exported entry point cmd/pr-pool/tui_cmd.go
// hands off to [design: Task 4.5 Interfaces].
type Options struct {
	// Poller is the TUI's one channel to a running core (Task 4.4). A nil
	// Poller disables polling entirely -- Init returns no command -- which
	// only matters to a test that drives Update by hand.
	Poller Poller
	// PollInterval schedules the recurring pollTickMsg tick (Task 4.2's
	// PR_POOL_TUI_INTERVAL, resolved by cmd/pr-pool before calling Run).
	// Zero or negative disables polling, same as a nil Poller.
	PollInterval time.Duration
	// CacheDir is where Task 4.8's ErrorLogger will persist; unused before
	// then.
	CacheDir string
	// Out is the bubbletea program's output. Production leaves it nil,
	// which Run defaults to os.Stdout; a test supplies its own writer --
	// render.Detect still requires that writer to be a real terminal, so a
	// plain *bytes.Buffer makes Run fail fast on theme detection rather
	// than starting a program nothing could ever drive to completion.
	Out io.Writer
	// Version is this TUI binary's own build identifier (cmd/pr-pool's
	// `version` var, main.go), named alongside the core's own reported
	// CoreInfo.Version in the [?] help modal's version pair (Task 4.8,
	// help.go). Empty falls back to "dev" -- mirrors pa-monitor's own
	// Options.Version precedent (packages/pa-monitor/internal/tui/model.go).
	Version string
}

// Model implements tea.Model: pr-pool's own health grammar rendered over
// the screen enum screen.go defines. Every field here is populated
// exclusively by the poll cycle (poll.go) or a bubbletea framework message
// (tea.WindowSizeMsg) -- nothing reacts to a keypress yet.
type Model struct {
	poller       Poller
	pollInterval time.Duration
	cacheDir     string
	theme        render.Theme

	// discoveryPath is where the no-core screen says it looked for a
	// running core: the same convention core.Discover itself resolves
	// against (config.LogDir's discovery record), computed independently
	// of whichever Poller the caller wired up -- the frozen Poller
	// interface (Task 4.4) exposes no accessor for it.
	discoveryPath string

	screen        screen
	width, height int

	// polling guards against a pollTickMsg firing a second, overlapping
	// pollNow while one is still in flight (mirrors pa-monitor's own
	// update.go pollTickMsg handling).
	polling bool

	reply StatusReply
	// sinceCursor is the activity-ring cursor threaded into the next
	// Snapshot call: the previous successful reply's highest Activity Seq,
	// so a subsequent poll asks the ring for only what's new (Task 4.4
	// Interfaces).
	sinceCursor uint64

	lastErr error
	// pollErrFlagged is set by a poll failure that does NOT wrap
	// core.ErrNoRunningCore -- the poll-error zone the sibling packet
	// covering Task 4.6 renders. It never changes which screen is showing
	// [design: Task 4.5 Step 3].
	pollErrFlagged bool

	// -- Task 4.8: keybindings, gate toggle, modals, flash, error log --

	activeModal       ModalKind
	modalScrollOffset int
	// preModalScreen is the screen active before a g/?/l keypress opened a
	// modal (openModal, keybindings.go), restored by esc. Modal is a peer
	// screen (screen.go's table), not nested under main -- esc must return
	// to wherever the operator actually was.
	preModalScreen screen

	// gateTogglePending is true from the moment P/R fires a ToggleGate RPC
	// until its gateToggleResultMsg (success or failure) arrives -- the
	// pending indicator, and the reason no rendered gate state changes
	// before then (no optimistic flip, ever) [design: Task 4.8 Step 1].
	gateTogglePending bool
	// gateToggleStartedAt stamps when the in-flight (or most recently
	// settled) toggle began. It is the asOf race guard's own threshold: a
	// pollResultMsg whose reply.AsOf predates it is discarded outright
	// rather than overwriting the pending/just-toggled gate state [design:
	// Task 4.8 Step 5]. Zero (its initial value) disables the guard.
	gateToggleStartedAt time.Time

	// flash / flashLevel / flashUntil back setFlash's TTL flash line
	// (flash.go): flashUntil is always armed to `now + flashTTL`, so a
	// later setFlash call can only ever push it further out, never earlier
	// [design: Task 4.8 Step 4].
	flash      string
	flashLevel FlashLevel
	flashUntil time.Time

	// errorLogger is Task 4.8's ErrorLogger (errorlog.go), lazily
	// constructed from m.cacheDir the same way pa-monitor's NewModel does.
	// nil when Options.CacheDir was empty; LogString is nil-safe either
	// way.
	errorLogger *ErrorLogger

	// clientVersion is this TUI binary's own build identifier (from
	// Options.Version, defaulting to "dev"), shown in the [?] modal's
	// version pair alongside the core's own reported CoreInfo.Version.
	clientVersion string
}

// NewModel constructs a Model in its pre-first-poll state (screenLoading).
// theme is resolved by the caller: Run detects it against the real output;
// a test may hand in any render.Theme directly.
func NewModel(opts Options, theme render.Theme) *Model {
	m := &Model{
		poller:        opts.Poller,
		pollInterval:  opts.PollInterval,
		cacheDir:      opts.CacheDir,
		theme:         theme,
		discoveryPath: core.RecordPath(config.LogDir()),
		screen:        screenLoading,
		clientVersion: opts.Version,
	}
	if m.clientVersion == "" {
		m.clientVersion = "dev"
	}
	if opts.CacheDir != "" {
		m.errorLogger = &ErrorLogger{CacheDir: opts.CacheDir, FileName: "tui-errors.log"}
	}
	return m
}

// Run is internal/tui's exported entry point: cmd/pr-pool/tui_cmd.go's
// runTUI constructs Options (a real SocketPoller, the resolved poll
// interval) and hands off here rather than driving bubbletea itself
// [design: Task 4.5 Interfaces].
//
// It resolves the render theme against the actual output (opts.Out, or
// os.Stdout in production) BEFORE constructing the program: render.Detect
// refuses a non-terminal writer (colorprofile.NoTTY), which is also this
// function's only way to fail before the program ever starts.
func Run(opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	theme, err := render.Detect(out)
	if err != nil {
		return err
	}
	m := NewModel(opts, theme)
	p := tea.NewProgram(m, tea.WithOutput(out))
	_, err = p.Run()
	return err
}

// Init implements tea.Model. With no Poller or no positive PollInterval it
// schedules nothing -- pollNow would otherwise dereference a nil Poller.
func (m *Model) Init() tea.Cmd {
	if m.poller == nil || m.pollInterval <= 0 {
		return nil
	}
	m.polling = true
	return tea.Batch(m.pollNow(), tickCmd(m.pollInterval))
}

// Update implements tea.Model. Task 4.5 reacted only to the poll cycle
// (pollTickMsg/pollResultMsg/pollErrMsg) and tea.WindowSizeMsg; Task 4.8
// (this packet) adds the keypress dispatch (via keybindings.go's Bindings
// table) plus the gate-toggle and flash-clear messages its own handlers
// produce.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		s := msg.String()
		for _, b := range Bindings {
			for _, k := range b.Keys {
				if k == s {
					if cmd := b.Handle(m); cmd != nil {
						return m, cmd
					}
					return m, nil
				}
			}
		}
		// No matching binding -- no-op fall-through.
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case pollTickMsg:
		if m.polling {
			return m, tickCmd(m.pollInterval)
		}
		m.polling = true
		return m, tea.Batch(m.pollNow(), tickCmd(m.pollInterval))
	case pollResultMsg:
		m.polling = false
		m.applyPollResult(msg.reply)
	case pollErrMsg:
		m.polling = false
		m.applyPollErr(msg.err)
	case gateToggleResultMsg:
		return m, m.applyGateToggleResult(msg)
	case flashClearMsg:
		m.applyFlashClear()
	}
	return m, nil
}

// applyPollResult advances the screen per the lifecycle table's loading/
// no-core/main/quiescing rows: any successful poll -- including the very
// first one, and including one that exits screenNoCore -- lands on
// screenMain when the core reports "started", screenQuiescing otherwise
// [design: Task 4.5 (Screen transition table); Task 4.5 Step 1].
//
// Task 4.8 adds two refinements on top of that Task 4.5 contract:
//
//   - The asOf race guard [design: Task 4.8 Step 5]: while
//     m.gateToggleStartedAt is armed (a gate toggle is in flight, or has
//     just settled), a reply whose AsOf predates it is stale relative to
//     that toggle and is discarded OUTRIGHT -- applying it would silently
//     overwrite the pending/just-toggled gate state with pre-toggle data.
//     The next poll will have caught up.
//   - A keyboard-driven screen (modal or drill-down) survives the poll
//     cycle: only the underlying reply data refreshes; the screen itself
//     is left exactly where the operator put it, never yanked back to
//     main/quiescing by the NEXT tick landing mid-interaction.
func (m *Model) applyPollResult(reply StatusReply) {
	if !m.gateToggleStartedAt.IsZero() && reply.AsOf.Before(m.gateToggleStartedAt) {
		return
	}
	m.reply = reply
	m.lastErr = nil
	m.pollErrFlagged = false
	m.advanceSinceCursor(reply)
	if m.screen == screenModal || m.screen == screenDrillDown {
		return
	}
	if reply.Core.State == coreStateStarted {
		m.screen = screenMain
		return
	}
	m.screen = screenQuiescing
}

// advanceSinceCursor adopts the highest Activity Seq in reply as the
// cursor for the NEXT Snapshot call. reply.Activity is oldest-first (the
// ring's own Read order, internal/activity/ring.go), so the last entry (if
// any) carries the highest Seq.
func (m *Model) advanceSinceCursor(reply StatusReply) {
	if n := len(reply.Activity); n > 0 {
		m.sinceCursor = reply.Activity[n-1].Seq
	}
}

// applyPollErr implements the no-core transition and the poll-error flag
// [design: Task 4.5 Step 3]: a pollErrMsg wrapping core.ErrNoRunningCore (a
// Dial failure, per the lifecycle table's no-core row) moves to
// screenNoCore; any other failure leaves the screen exactly where it was
// and only flags the poll-error zone (rendered by the sibling packet
// covering Task 4.6).
func (m *Model) applyPollErr(err error) {
	m.lastErr = err
	if errors.Is(err, core.ErrNoRunningCore) {
		m.screen = screenNoCore
		m.pollErrFlagged = false
		return
	}
	m.pollErrFlagged = true
}

// View implements tea.Model. The width==0 guard reuses pa-monitor's own
// pre-first-WindowSizeMsg contract (packages/pa-monitor/internal/tui/
// view.go); screenLoading renders the same literal regardless of width
// [design: Task 4.5 Step 4]. screenMain/screenDrillDown still share a
// minimal placeholder -- the full zone ladder/banner/dashboard panes
// (Task 4.6) and the drill-down screen's own content (Task 4.7) are out of
// scope here (section 8). screenModal now routes to real content
// (help.go's renderModal): Task 4.8's own modals are in scope.
func (m *Model) View() string {
	if m.width == 0 || m.screen == screenLoading {
		return "loading…"
	}
	switch m.screen {
	case screenNoCore:
		return noCoreMessage(m.discoveryPath, m.lastErr, m.theme)
	case screenQuiescing:
		return "pr-pool: quiescing (core.state != \"started\")"
	case screenModal:
		return m.renderModal()
	case screenMain, screenDrillDown:
		return "pr-pool: main"
	default:
		return "loading…"
	}
}
