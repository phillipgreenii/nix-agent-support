// pg-pr-backfill is the PR lane's backfill driver: it lists the backfill set
// (enumerate), runs the composed command pg-pr-change over it in plan or apply mode
// (run), marks the open-anchor PRs watched and checks the daemon's watched count
// (mark-watched), prints the plan summary or the apply residual check (summary),
// writes the flip marker (flip-marker) and lists every write since the flip (writes). The logic
// is in internal/backfill; this file is argument parsing and process wiring only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
	"github.com/phillipgreenii/pg-decider/internal/config"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version").
var Version = "dev"

// Exit codes beyond the ones of internal/backfill (0, 10, 11, 12, 20 and 64): none
// is 1 or 9.
const (
	exitEnumerateFailed = 30
	exitMarkerFailed    = 31
	exitCommentFailed   = 14
	exitNothingToList   = 20
)

const usage = `usage: pg-pr-backfill <command> [flags]

The PR lane backfill driver (docs/behavior/pg-decider/backfill.md).

  enumerate    list the backfill set: the tracker's open anchors plus the open PRs
               of the deployment's queries; persist it; fail closed when the
               query-name file is absent or lists no query, or the set is empty
  mark-watched mark every PR with an open anchor as watched in the daemon (a read
               with --watch), refresh them (hydrate only), and check that the
               daemon's watched count equals the tracker's open-anchor count
               (non-zero); anchors that name no PR are listed, anchors of a merged
               or closed PR are not marked; --from-set reads the persisted set
  run          run pg-pr-change over the set, paced; --mode plan|apply
  summary      print the plan summary (--mode plan, default) or the apply residual
               check (--mode apply) from the run record
  flip-marker  write the flip marker into the run record and comment on the epic bead
  writes       list every write since the flip marker, by timestamp

Common flags: --config FILE (env ` + backfill.ConfigEnv + `), --run-record FILE, --set FILE.
"run" and "mark-watched" end with one last line starting DONE, FLOOR or FAILED; exit
0, 10, 11 (FAILED: run it again) or 12 (FAILED and stopped at once: for "run", go to
the rollback). "mark-watched" also exits 20 when every PR was marked but the count
check does not hold. 64 is a usage error. None of the exit codes is 1 or 9.
`

type common struct {
	cfgPath   string
	runRecord string
	setFile   string
}

func newFlags(name string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet("pg-pr-backfill "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.cfgPath, "config", "", "")
	fs.StringVar(&c.runRecord, "run-record", "", "")
	fs.StringVar(&c.setFile, "set", "", "")
	return fs
}

// flagErr is why the arguments were refused: the parse error, or the arguments left over.
func flagErr(err error, fs *flag.FlagSet) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected argument(s) %v", fs.Args())
}

func usageErr(stderr io.Writer, name string, err error) int {
	fmt.Fprintf(stderr, "pg-pr-backfill %s: usage: %v: run pg-pr-backfill --help\n", name, err)
	return backfill.ExitUsage
}

// load reads the configuration, applies the flag overrides and the defaults, and
// takes the tracker backend and directory from the decider's configuration when
// neither the file nor a flag names them.
func (c *common) load() (*backfill.Config, error) {
	cfg, err := backfill.LoadConfig(c.cfgPath)
	if err != nil {
		return nil, err
	}
	if c.runRecord != "" {
		cfg.RunRecord = c.runRecord
	}
	if c.setFile != "" {
		cfg.SetFile = c.setFile
	}
	if cfg.Backend == "" || cfg.BeadsDir == "" {
		dc, err := config.Load()
		if err != nil {
			return nil, err
		}
		if cfg.Backend == "" {
			cfg.Backend = dc.AgentTrackerBackend
		}
		if cfg.BeadsDir == "" {
			cfg.BeadsDir = dc.BeadsDir
		}
	}
	cfg.WithDefaults()
	return cfg, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return backfill.ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version", "version":
		fmt.Fprintln(stdout, "pg-pr-backfill", Version)
		return 0
	case "enumerate":
		return cmdEnumerate(ctx, rest, stdout, stderr)
	case "run":
		return cmdRun(ctx, rest, stdout, stderr)
	case "mark-watched":
		return cmdMarkWatched(ctx, rest, stdout, stderr)
	case "summary":
		return cmdSummary(rest, stdout, stderr)
	case "flip-marker":
		return cmdFlipMarker(ctx, rest, stdout, stderr)
	case "writes":
		return cmdWrites(rest, stdout, stderr)
	}
	return usageErr(stderr, "", fmt.Errorf("unknown command %q", cmd))
}

func cmdEnumerate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var c common
	fs := newFlags("enumerate", &c)
	queries := fs.String("queries-file", "", "")
	anchors := fs.String("anchors-query", "", "")
	backend := fs.String("backend", "", "")
	beadsDir := fs.String("beads-dir", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(stderr, "enumerate", flagErr(err, fs))
	}
	cfg, err := c.load()
	if err != nil {
		return usageErr(stderr, "enumerate", err)
	}
	pick := func(flagV, cfgV string) string {
		if flagV != "" {
			return flagV
		}
		return cfgV
	}
	set, err := backfill.Enumerate(ctx, backfill.EnumerateOptions{
		QueriesFile:  pick(*queries, cfg.QueriesFile),
		AnchorsQuery: pick(*anchors, cfg.AnchorsQuery),
		Backend:      pick(*backend, cfg.Backend),
		BeadsDir:     pick(*beadsDir, cfg.BeadsDir),
		Connector:    cfg.Connector,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pg-pr-backfill enumerate: FAILED: %v\n", err)
		return exitEnumerateFailed
	}
	if err := set.Save(cfg.SetFile); err != nil {
		fmt.Fprintf(stderr, "pg-pr-backfill enumerate: cannot save the set to %s: %v\n", cfg.SetFile, err)
		return exitEnumerateFailed
	}
	for _, e := range set.Entries {
		fmt.Fprintf(stdout, "%s state=%s from=%s\n", e.PR, e.State, strings.Join(e.From, "+"))
	}
	for _, u := range set.Unparseable {
		fmt.Fprintf(stdout, "UNPARSEABLE %s %s %q: %s\n", u.From, u.ID, u.Title, u.Reason)
	}
	fmt.Fprintf(stdout, "enumerated=%d open_anchors=%d open_anchor_prs=%d query_prs=%d unparseable=%d set=%s\n",
		len(set.Entries), set.OpenAnchorCount, set.OpenAnchorPRCount, set.QueryPRCount, len(set.Unparseable), cfg.SetFile)
	return 0
}

// parseLimits reads --max-creates (kind=n,kind=n) and --limits-file (a JSON object).
func parseLimits(inline, file string) (map[string]int, error) {
	if inline == "" && file == "" {
		return nil, nil
	}
	out := map[string]int{}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("the limits file %s is not a JSON object of kind to number: %w", file, err)
		}
	}
	for _, kv := range strings.Split(inline, ",") {
		if kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.Atoi(v)
		if !ok || k == "" || err != nil || n < 0 {
			return nil, fmt.Errorf("--max-creates entry %q is not kind=<non-negative number>", kv)
		}
		out[k] = n
	}
	for k, n := range out {
		if n < 0 {
			return nil, fmt.Errorf("limit of kind %s is negative", k)
		}
	}
	return out, nil
}

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var c common
	fs := newFlags("run", &c)
	mode := fs.String("mode", "", "")
	pace := fs.Duration("pace", -1, "")
	floor := fs.Int("floor", 0, "")
	rateSource := fs.String("rate-source", "", "")
	rateLog := fs.String("rate-log", "", "")
	metricsURL := fs.String("metrics-url", "", "")
	lockDir := fs.String("lock-dir", "", "")
	lockWait := fs.String("lock-wait", "", "")
	changeCmd := fs.String("change-cmd", "", "")
	maxCreates := fs.String("max-creates", "", "")
	limitsFile := fs.String("limits-file", "", "")
	noLimits := fs.Bool("no-create-limits", false, "")
	newPass := fs.Bool("new-pass", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(stderr, "run", flagErr(err, fs))
	}
	cfg, err := c.load()
	if err != nil {
		return usageErr(stderr, "run", err)
	}
	if *rateSource != "" {
		cfg.RateSource = *rateSource
	}
	if *rateLog != "" {
		cfg.RateLog = *rateLog
	}
	if *metricsURL != "" {
		cfg.MetricsURL = *metricsURL
	}
	if *floor != 0 {
		cfg.Floor = *floor
	}
	if *changeCmd != "" {
		cfg.ChangeCmd = *changeCmd
	}
	p := cfg.PaceDuration()
	if *pace >= 0 {
		p = *pace
	}
	rate, err := cfg.RateSourceFor()
	if err != nil {
		return usageErr(stderr, "run", err)
	}
	limits, err := parseLimits(*maxCreates, *limitsFile)
	if err != nil {
		return usageErr(stderr, "run", err)
	}
	set, err := backfill.LoadSet(cfg.SetFile)
	if err != nil {
		return usageErr(stderr, "run", err)
	}
	var changeArgs []string
	if *lockDir != "" {
		changeArgs = append(changeArgs, "--lock-dir", *lockDir)
	}
	if *lockWait != "" {
		changeArgs = append(changeArgs, "--lock-wait", *lockWait)
	}
	res, err := backfill.Run(ctx, backfill.RunOptions{
		Mode: *mode, Set: set, RunRecord: cfg.RunRecord,
		ChangeCmd: cfg.ChangeCmd, ChangeArgs: changeArgs,
		Pace: p, Floor: cfg.Floor, Rate: rate,
		Limits: limits, NoLimits: *noLimits, NewPass: *newPass,
		Progress: stdout,
	})
	if err != nil {
		return usageErr(stderr, "run", err)
	}
	fmt.Fprintln(stdout, res.Line)
	return res.Code
}

// cmdMarkWatched is the marking step of the cutover (design 6.3): mark every PR
// that has an open anchor as watched, refresh them (hydrate only), and check the
// daemon's watched count against the tracker's open-anchor count. It enumerates
// afresh (read-only, the persisted set is left alone) unless --from-set says to
// read the persisted one. Its last line starts with DONE, FLOOR or FAILED; exit 0,
// 10, 11 (run it again), 12 (stopped at once), 20 (every PR done but the count
// check does not hold) or 30 (the enumeration failed closed).
func cmdMarkWatched(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var c common
	fs := newFlags("mark-watched", &c)
	fromSet := fs.Bool("from-set", false, "")
	queries := fs.String("queries-file", "", "")
	anchors := fs.String("anchors-query", "", "")
	backend := fs.String("backend", "", "")
	beadsDir := fs.String("beads-dir", "", "")
	prBackend := fs.String("pr-backend", "", "")
	deskCmd := fs.String("desk-cmd", "", "")
	pace := fs.Duration("pace", -1, "")
	floor := fs.Int("floor", 0, "")
	rateSource := fs.String("rate-source", "", "")
	rateLog := fs.String("rate-log", "", "")
	metricsURL := fs.String("metrics-url", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(stderr, "mark-watched", flagErr(err, fs))
	}
	cfg, err := c.load()
	if err != nil {
		return usageErr(stderr, "mark-watched", err)
	}
	pick := func(flagV, cfgV string) string {
		if flagV != "" {
			return flagV
		}
		return cfgV
	}
	if *rateSource != "" {
		cfg.RateSource = *rateSource
	}
	if *rateLog != "" {
		cfg.RateLog = *rateLog
	}
	if *metricsURL != "" {
		cfg.MetricsURL = *metricsURL
	}
	if *floor != 0 {
		cfg.Floor = *floor
	}
	p := cfg.PaceDuration()
	if *pace >= 0 {
		p = *pace
	}
	rate, err := cfg.RateSourceFor()
	if err != nil {
		return usageErr(stderr, "mark-watched", err)
	}
	var set *backfill.Set
	if *fromSet {
		set, err = backfill.LoadSet(cfg.SetFile)
		if err != nil {
			return usageErr(stderr, "mark-watched", err)
		}
	} else {
		set, err = backfill.Enumerate(ctx, backfill.EnumerateOptions{
			QueriesFile:  pick(*queries, cfg.QueriesFile),
			AnchorsQuery: pick(*anchors, cfg.AnchorsQuery),
			Backend:      pick(*backend, cfg.Backend),
			BeadsDir:     pick(*beadsDir, cfg.BeadsDir),
			Connector:    cfg.Connector,
		})
		if err != nil {
			fmt.Fprintf(stderr, "pg-pr-backfill mark-watched: enumeration FAILED: %v\n", err)
			return exitEnumerateFailed
		}
	}
	res, err := backfill.MarkWatched(ctx, backfill.MarkOptions{
		Set:       set,
		Connector: cfg.Connector,
		Desk:      pick(*deskCmd, cfg.DeskCmd),
		PRBackend: pick(*prBackend, cfg.PRBackend),
		Pace:      p, Floor: cfg.Floor, Rate: rate,
		Progress: stdout,
	})
	if err != nil {
		return usageErr(stderr, "mark-watched", err)
	}
	fmt.Fprintln(stdout, res.Line)
	return res.Code
}

func cmdSummary(args []string, stdout, stderr io.Writer) int {
	var c common
	fs := newFlags("summary", &c)
	mode := fs.String("mode", backfill.ModePlan, "")
	maxAge := fs.Duration("max-plan-age", -1, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(stderr, "summary", flagErr(err, fs))
	}
	if *mode != backfill.ModePlan && *mode != backfill.ModeApply {
		return usageErr(stderr, "summary", fmt.Errorf("--mode must be plan or apply, got %q", *mode))
	}
	cfg, err := c.load()
	if err != nil {
		return usageErr(stderr, "summary", err)
	}
	set, err := backfill.LoadSet(cfg.SetFile)
	if err != nil {
		return usageErr(stderr, "summary", err)
	}
	lg, err := backfill.ReadLog(cfg.RunRecord)
	if err != nil {
		fmt.Fprintf(stderr, "pg-pr-backfill summary: %v\n", err)
		return backfill.ExitSummaryFailed
	}
	age := cfg.MaxPlanAgeDuration()
	if *maxAge >= 0 {
		age = *maxAge
	}
	s := backfill.Summarize(lg, set, *mode, age, time.Now())
	s.Print(stdout)
	if !s.OK() {
		return backfill.ExitSummaryFailed
	}
	return 0
}

func cmdFlipMarker(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var c common
	fs := newFlags("flip-marker", &c)
	at := fs.String("time", "", "")
	epic := fs.String("epic-bead", "", "")
	epicBackend := fs.String("epic-backend", "", "")
	epicDir := fs.String("epic-beads-dir", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(stderr, "flip-marker", flagErr(err, fs))
	}
	cfg, err := c.load()
	if err != nil {
		return usageErr(stderr, "flip-marker", err)
	}
	var t time.Time
	if *at != "" {
		t, err = time.Parse(time.RFC3339, *at)
		if err != nil {
			return usageErr(stderr, "flip-marker", fmt.Errorf("--time %q is not an RFC 3339 time", *at))
		}
	}
	m, err := backfill.WriteFlipMarker(ctx, backfill.FlipMarkerOptions{
		RunRecord: cfg.RunRecord, Time: t, EpicBead: *epic, Backend: *epicBackend, BeadsDir: *epicDir, Connector: cfg.Connector,
	})
	switch {
	case err == nil:
	case strings.Contains(err.Error(), backfill.ErrEpicComment.Error()):
		fmt.Fprintf(stdout, "flip-marker recorded: mode=%s time=%s written=%s run-record=%s\n", m.Mode, m.Time, m.At, cfg.RunRecord)
		fmt.Fprintf(stderr, "pg-pr-backfill flip-marker: %v\n", err)
		return exitCommentFailed
	default:
		fmt.Fprintf(stderr, "pg-pr-backfill flip-marker: %v\n", err)
		return exitMarkerFailed
	}
	fmt.Fprintf(stdout, "flip-marker recorded: mode=%s time=%s written=%s run-record=%s\n", m.Mode, m.Time, m.At, cfg.RunRecord)
	return 0
}

func cmdWrites(args []string, stdout, stderr io.Writer) int {
	var c common
	fs := newFlags("writes", &c)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(stderr, "writes", flagErr(err, fs))
	}
	cfg, err := c.load()
	if err != nil {
		return usageErr(stderr, "writes", err)
	}
	lg, err := backfill.ReadLog(cfg.RunRecord)
	if err != nil {
		fmt.Fprintf(stderr, "pg-pr-backfill writes: %v\n", err)
		return exitMarkerFailed
	}
	if _, ok := backfill.PrintWrites(stdout, lg); !ok {
		return exitNothingToList
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
