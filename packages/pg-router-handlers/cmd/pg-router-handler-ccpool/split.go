package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router/conformance"
)

// splitHelp documents the split-triage write path (bead pg2-47rsh).
const splitHelp = `pg-router-handler-ccpool split — record a split-triage decision on a
needs-split-review bead. Run by the split-triage role's session; bd resolves
its tracker from the session's inherited environment (BEADS_DIR / cwd).

  split apply <bead-id>         SPLITTABLE: reads a JSON plan (stdin or --file)
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

Each verb reads its input from stdin, or from a file with a trailing
"--file <path>" (split apply <bead-id> --file <path>). Use the file form when
the input holds JSON: Claude Code's dontAsk permission matcher refuses a Bash
command whose heredoc body contains a brace followed by a double quote, so a
JSON plan on a heredoc never runs; a file written with the Write tool does.

All bead text arrives on stdin or in that file and goes to bd as discrete argv
elements, never through a shell.
`

func runSplit(args []string) int {
	return runSplitWith(args, os.Stdin, os.Stdout, os.Stderr, &beads.CLIRunner{})
}

func runSplitWith(args []string, stdin io.Reader, stdout, stderr io.Writer, br beads.Runner) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, splitHelp)
		return conformance.ExitOK
	}
	if len(args) == 4 && args[2] == "--file" {
		return runSplitInput(args[0], args[1], args[3], stdin, stdout, stderr, br)
	}
	if len(args) != 2 {
		fmt.Fprintln(stderr, "split: usage: split <apply|unsplittable> <bead-id> [--file <path>] (input on stdin by default)")
		return conformance.ExitUsage
	}
	return runSplitInput(args[0], args[1], "", stdin, stdout, stderr, br)
}

// runSplitInput runs one split verb, reading its input from path when it is
// set and from stdin otherwise.
func runSplitInput(verb, id, path string, stdin io.Reader, stdout, stderr io.Writer, br beads.Runner) int {
	if !beads.IsID(id) {
		fmt.Fprintf(stderr, "split: invalid bead id %q\n", id)
		return conformance.ExitUsage
	}
	var raw []byte
	var err error
	if path != "" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = io.ReadAll(stdin)
	}
	if err != nil {
		fmt.Fprintln(stderr, "split: read input:", err)
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
