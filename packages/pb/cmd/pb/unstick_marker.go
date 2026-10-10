package main

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pb/internal/unstick"
	"github.com/spf13/cobra"
)

type markerCheckLine struct {
	Line    int    `json:"line"`
	Text    string `json:"text"`
	Problem string `json:"problem"`
}

type markerBead struct {
	ID    string   `json:"id"`
	Lines []string `json:"lines"`
}

type markerCheckResult struct {
	Checked       int               `json:"checked"`
	NonConforming []markerCheckLine `json:"non_conforming"`
	Beads         []markerBead      `json:"malformed_beads,omitempty"`
}

func newUnstickMarkerCmd(env unstickEnv) *cobra.Command {
	var (
		outcome, reason, recheck, export string
		check, asJSON                    bool
		now                              nowFlag
	)
	cmd := &cobra.Command{
		Use:   "marker",
		Short: "Print one well-formed sweep marker line, or validate marker lines read from stdin",
		Long: `Generate: prints one marker line stamped with the current UTC time:

  [unstick YYYY-MM-DDTHH:MM:SSZ] <outcome>: <reason>; recheck-when: <recheck>

--outcome matches [a-z][a-z-]*. --reason is plain text: no control characters
(newline included), backtick, $, quotes or the substring "; recheck-when:".
--recheck-when is YYYY-MM-DD, "<bead-id> closes" or "on-change".

--check: reads marker lines from stdin (blank lines ignored) and exits non-zero
if any does not conform. With --export FILE it also scans the notes and
comments of every NON-CLOSED bead in that export and lists the beads carrying a
malformed marker (for example a date-only timestamp); those count as
non-conforming too.

Exit codes: 0 ok; 1 usage error or non-conforming input.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if check {
				if outcome != "" || reason != "" || recheck != "" {
					return errors.New("--check cannot be combined with --outcome, --reason or --recheck-when")
				}
				return runMarkerCheck(cmd, export, asJSON)
			}
			if export != "" {
				return errors.New("--export is only valid with --check")
			}
			if outcome == "" || reason == "" || recheck == "" {
				return errors.New("--outcome, --reason and --recheck-when are all required (or use --check)")
			}
			t, err := now.resolve(env)
			if err != nil {
				return err
			}
			m, err := unstick.NewMarker(t, outcome, reason, recheck)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]string{"marker": m.String()})
			}
			fmt.Fprintln(cmd.OutOrStdout(), m.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&outcome, "outcome", "", "marker outcome, [a-z][a-z-]*")
	cmd.Flags().StringVar(&reason, "reason", "", "one-line plain-text reason")
	cmd.Flags().StringVar(&recheck, "recheck-when", "", "YYYY-MM-DD | <bead-id> closes | on-change")
	cmd.Flags().BoolVar(&check, "check", false, "validate marker lines from stdin instead of generating one")
	cmd.Flags().StringVar(&export, "export", "", "with --check: also list beads in this bd export carrying a malformed marker")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	now.register(cmd)
	return cmd
}

func runMarkerCheck(cmd *cobra.Command, export string, asJSON bool) error {
	res := markerCheckResult{NonConforming: []markerCheckLine{}}
	sc := bufio.NewScanner(cmd.InOrStdin())
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		res.Checked++
		if _, err := unstick.ParseMarker(line); err != nil {
			res.NonConforming = append(res.NonConforming, markerCheckLine{Line: n, Text: strings.TrimSpace(line), Problem: err.Error()})
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if export != "" {
		rows, err := unstick.ReadExportFile(export)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Status == unstick.StatusClosed {
				continue
			}
			if m := r.Markers(); len(m.Malformed) > 0 {
				b := markerBead{ID: r.ID}
				for _, x := range m.Malformed {
					b.Lines = append(b.Lines, x.Line)
				}
				res.Beads = append(res.Beads, b)
			}
		}
	}
	bad := len(res.NonConforming) + len(res.Beads)
	if asJSON {
		if err := writeJSON(cmd.OutOrStdout(), res); err != nil {
			return err
		}
	} else {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "checked %d line(s): %d non-conforming\n", res.Checked, len(res.NonConforming))
		for _, l := range res.NonConforming {
			fmt.Fprintf(out, "  line %d: %s\n    %s\n", l.Line, l.Problem, l.Text)
		}
		if export != "" {
			fmt.Fprintf(out, "export %s: %d bead(s) with a malformed marker\n", export, len(res.Beads))
			for _, b := range res.Beads {
				fmt.Fprintf(out, "  %s\n", b.ID)
				for _, l := range b.Lines {
					fmt.Fprintf(out, "    %s\n", l)
				}
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d non-conforming marker(s) found", bad)
	}
	return nil
}
