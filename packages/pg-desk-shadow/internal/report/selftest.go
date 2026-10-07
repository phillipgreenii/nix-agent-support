package report

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Leaks returns every forbidden string (id, slug, title) found in text.
func Leaks(text string, s Synthetic) []string {
	var out []string
	for _, f := range append(append([]string{s.Slug}, s.IDs...), s.Titles...) {
		if strings.Contains(text, f) {
			out = append(out, f)
		}
	}
	return out
}

// SelfTest runs the whole pipeline (synthetic scratch directory, load,
// compute, render) and checks the headline expectations and the no-leak
// guarantee. It touches only a temporary directory.
func SelfTest(out io.Writer) error {
	dir, err := os.MkdirTemp("", "pg-desk-shadow-selftest-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	s, err := Synthesize(dir, SynthOptions{Seeded: true})
	if err != nil {
		return err
	}
	p := DefaultParams()
	p.Loc = time.UTC
	rep, err := Build([]string{dir}, p)
	if err != nil {
		return err
	}
	jp, mp, err := Write(rep, dir+"/reports")
	if err != nil {
		return err
	}
	for _, path := range []string{jp, mp} {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if l := Leaks(string(b), s); len(l) > 0 {
			return fmt.Errorf("selftest: report %s leaks %d identifier(s)", path, len(l))
		}
	}
	ph := rep.Phases[0]
	checks := []struct {
		name string
		ok   bool
	}{
		{"one unexplained miss", ph.Misses.Unexplained >= 1},
		{"a collector-down miss", ph.Misses.ByClass[ClassCollectorDown] == 1},
		{"hash signal unusable statement", ph.DataQuality.Baseline.SweepRows > 0},
		{"sweep headline counted", ph.Sweep.Headline == 2},
	}
	for _, c := range checks {
		if !c.ok {
			return fmt.Errorf("selftest: expectation failed: %s", c.name)
		}
	}
	_, _ = fmt.Fprintf(out, "selftest ok: %d live-detected, %d matched, %d missed (%d unexplained), %d sweep headline\n", ph.Live.InWindow, ph.Misses.Matched, ph.Misses.Missed, ph.Misses.Unexplained, ph.Sweep.Headline)
	return nil
}
