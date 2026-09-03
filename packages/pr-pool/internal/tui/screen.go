package tui

// screen selects which of internal/tui's six screens Model.View renders.
//
// Screen transition table (this packet's own normative contract, verbatim
// from the design) [design: Task 4.5 (Screen transition table)]:
//
//	Screen     | Entered when                                             | Exited by
//	-----------|-----------------------------------------------------------|------------------------------------------------------
//	loading    | before the first poll reply lands                        | first successful or failed poll
//	no-core    | ErrNoRunningCore (Dial failure)                           | a poll succeeds (core.state == "started")
//	main       | default once a reply has been received and the core is   | enter on a focusable row -> drill-down; g -> modal
//	           | not quiescing                                             |
//	drill-down | enter on a listener/source row                           | esc
//	modal      | g/?/l                                                    | esc
//	quiescing  | the reply's core.state != "started" and is not itself an | the core exits (-> no-core) or resumes serving
//	           | error (INV-LIFE-2)                                       | (-> main)
//
// Only loading/no-core/main/quiescing are actually driven by this packet
// (Task 4.5): the poll cycle (poll.go) is the only thing that ever changes
// screen here. drill-down and modal are reserved enum values with no
// keybinding that reaches them yet -- the enter/g/?/l handling (and the
// content those screens show) belongs to the sibling packets covering
// Tasks 4.6-4.8. Until then Model.View renders main/drill-down/modal with
// the same minimal placeholder (out of scope, section 8).
type screen int

const (
	screenLoading screen = iota
	screenNoCore
	screenMain
	screenDrillDown
	screenModal
	screenQuiescing
)

// String names a screen for diagnostics and test-failure messages.
func (s screen) String() string {
	switch s {
	case screenLoading:
		return "loading"
	case screenNoCore:
		return "no-core"
	case screenMain:
		return "main"
	case screenDrillDown:
		return "drill-down"
	case screenModal:
		return "modal"
	case screenQuiescing:
		return "quiescing"
	default:
		return "screen(?)"
	}
}
