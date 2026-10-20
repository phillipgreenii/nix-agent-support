package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/report"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/runner"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/telemetry"
)

func init() { registerCommand(newReportCmd) }

const (
	// outRemovedText is the migration text of the retired --out flag.
	outRemovedText = "--out was removed; use 'work-report report --print > FILE' for a single text report, or read the files in the output directory."
	// printLimit is the largest report --print writes to stdout.
	printLimit = 8 << 20
	// baselineFile is the file the built-in baseline generator writes.
	baselineFile = "baseline.md"
	// adhocDir is the default output parent of a --generator run.
	adhocDir = "$XDG_STATE_HOME/work-report/reports/adhoc"
)

type reportOutcomeJSON struct {
	Outcome string `json:"outcome"`
}

// fallbackJSON is what --output json prints when the baseline preset fell back
// to stdout: there is no manifest.json to print, so the report itself rides in
// content.
type fallbackJSON struct {
	Status         string   `json:"status"`
	OutcomeKind    string   `json:"outcome_kind"`
	StdoutFallback bool     `json:"stdout_fallback"`
	Warnings       []string `json:"warnings"`
	Content        string   `json:"content"`
}

func rfcOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// buildFooter gives one line per source named by the store's status or by a
// rendered entry. Counts come from the entries of THIS report, never from the
// all-time entry count.
func buildFooter(d store.StatusData, entries []store.Entry) []report.SourceFooter {
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.SourceID]++
	}
	lastOK := map[string]time.Time{}
	lastOutcome := map[string]string{}
	for _, s := range d.Sources {
		if _, ok := counts[s.Source]; !ok {
			counts[s.Source] = 0
		}
		lastOK[s.Source] = s.LastSuccessAt
		lastOutcome[s.Source] = s.LastStatus
	}
	names := make([]string, 0, len(counts))
	for s := range counts {
		names = append(names, s)
	}
	sort.Strings(names)
	out := make([]report.SourceFooter, 0, len(names))
	for _, s := range names {
		out = append(out, report.SourceFooter{Source: s, Count: counts[s], LastSuccessAt: lastOK[s], LastOutcome: lastOutcome[s]})
	}
	return out
}

// degradedWarnings is one warning for every source whose last pull degraded,
// whether or not it has entries in the range.
func degradedWarnings(footer []report.SourceFooter, d store.StatusData) []string {
	reasons := map[string]string{}
	for _, s := range d.Sources {
		reasons[s.Source] = s.LastReason
	}
	var out []string
	for _, f := range footer {
		if f.LastOutcome != "degraded" {
			continue
		}
		w := fmt.Sprintf("source %s: its last pull degraded", f.Source)
		if r := reasons[f.Source]; r != "" {
			w += ": " + r
		}
		out = append(out, w)
	}
	return out
}

func toFooterItems(in []report.SourceFooter) []runner.FooterItem {
	out := make([]runner.FooterItem, 0, len(in))
	for _, f := range in {
		out = append(out, runner.FooterItem{SourceID: f.Source, Count: f.Count, LastSuccessAt: f.LastSuccessAt, LastOutcome: f.LastOutcome})
	}
	return out
}

// sizeText renders a byte count the way the example output shows it: 4.1 KiB.
func sizeText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// newRunID makes a run id for a run that has no run directory (an outcome the
// command layer builds, or the baseline stdout fallback): it exists for the
// log only.
func newRunID(t time.Time) string {
	var b [2]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		n := t.UnixNano()
		b[0], b[1] = byte(n>>8), byte(n)
	}
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// reportOpts holds the flags of the report verb.
type reportOpts struct {
	rangeSpec, generator, outputDir string
	labels, sources, types          []string
	print                           bool
	// kind and out are the retired flags; they only fail.
	kind, out string
}

func newReportCmd() *cobra.Command {
	var o reportOpts
	cmd := &cobra.Command{
		Use:   "report [<preset>]",
		Short: "Run a report generator over a range of entries",
		Long: "Runs a report preset (default baseline, a plain chronological markdown view that " +
			"needs no external generator) over a range: the matching entries go to the preset's " +
			"generator, which writes its files into a new run directory beside manifest.json. " +
			"Define presets under reports: and generators under generators: in the config; " +
			"'work-report config show' lists them. A flag replaces the preset field of the same " +
			"name (--range, --label, --source, --type, --output-dir); narrowing flags compose AND " +
			"across dimensions and OR within one. --generator NAME runs a registered generator " +
			"ad hoc with an empty config and no preset. --print writes the single text report to " +
			"stdout. A run that cannot produce a report prints an outcome naming the reason and " +
			"the kept run directory and exits 3; a usage error exits 2. Range forms: " + rangespec.Forms + ".",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return doReport(cmd, &o, args) },
	}
	f := cmd.Flags()
	f.StringVar(&o.rangeSpec, "range", "today", "range spec: "+rangespec.Forms+" (default: the preset's range, else today)")
	f.StringArrayVar(&o.labels, "label", nil, "only entries carrying this label (repeatable; OR within; replaces the preset's labels)")
	f.StringArrayVar(&o.sources, "source", nil, "only entries from this source (repeatable; OR within; replaces the preset's sources)")
	f.StringArrayVar(&o.types, "type", nil, "only entries of this type (repeatable; OR within; replaces the preset's types)")
	f.StringVar(&o.generator, "generator", "", "run this registered generator ad hoc, with an empty config and no preset")
	f.StringVar(&o.outputDir, "output-dir", "", "parent directory of the run directory (replaces the preset's output.dir)")
	f.BoolVar(&o.print, "print", false, "write the single text report to stdout (summary, warnings and files go to stderr)")
	f.StringVar(&o.kind, "kind", "", "removed; fails with migration guidance")
	f.StringVar(&o.out, "out", "", "removed; fails with migration guidance")
	_ = f.MarkHidden("kind")
	_ = f.MarkHidden("out")
	return cmd
}

// reportRun is the state of one report invocation.
type reportRun struct {
	cmd     *cobra.Command
	format  string
	print   bool
	started time.Time
	log     *telemetry.Logger

	preset  string // "" for an ad hoc run
	adhoc   bool
	spec    string
	zone    string
	rng     rangespec.Range
	nar     runner.Narrowing
	overrid []string
	parent  string
	notes   []string
}

func (r *reportRun) isBaselinePreset() bool { return !r.adhoc && r.preset == config.BaselineName }

func doReport(cmd *cobra.Command, o *reportOpts, args []string) error {
	flags := cmd.Flags()
	if flags.Changed("kind") {
		return usageErrorf("%s", config.KindsMigrationText)
	}
	if flags.Changed("out") {
		return usageErrorf("%s", outRemovedText)
	}
	cfgPath, rawOut, storeFlag := globalFlags(cmd)
	format, err := outputFormat(rawOut)
	if err != nil {
		return asUsage(err)
	}
	if o.generator != "" && len(args) > 0 {
		return usageErrorf("--generator %s and the preset %q are mutually exclusive: give one", o.generator, args[0])
	}
	if o.print && format == "json" {
		return usageErrorf("--print cannot be combined with --output json")
	}
	if flags.Changed("output-dir") && o.outputDir == "" {
		return usageErrorf("--output-dir must not be empty")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	loc, err := cfg.Location()
	if err != nil {
		return err
	}
	r := &reportRun{cmd: cmd, format: format, print: o.print, started: time.Now(), log: telemetry.New(""), adhoc: o.generator != "", zone: loc.String()}
	set := config.Presets(cfg)

	// Resolve the preset. The built-in baseline is always present, so a broken
	// generators or reports section never stops `report` or `report baseline`.
	var (
		preset config.Preset
		gen    config.Generator
		broken []string
	)
	if !r.adhoc {
		r.preset = config.BaselineName
		if len(args) == 1 {
			r.preset = args[0]
		}
		if p, ok := set.Presets[r.preset]; ok {
			preset, gen = p, set.Generators[p.Generator]
		} else {
			for _, pr := range set.Problems {
				if pr.Preset == r.preset {
					broken = append(broken, pr.Kind+": "+pr.Message)
				}
			}
			if len(broken) == 0 {
				return usageErrorf("unknown preset %q; known presets: %s (run 'work-report config show' for their definitions)",
					r.preset, strings.Join(sortedKeys(set.Presets), ", "))
			}
		}
	}

	// Range, narrowing and output parent: a flag replaces the preset field of
	// the same name, detected with Changed, never by comparing with a default.
	r.spec = o.rangeSpec
	if !flags.Changed("range") && preset.Range != "" {
		r.spec = preset.Range
	}
	if flags.Changed("range") {
		r.overrid = append(r.overrid, "--range")
	}
	r.rng, err = rangespec.Resolve(r.spec, r.started, loc)
	if err != nil {
		return asUsage(err)
	}
	r.nar = runner.Narrowing{Labels: preset.Narrowing.Labels, Sources: preset.Narrowing.Sources, Types: preset.Narrowing.Types}
	replace := func(flag string, vals []string, cur *[]string) {
		if !flags.Changed(flag) {
			return
		}
		r.overrid = append(r.overrid, "--"+flag)
		note := fmt.Sprintf("narrowing: %ss=%s", flag, strings.Join(vals, ","))
		if len(*cur) > 0 {
			note += fmt.Sprintf(" (replaces preset %ss=%s)", flag, strings.Join(*cur, ","))
		}
		r.notes = append(r.notes, note)
		*cur = vals
	}
	replace("label", o.labels, &r.nar.Labels)
	replace("source", o.sources, &r.nar.Sources)
	replace("type", o.types, &r.nar.Types)

	r.parent = preset.OutputDir
	if r.adhoc || r.parent == "" {
		if r.parent, err = config.ExpandOutputDir(adhocDir); err != nil {
			return err
		}
	}
	if flags.Changed("output-dir") {
		r.overrid = append(r.overrid, "--output-dir")
		if r.parent, err = filepath.Abs(o.outputDir); err != nil {
			return err
		}
	}

	// An outcome the command layer builds: no run directory exists, so there is
	// no manifest.json, and the runner is never called.
	if r.adhoc {
		g, ok := set.Generators[o.generator]
		if !ok {
			reason := fmt.Sprintf("no generator is registered under %q (registered: %s)", o.generator, strings.Join(sortedKeys(set.Generators), ", "))
			for _, pr := range set.Problems {
				if pr.Generator == o.generator {
					reason = fmt.Sprintf("generator %q is not usable: %s", o.generator, pr.Message)
				}
			}
			return r.preRunOutcome(o.generator, runner.KindNotFound, reason)
		}
		gen = g
	}
	if len(broken) > 0 {
		return r.preRunOutcome("", runner.KindConfigInvalid,
			fmt.Sprintf("preset %q is invalid: %s", r.preset, strings.Join(broken, "; ")))
	}

	st, err := store.Open(config.StorePath(cfg, storeFlag))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := cmd.Context()
	entries, err := query.Run(ctx, st, store.Filter{
		Since: r.rng.Since, Before: r.rng.Before, OpenStart: r.rng.OpenStart,
		Types: r.nar.Types, Labels: r.nar.Labels, Sources: r.nar.Sources,
	})
	if err != nil {
		return err
	}
	status, err := st.Status(ctx)
	if err != nil {
		return err
	}
	report.SortBaselineOrder(entries)
	footer := buildFooter(status, entries)
	breq := report.Request{
		Range:     r.rng,
		Narrowing: report.Narrowing{Labels: r.nar.Labels, Sources: r.nar.Sources, Types: r.nar.Types},
		Entries:   entries, Footer: footer,
	}

	req := runner.Request{
		Generator:     gen,
		Preset:        r.preset,
		Overrides:     r.overrid,
		ExtraWarnings: degradedWarnings(footer, status),
		Range:         runner.Range{Range: r.rng, Spec: r.spec, Zone: r.zone},
		Narrowing:     r.nar,
		Entries:       entries,
		Footer:        toFooterItems(footer),
		Config:        preset.Config,
		OutputParent:  r.parent,
	}
	if gen.Builtin {
		req.Builtin = baselineBuiltin(breq)
	}
	if slices.Contains(gen.Inputs, config.BaselineName) {
		req.Baseline = report.Baseline(breq)
	}

	out := runner.New(Version).Run(ctx, req)

	// INV-BASELINE-1: a plain view of the data is always available, so the
	// baseline preset prints to stdout when its output parent is unusable. No
	// run directory, manifest.json or latest update exists in that case.
	if r.isBaselinePreset() && out.Record.OutcomeKind == runner.KindOutputDirUnwritable && out.RunDir == "" {
		return r.stdoutFallback(out, report.Baseline(breq))
	}
	if !out.OK() {
		return r.failure(out)
	}
	return r.success(out)
}

// baselineBuiltin is the in-process baseline generator: it writes
// baseline.md and returns the manifest an external generator would print.
func baselineBuiltin(breq report.Request) runner.BuiltinFunc {
	return func(_ context.Context, _ runner.Envelope, dir string) (runner.GeneratorManifest, error) {
		if err := os.WriteFile(filepath.Join(dir, baselineFile), []byte(report.Baseline(breq)), 0o600); err != nil {
			return runner.GeneratorManifest{}, err
		}
		n := len(breq.Entries)
		return runner.GeneratorManifest{
			Files:   []runner.ManifestFile{{Path: baselineFile, ContentType: "text/markdown", Title: "Work report"}},
			Summary: fmt.Sprintf("Rendered %d %s into 1 file.", n, plural(n, "entry", "entries")),
		}, nil
	}
}

// preRunOutcome builds the failed outcome of a request that never reaches the
// runner (an unregistered --generator name, an invalid preset).
func (r *reportRun) preRunOutcome(generator, kind, reason string) error {
	rec := runner.Record{
		Status: runner.StatusFailed, OutcomeKind: kind, Reason: reason,
		ToolVersion: Version, ContractVersion: runner.ContractVersion,
		RunID:     newRunID(r.started),
		StartedAt: r.started.UTC().Format(time.RFC3339), EndedAt: time.Now().UTC().Format(time.RFC3339),
		TotalMS:   time.Since(r.started).Milliseconds(),
		Generator: runner.RecordGenerator{Name: generator},
		Range:     recordRange(r.rng, r.spec, r.zone),
		Narrowing: r.nar, Overrides: r.overrid,
	}
	if !r.adhoc {
		p := r.preset
		rec.Preset = &p
	}
	return r.failure(runner.Outcome{Record: rec})
}

func recordRange(rng rangespec.Range, spec, zone string) runner.EnvelopeRange {
	out := runner.EnvelopeRange{Spec: spec, Before: rng.Before.Format(time.RFC3339), OpenStart: rng.OpenStart, Zone: zone}
	if !rng.OpenStart {
		s := rng.Since.Format(time.RFC3339)
		out.Since = &s
	}
	return out
}

// writeLog appends the run's line to report.jsonl. A failure to log never
// fails the run; it is noted on stderr.
func (r *reportRun) writeLog(out runner.Outcome) {
	rec := out.Record
	l := telemetry.ReportLog{
		TS: r.started.UTC(), RunID: rec.RunID, Preset: rec.Preset, Generator: rec.Generator.Name,
		Status: rec.Status, OutcomeKind: rec.OutcomeKind, GeneratorExit: rec.GeneratorExit, Reason: rec.Reason,
		Range:     telemetry.ReportRange{Spec: rec.Range.Spec, Since: rec.Range.Since, Before: rec.Range.Before},
		Narrowing: telemetry.ReportNarrowing{Labels: rec.Narrowing.Labels, Sources: rec.Narrowing.Sources, Types: rec.Narrowing.Types},
		Overrides: rec.Overrides, EntryCount: rec.EntryCount, FileCount: len(rec.Files),
		ToolVersion: rec.ToolVersion, GeneratorMS: rec.GeneratorMS, TotalMS: rec.TotalMS,
	}
	if out.RunDir != "" {
		d := out.RunDir
		l.OutputDir = &d
	}
	if rec.Status == runner.StatusFailed {
		tail := out.Stderr
		if len(tail) > telemetry.StderrTailKeep {
			tail = tail[len(tail)-telemetry.StderrTailKeep:]
		}
		l.StderrTail = &tail
	}
	if err := r.log.LogReport(l); err != nil {
		fmt.Fprintf(r.cmd.ErrOrStderr(), "note: the report log could not be written: %v\n", err)
	}
}

func (r *reportRun) noteNotices(out runner.Outcome) {
	for _, n := range out.Notices {
		fmt.Fprintf(r.cmd.ErrOrStderr(), "note: %s\n", n)
	}
}

// failure prints an outcome and exits 3: the reason, the kept run directory
// and, for any preset but baseline, the way to a plain view.
func (r *reportRun) failure(out runner.Outcome) error {
	rec := out.Record
	msg := rec.Reason
	if msg == "" {
		msg = "the report failed (" + rec.OutcomeKind + ")"
	}
	if r.adhoc && rec.OutcomeKind == runner.KindConfigInvalid {
		msg += "; this generator was run ad hoc with an empty config: define a preset with a config"
	}
	if out.RunDir != "" {
		msg += "\nrun directory kept: " + out.RunDir
	}
	if !r.isBaselinePreset() {
		msg += "\nfor a plain view of the data, run: work-report report " + config.BaselineName
	}
	r.writeLog(out)
	r.noteNotices(out)
	if r.format == "json" {
		_ = writeJSON(r.cmd.OutOrStdout(), reportOutcomeJSON{Outcome: msg})
	}
	return &exitError{code: 3, msg: "work-report report: " + msg}
}

// stdoutFallback prints the baseline report to stdout with a warning on
// stderr, because the output parent is unusable.
func (r *reportRun) stdoutFallback(out runner.Outcome, text string) error {
	warning := fmt.Sprintf("the output directory %s is not usable (%s); printing the baseline report to stdout, no run directory or manifest.json was written",
		r.parent, out.Record.Reason)
	rec := out.Record
	rec.Status, rec.OutcomeKind, rec.Reason = runner.StatusOK, runner.KindOK, warning
	logged := out
	logged.Record = rec
	r.writeLog(logged)
	fmt.Fprintf(r.cmd.ErrOrStderr(), "warning: %s\n", warning)
	w := r.cmd.OutOrStdout()
	if r.format == "json" {
		return writeJSON(w, fallbackJSON{Status: runner.StatusOK, OutcomeKind: runner.KindOK, StdoutFallback: true, Warnings: []string{warning}, Content: text})
	}
	_, err := io.WriteString(w, text)
	return err
}

// success prints the result of an ok run. --print puts only the report on
// stdout; everything else goes to stderr then.
func (r *reportRun) success(out runner.Outcome) error {
	rec := out.Record
	r.writeLog(out)
	r.noteNotices(out)
	stdout := r.cmd.OutOrStdout()
	if r.format == "json" {
		return writeJSON(stdout, rec)
	}
	human := stdout
	if r.print {
		human = r.cmd.ErrOrStderr()
	}
	r.writeHuman(human, out)
	if !r.print {
		return nil
	}

	// A refused --print is not a run failure: the run stays status ok in
	// manifest.json and in the log, and only the exit code and message report
	// the refusal.
	if len(rec.Files) != 1 || !strings.HasPrefix(rec.Files[0].ContentType, "text/") || rec.Files[0].SizeBytes > printLimit {
		var list []string
		for _, f := range rec.Files {
			list = append(list, fmt.Sprintf("%s (%s, %s)", f.Path, f.ContentType, sizeText(f.SizeBytes)))
		}
		if len(list) == 0 {
			list = []string{"none"}
		}
		return &exitError{code: 3, msg: fmt.Sprintf(
			"work-report report: --print writes exactly one text/* file of at most %s, but this run produced %d %s: %s\nrun directory kept: %s",
			sizeText(printLimit), len(rec.Files), plural(len(rec.Files), "file", "files"), strings.Join(list, ", "), out.RunDir,
		)}
	}
	b, err := os.ReadFile(filepath.Join(out.RunDir, rec.Files[0].Path))
	if err != nil {
		return fmt.Errorf("read the report to print: %w", err)
	}
	_, err = stdout.Write(b)
	return err
}

// writeHuman prints the default human output: the narrowing note, the
// preset, range and run directory, the entry count, one line per file, then
// the generator's summary and each warning.
func (r *reportRun) writeHuman(w io.Writer, out runner.Outcome) {
	rec := out.Record
	for _, n := range r.notes {
		fmt.Fprintln(w, n)
	}
	name := r.preset
	if r.adhoc {
		name = "adhoc"
	}
	fmt.Fprintf(w, "%s %s -> %s\n", name, r.spec, out.RunDir)
	fmt.Fprintf(w, "entries: %d\n", rec.EntryCount)
	if rec.EntryCount == 0 {
		fmt.Fprintf(w, "warning: no entries in %s\n", report.FormatRange(r.rng))
	}
	for _, f := range rec.Files {
		fmt.Fprintf(w, "%s  %s  %s\n", f.Path, f.ContentType, sizeText(f.SizeBytes))
	}
	if rec.Summary != "" {
		fmt.Fprintln(w, rec.Summary)
	}
	for _, warn := range rec.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
}
