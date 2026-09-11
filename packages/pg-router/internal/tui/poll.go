package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// pollTimeout bounds one Snapshot call from the Model's side, as a safety
// net above the Poller's own internal RPC deadline (poller.go's
// pollerRPCDeadline): Discover+Dial can add latency Snapshot's own
// context.WithTimeout around the RPC call itself does not cover.
const pollTimeout = 10 * time.Second

// pollTickMsg schedules the recurring poll; tickCmd is pa-monitor's own
// tickCmd shape (packages/pa-monitor/internal/tui/poll.go), reused verbatim.
type pollTickMsg struct{}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// pollResultMsg/pollErrMsg carry pollNow's outcome back into Update.
type (
	pollResultMsg struct{ reply StatusReply }
	pollErrMsg    struct{ err error }
)

// pollNow issues exactly one Snapshot call against the Model's Poller,
// carrying forward the activity-ring cursor from the previous successful
// reply (Task 4.4 Interfaces: Snapshot performs at most one RPC attempt per
// call, so nothing here loops or retries on failure).
//
// poller and since are read from m and captured into locals BEFORE the
// returned tea.Cmd closure runs -- pollNow is always called synchronously
// from Update (the only goroutine that ever mutates m), but the returned
// closure itself runs later in a bubbletea-owned goroutine, so closing over
// m.sinceCursor directly would race against a subsequent Update call
// advancing it.
func (m *Model) pollNow() tea.Cmd {
	poller := m.poller
	since := m.sinceCursor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
		defer cancel()
		reply, err := poller.Snapshot(ctx, since)
		if err != nil {
			return pollErrMsg{err: err}
		}
		return pollResultMsg{reply: reply}
	}
}
