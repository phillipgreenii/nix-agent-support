package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router/conformance"
)

// splitHelp documents the split-triage write path (bead pg2-47rsh).
const splitHelp = `pg-router-ccpool-handler split — record a split-triage decision on a
needs-split-review bead. Run by the split-triage role's session; bd resolves
its tracker from the session's inherited environment (BEADS_DIR / cwd).

  split apply <bead-id>         SPLITTABLE: reads a JSON plan on stdin
                                 {"rationale": "...", "children": [{"title",
                                 "description", "acceptance", "priority"?,
                                 "type"?}, ...]}; creates the children
                                 (labelled split-from:<bead-id>, no stop-count
                                 inheritance), wires bead-id blocked-by each
                                 child and verifies the edges, labels bead-id
                                 was-split, comments, releases a held claim,
                                 and removes needs-split-review last.
                                 Prints one child id per line.
  split unsplittable <bead-id>  NOT splittable: reads the reason (plain text)
                                 on stdin; adds human, comments the reason plus
                                 stop history, releases a held claim, removes
                                 needs-split-review last.

All bead text arrives on stdin and goes to bd as discrete argv elements, never
through a shell.
`

// beadIDPattern restricts a positional bead id to bd id characters, so an
// id can never be read as a flag by bd.
var beadIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func runSplit(args []string) int {
	return runSplitWith(args, os.Stdin, os.Stdout, os.Stderr, &beads.CLIRunner{})
}

func runSplitWith(args []string, stdin io.Reader, stdout, stderr io.Writer, br beads.Runner) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, splitHelp)
		return conformance.ExitOK
	}
	if len(args) != 2 {
		fmt.Fprintln(stderr, "split: usage: split <apply|unsplittable> <bead-id> (input on stdin)")
		return conformance.ExitUsage
	}
	verb, id := args[0], args[1]
	if !beadIDPattern.MatchString(id) {
		fmt.Fprintf(stderr, "split: invalid bead id %q\n", id)
		return conformance.ExitUsage
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "split: read stdin:", err)
		return conformance.ExitError
	}
	ctx := context.Background()
	switch verb {
	case "apply":
		plan, err := beads.DecodeSplitPlan(raw)
		if err != nil {
			fmt.Fprintln(stderr, "split apply:", err)
			return conformance.ExitUsage
		}
		ids, err := beads.ApplySplit(ctx, br, id, plan)
		for _, c := range ids {
			fmt.Fprintln(stdout, c)
		}
		if err != nil {
			fmt.Fprintln(stderr, "split apply:", err)
			return conformance.ExitError
		}
		return conformance.ExitOK
	case "unsplittable":
		if err := beads.MarkUnsplittable(ctx, br, id, string(raw)); err != nil {
			fmt.Fprintln(stderr, "split unsplittable:", err)
			return conformance.ExitError
		}
		return conformance.ExitOK
	default:
		fmt.Fprintf(stderr, "split: unknown verb %q (want apply|unsplittable)\n", verb)
		return conformance.ExitUsage
	}
}
