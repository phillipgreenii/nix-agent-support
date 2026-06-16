package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/state"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

// runState computes and prints the RECONCILED state of one session — a live,
// multi-signal answer (tmux liveness + pane sub-phase + transcript awaiting +
// store row) that OVERRIDES the cached store state `doctor` reports. Read-only:
// no session.Service (no mutation), just config + store + a tmux Paner.
//
// Exit: 0 on a successful classification (the state is in the body, not the exit
// code — like doctor/list); 1 for config/store/no-such-session; 2 for usage.
func runState(args []string) int {
	fs := flag.NewFlagSet("state", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool state <external_id> [--json]")
		return 2
	}
	externalID := fs.Arg(0)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()

	ctx := context.Background()
	row, ok, _ := st.GetByExternalID(ctx, externalID)
	if !ok {
		fmt.Fprintln(os.Stderr, "no such session")
		return 1
	}

	cl := tmux.NewClient(cfg.Tmux.Socket)
	tmuxName := session.TmuxName(cfg.Tmux.Prefix, externalID)
	// Awaiting wraps claude-transcript's IsAwaitingInput over the row's transcript
	// path; an empty path means there is nothing to await (false, no read).
	awaiting := func() (bool, error) {
		if row.TranscriptPath == "" {
			return false, nil
		}
		return transcriptAdapter{}.IsAwaitingInput(row.TranscriptPath)
	}
	// lastText mirrors awaiting: an empty path means there is nothing to read
	// (no transcript anchor yet). Gather only consults it for idle/error.
	lastText := func() (string, error) {
		if row.TranscriptPath == "" {
			return "", nil
		}
		return transcriptAdapter{}.LastAssistantText(row.TranscriptPath)
	}
	res, err := state.Gather(cl, time.Sleep, awaiting, lastText, tmuxName, externalID, row)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}

	if *jsonOut {
		b, err := renderStateJSON(res)
		if err != nil {
			fmt.Fprintln(os.Stderr, "state:", err)
			return 1
		}
		fmt.Println(string(b))
	} else {
		fmt.Print(renderState(res))
	}
	return 0
}

// renderState is the pure human-line renderer (mirrors doctor.go's
// `name= state= live=` style). `sub=` is appended only when present; for
// not-live `last_known=` is appended. For waiting-for-human the AskUserQuestion
// text is appended (`question=`); for idle/error the last reply/error text is
// appended (`last_reply=`/`last_error=`), each collapsed to its first line so the
// renderer stays one-line. Returns a trailing newline.
func renderState(res state.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name=%s state=%s", res.Name, res.State)
	if res.SubState != state.SubNone {
		fmt.Fprintf(&b, " sub=%s", res.SubState)
	}
	if res.State == state.NotLive {
		fmt.Fprintf(&b, " last_known=%s", res.LastKnown)
	}
	// waiting-for-human surfaces the AskUserQuestion text (hook-set, pg2-7a5b),
	// collapsed to its first line so the human line stays one-line.
	if res.State == state.WaitingForHuman && res.Question != "" {
		fmt.Fprintf(&b, " question=%s", firstLine(res.Question))
	}
	// Single source field (res.LastText), state-appropriate key. For error the
	// text is the best-available last assistant message — there is no structured
	// error extractor yet (a dedicated extractor is future work); this is honest
	// and additive.
	if res.LastText != "" {
		switch res.State {
		case state.Idle:
			fmt.Fprintf(&b, " last_reply=%s", firstLine(res.LastText))
		case state.Error:
			fmt.Fprintf(&b, " last_error=%s", firstLine(res.LastText))
		}
	}
	fmt.Fprintf(&b, " live=%v\n", res.Live)
	return b.String()
}

// firstLine collapses a possibly multi-line reply to its first line so the
// human renderer stays one-line (mirrors the one-line `name= state=` style).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// stateJSON is the --json shape; sub_state, last_known, last_reply and
// last_error are omitted when empty. last_reply (idle) and last_error (error)
// are state-appropriate keys over the single source field res.LastText.
type stateJSON struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	SubState  string `json:"sub_state,omitempty"`
	Live      bool   `json:"live"`
	LastKnown string `json:"last_known,omitempty"`
	LastReply string `json:"last_reply,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Question is the AskUserQuestion text; emitted only for waiting-for-human
	// (the hook-set signal, pg2-7a5b).
	Question string `json:"question,omitempty"`
}

// renderStateJSON is the pure JSON renderer. last_known is emitted only for
// not-live (where it is the headline); sub_state only when set (working);
// last_reply only for idle and last_error only for error (the two states that
// surface res.LastText). For error the text is the best-available last
// assistant message — there is no structured error extractor yet (a dedicated
// extractor is future work); this is honest and additive.
func renderStateJSON(res state.Result) ([]byte, error) {
	v := stateJSON{
		Name:     res.Name,
		State:    string(res.State),
		SubState: string(res.SubState),
		Live:     res.Live,
	}
	if res.State == state.NotLive {
		v.LastKnown = string(res.LastKnown)
	}
	switch res.State {
	case state.Idle:
		v.LastReply = res.LastText
	case state.Error:
		v.LastError = res.LastText
	case state.WaitingForHuman:
		v.Question = res.Question
	}
	return json.Marshal(v)
}
