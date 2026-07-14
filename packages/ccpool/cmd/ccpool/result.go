package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// replyResolver lazily resolves a turn's reply from its stamped transcript anchor.
// transcriptAdapter satisfies it; tests inject a stub.
type replyResolver interface {
	LastAssistantText(path string) (string, error)
}

// runResult is `ccpool result <turn-id>`: the retrieval side of fire-and-forget
// reply (pg2-12ko). It loads the turn recorded at emit and either reports it
// pending or resolves the reply LAZILY from the transcript anchor the Stop hook
// stamped on it.
//
// Exit-code contract:
//
//	0 — resolved; the reply is on stdout.
//	1 — unknown turn-id, or resolved-but-unreadable transcript (generic error).
//	2 — pending (turn not yet completed); "pending" on stderr. Distinct non-zero,
//	    mirroring reply's "needs input" code so callers can poll on 2.
func runResult(args []string) int {
	fs := flag.NewFlagSet("result", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool result <turn-id>")
		return 2
	}
	turnID := fs.Arg(0)

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
	defer func() { _ = st.Close() }()

	return resultForTurn(context.Background(), st, transcriptAdapter{}, turnID, os.Stdout, os.Stderr)
}

// resultForTurn is the testable core: it loads the turn, then prints the lazily
// resolved reply / pending indicator and returns the exit code (see runResult's
// contract). Pure I/O via the passed writers so tests stay hermetic.
func resultForTurn(ctx context.Context, st *store.Store, rr replyResolver, turnID string, stdout, stderr io.Writer) int {
	t, ok, err := st.GetTurn(ctx, turnID)
	if err != nil {
		fmt.Fprintln(stderr, "result:", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "unknown turn-id: %s\n", turnID)
		return 1
	}
	if t.Status != store.TurnResolved {
		// Not yet completed; the Stop hook hasn't stamped a transcript anchor.
		fmt.Fprintln(stderr, "pending")
		return 2
	}
	if t.TranscriptPath == "" {
		fmt.Fprintf(stderr, "result: turn %s resolved without a transcript anchor\n", turnID)
		return 1
	}
	reply, err := rr.LastAssistantText(t.TranscriptPath)
	if err != nil {
		fmt.Fprintf(stderr, "result: read transcript %s: %v\n", t.TranscriptPath, err)
		return 1
	}
	fmt.Fprintln(stdout, reply)
	return 0
}
