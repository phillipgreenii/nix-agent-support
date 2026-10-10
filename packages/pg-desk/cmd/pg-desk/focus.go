package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The `pg-desk focus` command group and the machinery every focus verb
// shares: the group itself, the shared flags, the exit-code vocabulary, the
// one-line stderr remedy, the unmigrated-store refusal and the JSON envelope
// convention (docs/behavior/pg-desk/focus.md; spec sections 7 and 8.2).
// Period addressing lives in focus_period.go and the run record in
// focus_run.go. This file registers no verb: each later verb adds itself
// from its own init() with focusGroup().AddCommand(...).
//
// Telemetry: the focus verbs emit no OpenTelemetry and no Prometheus signal
// of their own; the run record (focus_run row plus one structured stderr
// line) is the whole of what a verb leaves behind (see focus_run.go).

// exitPeriodClosed is the focus-only exit code for "the addressed period is
// closed". 4 (head-check's), 5 (the retired df-pull code) and 7 (removed by
// ruling RV-A) are never used by a focus verb.
const exitPeriodClosed = 6

// focusContract is the contract string every focus --json output carries in
// its `contract` member, the convention show.md's own contract follows.
const focusContract = "pg-desk.focus/v1"

// Flag names shared by every focus verb.
const (
	focusFlagDate   = "date"
	focusFlagPeriod = "period"
	focusFlagJSON   = "json"
)

// focusPeriodTypeDay is the only period type implemented this phase.
const focusPeriodTypeDay = store.FocusPeriodDay

// focusDayLayout is the canonical period-key layout of a day period.
const focusDayLayout = "2006-01-02"

// focusNow is the clock the focus verbs read; tests replace it.
var focusNow = func() time.Time { return time.Now().UTC() }

var focusGroupCmd *cobra.Command

func init() {
	// Create the group up front so `pg-desk focus` exists even before any
	// verb registers under it.
	focusGroup()
}

// focusGroup returns the `pg-desk focus` group command, creating it on
// rootCmd on first use. Every focus verb registers with
// focusGroup().AddCommand(...) from its own init(); creating the group lazily
// makes that independent of Go file-init order. The group has no behavior of
// its own: run bare it prints its help (and being runnable keeps it listed in
// `pg-desk --help` before any verb registers).
func focusGroup() *cobra.Command {
	if focusGroupCmd != nil {
		return focusGroupCmd
	}
	focusGroupCmd = &cobra.Command{
		Use:   "focus",
		Short: "Plan and track the daily focus (select, show, pull, close, replan, explain)",
		Long: `The daily focus: a plan of the entities the operator chose to work on in one
period (a day), kept in the pg-desk store.

Every verb addresses ONE period: --date, else today in focus.time_zone. The
exit codes are pg-desk's own scheme: 0 ok, 1 usage error or a store not yet
migrated, 2 partial, 3 total failure, 6 period closed. Every non-zero exit
prints a one-line remedy on stderr. --json (or PG_DESK_OUTPUT=json) prints one
object whose contract member is "` + focusContract + `".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	rootCmd.AddCommand(focusGroupCmd)
	return focusGroupCmd
}

// focusError is an error that carries a focus exit code and a one-line
// remedy. Error() is "<message>; <remedy>" on ONE line, which is what main
// prints to stderr on a non-zero exit.
type focusError struct {
	code   int
	msg    string
	remedy string
}

func (e *focusError) Error() string {
	if e.remedy == "" {
		return focusOneLine(e.msg)
	}
	return focusOneLine(e.msg) + "; " + focusOneLine(e.remedy)
}

// ExitCode lets exitCodeFor map the error to the process exit code.
func (e *focusError) ExitCode() int { return e.code }

// Remedy is the one-line remedy alone.
func (e *focusError) Remedy() string { return focusOneLine(e.remedy) }

func focusOneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// focusUsageError is exit 1: a usage or flag error, a malformed reply or
// stdin, a store that is not migrated for the focus tables. msg names the
// problem, remedy the one-line way out.
func focusUsageError(msg, remedy string) error {
	return &focusError{code: 1, msg: msg, remedy: remedy}
}

// focusPartialError is exit 2 and focusTotalError exit 3, with a remedy.
func focusPartialError(msg, remedy string) error {
	return &focusError{code: exitPartial, msg: msg, remedy: remedy}
}

func focusTotalError(msg, remedy string) error {
	return &focusError{code: exitTotal, msg: msg, remedy: remedy}
}

// focusPeriodClosedError is exit 6: the addressed period is closed. The
// wording is the spec's: a closed day cannot be amended or reopened this
// phase, so the remedy is to address a later one.
func focusPeriodClosedError(periodKey string) error {
	return &focusError{
		code:   exitPeriodClosed,
		msg:    fmt.Sprintf("period %s is closed", periodKey),
		remedy: "use --date <tomorrow>",
	}
}

// focusExitCode is the exit code of err under the focus scheme: the code of
// the first error in the chain that carries one, else 1.
func focusExitCode(err error) int {
	if err == nil {
		return 0
	}
	return exitCodeFor(err)
}

// ensureFocusRemedy returns err unchanged when it already carries a focus
// remedy and otherwise wraps it (keeping its exit code) with the generic
// remedy, so every non-zero exit prints one.
func ensureFocusRemedy(err error, remedy string) error {
	if err == nil {
		return nil
	}
	var fe *focusError
	if errors.As(err, &fe) {
		return err
	}
	return &focusError{code: focusExitCode(err), msg: err.Error(), remedy: remedy}
}

// addFocusFlags registers the flags every focus verb shares on c and makes
// its errors single-line: --date, --period (accepted for the day it names and
// hidden from help until a second value exists) and --json. A flag-parse
// error becomes a usage error with a remedy.
func addFocusFlags(c *cobra.Command) {
	c.Flags().String(focusFlagDate, "", "Period day YYYY-MM-DD (default: today in focus.time_zone)")
	c.Flags().String(focusFlagPeriod, focusPeriodTypeDay, "Period type (only \"day\" is implemented)")
	_ = c.Flags().MarkHidden(focusFlagPeriod)
	c.Flags().Bool(focusFlagJSON, false, "Print one JSON object (also selected by PG_DESK_OUTPUT=json)")
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return focusUsageError(err.Error(), "see pg-desk focus "+cmd.Name()+" --help")
	})
}

// focusEnv is what a focus verb needs before it does its own work: the
// config, an opened and migrated store, the one addressed period and the
// output mode.
type focusEnv struct {
	Cfg    *config.Config
	Store  *store.Store
	Period focusPeriod
	// Today reports that the addressed period is today in focus.time_zone.
	Today bool
	Now   time.Time
	JSON  bool
	Actor string
}

// Close closes the store.
func (e *focusEnv) Close() {
	if e != nil && e.Store != nil {
		_ = e.Store.Close()
	}
}

// openFocusEnv runs the shared prefix of a focus verb, in the spec's check
// order (section 7, "Exit codes"): usage and flag errors (--period, --date),
// then the unmigrated-store refusal. A verb that writes (writing true) and
// addresses a day other than today prints the NOTE line on stderr. The
// caller MUST Close the returned env. Period-closed (exit 6) and everything
// after it is the verb's own business: CheckPeriodClosed is the helper.
func openFocusEnv(cmd *cobra.Command, writing bool) (*focusEnv, error) {
	now := focusNow()
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return nil, focusUsageError("focus: load config: "+err.Error(), "fix the pg-desk config and re-run")
	}
	period, today, err := resolveFocusPeriod(cmd, cfg, now)
	if err != nil {
		return nil, err
	}
	st, err := openNewSchemaStore("focus")
	if err != nil {
		if errors.Is(err, store.ErrOldSchema) {
			return nil, focusUsageError(
				"focus: the store is not migrated for the focus tables ("+err.Error()+"); nothing was applied",
				"run pg-desk migrate --cutover, then re-run",
			)
		}
		return nil, focusUsageError(err.Error(), "check the pg-desk store path and permissions, then re-run")
	}
	jsonFlag, _ := cmd.Flags().GetBool(focusFlagJSON)
	env := &focusEnv{
		Cfg: cfg, Store: st, Period: period, Today: today, Now: now,
		JSON: resolveJSONOutput(jsonFlag), Actor: actorFor(cfg, ""),
	}
	if writing && !today {
		printFocusNote(cmd.ErrOrStderr(), period)
	}
	return env, nil
}

// printFocusNote is the line a writing verb prints when the addressed period
// is not today: a mistyped date would otherwise silently create a period.
func printFocusNote(w io.Writer, p focusPeriod) {
	_, _ = fmt.Fprintf(w, "NOTE: writing into period %s, not today\n", p.Key)
}

// CheckPeriodClosed is the period-closed check (exit 6) of the spec's check
// order: nil when the addressed period is open or has no row yet.
func (e *focusEnv) CheckPeriodClosed() error {
	closed, err := focusPeriodIsClosed(e.Store, e.Period)
	if err != nil {
		return focusTotalError("focus: read period: "+err.Error(), "check the pg-desk store, then re-run")
	}
	if closed {
		return focusPeriodClosedError(e.Period.Key)
	}
	return nil
}

// PeriodClosedFunc is the periodClosed callback isStaleDraft takes: it
// reports whether the day period with the given key is closed. A read error
// counts as not closed (the callback has no error return; the verb's own
// reads surface a broken store).
func (e *focusEnv) PeriodClosedFunc() func(key string) bool {
	return func(key string) bool {
		closed, err := focusPeriodIsClosed(e.Store, focusPeriod{Type: e.Period.Type, Key: key})
		return err == nil && closed
	}
}

// focusPeriodIsClosed reports whether the stored period row has a closed_at.
func focusPeriodIsClosed(st *store.Store, p focusPeriod) (bool, error) {
	row, found, err := st.FocusPeriodGet(p.Type, p.Key)
	if err != nil {
		return false, err
	}
	return found && row.ClosedAt != "", nil
}

// printFocusJSON prints the envelope every focus verb's --json shares: one
// object with the `contract` member (focusContract), the `verb`, the
// verb-specific payload members, and `run_id` for the verbs that write
// (runID "" omits it). The contract, verb and run_id members win over a
// payload member of the same name.
func printFocusJSON(w io.Writer, verb string, payload map[string]any, runID string) error {
	obj := make(map[string]any, len(payload)+3)
	for k, v := range payload {
		obj[k] = v
	}
	obj["contract"] = focusContract
	obj["verb"] = verb
	if runID != "" {
		obj["run_id"] = runID
	}
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return fmt.Errorf("focus %s: marshal json: %w", verb, err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}
