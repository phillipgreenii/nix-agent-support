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
		fmt.Fprintln(os.Stderr, "usage: ccpool state <name> [--json]")
		return 2
	}
	name := fs.Arg(0)

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
	row, ok, _ := st.GetByName(ctx, name)
	if !ok {
		fmt.Fprintln(os.Stderr, "no such session")
		return 1
	}

	cl := tmux.NewClient(cfg.Tmux.Socket)
	tmuxName := cfg.Tmux.Prefix + name
	// Awaiting wraps claude-transcript's IsAwaitingInput over the row's transcript
	// path; an empty path means there is nothing to await (false, no read).
	awaiting := func() (bool, error) {
		if row.TranscriptPath == "" {
			return false, nil
		}
		return transcriptAdapter{}.IsAwaitingInput(row.TranscriptPath)
	}
	res, err := state.Gather(cl, time.Sleep, awaiting, tmuxName, name, row)
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
// not-live `last_known=` is appended. Returns a trailing newline.
func renderState(res state.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name=%s state=%s", res.Name, res.State)
	if res.SubState != state.SubNone {
		fmt.Fprintf(&b, " sub=%s", res.SubState)
	}
	if res.State == state.NotLive {
		fmt.Fprintf(&b, " last_known=%s", res.LastKnown)
	}
	fmt.Fprintf(&b, " live=%v\n", res.Live)
	return b.String()
}

// stateJSON is the --json shape; sub_state and last_known are omitted when empty.
type stateJSON struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	SubState  string `json:"sub_state,omitempty"`
	Live      bool   `json:"live"`
	LastKnown string `json:"last_known,omitempty"`
}

// renderStateJSON is the pure JSON renderer. last_known is emitted only for
// not-live (where it is the headline); sub_state only when set (working).
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
	return json.Marshal(v)
}
