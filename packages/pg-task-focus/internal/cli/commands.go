package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

func newRoot(s *state) *cobra.Command {
	root := &cobra.Command{
		Use:   "pg-task-focus",
		Short: "Keep a written daily routine: checklists and timed work cycles",
		Long: `pg-task-focus is a local daemon with a command-line client. "serve" runs the daemon;
every other verb talks to it over loopback HTTP (127.0.0.1:49210 unless --addr or
PG_TASK_FOCUS_ADDR says otherwise). Every client verb takes --json.

Exit codes: 0 success; 1 an unexpected error; 2 a bad flag or argument; 3 the daemon is
not reachable; 4 the daemon refused the request; 5 the store did not take the change
(read-only mode, or an unknown outcome); 6 the daemon is starting and not ready; 7 serve
could not start; 8 check found a problem.`,
		Version: s.app.Version,
	}
	root.PersistentFlags().StringVar(&s.addr, "addr", "", "daemon address, host:port (default $PG_TASK_FOCUS_ADDR, else 127.0.0.1:49210)")
	root.PersistentFlags().BoolVar(&s.json, "json", false, "print the daemon's JSON document instead of text")
	root.PersistentFlags().DurationVar(&s.wait, "timeout", 15*time.Second, "how long to wait for the daemon to answer")
	root.AddCommand(
		s.serveCmd(), s.statusCmd(), s.periodCmd(), s.profileCmd(), s.taskCmd(), s.cycleCmd(), s.eventsCmd(),
		s.undoCmd(), s.checkCmd(), s.configCmd(),
	)
	return root
}

// mutating adds the flags every mutation shares.
type mutating struct {
	id string
	at string
}

func (m *mutating) flags(c *cobra.Command, withAt bool) {
	c.Flags().StringVar(&m.id, "id", "", "the request id (a ULID); give the one a failed attempt printed to retry it safely")
	if withAt {
		c.Flags().StringVar(&m.at, "at", "", "when it happened: a time today (9:05), `yesterday 21:30`, or an RFC 3339 instant")
	}
}

func (s *state) statusCmd() *cobra.Command {
	var watch bool
	var every time.Duration
	c := &cobra.Command{
		Use:   "status",
		Short: "Show what is due, what is next and what is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch {
				return s.watch(cmd.Context(), every)
			}
			raw, st, err := s.client().State(cmd.Context())
			if err != nil {
				return err
			}
			return s.out(raw, func(w io.Writer) error { renderStatus(w, st); return nil })
		},
	}
	c.Flags().BoolVar(&watch, "watch", false, "stay connected and print one full state object per line (NDJSON) on every change")
	c.Flags().DurationVar(&every, "interval", 30*time.Second, "with --watch, also reprint at least this often, so elapsed times stay fresh")
	return c
}

// watch prints the state as NDJSON: once at connect and on every event of the
// stream (a change of the version or of the store), and at least every
// interval.
func (s *state) watch(ctx context.Context, every time.Duration) error {
	cl := s.client()
	emit := func() error {
		raw, _, err := cl.State(ctx)
		if err != nil {
			return err
		}
		line := new(strings.Builder)
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		line.Write(b)
		fmt.Fprintln(s.app.Stdout, line.String())
		return nil
	}
	if err := emit(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = emit()
			}
		}
	}()
	err := cl.Stream(ctx, func(ev clientEvent) error {
		if ev.Name == "state" || ev.Name == "store" {
			return emit()
		}
		return nil
	})
	if ctx.Err() != nil {
		return nil // interrupted: a clean end of the watch
	}
	return err
}

func (s *state) periodCmd() *cobra.Command {
	root := &cobra.Command{Use: "period", Short: "Change or roll the day, week and sprint"}
	var m mutating
	var day, week, sprint, tz, label, profile, skipAll string
	var skips []string
	var dry bool
	change := &cobra.Command{
		Use:   "change",
		Short: "Begin new periods (one atomic batch), backdating with --at",
		Long: `Begin a new day, week or sprint. --day takes a date, --week and --sprint take START..END
(both dates). Each kind changes independently. Open tasks of the periods being left become
missed, or skipped with --skip TASK=REASON or --skip-all REASON. The zone defaults from the
machine (TZ, then /etc/localtime) and the command fails if neither names one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			zname := tz
			if zname == "" {
				z, err := s.hostZone()
				if err != nil {
					return err
				}
				zname = z.Name()
			}
			changes, err := periodChanges(day, week, sprint, zname, label)
			if err != nil {
				return err
			}
			body := map[string]any{"changes": changes, "id": s.newID(m.id)}
			if err := s.withAt(body, m.at); err != nil {
				return err
			}
			if profile != "" {
				body["profile"] = profile
			}
			if skipAll != "" {
				body["skip_all_reason"] = skipAll
			}
			for _, kv := range skips {
				task, reason, ok := strings.Cut(kv, "=")
				if !ok || task == "" {
					return usagef("--skip %q is not TASK=REASON", kv)
				}
				ov, _ := body["overrides"].([]map[string]any)
				body["overrides"] = append(ov, map[string]any{"task_id": task, "reason": reason})
			}
			if dry {
				body["dry_run"] = true
			}
			return s.mutate(cmd.Context(), "/api/v1/periods/change", body)
		},
	}
	m.flags(change, true)
	change.Flags().StringVar(&day, "day", "", "start a day: the date")
	change.Flags().StringVar(&week, "week", "", "start a week: START..END")
	change.Flags().StringVar(&sprint, "sprint", "", "start a sprint: START..END")
	change.Flags().StringVar(&tz, "tz", "", "the zone in force (an IANA name); default the machine's")
	change.Flags().StringVar(&label, "label", "", "a label for the new periods")
	change.Flags().StringVar(&profile, "profile", "", "also make this profile active")
	change.Flags().StringVar(&skipAll, "skip-all", "", "skip every open task of the periods being left, with this one reason")
	change.Flags().StringArrayVar(&skips, "skip", nil, "skip one open task: TASK=REASON (repeatable)")
	change.Flags().BoolVar(&dry, "dry-run", false, "show what would happen and change nothing")

	var rm mutating
	var rtz, rprofile string
	var rdry bool
	roll := &cobra.Command{
		Use:   "roll",
		Short: "Roll over every period that has ended, after previewing it",
		Long: `Change every kind of period that has ended: start is today in the period's zone and, for a
week or sprint, end is start plus the length of the previous one. The preview is shown and its
version carried back, so what you saw is what happens. While any cycle is running or paused it
prints the blocking cycles and exits non-zero.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return s.roll(cmd.Context(), rm, rtz, rprofile, rdry)
		},
	}
	rm.flags(roll, false)
	roll.Flags().StringVar(&rtz, "tz", "", "the zone in force; default the machine's")
	roll.Flags().StringVar(&rprofile, "profile", "", "also make this profile active")
	roll.Flags().BoolVar(&rdry, "dry-run", false, "show the preview and change nothing")
	root.AddCommand(change, roll)
	return root
}

// periodChanges builds the changes of `period change`.
func periodChanges(day, week, sprint, tz, label string) ([]map[string]any, error) {
	var out []map[string]any
	add := func(kind, spec string) error {
		if spec == "" {
			return nil
		}
		c := map[string]any{"kind": kind, "tz": tz}
		if label != "" {
			c["label"] = label
		}
		if kind == "day" {
			c["start"] = spec
		} else {
			start, end, ok := strings.Cut(spec, "..")
			if !ok || start == "" || end == "" {
				return usagef("--%s %q is not START..END", kind, spec)
			}
			c["start"], c["end"] = start, end
		}
		out = append(out, c)
		return nil
	}
	for _, k := range []struct{ kind, spec string }{{"day", day}, {"week", week}, {"sprint", sprint}} {
		if err := add(k.kind, k.spec); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, usagef("name at least one of --day, --week, --sprint")
	}
	return out, nil
}

// roll implements `period roll`.
func (s *state) roll(ctx context.Context, m mutating, tz, profile string, dryOnly bool) error {
	cl := s.client()
	_, st, err := cl.State(ctx)
	if err != nil {
		return err
	}
	zname := tz
	var changes []map[string]any
	for _, p := range st.Periods {
		if !p.Ended {
			continue
		}
		if zname == "" {
			z, err := s.hostZone()
			if err != nil {
				return err
			}
			zname = z.Name()
		}
		change := map[string]any{"kind": p.Kind, "start": p.Today, "tz": zname}
		if p.Kind != "day" {
			start, e1 := civil.ParseDate(p.Start)
			end, e2 := civil.ParseDate(p.End)
			today, e3 := civil.ParseDate(p.Today)
			if e1 != nil || e2 != nil || e3 != nil {
				return fmt.Errorf("the daemon sent a period with a date that does not parse")
			}
			change["end"] = today.AddDays(start.DaysUntil(end)).String()
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 {
		s.printf("Nothing to roll: no period has ended.\n")
		return nil
	}
	body := map[string]any{"changes": changes}
	if profile != "" {
		body["profile"] = profile
	}
	preview := map[string]any{"changes": changes, "dry_run": true}
	if profile != "" {
		preview["profile"] = profile
	}
	raw, err := cl.Do(ctx, "POST", "/api/v1/periods/change", preview)
	if err != nil {
		return err
	}
	var dry wire.DryRunResult
	if err := json.Unmarshal(raw, &dry); err != nil {
		return err
	}
	if !s.json {
		renderPreview(s.app.Stdout, dry)
	}
	if len(dry.Preview.BlockingCycles) > 0 {
		if s.json {
			_ = s.out(raw, nil)
		}
		return &exitError{code: ExitRefused, msg: "cannot roll while a cycle is running or paused: stop " + blockingList(dry.Preview.BlockingCycles) + " first (or end it at a time with `cycle stop --at`)"}
	}
	if dryOnly {
		if s.json {
			return s.out(raw, nil)
		}
		return nil
	}
	body["id"] = s.newID(m.id)
	body["expected_version"] = dry.StateVersion
	return s.mutate(ctx, "/api/v1/periods/change", body)
}

func blockingList(cs []wire.CycleRef) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = fmt.Sprintf("%s (%s)", c.Title, c.ID)
	}
	return strings.Join(names, ", ")
}

func (s *state) profileCmd() *cobra.Command {
	root := &cobra.Command{Use: "profile", Short: "Change the active profile"}
	var m mutating
	var dry bool
	change := &cobra.Command{
		Use:   "change PROFILE",
		Short: "Make another profile active",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"profile": args[0], "id": s.newID(m.id)}
			if dry {
				body["dry_run"] = true
			}
			return s.mutate(cmd.Context(), "/api/v1/profile/change", body)
		},
	}
	m.flags(change, false)
	change.Flags().BoolVar(&dry, "dry-run", false, "list what would be added, withdrawn and reinstated, and change nothing")
	root.AddCommand(change)
	return root
}

func (s *state) taskCmd() *cobra.Command {
	root := &cobra.Command{Use: "task", Short: "Complete or skip a task"}
	var dm mutating
	done := &cobra.Command{
		Use:   "done NAME",
		Short: "Complete a task; NAME is a definition name or a unique prefix",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := s.taskID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"id": s.newID(dm.id)}
			if err := s.withAt(body, dm.at); err != nil {
				return err
			}
			return s.mutate(cmd.Context(), "/api/v1/tasks/"+url.PathEscape(id)+"/complete", body)
		},
	}
	dm.flags(done, true)
	var sm mutating
	var reason string
	skip := &cobra.Command{
		Use:   "skip NAME --reason TEXT",
		Short: "Skip a task, for a reason",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return usagef("--reason is required and MUST NOT be blank")
			}
			id, err := s.taskID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"id": s.newID(sm.id), "reason": reason}
			if err := s.withAt(body, sm.at); err != nil {
				return err
			}
			return s.mutate(cmd.Context(), "/api/v1/tasks/"+url.PathEscape(id)+"/skip", body)
		},
	}
	sm.flags(skip, true)
	skip.Flags().StringVar(&reason, "reason", "", "why (required, not blank)")
	root.AddCommand(done, skip)
	return root
}

func (s *state) cycleCmd() *cobra.Command {
	root := &cobra.Command{Use: "cycle", Short: "Start, pause, resume, boost, stop, switch, back-fill and annotate work cycles"}

	var sm mutating
	var minutes int
	start := &cobra.Command{
		Use:   "start TYPE",
		Short: "Start a cycle; a running cycle is interrupted, not refused",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"type": args[0], "id": s.newID(sm.id)}
			if minutes > 0 {
				body["minutes"] = minutes
			}
			if err := s.withAt(body, sm.at); err != nil {
				return err
			}
			err := s.mutateRaw(cmd.Context(), "/api/v1/cycles/start", body)
			return s.decorateUnknownType(cmd.Context(), err, args[0])
		},
	}
	sm.flags(start, true)
	start.Flags().IntVar(&minutes, "minutes", 0, "planned minutes, overriding the type's")

	verb := func(name, short string) *cobra.Command {
		var m mutating
		c := &cobra.Command{
			Use:   name + " [CYCLE]",
			Short: short + "; CYCLE is an id or a unique title prefix and may be omitted when only one cycle qualifies",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ref := ""
				if len(args) == 1 {
					r, err := s.cycleRef(cmd.Context(), args[0])
					if err != nil {
						return err
					}
					ref = r
				}
				body := map[string]any{"id": s.newID(m.id)}
				if ref != "" {
					body["cycle_id"] = ref
				}
				if err := s.withAt(body, m.at); err != nil {
					return err
				}
				return s.mutate(cmd.Context(), "/api/v1/cycles/"+name, body)
			},
		}
		m.flags(c, true)
		return c
	}

	var bm mutating
	var boostMinutes int
	boost := &cobra.Command{
		Use:   "boost [CYCLE] --minutes N",
		Short: "Add minutes to a running or paused cycle",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if boostMinutes < 1 {
				return usagef("--minutes MUST be a positive number")
			}
			body := map[string]any{"minutes": boostMinutes, "id": s.newID(bm.id)}
			if len(args) == 1 {
				r, err := s.cycleRef(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				body["cycle_id"] = r
			}
			if err := s.withAt(body, bm.at); err != nil {
				return err
			}
			return s.mutate(cmd.Context(), "/api/v1/cycles/boost", body)
		},
	}
	bm.flags(boost, true)
	boost.Flags().IntVar(&boostMinutes, "minutes", 0, "minutes to add")

	var wm mutating
	switchCmd := &cobra.Command{
		Use:   "switch CYCLE",
		Short: "Make a paused cycle the focus and pause the running one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := s.cycleRef(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"to": r, "id": s.newID(wm.id)}
			if err := s.withAt(body, wm.at); err != nil {
				return err
			}
			return s.mutate(cmd.Context(), "/api/v1/cycles/switch", body)
		},
	}
	wm.flags(switchCmd, true)

	var km mutating
	var from, to string
	brk := &cobra.Command{
		Use:   "break CYCLE --from TIME --to TIME",
		Short: "Back-fill a break the cycle forgot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return usagef("--from and --to are required")
			}
			f, err := s.parseAt(from)
			if err != nil {
				return err
			}
			t, err := s.parseAt(to)
			if err != nil {
				return err
			}
			r, err := s.cycleRef(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return s.mutate(cmd.Context(), "/api/v1/cycles/break", map[string]any{"cycle_id": r, "from": f, "to": t, "id": s.newID(km.id)})
		},
	}
	km.flags(brk, false)
	brk.Flags().StringVar(&from, "from", "", "when the break began")
	brk.Flags().StringVar(&to, "to", "", "when the break ended")

	var nm mutating
	var note string
	var kvs []string
	noteCmd := &cobra.Command{
		Use:   "note [CYCLE] [--note TEXT] [--kv key=value]...",
		Short: "Replace a cycle's note and key/value pairs (the whole form, in any state)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"id": s.newID(nm.id)}
			if len(args) == 1 {
				r, err := s.cycleRef(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				body["cycle_id"] = r
			}
			if note != "" {
				body["note"] = note
			}
			var pairs []map[string]string
			for _, kv := range kvs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usagef("--kv %q is not key=value", kv)
				}
				pairs = append(pairs, map[string]string{"key": k, "value": v})
			}
			if pairs != nil {
				body["kv"] = pairs
			}
			return s.mutate(cmd.Context(), "/api/v1/cycles/annotate", body)
		},
	}
	nm.flags(noteCmd, false)
	noteCmd.Flags().StringVar(&note, "note", "", "the note")
	noteCmd.Flags().StringArrayVar(&kvs, "kv", nil, "a key/value pair, key=value (repeatable; a repeated key is several values)")

	root.AddCommand(start, verb("pause", "Pause a cycle"), verb("resume", "Resume a paused cycle"), verb("stop", "Stop a cycle"), boost, switchCmd, brk, noteCmd)
	return root
}

// mutateRaw is mutate that hands the error back for the caller to decorate.
func (s *state) mutateRaw(ctx context.Context, path string, body map[string]any) error {
	return s.mutate(ctx, path, body)
}

// decorateUnknownType lists the valid cycle types when a start names one the
// configuration does not define.
func (s *state) decorateUnknownType(ctx context.Context, err error, typ string) error {
	if err == nil {
		return nil
	}
	if pe, ok := asProblem(err); ok && pe.Reason() == "unknown_cycle_type" {
		if raw, cerr := s.client().Do(ctx, "GET", "/api/v1/config", nil); cerr == nil {
			var cfg wire.ConfigDoc
			if json.Unmarshal(raw, &cfg) == nil {
				var names []string
				for _, c := range cfg.Cycles {
					names = append(names, c.ID)
				}
				fmt.Fprintf(s.app.Stderr, "pg-task-focus: %q is not a cycle type; the types are: %s\n", typ, strings.Join(names, ", "))
			}
		}
	}
	return err
}

func (s *state) eventsCmd() *cobra.Command {
	root := &cobra.Command{Use: "events", Short: "The editor: list, correct and retract events"}
	var from, to, view string
	var types []string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the logged events, corrected or as first logged",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if from != "" {
				q.Set("from", from)
			}
			if to != "" {
				q.Set("to", to)
			}
			if view != "" {
				q.Set("view", view)
			}
			for _, t := range types {
				q.Add("type", t)
			}
			path := "/api/v1/events"
			if enc := q.Encode(); enc != "" {
				path += "?" + enc
			}
			raw, err := s.client().Do(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			return s.out(raw, func(w io.Writer) error { return renderEvents(w, raw) })
		},
	}
	list.Flags().StringVar(&from, "from", "", "inclusive lower bound, an RFC 3339 instant")
	list.Flags().StringVar(&to, "to", "", "exclusive upper bound, an RFC 3339 instant")
	list.Flags().StringVar(&view, "view", "", "corrected (default) or original")
	list.Flags().StringArrayVar(&types, "type", nil, "an event type to keep (repeatable)")

	var cm mutating
	var sets []string
	var reason string
	correct := &cobra.Command{
		Use:   "correct EVENT-ID --set field=value...",
		Short: "Replace fields of a stored event; a value is JSON when it parses, else text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(sets) == 0 {
				return usagef("name at least one --set field=value")
			}
			fields := map[string]json.RawMessage{}
			for _, kv := range sets {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usagef("--set %q is not field=value", kv)
				}
				if k == "effective_at" {
					iso, err := s.parseAt(v)
					if err != nil {
						return err
					}
					v = fmt.Sprintf("%q", iso)
				}
				if !json.Valid([]byte(v)) {
					b, _ := json.Marshal(v)
					v = string(b)
				}
				fields[k] = json.RawMessage(v)
			}
			body := map[string]any{"fields": fields, "id": s.newID(cm.id)}
			if reason != "" {
				body["reason"] = reason
			}
			return s.mutate(cmd.Context(), "/api/v1/events/"+url.PathEscape(args[0])+"/correct", body)
		},
	}
	cm.flags(correct, false)
	correct.Flags().StringArrayVar(&sets, "set", nil, "field=value (repeatable)")
	correct.Flags().StringVar(&reason, "reason", "", "why")

	var rm mutating
	var batch bool
	var rreason string
	retract := &cobra.Command{
		Use:   "retract ID [--batch]",
		Short: "Take back an event, or a whole batch with --batch (undo)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return s.retract(cmd.Context(), args[0], batch, rreason, rm.id)
		},
	}
	rm.flags(retract, false)
	retract.Flags().BoolVar(&batch, "batch", false, "the id names a batch")
	retract.Flags().StringVar(&rreason, "reason", "", "why")
	root.AddCommand(list, correct, retract)
	return root
}

func (s *state) retract(ctx context.Context, id string, batch bool, reason, given string) error {
	body := map[string]any{"id": s.newID(given)}
	if reason != "" {
		body["reason"] = reason
	}
	kind := "events"
	if batch {
		kind = "batches"
	}
	return s.mutate(ctx, "/api/v1/"+kind+"/"+url.PathEscape(id)+"/retract", body)
}

func (s *state) undoCmd() *cobra.Command {
	var m mutating
	c := &cobra.Command{
		Use:   "undo",
		Short: "Retract the most recent live event or batch (subject to the dependents rule)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := s.client().Do(cmd.Context(), "GET", "/api/v1/events", nil)
			if err != nil {
				return err
			}
			var evs wire.Events
			if err := json.Unmarshal(raw, &evs); err != nil {
				return err
			}
			for i := len(evs.Events) - 1; i >= 0; i-- {
				ev := evs.Events[i]
				if ev.Retracted {
					continue
				}
				var head struct {
					Type string `json:"type"`
					Data struct {
						Batch string `json:"batch"`
					} `json:"data"`
				}
				if err := json.Unmarshal(ev.Event, &head); err != nil {
					return err
				}
				if head.Type == "event.retracted" {
					continue
				}
				if head.Data.Batch != "" {
					return s.retract(cmd.Context(), head.Data.Batch, true, "undo", m.id)
				}
				return s.retract(cmd.Context(), ev.ID, false, "undo", m.id)
			}
			return &exitError{code: ExitRefused, msg: "there is nothing to undo"}
		},
	}
	m.flags(c, false)
	return c
}

func renderEvents(w io.Writer, raw []byte) error {
	var evs wire.Events
	if err := json.Unmarshal(raw, &evs); err != nil {
		return err
	}
	for _, ev := range evs.Events {
		var head struct {
			Type        string `json:"type"`
			EffectiveAt string `json:"effective_at"`
		}
		if err := json.Unmarshal(ev.Event, &head); err != nil {
			return err
		}
		flags := ""
		if len(ev.CorrectedBy) > 0 {
			flags += " [corrected]"
		}
		if ev.Retracted {
			flags += " [retracted]"
		}
		fmt.Fprintf(w, "%s  %s  %s%s\n", ev.ID, head.EffectiveAt, head.Type, flags)
	}
	return nil
}
