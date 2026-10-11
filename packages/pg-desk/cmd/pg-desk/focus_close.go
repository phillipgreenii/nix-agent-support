package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// `pg-desk focus close` (spec section 7.4): the end of a day. It reads ONE
// JSON object on stdin, {"summary": "<day summary text>"} (free-form text,
// quotes and newlines included, which is why it is not an argv value), and in
// one transaction get-or-creates the period row, writes closed_at and
// close_note (= summary) onto it, deletes the stored draft when it is the
// draft of exactly that period, and writes the run's focus_run row.
//
// Per-bead progress notes are NOT written here: pg-desk MUST NOT execute a
// tracker write verb (D-F10, G5). The command prose appends each note with the
// connector's issue comment, refreshes the bead, and only then calls close.
//
// Idempotence: closing an already-closed period with an IDENTICAL summary is
// exit 0 (reported "already closed"; the run record is still written, nothing
// else changes); with a DIFFERENT summary it is exit 6, the recorded summary
// is kept and the message names the recorded close time. A closed day cannot
// be amended or reopened this phase.
//
// Check order (the exit code a run takes is the first failing check): usage
// and flag errors (1: --period, --date, a malformed stdin, a --date later than
// today), then the store (1 when not migrated), then the closed-period
// comparison (0 or 6), then the write (3).
//
// Output: one text line, `closed <date>` or `already closed <date> (same
// summary)`; --json prints {contract, verb, run_id, period, closed_at,
// close_note, already_closed, dry_run}. Under --dry-run nothing is written, not
// even the period row, run_id is empty, and the text line is `would close
// <date> with close_note <quoted summary>`.
//
// Telemetry (D24): the verb emits no OpenTelemetry and no Prometheus signal.
// It writes one focus_run row per non-dry run and prints the same record as
// one structured stderr line (focus_run.go); it logs nothing else.

// focusCloseStdinLimit bounds the summary read from stdin.
const focusCloseStdinLimit = 1 << 20

func init() {
	focusGroup().AddCommand(newFocusCloseCmd())
}

func newFocusCloseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "close [--date YYYY-MM-DD] [--dry-run] [--json]  (day summary on stdin)",
		Short: "Close the day: record the day summary and drop the period's stored draft",
		Long: `Close the addressed period. The day summary is read from stdin as ONE JSON
object, {"summary": "<day summary text>"}; the text is free-form and may hold
quotes and newlines. Unknown members are ignored; summary must be a string.

close writes closed_at and close_note (the summary) onto the period row,
creating the row when the day has none, and deletes the stored draft when it
is the draft of this period, all in one transaction. It writes one run record
row.

Per-bead progress notes are not written here: pg-desk executes no tracker
write. Append them first with the connector's issue comment, refresh each bead
with pg-desk issue refresh, and only then call close.

Closing an already-closed period with an identical summary is a success
("already closed"). With a different summary it is exit 6: the recorded summary
is kept and the message names when the day was closed. A closed day cannot be
amended or reopened. --dry-run reports the resolved close_note and writes
nothing, not even the period row.

Exit codes: 0 closed (or already closed with the same summary), 1 usage
(malformed stdin, a --date later than today, a store not yet migrated), 3 a
store write failed, 6 already closed with a different summary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runFocusClose(cmd) },
	}
	addFocusFlags(c)
	c.Flags().Bool(focusFlagDryRun, false, "Report the close_note that would be recorded; write nothing")
	return c
}

// closeRun is one close run.
type closeRun struct {
	cmd     *cobra.Command
	env     *focusEnv
	run     *focusRun
	writing bool
	done    bool
}

// finish ends the run with err's exit code when the write path did not: a
// writing run still writes its focus_run row (exit 1, 3 and 6 are counted by
// the run statistics), then prints the stderr line.
func (c *closeRun) finish(err error) error {
	if c.done {
		return err
	}
	c.done = true
	if c.writing && !c.run.Inserted() {
		_ = c.run.Start(c.env.Store)
	}
	_ = c.run.Finish(focusExitCode(err))
	return err
}

func runFocusClose(cmd *cobra.Command) error {
	dry, _ := cmd.Flags().GetBool(focusFlagDryRun)
	writing := !dry
	env, err := openFocusEnv(cmd, writing)
	if err != nil {
		return err
	}
	defer env.Close()
	c := &closeRun{cmd: cmd, env: env, writing: writing}
	c.run = newFocusRun(cmd.ErrOrStderr(), focusVerbClose, env.Period, env.Actor, dry, env.Now)
	return c.finish(c.once())
}

// decodeCloseSummary reads the one JSON object of stdin and returns its
// summary. Unknown members are ignored; summary must be present and a string.
func decodeCloseSummary(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, focusCloseStdinLimit+1))
	if err != nil {
		return "", focusUsageError("focus close: cannot read stdin: "+err.Error(), `pipe {"summary": "<text>"} on stdin`)
	}
	remedy := `pipe {"summary": "<day summary text>"} on stdin`
	if len(raw) > focusCloseStdinLimit {
		return "", focusUsageError("focus close: the summary is larger than 1 MiB", remedy)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", focusUsageError("focus close: stdin is empty (want one JSON object with a summary)", remedy)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return "", focusUsageError("focus close: stdin is not one JSON object", remedy)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", focusUsageError("focus close: stdin holds more than one JSON value", remedy)
	}
	field, ok := obj["summary"]
	if !ok {
		return "", focusUsageError(`focus close: the JSON object has no "summary" member`, remedy)
	}
	var summary string
	if err := json.Unmarshal(field, &summary); err != nil {
		return "", focusUsageError(`focus close: "summary" must be a string`, remedy)
	}
	return summary, nil
}

// addressesFuture reports whether the addressed day is later than today in
// the configured zone.
func (e *focusEnv) addressesFuture() bool {
	loc := time.Local
	if e.Cfg != nil {
		loc = e.Cfg.FocusTimeZone()
	}
	return e.Period.Key > e.Now.In(loc).Format(focusDayLayout)
}

// closeState is how a period stands against the summary being recorded.
type closeState struct {
	closedAt, note string
	closed         bool
}

func (c *closeRun) readState() (closeState, error) {
	p, found, err := c.env.Store.FocusPeriodGet(c.env.Period.Type, c.env.Period.Key)
	if err != nil {
		return closeState{}, focusTotalError("focus close: read period: "+err.Error(), "check the pg-desk store, then re-run")
	}
	return closeState{closed: found && p.ClosedAt != "", closedAt: p.ClosedAt, note: p.CloseNote}, nil
}

// refusal is exit 6 for a different summary: the recorded one is kept and the
// message names the recorded close time.
func closeRefusal(period string, st closeState) error {
	return &focusError{
		code:   exitPeriodClosed,
		msg:    fmt.Sprintf("period %s is already closed (at %s) with a different summary; the recorded summary is kept", period, st.closedAt),
		remedy: "a closed day cannot be amended or reopened; use --date <tomorrow>",
	}
}

// once is the whole run after the environment is open; its error is the
// run's exit.
func (c *closeRun) once() error {
	env := c.env
	summary, err := decodeCloseSummary(c.cmd.InOrStdin())
	if err != nil {
		return err
	}
	if env.addressesFuture() {
		return focusUsageError(
			fmt.Sprintf("--date %s is later than today (%s)", env.Period.Key, env.Now.Format(focusDayLayout)),
			"a day can only be closed on or after it: pass today's or an earlier --date",
		)
	}

	for attempt := 0; ; attempt++ {
		st, err := c.readState()
		if err != nil {
			return err
		}
		if st.closed {
			if st.note != summary {
				return closeRefusal(env.Period.Key, st)
			}
			return c.report(st, true)
		}
		at := env.Now.UTC().Format(time.RFC3339)
		if !c.writing {
			return c.report(closeState{closed: false, closedAt: at, note: summary}, false)
		}
		werr := env.Store.FocusLockTx(func(tx *store.FocusTx) error {
			p, err := tx.CloseOpenPeriod(env.Period.Type, env.Period.Key, summary, at)
			if err != nil {
				return err
			}
			pid := p.ID
			_, err = tx.InsertRun(store.FocusRun{
				RunID: c.run.RunID(), Verb: focusVerbClose, FocusPeriodID: &pid,
				StartedAt: env.Now.UTC().Format(time.RFC3339), Actor: env.Actor, CountsJSON: "{}",
			})
			return err
		})
		var closedErr *store.FocusPeriodClosedError
		switch {
		case werr == nil:
			c.run.Adopt(env.Store)
			return c.report(closeState{closed: true, closedAt: at, note: summary}, false)
		case errors.As(werr, &closedErr) && attempt == 0:
			// Another close committed between the read and the write: decide
			// again against what it recorded.
			continue
		case errors.As(werr, &closedErr):
			return closeRefusal(env.Period.Key, closeState{closedAt: closedErr.ClosedAt, note: closedErr.CloseNote})
		default:
			return focusTotalError("focus close: write the period: "+werr.Error(), "check the pg-desk store, then re-run close")
		}
	}
}

// report prints the outcome and finishes the run with exit 0. st carries the
// recorded (or would-be) close time and note.
func (c *closeRun) report(st closeState, already bool) error {
	env := c.env
	w := c.cmd.OutOrStdout()
	if env.JSON {
		runID := ""
		if c.writing {
			runID = c.run.RunID()
		}
		if err := printFocusJSON(w, focusVerbClose, map[string]any{
			"period": env.Period.Key, "closed_at": st.closedAt, "close_note": st.note,
			"already_closed": already, "dry_run": !c.writing,
		}, runID); err != nil {
			return err
		}
	} else {
		var line string
		switch {
		case already:
			line = fmt.Sprintf("already closed %s (same summary)", env.Period.Key)
		case !c.writing:
			line = fmt.Sprintf("would close %s with close_note %s", env.Period.Key, strconv.Quote(st.note))
		default:
			line = fmt.Sprintf("closed %s", env.Period.Key)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	c.done = true
	if ferr := c.run.Finish(0); ferr != nil {
		return focusTotalError("focus close: finalize the run record: "+ferr.Error(), "the day is closed; check the pg-desk store")
	}
	return nil
}
