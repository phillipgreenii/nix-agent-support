package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/textsafe"
)

// The text shapes of the level operator surfaces shared by status, explain and
// doctor --clauses: the paused-role line, the paused roles block and the reading
// and time wording.
//
// Exported names: none (package main). Names: pausedRoleView, failingClauseView,
// pausedRoleLine, pausedRoleSummary, formatSince, formatNumber.

// readingView is a level's reading as the status reply carries it.
type readingView struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// String renders "19.9 GiB".
func (r readingView) String() string {
	return strings.TrimSpace(formatNumber(r.Value) + " " + textsafe.Sanitize(r.Unit))
}

// failingClauseView is one entry of status pausedRoles[].failing.
type failingClauseView struct {
	Clause   string       `json:"clause"`
	Resource string       `json:"resource"`
	Level    string       `json:"level"`
	Kind     string       `json:"kind"`
	Source   string       `json:"source"`
	Owner    string       `json:"owner"`
	Since    string       `json:"since"`
	Allowed  []string     `json:"allowed"`
	Reading  *readingView `json:"reading"`
}

// pausedRoleView is one entry of the status reply's pausedRoles array.
type pausedRoleView struct {
	Role    string              `json:"role"`
	Failing []failingClauseView `json:"failing"`
}

// formatNumber writes v with at most two decimals and no trailing zeros.
func formatNumber(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// formatSince writes the time a resource entered its level: HH:MMZ (UTC) when it
// is the same UTC day as now, RFC 3339 UTC otherwise. A time that does not parse
// is returned sanitized as is.
func formatSince(since string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return textsafe.Sanitize(since)
	}
	t, n := t.UTC(), now.UTC()
	if t.Year() == n.Year() && t.YearDay() == n.YearDay() {
		return t.Format("15:04") + "Z"
	}
	return t.Format(time.RFC3339)
}

// who names where a level came from: the sensor, an operator override, or a
// sensor value held through its grace period.
func (f failingClauseView) who() string {
	switch f.Source {
	case "operator":
		return "OVERRIDE by " + textsafe.Sanitize(f.Owner)
	case "grace_fallback":
		return textsafe.Sanitize(f.Owner) + ", stale"
	}
	return textsafe.Sanitize(f.Owner)
}

// explainText is one failing clause as the paused-role line writes it:
// "<clause> now <LEVEL> (<reading>), since <time>, <sensor>".
func (f failingClauseView) explainText(now time.Time) string {
	level := textsafe.Sanitize(f.Level)
	if f.Kind == "unknown" {
		return fmt.Sprintf("%s now %s (no reading)", textsafe.Sanitize(f.Clause), level)
	}
	reading := "no numeric reading"
	if f.Reading != nil {
		reading = f.Reading.String()
	}
	s := fmt.Sprintf("%s now %s (%s)", textsafe.Sanitize(f.Clause), level, reading)
	if f.Since != "" {
		s += ", since " + formatSince(f.Since, now)
	}
	if w := f.who(); w != "" {
		s += ", " + w
	}
	return s
}

// pausedRoleLine is the explain / status line of a paused role:
//
//	<role> is paused by N requirements: <clause> now <LEVEL> (<reading>), since <time>, <sensor>; running work is not affected; see 'pg-router explain <role>'
//
// N counts the failing clauses only; several clauses are joined with "; ".
func pausedRoleLine(p pausedRoleView, now time.Time) string {
	parts := make([]string, 0, len(p.Failing))
	for _, f := range p.Failing {
		parts = append(parts, f.explainText(now))
	}
	noun := "requirements"
	if len(p.Failing) == 1 {
		noun = "requirement"
	}
	role := textsafe.Sanitize(p.Role)
	return fmt.Sprintf("%s is paused by %d %s: %s; running work is not affected; see 'pg-router explain %s'",
		role, len(p.Failing), noun, strings.Join(parts, "; "), role)
}

// pausedRoleSummary is the short form of the status paused roles block:
// "disk >= OK (disk TIGHT, 19.9 GiB); git_auth == AVAILABLE (UNAVAILABLE since 22:10Z)".
func pausedRoleSummary(p pausedRoleView, now time.Time) string {
	parts := make([]string, 0, len(p.Failing))
	for _, f := range p.Failing {
		inner := textsafe.Sanitize(f.Resource) + " " + textsafe.Sanitize(f.Level)
		switch {
		case f.Kind == "unknown":
			inner = "no reading, reads " + textsafe.Sanitize(f.Level)
		case f.Reading != nil:
			inner += ", " + f.Reading.String()
		}
		if f.Kind != "unknown" && f.Since != "" {
			inner += " since " + formatSince(f.Since, now)
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", textsafe.Sanitize(f.Clause), inner))
	}
	return strings.Join(parts, "; ")
}
