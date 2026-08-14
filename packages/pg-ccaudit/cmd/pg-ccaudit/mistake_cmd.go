package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
	"github.com/phillipgreenii/pg-ccaudit/internal/classify"
	"github.com/phillipgreenii/pg-ccaudit/internal/gold"
	"github.com/phillipgreenii/pg-ccaudit/internal/route"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// censusFlags are the flags every mistake-census subcommand shares.
type censusFlags struct {
	db         *string
	since      *string
	until      *string
	churnMin   *int
	retryGap   *int
	max        *int
	signals    *string
	classifier *string
	batch      *int
	goldPath   *string
	format     *string
}

func addCensusFlags(fs *flag.FlagSet) *censusFlags {
	return &censusFlags{
		db:    fs.String("db", "", "database path"),
		since: fs.String("since", "", "include events with ts >= this ISO-8601 prefix"),
		until: fs.String("until", "", "include events with ts < this ISO-8601 prefix (exclusive)"),
		churnMin: fs.Int("churn-min", 0,
			"file-churn threshold N (0 = the query's own default of 5; see `pg-ccaudit queries --verbose file-churn` for the measured rationale)"),
		retryGap: fs.Int("retry-gap", 0, "escaping-retries seq-gap N (0 = the query's default of 40)"),
		max: fs.Int("max", 0,
			"classify at most N candidates (0 = all). A truncated run is reported as truncated: every rate downstream is over the truncated set"),
		signals: fs.String("signals", "",
			"comma-separated Tier 1 signals to include (default all): "+strings.Join(signalNames(), ",")),
		classifier: fs.String("classifier", "baseline",
			"baseline | cli. baseline is the rule the semantic classifier must BEAT, not a fallback"),
		batch:    fs.Int("batch", classify.DefaultBatch, "candidates per model call (cli classifier only)"),
		goldPath: fs.String("gold", "", "gold set path (default $PG_CCAUDIT_GOLD, else beside the index)"),
		format:   fs.String("format", "table", "output format: table, tsv or json"),
	}
}

func signalNames() []string {
	return []string{
		string(candidate.TypedTurn), string(candidate.Interruption), string(candidate.Denial),
		string(candidate.HookRejection), string(candidate.Undo), string(candidate.Churn),
		string(candidate.EscapingRetry), string(candidate.Ack),
	}
}

// openIndex opens the index READ-ONLY. Every census subcommand goes through it:
// the corpus and the index are shared with running sessions, so a census that could
// write is a census that can corrupt what it is measuring.
func openIndex(path string) (*sql.DB, string, error) {
	resolved, err := resolveDB(path)
	if err != nil {
		return nil, "", err
	}
	db, err := store.OpenReadOnly(resolved)
	if err != nil {
		return nil, "", err
	}
	return db, resolved, nil
}

func extract(ctx context.Context, db *sql.DB, cf *censusFlags) (candidate.Set, error) {
	set, err := candidate.Extract(ctx, db, candidate.Options{
		Since:    *cf.since,
		Until:    *cf.until,
		ChurnMin: *cf.churnMin,
		RetryGap: *cf.retryGap,
	})
	if err != nil {
		return set, err
	}
	if *cf.signals != "" {
		want := map[candidate.Signal]bool{}
		for _, s := range strings.Split(*cf.signals, ",") {
			want[candidate.Signal(strings.TrimSpace(s))] = true
		}
		var kept []candidate.Candidate
		for _, c := range set.Candidates {
			if want[c.Signal] {
				kept = append(kept, c)
			}
		}
		set.Candidates = kept
	}
	return set, nil
}

func cmdCandidates(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("candidates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := addCensusFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit candidates — Tier 1: structural mistake candidates (read-only, no model calls)

Runs every Tier 1 canned query and prints one typed candidate per row. Tuned for
RECALL, so most rows are NOT mistakes — deciding that is 'pg-ccaudit classify'.

FLAGS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	db, _, err := openIndex(*cf.db)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	set, err := extract(ctx, db, cf)
	if err != nil {
		return err
	}
	if *cf.format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(set)
	}
	fmt.Fprintf(stdout, "# pg-ccaudit candidates window=%s\n", windowLabel(*cf.since, *cf.until))
	for _, s := range set.Sources {
		fmt.Fprintf(stdout, "# %-18s %-24s v%d rows=%d\n", s.Signal, s.Query, s.Version, s.Rows)
	}
	if empty := set.EmptySignals(); len(empty) > 0 {
		names := make([]string, 0, len(empty))
		for _, e := range empty {
			names = append(names, string(e))
		}
		fmt.Fprintf(stderr, "warning: EMPTY SIGNALS: %s — an empty detector is not evidence that the thing it detects did not happen; read the query's notes\n",
			strings.Join(names, ", "))
	}
	fmt.Fprintf(stdout, "# total candidates: %d\n\n", len(set.Candidates))
	for _, c := range set.Candidates {
		supp := ""
		if c.Supplementary {
			supp = " SUPPLEMENTARY"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\tsidechain=%t\tspan_ms=%d%s\n",
			classify.CandidateID(c), c.Signature, c.TS, c.IsSidechain, c.SpanMS, supp)
		if c.Excerpt != "" {
			fmt.Fprintf(stdout, "\t%s\n", c.Excerpt)
		}
	}
	return nil
}

func newClassifier(cf *censusFlags) (classify.Classifier, error) {
	switch *cf.classifier {
	case "baseline":
		return classify.Baseline{}, nil
	case "cli":
		c := classify.NewCLI()
		c.Batch = *cf.batch
		return c, nil
	default:
		return nil, fmt.Errorf("unknown classifier %q (want baseline or cli)", *cf.classifier)
	}
}

func runClassifier(ctx context.Context, cl classify.Classifier, set candidate.Set, max int) (classify.Result, error) {
	cands := set.Candidates
	truncated := false
	if max > 0 && len(cands) > max {
		cands = cands[:max]
		truncated = true
	}
	res, err := cl.Classify(ctx, cands)
	res.Cost.Truncated = truncated
	return res, err
}

func cmdClassify(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := addCensusFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit classify — Tier 2: decide which candidates are real, and why

Reports the run cost on stderr whether or not it succeeded, because an
unbounded classification pass over a growing corpus is how this stops being run.

  --classifier baseline   the rule the semantic classifier must BEAT:
                          "every typed turn following a tool call is a correction".
                          Zero model calls.
  --classifier cli        the semantic pass, via %s
                          (override with $%s)

FLAGS
`, strings.Join(classify.DefaultCommand(), " "), classify.EnvCommand)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	db, _, err := openIndex(*cf.db)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	set, err := extract(ctx, db, cf)
	if err != nil {
		return err
	}
	cl, err := newClassifier(cf)
	if err != nil {
		return err
	}
	res, cerr := runClassifier(ctx, cl, set, *cf.max)
	fmt.Fprintln(stderr, res.Cost.Line())
	if cerr != nil {
		return cerr
	}
	if *cf.format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	for _, c := range res.Classifications {
		if !c.Class.IsMistake() {
			continue
		}
		fmt.Fprintf(stdout, "%s\t%s\tconfidence=%s\n", classify.CandidateID(c.Candidate), c.Class, c.Confidence)
		if c.What != "" {
			fmt.Fprintf(stdout, "\twhat: %s\n", c.What)
		}
		if c.Prevention != "" {
			fmt.Fprintf(stdout, "\tprevention: %s\n", c.Prevention)
		}
	}
	return nil
}

func cmdEvaluate(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := addCensusFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit evaluate — score a classifier against the gold set, against the baseline

Runs BOTH the chosen classifier and the baseline over the same candidates and
reports precision and recall per class, plus the criterion-4 verdict. A classifier
that does not beat the baseline MUST be reworked, not shipped.

Also reports the measured RECALL of the M-1 'Correction:' marker, so marker
compliance is known rather than assumed.

FLAGS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	goldFile := *cf.goldPath
	if goldFile == "" {
		p, err := gold.DefaultPath()
		if err != nil {
			return err
		}
		goldFile = p
	}
	g, err := gold.Load(goldFile)
	if err != nil {
		return err
	}

	db, _, err := openIndex(*cf.db)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	set, err := extract(ctx, db, cf)
	if err != nil {
		return err
	}
	// Only candidates the gold set actually labels are classified. Scoring is over
	// the intersection either way, so classifying the rest would spend model calls
	// on rows that cannot affect a single reported number.
	labelled := map[string]bool{}
	for _, e := range g.Labelled() {
		labelled[e.ID] = true
	}
	var scored []candidate.Candidate
	for _, c := range set.Candidates {
		if labelled[classify.CandidateID(c)] {
			scored = append(scored, c)
		}
	}
	subset := candidate.Set{Candidates: scored, Since: set.Since, Until: set.Until, Sources: set.Sources}

	cl, err := newClassifier(cf)
	if err != nil {
		return err
	}
	res, cerr := runClassifier(ctx, cl, subset, *cf.max)
	fmt.Fprintln(stderr, res.Cost.Line())
	if cerr != nil {
		return cerr
	}
	base, err := classify.Baseline{}.Classify(ctx, subset.Candidates)
	if err != nil {
		return err
	}

	cmp := classify.Compare(
		classify.Evaluate(cl.Name(), g, res.Classifications, set.Candidates),
		classify.Evaluate("baseline", g, base.Classifications, set.Candidates),
	)
	if *cf.format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cmp)
	}
	var sb strings.Builder
	classify.Render(&sb, cmp)
	fmt.Fprint(stdout, sb.String())
	if !cmp.Beats {
		// Exit non-zero so a gate can enforce criterion 4 rather than a reader having
		// to notice the word FAIL in a wall of numbers.
		return fmt.Errorf("criterion 4 not met: %s", cmp.Reason)
	}
	return nil
}

func cmdReport(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := addCensusFlags(fs)
	noEval := fs.Bool("no-evaluation", false, "omit the Tier 2 evaluation section (skips loading the gold set)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit report — Tier 3: ONE ranked report, mistakes and command failures together

Every finding carries exactly one route. Ranked by
  score = occurrences x (1 + cost_ms/1000) x preventability(route)
with cost MEASURED from transcript timestamps, never estimated.

FLAGS
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	db, _, err := openIndex(*cf.db)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	set, err := extract(ctx, db, cf)
	if err != nil {
		return err
	}
	cl, err := newClassifier(cf)
	if err != nil {
		return err
	}
	res, cerr := runClassifier(ctx, cl, set, *cf.max)
	fmt.Fprintln(stderr, res.Cost.Line())
	if cerr != nil {
		return cerr
	}

	cov, err := route.Coverage(ctx, db)
	if err != nil {
		return err
	}
	failures, err := route.CommandFailures(ctx, db, *cf.since, *cf.until)
	if err != nil {
		return err
	}
	findings := route.Rank(append(route.FromClassifications(res.Classifications), failures...))

	rep := route.Report{
		Since:         *cf.since,
		Until:         *cf.until,
		Coverage:      cov,
		Sources:       set.Sources,
		Empty:         set.EmptySignals(),
		Classifier:    cl.Name(),
		PromptVersion: classify.PromptVersion,
		Cost:          res.Cost,
		Findings:      findings,
	}

	// The gold set is loaded for TWO independent reasons, and the file-channel count
	// is the one that must not be skipped: without it the report quotes a
	// transcript-only correction count with no statement of what it misses.
	goldFile := *cf.goldPath
	if goldFile == "" {
		if p, gerr := gold.DefaultPath(); gerr == nil {
			goldFile = p
		}
	}
	if g, gerr := gold.Load(goldFile); gerr == nil {
		rep.FileChannel, rep.FileChannelPaths = route.FileChannel(g)
		if !*noEval {
			base, berr := classify.Baseline{}.Classify(ctx, set.Candidates)
			if berr == nil {
				cmp := classify.Compare(
					classify.Evaluate(cl.Name(), g, res.Classifications, set.Candidates),
					classify.Evaluate("baseline", g, base.Classifications, set.Candidates),
				)
				rep.Evaluation = &cmp
			}
		}
	} else {
		fmt.Fprintf(stderr, "warning: no gold set at %s (%v) — the file-channel undercount is reported as 0, which is a MISSING MEASUREMENT, not a measured zero. Run `pg-ccaudit gold seed`.\n",
			goldFile, gerr)
	}

	if *cf.format == "json" {
		return route.RenderJSON(stdout, rep)
	}
	return route.Render(stdout, rep)
}

func cmdGold(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("gold", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := addCensusFlags(fs)
	memoryRoot := fs.String("memory-root", "",
		"root holding <project>/memory/feedback_*.md (default ~/.claude/projects)")
	feedback := fs.String("feedback", "", "comma-separated extra file-channel sources, e.g. a workspace FEEDBACK.md")
	sample := fs.Int("sample", 0, "also emit N UNLABELLED candidate rows, stratified across signals, for labelling")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `pg-ccaudit gold <seed|sample|status> — maintain the Tier 2 evaluation set

  seed    record the FILE-CHANNEL corrections (feedback_*.md memories, FEEDBACK.md).
          These are the operator's own labelled corrections AND the measurement of a
          channel a transcript census structurally cannot see.
  sample  add UNLABELLED Tier 1 candidates for a person to label. Existing labels are
          never overwritten.
  status  report the set's size, labelling progress and file-channel count.

The set lives OUTSIDE this repository by default (%s, else beside the index): every
entry quotes a real transcript or the operator's own critique.

FLAGS
`, gold.EnvPath)
		fs.PrintDefaults()
	}
	rest, perr := parseInterspersed(fs, args)
	if perr != nil {
		return perr
	}
	if len(rest) == 0 {
		fs.Usage()
		return errUsage
	}

	goldFile := *cf.goldPath
	if goldFile == "" {
		p, err := gold.DefaultPath()
		if err != nil {
			return err
		}
		goldFile = p
	}
	existing, err := gold.Load(goldFile)
	if err != nil && !os.IsNotExist(unwrapPathErr(err)) {
		if _, statErr := os.Stat(goldFile); statErr == nil {
			return err
		}
	}

	switch rest[0] {
	case "status":
		fmt.Fprintf(stdout, "gold set: %s\n", goldFile)
		fmt.Fprintf(stdout, "  labelled candidates:   %d\n", len(existing.Labelled()))
		fmt.Fprintf(stdout, "  unlabelled candidates: %d\n", len(existing.Unlabelled()))
		fmt.Fprintf(stdout, "  file-channel entries:  %d\n", len(existing.FileChannel()))
		if len(existing.Labelled()) < classify.MinGoldEntries {
			fmt.Fprintf(stdout, "  criterion 4 needs at least %d labelled candidates\n", classify.MinGoldEntries)
		}
		return nil

	case "seed":
		root := *memoryRoot
		if root == "" {
			r, rerr := store.DefaultTranscriptRoot()
			if rerr != nil {
				return rerr
			}
			root = r
		}
		var extra []string
		if *feedback != "" {
			extra = strings.Split(*feedback, ",")
		}
		files, ferr := gold.DiscoverFeedback(root, extra)
		if ferr != nil {
			return ferr
		}
		merged := gold.Merge(existing, gold.FromFeedback(files))
		if err := gold.Save(goldFile, merged); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "seeded %d file-channel correction(s) into %s\n", len(files), goldFile)
		for _, f := range files {
			fmt.Fprintf(stdout, "  %s (%d lines) — %s\n", f.Path, f.Lines, f.Title)
		}
		return nil

	case "sample":
		if *sample <= 0 {
			return fmt.Errorf("gold sample needs --sample N")
		}
		db, _, derr := openIndex(*cf.db)
		if derr != nil {
			return derr
		}
		defer func() { _ = db.Close() }()
		set, eerr := extract(ctx, db, cf)
		if eerr != nil {
			return eerr
		}
		merged := gold.Merge(existing, sampleSet(set, *sample))
		if err := gold.Save(goldFile, merged); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "gold set now %d labelled / %d unlabelled / %d file-channel: %s\n",
			len(merged.Labelled()), len(merged.Unlabelled()), len(merged.FileChannel()), goldFile)
		fmt.Fprintf(stdout, "label each unlabelled row's \"class\" field with one of: %s\n", classNames())
		return nil

	default:
		fs.Usage()
		return errUsage
	}
}

func classNames() string {
	names := make([]string, 0, len(classify.Classes))
	for _, c := range classify.Classes {
		names = append(names, string(c))
	}
	return strings.Join(names, ", ")
}

// sampleSet takes a STRATIFIED sample: round-robin across signals rather than the
// first N candidates.
//
// The first N would be almost entirely typed turns — the highest-volume signal —
// so every rarer class would have a support of zero and its precision and recall
// would be reported as 0.000, which reads as "the classifier is bad at it" when it
// means "nothing was measured".
func sampleSet(set candidate.Set, n int) gold.Set {
	bySignal := map[candidate.Signal][]candidate.Candidate{}
	var order []candidate.Signal
	for _, c := range set.Candidates {
		if _, ok := bySignal[c.Signal]; !ok {
			order = append(order, c.Signal)
		}
		bySignal[c.Signal] = append(bySignal[c.Signal], c)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	var out gold.Set
	for i := 0; len(out.Entries) < n; i++ {
		progressed := false
		for _, s := range order {
			if i >= len(bySignal[s]) {
				continue
			}
			progressed = true
			c := bySignal[s][i]
			out.Entries = append(out.Entries, gold.Entry{
				ID:      classify.CandidateID(c),
				Source:  gold.SourceHandLabelled,
				Signal:  string(c.Signal),
				Path:    c.Path,
				Seq:     c.Seq,
				Class:   "",
				Excerpt: c.Excerpt,
				Note:    c.Signature,
			})
			if len(out.Entries) >= n {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

func unwrapPathErr(err error) error {
	for err != nil {
		if pe, ok := err.(*os.PathError); ok {
			return pe
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
	return err
}

func windowLabel(since, until string) string {
	if since == "" && until == "" {
		return "all"
	}
	if since == "" {
		since = "-inf"
	}
	if until == "" {
		until = "+inf"
	}
	return "[" + since + "," + until + ")"
}
