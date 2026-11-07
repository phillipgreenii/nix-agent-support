package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/timing"
)

// runTiming prints the two timing rankings from the database.
func runTiming(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("timing", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "database path (default $XDG_STATE_HOME/claude-tool-tracker/tracker.db)")
	format := fs.String("format", "table", "output format: table or json")
	includeFailed := fs.Bool("include-failed", false, "also count calls that errored or were interrupted")
	top := fs.Int("top", 20, "rows per ranking in table output (0 for all); json always has all")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*format != "table" && *format != "json") || *top < 0 {
		fmt.Fprintln(stderr, "claude-tool-tracker timing: usage: timing [--db PATH] [--format table|json] [--include-failed] [--top N]")
		return 2
	}
	if *dbPath == "" {
		p, err := store.DefaultPath()
		if err != nil {
			fmt.Fprintln(stderr, "claude-tool-tracker timing:", err)
			return 1
		}
		*dbPath = p
	}
	db, err := store.OpenReadOnly(*dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "claude-tool-tracker timing:", err)
		return 1
	}
	defer db.Close()
	calls, err := timing.Load(db)
	if err != nil {
		fmt.Fprintln(stderr, "claude-tool-tracker timing:", err)
		return 1
	}
	rep := timing.Analyze(calls, timing.Options{IncludeFailed: *includeFailed})
	if rep.ByTotal == nil {
		rep.ByTotal, rep.ByP90 = []timing.Group{}, []timing.Group{}
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(stderr, "claude-tool-tracker timing:", err)
			return 1
		}
		return 0
	}
	writeTimingTable(stdout, rep, *top)
	return 0
}

func writeTimingTable(w io.Writer, rep *timing.Report, top int) {
	fmt.Fprintf(w, "calls=%d used=%d excluded: failed=%d interrupted=%d refused=%d no_usable_time=%d\n",
		rep.Calls, rep.Used, rep.Excluded.Failed, rep.Excluded.Interrupted, rep.Excluded.Refused, rep.Excluded.NoTime)
	section := func(title string, gs []timing.Group) {
		fmt.Fprintf(w, "\n%s\n", title)
		if top > 0 && len(gs) > top {
			gs = gs[:top]
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "TOOL\tCLASS\tREPO\tRUNS\tTOTAL_MS\tMEDIAN_MS\tP90_MS\tMAX_MS")
		for _, g := range gs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%d\t%d\n", g.Tool, dash(g.Class), g.Repo, g.Runs, g.TotalMS,
				fmtMedian(g.MedianMS), g.P90MS, g.MaxMS)
		}
		tw.Flush()
	}
	section("by total time", rep.ByTotal)
	section("by p90 (median beside it)", rep.ByP90)
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func fmtMedian(m float64) string {
	if m == float64(int64(m)) {
		return fmt.Sprintf("%d", int64(m))
	}
	return fmt.Sprintf("%.1f", m)
}
