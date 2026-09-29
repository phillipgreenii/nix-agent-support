// Command legacyextract is docket tc-o14i5.1.3's own converter CLI: a thin
// wrapper over internal/legacyextract that can regenerate
// internal/goldencorpus/testdata/corpus_legacy.json and
// internal/goldencorpus/testdata/legacy_disposition_report.md from the
// current state of internal/engine and internal/rules/*.
//
// `LEGACYEXTRACT_WRITE=1 go test ./internal/legacyextract/... -run
// TestLegacyExtract` (the docket's own Validation command, with the write
// gate set -- see internal/legacyextract's legacyExtractWriteEnv doc
// comment for why a bare `go test` must NOT write) already regenerates
// both files as a side effect of running; this binary exists so the same
// conversion can also be run directly (`go run ./cmd/legacyextract
// -write`) without invoking the test runner, and so `go build ./...` has a
// real command to compile (the packet's own Validation section: "go build
// ./... for any new Go code"). Either way, run this repo's formatter
// (treefmt/prettier) over the two output files before committing.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/legacyextract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "legacyextract:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("legacyextract", flag.ContinueOnError)
	write := fs.Bool("write", false, "write corpus_legacy.json and legacy_disposition_report.md into internal/goldencorpus/testdata")
	repoRoot := fs.String("repo-root", ".", "claude-extended-tool-approver module root (the directory containing go.mod)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res, err := legacyextract.Run(*repoRoot)
	if err != nil {
		return err
	}
	cmp, err := legacyextract.CompareAgainstNewEngine(res.Rows)
	if err != nil {
		return err
	}

	if *write {
		corpusPath := *repoRoot + "/internal/goldencorpus/testdata/corpus_legacy.json"
		reportPath := *repoRoot + "/internal/goldencorpus/testdata/legacy_disposition_report.md"
		if err := legacyextract.WriteCorpus(corpusPath, res.Rows); err != nil {
			return err
		}
		if err := legacyextract.WriteDispositionReport(reportPath, res, cmp); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s and %s -- run this repo's formatter (treefmt/prettier) over them before committing\n", corpusPath, reportPath)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"files_scanned":      res.Stats.FilesScanned,
		"recognised_tables":  res.Stats.RecognisedTables,
		"denominator":        res.Stats.Denominator,
		"extracted":          res.Stats.Extracted,
		"unconvertible_rows": res.Stats.UnconvertibleRows,
		"ratio":              res.Stats.Ratio(),
		"fake_stub_files":    len(res.FakeStubFiles),
		"tempdir_files":      len(res.TempDirFiles),
		"comparable":         cmp.Comparable,
		"not_comparable":     cmp.NotComparable,
		"agreed":             cmp.Agreed,
		"disagreements":      len(cmp.Disagreements),
	})
}
